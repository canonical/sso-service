// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package admin

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"buf.build/go/protovalidate"
	v0sso "github.com/canonical/identity-platform-api/v0/sso"
	v0tenant "github.com/canonical/identity-platform-api/v0/tenant"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/canonical/sso-service/internal/apierrors"
	"github.com/canonical/sso-service/internal/idp"
	"github.com/canonical/sso-service/internal/storage"
	"github.com/canonical/sso-service/internal/tenants"
	"github.com/canonical/sso-service/internal/types"
)

//go:generate mockgen -build_flags=--mod=mod -package admin -destination ./mock_admin.go -source=./interfaces.go
//go:generate mockgen -build_flags=--mod=mod -package admin -destination ./mock_logger.go -source=../../internal/logging/interfaces.go
//go:generate mockgen -build_flags=--mod=mod -package admin -destination ./mock_monitor.go -source=../../internal/monitoring/interfaces.go
//go:generate mockgen -build_flags=--mod=mod -package admin -destination ./mock_tracing.go -source=../../internal/tracing/interfaces.go

var testValidator = func() protovalidate.Validator {
	v, err := protovalidate.New()
	if err != nil {
		panic(err)
	}
	return v
}()

// newTestHandler returns a Handler on a mocked service whose span is
// expected to be spanName.
func newTestHandler(t *testing.T, spanName string) (*Handler, *MockServiceInterface) {
	t.Helper()
	ctrl := gomock.NewController(t)

	mockSvc := NewMockServiceInterface(ctrl)
	mockTracer := NewMockTracingInterface(ctrl)
	mockLogger := NewMockLoggerInterface(ctrl)
	setupLoggerMock(ctrl, mockLogger)
	mockLogger.EXPECT().Errorf(gomock.Any(), gomock.Any()).AnyTimes()

	h := NewHandler(mockSvc, testValidator, publicURL, mockTracer, mockLogger)

	mockTracer.EXPECT().Start(gomock.Any(), spanName).Return(context.Background(), trace.SpanFromContext(context.Background()))

	return h, mockSvc
}

func expectStatus(t *testing.T, err error, wantCode codes.Code, wantReason string) {
	t.Helper()
	if status.Code(err) != wantCode || apierrors.Reason(err) != wantReason {
		t.Fatalf("expected code %v and reason %q, got %v (reason %q)", wantCode, wantReason, err, apierrors.Reason(err))
	}
}

func TestHandler_ListConnections(t *testing.T) {
	tests := []struct {
		name       string
		request    *v0sso.ListConnectionsRequest
		setupMocks func(*MockServiceInterface)
		wantCode   codes.Code
	}{
		{
			name:    "success",
			request: &v0sso.ListConnectionsRequest{TenantId: strings.ToUpper(tenantA), PageToken: "token", PageSize: 10},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().ListConnections(gomock.Any(), tenantA, "token", 10).
					Return([]*types.Connection{connectionOf(tenantA, true)}, "next", nil)
			},
			wantCode: codes.OK,
		},
		{
			name:       "invalid tenant id",
			request:    &v0sso.ListConnectionsRequest{TenantId: "t"},
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name:       "page size too large",
			request:    &v0sso.ListConnectionsRequest{TenantId: tenantA, PageSize: 101},
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name:    "invalid page token",
			request: &v0sso.ListConnectionsRequest{TenantId: tenantA, PageToken: "!"},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().ListConnections(gomock.Any(), tenantA, "!", 0).Return(nil, "", ErrInvalidPageToken)
			},
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, mockSvc := newTestHandler(t, "admin.Handler.ListConnections")
			tt.setupMocks(mockSvc)

			resp, err := h.ListConnections(context.Background(), tt.request)

			expectStatus(t, err, tt.wantCode, "")
			if tt.wantCode != codes.OK {
				return
			}
			if len(resp.Connections) != 1 || resp.Connections[0].Id != connA || resp.NextPageToken != "next" {
				t.Errorf("unexpected response: %v", resp)
			}
		})
	}
}

func TestHandler_CreateConnection(t *testing.T) {
	request := func() *v0sso.CreateConnectionRequest {
		return &v0sso.CreateConnectionRequest{
			TenantId: strings.ToUpper(tenantA), Label: "Acme", Issuer: "https://idp.example",
			ClientId: "client", ClientSecret: "secret",
		}
	}
	creates := func(mockSvc *MockServiceInterface, connection *types.Connection, err error) {
		mockSvc.EXPECT().CreateConnection(gomock.Any(), tenantA, "Acme", "https://idp.example", "client", "secret").
			Return(connection, err)
	}

	tests := []struct {
		name       string
		request    *v0sso.CreateConnectionRequest
		setupMocks func(*MockServiceInterface)
		wantCode   codes.Code
		wantReason string
	}{
		{
			name:       "success",
			request:    request(),
			setupMocks: func(mockSvc *MockServiceInterface) { creates(mockSvc, connectionOf(tenantA, false), nil) },
			wantCode:   codes.OK,
		},
		{
			name: "missing client secret",
			request: func() *v0sso.CreateConnectionRequest {
				r := request()
				r.ClientSecret = ""
				return r
			}(),
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name: "label too long",
			request: func() *v0sso.CreateConnectionRequest {
				r := request()
				r.Label = strings.Repeat("l", 101)
				return r
			}(),
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name:       "connection limit",
			request:    request(),
			setupMocks: func(mockSvc *MockServiceInterface) { creates(mockSvc, nil, ErrConnectionLimit) },
			wantCode:   codes.FailedPrecondition,
			wantReason: apierrors.ConnectionLimit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, mockSvc := newTestHandler(t, "admin.Handler.CreateConnection")
			tt.setupMocks(mockSvc)

			resp, err := h.CreateConnection(context.Background(), tt.request)

			expectStatus(t, err, tt.wantCode, tt.wantReason)
			if tt.wantCode != codes.OK {
				return
			}
			c := resp.Connection
			if c.Id != connA || c.Status != v0sso.ConnectionStatus_CONNECTION_STATUS_DRAFT || c.RedirectUri != redirectA {
				t.Errorf("unexpected connection: %v", c)
			}
		})
	}
}

func TestHandler_GetConnection(t *testing.T) {
	tests := []struct {
		name       string
		request    *v0sso.GetConnectionRequest
		setupMocks func(*MockServiceInterface)
		wantCode   codes.Code
		wantReason string
	}{
		{
			name:    "success",
			request: &v0sso.GetConnectionRequest{TenantId: strings.ToUpper(tenantA), ConnectionId: strings.ToUpper(connA)},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().GetConnection(gomock.Any(), tenantA, connA).Return(connectionOf(tenantA, true), nil)
			},
			wantCode: codes.OK,
		},
		{
			name:       "invalid connection id",
			request:    &v0sso.GetConnectionRequest{TenantId: tenantA, ConnectionId: "c"},
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name:    "connection not found",
			request: &v0sso.GetConnectionRequest{TenantId: tenantA, ConnectionId: connA},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().GetConnection(gomock.Any(), tenantA, connA).Return(nil, ErrConnectionNotFound)
			},
			wantCode:   codes.NotFound,
			wantReason: apierrors.ConnectionNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, mockSvc := newTestHandler(t, "admin.Handler.GetConnection")
			tt.setupMocks(mockSvc)

			resp, err := h.GetConnection(context.Background(), tt.request)

			expectStatus(t, err, tt.wantCode, tt.wantReason)
			if tt.wantCode == codes.OK && resp.Connection.Status != v0sso.ConnectionStatus_CONNECTION_STATUS_TESTED {
				t.Errorf("unexpected connection: %v", resp.Connection)
			}
		})
	}
}

func TestHandler_UpdateConnection(t *testing.T) {
	label, secret := "New", "new-secret"
	request := func(update *v0sso.ConnectionUpdate, paths ...string) *v0sso.UpdateConnectionRequest {
		return &v0sso.UpdateConnectionRequest{
			TenantId: strings.ToUpper(tenantA), ConnectionId: strings.ToUpper(connA),
			Connection: update, UpdateMask: &fieldmaskpb.FieldMask{Paths: paths},
		}
	}

	tests := []struct {
		name       string
		request    *v0sso.UpdateConnectionRequest
		setupMocks func(*MockServiceInterface)
		wantCode   codes.Code
		wantReason string
	}{
		{
			name:    "label",
			request: request(&v0sso.ConnectionUpdate{Label: label, ClientSecret: "ignored"}, "label"),
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().UpdateConnection(gomock.Any(), tenantA, connA, &label, nil).Return(connectionOf(tenantA, true), nil)
			},
			wantCode: codes.OK,
		},
		{
			name:    "label and client secret",
			request: request(&v0sso.ConnectionUpdate{Label: label, ClientSecret: secret}, "label", "client_secret"),
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().UpdateConnection(gomock.Any(), tenantA, connA, &label, &secret).Return(connectionOf(tenantA, true), nil)
			},
			wantCode: codes.OK,
		},
		{
			name:       "empty mask",
			request:    request(&v0sso.ConnectionUpdate{Label: label}),
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name:       "issuer is fixed",
			request:    request(&v0sso.ConnectionUpdate{Label: label}, "issuer"),
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name:       "empty label",
			request:    request(&v0sso.ConnectionUpdate{}, "label"),
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			// The limit counts characters, as at create.
			name:    "label of 100 non-ASCII characters",
			request: request(&v0sso.ConnectionUpdate{Label: strings.Repeat("é", 100)}, "label"),
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().UpdateConnection(gomock.Any(), tenantA, connA, gomock.Any(), nil).Return(connectionOf(tenantA, true), nil)
			},
			wantCode: codes.OK,
		},
		{
			name:       "label too long",
			request:    request(&v0sso.ConnectionUpdate{Label: strings.Repeat("l", 101)}, "label"),
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name:       "empty client secret",
			request:    request(&v0sso.ConnectionUpdate{}, "client_secret"),
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name:       "client secret too long",
			request:    request(&v0sso.ConnectionUpdate{ClientSecret: strings.Repeat("s", 4097)}, "client_secret"),
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name:       "no update",
			request:    request(nil, "label"),
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name:    "connection not found",
			request: request(&v0sso.ConnectionUpdate{Label: label}, "label"),
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().UpdateConnection(gomock.Any(), tenantA, connA, &label, nil).Return(nil, ErrConnectionNotFound)
			},
			wantCode:   codes.NotFound,
			wantReason: apierrors.ConnectionNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, mockSvc := newTestHandler(t, "admin.Handler.UpdateConnection")
			tt.setupMocks(mockSvc)

			resp, err := h.UpdateConnection(context.Background(), tt.request)

			expectStatus(t, err, tt.wantCode, tt.wantReason)
			if tt.wantCode == codes.OK && resp.Connection.Id != connA {
				t.Errorf("unexpected connection: %v", resp.Connection)
			}
		})
	}
}

func TestHandler_DeleteConnection(t *testing.T) {
	tests := []struct {
		name       string
		request    *v0sso.DeleteConnectionRequest
		setupMocks func(*MockServiceInterface)
		wantCode   codes.Code
		wantReason string
	}{
		{
			name:    "success",
			request: &v0sso.DeleteConnectionRequest{TenantId: strings.ToUpper(tenantA), ConnectionId: strings.ToUpper(connA)},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().DeleteConnection(gomock.Any(), tenantA, connA).Return(nil)
			},
			wantCode: codes.OK,
		},
		{
			name:       "invalid tenant id",
			request:    &v0sso.DeleteConnectionRequest{TenantId: "t", ConnectionId: connA},
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name:    "required needs an active binding",
			request: &v0sso.DeleteConnectionRequest{TenantId: tenantA, ConnectionId: connA},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().DeleteConnection(gomock.Any(), tenantA, connA).Return(tenants.ErrRequiredNeedsActiveBinding)
			},
			wantCode:   codes.FailedPrecondition,
			wantReason: apierrors.RequiredNeedsActiveBinding,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, mockSvc := newTestHandler(t, "admin.Handler.DeleteConnection")
			tt.setupMocks(mockSvc)

			_, err := h.DeleteConnection(context.Background(), tt.request)

			expectStatus(t, err, tt.wantCode, tt.wantReason)
		})
	}
}

func TestHandler_StartTestLogin(t *testing.T) {
	tests := []struct {
		name       string
		request    *v0sso.StartTestLoginRequest
		setupMocks func(*MockServiceInterface)
		wantCode   codes.Code
		wantReason string
	}{
		{
			name:    "success",
			request: &v0sso.StartTestLoginRequest{TenantId: strings.ToUpper(tenantA), ConnectionId: strings.ToUpper(connA)},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().StartTestLogin(gomock.Any(), tenantA, connA).Return("https://idp.example/authorize", nil)
			},
			wantCode: codes.OK,
		},
		{
			name:       "invalid connection id",
			request:    &v0sso.StartTestLoginRequest{TenantId: tenantA, ConnectionId: "c"},
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name:    "identity provider check failed",
			request: &v0sso.StartTestLoginRequest{TenantId: tenantA, ConnectionId: connA},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().StartTestLogin(gomock.Any(), tenantA, connA).
					Return("", fmt.Errorf("%w: %w", ErrIdPCheckFailed, idp.ErrCredentials))
			},
			wantCode:   codes.FailedPrecondition,
			wantReason: apierrors.IdPCheckFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, mockSvc := newTestHandler(t, "admin.Handler.StartTestLogin")
			tt.setupMocks(mockSvc)

			resp, err := h.StartTestLogin(context.Background(), tt.request)

			expectStatus(t, err, tt.wantCode, tt.wantReason)
			if tt.wantCode == codes.OK && resp.Url != "https://idp.example/authorize" {
				t.Errorf("unexpected response: %v", resp)
			}
		})
	}
}

func TestHandler_GetTenantSSOPolicy(t *testing.T) {
	policy := &v0tenant.TenantSSOPolicy{TenantId: tenantA, Enforcement: v0tenant.Enforcement_ENFORCEMENT_OPTIONAL}

	tests := []struct {
		name       string
		request    *v0sso.GetTenantSSOPolicyRequest
		setupMocks func(*MockServiceInterface)
		wantCode   codes.Code
		wantReason string
	}{
		{
			name:    "success",
			request: &v0sso.GetTenantSSOPolicyRequest{TenantId: strings.ToUpper(tenantA)},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().GetTenantSSOPolicy(gomock.Any(), tenantA).Return(policy, nil)
			},
			wantCode: codes.OK,
		},
		{
			name:       "invalid tenant id",
			request:    &v0sso.GetTenantSSOPolicyRequest{TenantId: "t"},
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name:    "personal tenant",
			request: &v0sso.GetTenantSSOPolicyRequest{TenantId: tenantA},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().GetTenantSSOPolicy(gomock.Any(), tenantA).Return(nil, tenants.ErrPersonalTenant)
			},
			wantCode:   codes.FailedPrecondition,
			wantReason: apierrors.PersonalTenant,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, mockSvc := newTestHandler(t, "admin.Handler.GetTenantSSOPolicy")
			tt.setupMocks(mockSvc)

			resp, err := h.GetTenantSSOPolicy(context.Background(), tt.request)

			expectStatus(t, err, tt.wantCode, tt.wantReason)
			if tt.wantCode == codes.OK && resp.Policy != policy {
				t.Errorf("unexpected policy: %v", resp.Policy)
			}
		})
	}
}

func TestHandler_PutTenantSSOPolicy(t *testing.T) {
	required := v0tenant.Enforcement_ENFORCEMENT_REQUIRED
	policy := &v0tenant.TenantSSOPolicy{TenantId: tenantA, Enforcement: required}
	bindings := []*v0tenant.SSOBinding{{ConnectionId: connA, Active: true}}
	request := func() *v0sso.PutTenantSSOPolicyRequest {
		return &v0sso.PutTenantSSOPolicyRequest{
			TenantId: strings.ToUpper(tenantA), Enforcement: required, AutoJoin: true,
			Bindings: []*v0tenant.SSOBinding{{ConnectionId: strings.ToUpper(connA), Active: true}},
		}
	}
	puts := func(mockSvc *MockServiceInterface, written *v0tenant.TenantSSOPolicy, err error) {
		mockSvc.EXPECT().PutTenantSSOPolicy(gomock.Any(), tenantA, required, true, bindings).Return(written, err)
	}

	tests := []struct {
		name       string
		request    *v0sso.PutTenantSSOPolicyRequest
		setupMocks func(*MockServiceInterface)
		wantCode   codes.Code
		wantReason string
	}{
		{
			name:       "success",
			request:    request(),
			setupMocks: func(mockSvc *MockServiceInterface) { puts(mockSvc, policy, nil) },
			wantCode:   codes.OK,
		},
		{
			name: "enforcement off cannot be written",
			request: func() *v0sso.PutTenantSSOPolicyRequest {
				r := request()
				r.Enforcement = v0tenant.Enforcement_ENFORCEMENT_OFF
				return r
			}(),
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name: "invalid connection id",
			request: func() *v0sso.PutTenantSSOPolicyRequest {
				r := request()
				r.Bindings[0].ConnectionId = "c"
				return r
			}(),
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name:       "connection not tested",
			request:    request(),
			setupMocks: func(mockSvc *MockServiceInterface) { puts(mockSvc, nil, ErrConnectionNotTested) },
			wantCode:   codes.FailedPrecondition,
			wantReason: apierrors.ConnectionNotTested,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, mockSvc := newTestHandler(t, "admin.Handler.PutTenantSSOPolicy")
			tt.setupMocks(mockSvc)

			resp, err := h.PutTenantSSOPolicy(context.Background(), tt.request)

			expectStatus(t, err, tt.wantCode, tt.wantReason)
			if tt.wantCode == codes.OK && resp.Policy != policy {
				t.Errorf("unexpected policy: %v", resp.Policy)
			}
		})
	}
}

func TestHandler_ListAllConnections(t *testing.T) {
	tests := []struct {
		name       string
		request    *v0sso.ListAllConnectionsRequest
		setupMocks func(*MockServiceInterface)
		wantCode   codes.Code
	}{
		{
			name:    "every connection",
			request: &v0sso.ListAllConnectionsRequest{},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().ListConnections(gomock.Any(), "", "", 0).
					Return([]*types.Connection{connectionOf(tenantB, true)}, "next", nil)
			},
			wantCode: codes.OK,
		},
		{
			name:    "one owner's connections",
			request: &v0sso.ListAllConnectionsRequest{OwnerTenantId: strings.ToUpper(tenantB), PageToken: "token", PageSize: 10},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().ListConnections(gomock.Any(), tenantB, "token", 10).
					Return([]*types.Connection{connectionOf(tenantB, true)}, "next", nil)
			},
			wantCode: codes.OK,
		},
		{
			name:       "invalid owner tenant id",
			request:    &v0sso.ListAllConnectionsRequest{OwnerTenantId: "t"},
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name:    "service error",
			request: &v0sso.ListAllConnectionsRequest{},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().ListConnections(gomock.Any(), "", "", 0).Return(nil, "", errors.New("service error"))
			},
			wantCode: codes.Internal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, mockSvc := newTestHandler(t, "admin.Handler.ListAllConnections")
			tt.setupMocks(mockSvc)

			resp, err := h.ListAllConnections(context.Background(), tt.request)

			expectStatus(t, err, tt.wantCode, "")
			if tt.wantCode != codes.OK {
				return
			}
			if len(resp.Connections) != 1 || resp.Connections[0].OwnerTenantId != tenantB || resp.NextPageToken != "next" {
				t.Errorf("unexpected response: %v", resp)
			}
		})
	}
}

func TestHandler_GetTenantDomains(t *testing.T) {
	domains := []string{"acme.example"}

	tests := []struct {
		name       string
		request    *v0sso.GetTenantDomainsRequest
		setupMocks func(*MockServiceInterface)
		wantCode   codes.Code
		wantReason string
	}{
		{
			name:    "success",
			request: &v0sso.GetTenantDomainsRequest{TenantId: strings.ToUpper(tenantA)},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().GetTenantDomains(gomock.Any(), tenantA).Return(domains, nil)
			},
			wantCode: codes.OK,
		},
		{
			name:       "invalid tenant id",
			request:    &v0sso.GetTenantDomainsRequest{TenantId: "t"},
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name:    "personal tenant",
			request: &v0sso.GetTenantDomainsRequest{TenantId: tenantA},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().GetTenantDomains(gomock.Any(), tenantA).Return(nil, tenants.ErrPersonalTenant)
			},
			wantCode:   codes.FailedPrecondition,
			wantReason: apierrors.PersonalTenant,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, mockSvc := newTestHandler(t, "admin.Handler.GetTenantDomains")
			tt.setupMocks(mockSvc)

			resp, err := h.GetTenantDomains(context.Background(), tt.request)

			expectStatus(t, err, tt.wantCode, tt.wantReason)
			if tt.wantCode == codes.OK && !slices.Equal(resp.Domains, domains) {
				t.Errorf("unexpected domains: %v", resp.Domains)
			}
		})
	}
}

func TestHandler_SetTenantDomains(t *testing.T) {
	domains := []string{"Acme.Example"}
	policy := &v0tenant.TenantSSOPolicy{TenantId: tenantA, Domains: []string{"acme.example"}}

	tests := []struct {
		name       string
		request    *v0sso.SetTenantDomainsRequest
		setupMocks func(*MockServiceInterface)
		wantCode   codes.Code
		wantReason string
	}{
		{
			name:    "success",
			request: &v0sso.SetTenantDomainsRequest{TenantId: strings.ToUpper(tenantA), Domains: domains},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().SetTenantDomains(gomock.Any(), tenantA, domains).Return(policy, nil)
			},
			wantCode: codes.OK,
		},
		{
			name:       "too many domains",
			request:    &v0sso.SetTenantDomainsRequest{TenantId: tenantA, Domains: make([]string, 51)},
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name:    "refused by tenant-service",
			request: &v0sso.SetTenantDomainsRequest{TenantId: tenantA, Domains: domains},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().SetTenantDomains(gomock.Any(), tenantA, domains).Return(nil, tenants.ErrAutoJoinNeedsRequiredAndDomains)
			},
			wantCode:   codes.FailedPrecondition,
			wantReason: apierrors.AutoJoinNeedsRequiredAndDomains,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, mockSvc := newTestHandler(t, "admin.Handler.SetTenantDomains")
			tt.setupMocks(mockSvc)

			resp, err := h.SetTenantDomains(context.Background(), tt.request)

			expectStatus(t, err, tt.wantCode, tt.wantReason)
			if tt.wantCode == codes.OK && resp.Policy != policy {
				t.Errorf("unexpected policy: %v", resp.Policy)
			}
		})
	}
}

func TestHandler_DeleteAnyConnection(t *testing.T) {
	tests := []struct {
		name       string
		request    *v0sso.DeleteAnyConnectionRequest
		setupMocks func(*MockServiceInterface)
		wantCode   codes.Code
		wantReason string
	}{
		{
			name:    "success",
			request: &v0sso.DeleteAnyConnectionRequest{ConnectionId: strings.ToUpper(connA)},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().DeleteAnyConnection(gomock.Any(), connA).Return(nil)
			},
			wantCode: codes.OK,
		},
		{
			name:       "invalid connection id",
			request:    &v0sso.DeleteAnyConnectionRequest{ConnectionId: "c"},
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name:    "required needs an active binding",
			request: &v0sso.DeleteAnyConnectionRequest{ConnectionId: connA},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().DeleteAnyConnection(gomock.Any(), connA).Return(tenants.ErrRequiredNeedsActiveBinding)
			},
			wantCode:   codes.FailedPrecondition,
			wantReason: apierrors.RequiredNeedsActiveBinding,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, mockSvc := newTestHandler(t, "admin.Handler.DeleteAnyConnection")
			tt.setupMocks(mockSvc)

			_, err := h.DeleteAnyConnection(context.Background(), tt.request)

			expectStatus(t, err, tt.wantCode, tt.wantReason)
		})
	}
}

func TestHandler_mapErrorToStatus(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantCode    codes.Code
		wantReason  string
		wantMessage string
	}{
		{
			name:        "connection not found",
			err:         ErrConnectionNotFound,
			wantCode:    codes.NotFound,
			wantReason:  apierrors.ConnectionNotFound,
			wantMessage: "CONNECTION_NOT_FOUND: no such connection",
		},
		{
			name:        "connection not tested",
			err:         ErrConnectionNotTested,
			wantCode:    codes.FailedPrecondition,
			wantReason:  apierrors.ConnectionNotTested,
			wantMessage: "CONNECTION_NOT_TESTED: only a tested connection can be active",
		},
		{
			name:        "connection limit",
			err:         ErrConnectionLimit,
			wantCode:    codes.FailedPrecondition,
			wantReason:  apierrors.ConnectionLimit,
			wantMessage: "CONNECTION_LIMIT: a tenant owns at most 5 connections",
		},
		{
			name:        "client credentials refused",
			err:         fmt.Errorf("%w: %w", ErrIdPCheckFailed, idp.ErrCredentials),
			wantCode:    codes.FailedPrecondition,
			wantReason:  apierrors.IdPCheckFailed,
			wantMessage: "IDP_CHECK_FAILED: The identity provider refused the client credentials.",
		},
		{
			name:        "client secret unreadable",
			err:         ErrClientSecretUnreadable,
			wantCode:    codes.FailedPrecondition,
			wantReason:  apierrors.IdPCheckFailed,
			wantMessage: "IDP_CHECK_FAILED: The connection's client secret cannot be read: set it again.",
		},
		{
			name:        "invalid issuer",
			err:         fmt.Errorf("%w: the IdP URL is not https", ErrInvalidIssuer),
			wantCode:    codes.InvalidArgument,
			wantMessage: "invalid issuer: the IdP URL is not https",
		},
		{
			name:        "invalid page token",
			err:         ErrInvalidPageToken,
			wantCode:    codes.InvalidArgument,
			wantMessage: "invalid page_token",
		},
		{
			name:        "personal tenant",
			err:         tenants.ErrPersonalTenant,
			wantCode:    codes.FailedPrecondition,
			wantReason:  apierrors.PersonalTenant,
			wantMessage: "PERSONAL_TENANT: a personal tenant has no SSO policy and no connections",
		},
		{
			name:        "required needs an active binding",
			err:         tenants.ErrRequiredNeedsActiveBinding,
			wantCode:    codes.FailedPrecondition,
			wantReason:  apierrors.RequiredNeedsActiveBinding,
			wantMessage: "REQUIRED_NEEDS_ACTIVE_BINDING: a tenant that requires company sign-in needs an active binding",
		},
		{
			name:        "auto-join needs required and domains",
			err:         tenants.ErrAutoJoinNeedsRequiredAndDomains,
			wantCode:    codes.FailedPrecondition,
			wantReason:  apierrors.AutoJoinNeedsRequiredAndDomains,
			wantMessage: "AUTO_JOIN_NEEDS_REQUIRED_AND_DOMAINS: auto-join needs domains and REQUIRED enforcement",
		},
		{
			name:        "tenant not found",
			err:         tenants.ErrTenantNotFound,
			wantCode:    codes.NotFound,
			wantMessage: "no such tenant",
		},
		{
			name:        "policy being changed",
			err:         tenants.ErrBusy,
			wantCode:    codes.Aborted,
			wantMessage: "another request is changing this tenant's policy; try again",
		},
		{
			name:        "invalid for tenant-service",
			err:         fmt.Errorf("%w: invalid sso policy: a connection is bound twice", tenants.ErrInvalid),
			wantCode:    codes.InvalidArgument,
			wantMessage: "tenant-service found the request invalid: invalid sso policy: a connection is bound twice",
		},
		{
			name:        "refused by tenant-service",
			err:         fmt.Errorf("%w: a connection is bound by another tenant", tenants.ErrRefused),
			wantCode:    codes.FailedPrecondition,
			wantMessage: "tenant-service refused the request: a connection is bound by another tenant",
		},
		{
			name:        "tenant-service unavailable",
			err:         unavailable(),
			wantCode:    codes.Unavailable,
			wantMessage: "tenant-service is unavailable",
		},
		{
			name:        "lock held too long",
			err:         fmt.Errorf("failed to lock connections: %w", fmt.Errorf("%w: canceling statement due to lock timeout", storage.ErrBusy)),
			wantCode:    codes.Aborted,
			wantMessage: "another request is changing this resource; try again",
		},
		{
			name:        "database too slow",
			err:         fmt.Errorf("%w: canceling statement due to statement timeout", storage.ErrTimeout),
			wantCode:    codes.Unavailable,
			wantMessage: "the database did not answer in time; try again later",
		},
		{
			name:        "caller went away",
			err:         context.Canceled,
			wantCode:    codes.Canceled,
			wantMessage: "the request was cancelled",
		},
		{
			name:        "anything else is hidden",
			err:         errors.New("pq: relation does not exist"),
			wantCode:    codes.Internal,
			wantMessage: "internal error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockLogger := NewMockLoggerInterface(ctrl)
			mockLogger.EXPECT().Errorf(gomock.Any(), gomock.Any()).AnyTimes()
			h := NewHandler(NewMockServiceInterface(ctrl), testValidator, publicURL, NewMockTracingInterface(ctrl), mockLogger)

			err := h.mapErrorToStatus(tt.err, "test")

			st := status.Convert(err)
			if st.Code() != tt.wantCode || st.Message() != tt.wantMessage || apierrors.Reason(err) != tt.wantReason {
				t.Errorf("expected %v %q (reason %q), got %v %q (reason %q)",
					tt.wantCode, tt.wantMessage, tt.wantReason, st.Code(), st.Message(), apierrors.Reason(err))
			}
		})
	}

	t.Run("nil", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		h := NewHandler(NewMockServiceInterface(ctrl), testValidator, publicURL, NewMockTracingInterface(ctrl), NewMockLoggerInterface(ctrl))
		if err := h.mapErrorToStatus(nil, "test"); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})
}
