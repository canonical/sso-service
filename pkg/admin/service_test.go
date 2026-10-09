// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package admin

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	v0tenant "github.com/canonical/identity-platform-api/v0/tenant"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/mock/gomock"

	"github.com/canonical/sso-service/internal/idp"
	"github.com/canonical/sso-service/internal/limits"
	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/storage"
	"github.com/canonical/sso-service/internal/tenants"
	"github.com/canonical/sso-service/internal/types"
	"github.com/canonical/sso-service/pkg/authentication"
)

//go:generate mockgen -build_flags=--mod=mod -package admin -destination ./mock_admin.go -source=./interfaces.go
//go:generate mockgen -build_flags=--mod=mod -package admin -destination ./mock_logger.go -source=../../internal/logging/interfaces.go
//go:generate mockgen -build_flags=--mod=mod -package admin -destination ./mock_monitor.go -source=../../internal/monitoring/interfaces.go
//go:generate mockgen -build_flags=--mod=mod -package admin -destination ./mock_tracing.go -source=../../internal/tracing/interfaces.go

const (
	tenantA   = "11111111-1111-4111-8111-111111111111"
	tenantB   = "22222222-2222-4222-8222-222222222222"
	adminID   = "6c2f1a8e-1f0a-4c3b-9d7e-2b1d6f0e4a11"
	connA     = "0190a0b0-0000-7000-8000-00000000000a"
	connB     = "0190a0b0-0000-7000-8000-00000000000b"
	publicURL = "https://sso.example"
	redirectA = publicURL + "/callback/" + connA
)

var (
	now      = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	testedAt = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
)

// setupLoggerMock configures a MockLoggerInterface with AnyTimes() stubs for all
// structured logging methods (w-suffix) and for the security logger.
func setupLoggerMock(ctrl *gomock.Controller, mockLogger *MockLoggerInterface) *MockSecurityLoggerInterface {
	mockSecurityLogger := NewMockSecurityLoggerInterface(ctrl)
	mockLogger.EXPECT().Debugw(gomock.Any(), gomock.Any()).AnyTimes()
	mockLogger.EXPECT().Infow(gomock.Any(), gomock.Any()).AnyTimes()
	mockLogger.EXPECT().Errorw(gomock.Any(), gomock.Any()).AnyTimes()
	mockLogger.EXPECT().Warnw(gomock.Any(), gomock.Any()).AnyTimes()
	mockLogger.EXPECT().Security().Return(mockSecurityLogger).AnyTimes()
	return mockSecurityLogger
}

type serviceMocks struct {
	storage  *MockStorageInterface
	tenants  *MockTenantsClientInterface
	idp      *MockIdPClientInterface
	envelope *MockEnvelopeInterface
	monitor  *MockMonitorInterface

	// actions are the admin actions recorded, as "<actor> <action> <resource>".
	actions []string
}

type txKey struct{}

// inTx matches the context of an open storage transaction.
var inTx = gomock.Cond(func(ctx context.Context) bool { return ctx.Value(txKey{}) != nil })

// newTestService returns a Service on mocks whose span is expected to be
// spanName, and whose clock stands at now. WithTx runs its function with a
// context inTx matches.
func newTestService(t *testing.T, spanName string) (*Service, *serviceMocks) {
	t.Helper()
	ctrl := gomock.NewController(t)

	mocks := &serviceMocks{
		storage:  NewMockStorageInterface(ctrl),
		tenants:  NewMockTenantsClientInterface(ctrl),
		idp:      NewMockIdPClientInterface(ctrl),
		envelope: NewMockEnvelopeInterface(ctrl),
		monitor:  NewMockMonitorInterface(ctrl),
	}
	mockTracer := NewMockTracingInterface(ctrl)
	mockLogger := NewMockLoggerInterface(ctrl)
	setupLoggerMock(ctrl, mockLogger).EXPECT().AdminAction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Do(
		func(actor, action, _, resource string, _ ...logging.Option) {
			mocks.actions = append(mocks.actions, actor+" "+action+" "+resource)
		}).AnyTimes()

	s := NewService(mocks.storage, mocks.tenants, mocks.idp, mocks.envelope, publicURL, mockTracer, mocks.monitor, mockLogger)
	s.now = func() time.Time { return now }

	// The span's context is the caller's: it carries who is calling.
	mockTracer.EXPECT().Start(gomock.Any(), spanName).DoAndReturn(
		func(ctx context.Context, _ string, _ ...trace.SpanStartOption) (context.Context, trace.Span) {
			return ctx, trace.SpanFromContext(ctx)
		})
	mocks.storage.EXPECT().WithTx(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(context.Context) error) error {
			return fn(context.WithValue(ctx, txKey{}, true))
		}).AnyTimes()

	return s, mocks
}

func asAdmin() context.Context {
	return authentication.WithUserID(context.Background(), adminID)
}

func connectionOf(owner string, tested bool) *types.Connection {
	c := &types.Connection{ID: connA, OwnerTenantID: owner, Label: "Acme", ClientSecret: []byte("sealed")}
	if tested {
		c.TestedAt = &testedAt
	}
	return c
}

func unavailable() error {
	return fmt.Errorf("%w: dial tcp 10.1.2.3:50051", tenants.ErrUnavailable)
}

func TestService_ListConnections(t *testing.T) {
	dbErr := errors.New("db error")
	tokenA := base64.RawURLEncoding.EncodeToString([]byte(connA))

	testCases := []struct {
		name          string
		owner         string
		pageToken     string
		pageSize      int
		setupMocks    func(*serviceMocks)
		expectedCount int
		expectedToken string
		expectedErr   error
	}{
		{
			name:  "default page size",
			owner: tenantA,
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().ListConnections(gomock.Any(), tenantA, "", defaultPageSize+1).
					Return([]*types.Connection{{ID: connA}}, nil)
			},
			expectedCount: 1,
		},
		{
			name:     "more pages",
			owner:    tenantA,
			pageSize: 1,
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().ListConnections(gomock.Any(), tenantA, "", 2).
					Return([]*types.Connection{{ID: connA}, {ID: connB}}, nil)
			},
			expectedCount: 1,
			expectedToken: tokenA,
		},
		{
			name:      "next page",
			owner:     tenantA,
			pageToken: tokenA,
			pageSize:  1,
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().ListConnections(gomock.Any(), tenantA, connA, 2).
					Return([]*types.Connection{{ID: connB}}, nil)
			},
			expectedCount: 1,
		},
		{
			// The id reaches the database in its canonical form.
			name:      "page token with the id as a URN",
			owner:     tenantA,
			pageToken: base64.RawURLEncoding.EncodeToString([]byte("urn:uuid:" + connA)),
			pageSize:  1,
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().ListConnections(gomock.Any(), tenantA, connA, 2).
					Return([]*types.Connection{{ID: connB}}, nil)
			},
			expectedCount: 1,
		},
		{
			name: "every owner",
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().ListConnections(gomock.Any(), "", "", defaultPageSize+1).
					Return([]*types.Connection{{ID: connA}, {ID: connB}}, nil)
			},
			expectedCount: 2,
		},
		{
			name:        "page token not base64",
			owner:       tenantA,
			pageToken:   "!",
			setupMocks:  func(m *serviceMocks) {},
			expectedErr: ErrInvalidPageToken,
		},
		{
			name:        "page token not an id",
			owner:       tenantA,
			pageToken:   base64.RawURLEncoding.EncodeToString([]byte("not-a-uuid")),
			setupMocks:  func(m *serviceMocks) {},
			expectedErr: ErrInvalidPageToken,
		},
		{
			name:  "storage error",
			owner: tenantA,
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().ListConnections(gomock.Any(), tenantA, "", defaultPageSize+1).Return(nil, dbErr)
			},
			expectedErr: dbErr,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s, mocks := newTestService(t, "admin.Service.ListConnections")
			tc.setupMocks(mocks)

			connections, nextPageToken, err := s.ListConnections(context.Background(), tc.owner, tc.pageToken, tc.pageSize)

			if !errors.Is(err, tc.expectedErr) {
				t.Fatalf("expected error %v, got %v", tc.expectedErr, err)
			}
			if len(connections) != tc.expectedCount || nextPageToken != tc.expectedToken {
				t.Errorf("expected %d connections and token %q, got %d and %q",
					tc.expectedCount, tc.expectedToken, len(connections), nextPageToken)
			}
		})
	}
}

func TestService_CreateConnection(t *testing.T) {
	dbErr := errors.New("db error")
	encryptErr := errors.New("encrypt error")
	issuerErr := errors.New("the IdP URL is not https")

	// stores expects the connection stored as asked for, by createdBy, with
	// its secret encrypted under the connection's own id.
	stores := func(m *serviceMocks, createdBy string) {
		var id string
		m.envelope.EXPECT().Encrypt("secret", gomock.Any()).DoAndReturn(func(_, associated string) ([]byte, error) {
			id = associated
			return []byte("sealed"), nil
		})
		m.storage.EXPECT().CreateConnection(gomock.Any(), gomock.Any(), limits.MaxConnectionsPerTenant).DoAndReturn(
			func(_ context.Context, c *types.Connection, _ int) (*types.Connection, error) {
				want := &types.Connection{
					ID: id, OwnerTenantID: tenantA, Label: "Acme", Issuer: "https://idp.example", ClientID: "client",
					ClientSecret: []byte("sealed"), CreatedBy: createdBy,
				}
				if id == "" || fmt.Sprintf("%+v", c) != fmt.Sprintf("%+v", want) {
					return nil, fmt.Errorf("stored %+v, expected %+v", c, want)
				}
				return c, nil
			})
	}

	testCases := []struct {
		name        string
		ctx         context.Context
		setupMocks  func(*serviceMocks)
		expectedErr error
	}{
		{
			name: "success",
			ctx:  asAdmin(),
			setupMocks: func(m *serviceMocks) {
				m.tenants.EXPECT().GetTenantSSOPolicy(gomock.Any(), tenantA).Return(&v0tenant.TenantSSOPolicy{}, nil)
				m.idp.EXPECT().ValidateIssuer("https://idp.example").Return(nil)
				stores(m, adminID)
			},
		},
		{
			name: "created by a client",
			ctx:  authentication.WithUserID(context.Background(), "some-client"),
			setupMocks: func(m *serviceMocks) {
				m.tenants.EXPECT().GetTenantSSOPolicy(gomock.Any(), tenantA).Return(&v0tenant.TenantSSOPolicy{}, nil)
				m.idp.EXPECT().ValidateIssuer("https://idp.example").Return(nil)
				stores(m, "")
			},
		},
		{
			name: "connection limit",
			ctx:  asAdmin(),
			setupMocks: func(m *serviceMocks) {
				m.tenants.EXPECT().GetTenantSSOPolicy(gomock.Any(), tenantA).Return(&v0tenant.TenantSSOPolicy{}, nil)
				m.idp.EXPECT().ValidateIssuer(gomock.Any()).Return(nil)
				m.envelope.EXPECT().Encrypt("secret", gomock.Any()).Return([]byte("sealed"), nil)
				m.storage.EXPECT().CreateConnection(gomock.Any(), gomock.Any(), limits.MaxConnectionsPerTenant).
					Return(nil, storage.ErrConnectionLimit)
			},
			expectedErr: ErrConnectionLimit,
		},
		{
			name: "personal tenant",
			ctx:  asAdmin(),
			setupMocks: func(m *serviceMocks) {
				m.tenants.EXPECT().GetTenantSSOPolicy(gomock.Any(), tenantA).Return(nil, tenants.ErrPersonalTenant)
			},
			expectedErr: tenants.ErrPersonalTenant,
		},
		{
			name: "tenant not found",
			ctx:  asAdmin(),
			setupMocks: func(m *serviceMocks) {
				m.tenants.EXPECT().GetTenantSSOPolicy(gomock.Any(), tenantA).Return(nil, tenants.ErrTenantNotFound)
			},
			expectedErr: tenants.ErrTenantNotFound,
		},
		{
			name: "tenant-service unavailable",
			ctx:  asAdmin(),
			setupMocks: func(m *serviceMocks) {
				m.tenants.EXPECT().GetTenantSSOPolicy(gomock.Any(), tenantA).Return(nil, unavailable())
			},
			expectedErr: tenants.ErrUnavailable,
		},
		{
			name: "invalid issuer",
			ctx:  asAdmin(),
			setupMocks: func(m *serviceMocks) {
				m.tenants.EXPECT().GetTenantSSOPolicy(gomock.Any(), tenantA).Return(&v0tenant.TenantSSOPolicy{}, nil)
				m.idp.EXPECT().ValidateIssuer("https://idp.example").Return(issuerErr)
			},
			expectedErr: ErrInvalidIssuer,
		},
		{
			name: "encrypt error",
			ctx:  asAdmin(),
			setupMocks: func(m *serviceMocks) {
				m.tenants.EXPECT().GetTenantSSOPolicy(gomock.Any(), tenantA).Return(&v0tenant.TenantSSOPolicy{}, nil)
				m.idp.EXPECT().ValidateIssuer(gomock.Any()).Return(nil)
				m.envelope.EXPECT().Encrypt("secret", gomock.Any()).Return(nil, encryptErr)
			},
			expectedErr: encryptErr,
		},
		{
			name: "storage error",
			ctx:  asAdmin(),
			setupMocks: func(m *serviceMocks) {
				m.tenants.EXPECT().GetTenantSSOPolicy(gomock.Any(), tenantA).Return(&v0tenant.TenantSSOPolicy{}, nil)
				m.idp.EXPECT().ValidateIssuer(gomock.Any()).Return(nil)
				m.envelope.EXPECT().Encrypt("secret", gomock.Any()).Return([]byte("sealed"), nil)
				m.storage.EXPECT().CreateConnection(gomock.Any(), gomock.Any(), limits.MaxConnectionsPerTenant).Return(nil, dbErr)
			},
			expectedErr: dbErr,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s, mocks := newTestService(t, "admin.Service.CreateConnection")
			tc.setupMocks(mocks)

			created, err := s.CreateConnection(tc.ctx, tenantA, "Acme", "https://idp.example", "client", "secret")

			if !errors.Is(err, tc.expectedErr) {
				t.Fatalf("expected error %v, got %v", tc.expectedErr, err)
			}
			if tc.expectedErr == nil && (created == nil || created.Tested()) {
				t.Errorf("expected a draft connection, got %+v", created)
			}
		})
	}
}

func TestService_GetConnection(t *testing.T) {
	dbErr := errors.New("db error")

	testCases := []struct {
		name        string
		setupMocks  func(*serviceMocks)
		expectedErr error
	}{
		{
			name: "success",
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(connectionOf(tenantA, true), nil)
			},
		},
		{
			name: "another tenant's connection",
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(connectionOf(tenantB, true), nil)
			},
			expectedErr: ErrConnectionNotFound,
		},
		{
			name: "not found",
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(nil, storage.ErrNotFound)
			},
			expectedErr: ErrConnectionNotFound,
		},
		{
			name: "storage error",
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(nil, dbErr)
			},
			expectedErr: dbErr,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s, mocks := newTestService(t, "admin.Service.GetConnection")
			tc.setupMocks(mocks)

			connection, err := s.GetConnection(context.Background(), tenantA, connA)

			if !errors.Is(err, tc.expectedErr) {
				t.Fatalf("expected error %v, got %v", tc.expectedErr, err)
			}
			if tc.expectedErr == nil && connection.ID != connA {
				t.Errorf("expected connection %s, got %+v", connA, connection)
			}
		})
	}
}

func TestService_UpdateConnection(t *testing.T) {
	dbErr := errors.New("db error")
	encryptErr := errors.New("encrypt error")
	label, secret := "New", "new-secret"

	testCases := []struct {
		name         string
		label        *string
		clientSecret *string
		setupMocks   func(*serviceMocks)
		expectedErr  error
	}{
		{
			name:  "label",
			label: &label,
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(connectionOf(tenantA, true), nil)
				m.storage.EXPECT().UpdateConnection(gomock.Any(), connA, storage.ConnectionUpdate{Label: &label}).
					Return(connectionOf(tenantA, true), nil)
			},
		},
		{
			name:         "client secret",
			clientSecret: &secret,
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(connectionOf(tenantA, true), nil)
				m.envelope.EXPECT().Encrypt("new-secret", connA).Return([]byte("resealed"), nil)
				m.storage.EXPECT().UpdateConnection(gomock.Any(), connA,
					storage.ConnectionUpdate{ClientSecret: []byte("resealed")}).
					Return(connectionOf(tenantA, true), nil)
			},
		},
		{
			name:         "label and client secret",
			label:        &label,
			clientSecret: &secret,
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(connectionOf(tenantA, true), nil)
				m.envelope.EXPECT().Encrypt("new-secret", connA).Return([]byte("resealed"), nil)
				m.storage.EXPECT().UpdateConnection(gomock.Any(), connA,
					storage.ConnectionUpdate{Label: &label, ClientSecret: []byte("resealed")}).
					Return(connectionOf(tenantA, true), nil)
			},
		},
		{
			name:  "another tenant's connection",
			label: &label,
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(connectionOf(tenantB, true), nil)
			},
			expectedErr: ErrConnectionNotFound,
		},
		{
			name:  "deleted meanwhile",
			label: &label,
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(connectionOf(tenantA, true), nil)
				m.storage.EXPECT().UpdateConnection(gomock.Any(), connA, gomock.Any()).Return(nil, storage.ErrNotFound)
			},
			expectedErr: ErrConnectionNotFound,
		},
		{
			name:         "encrypt error",
			clientSecret: &secret,
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(connectionOf(tenantA, true), nil)
				m.envelope.EXPECT().Encrypt("new-secret", connA).Return(nil, encryptErr)
			},
			expectedErr: encryptErr,
		},
		{
			name:  "storage error",
			label: &label,
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(connectionOf(tenantA, true), nil)
				m.storage.EXPECT().UpdateConnection(gomock.Any(), connA, gomock.Any()).Return(nil, dbErr)
			},
			expectedErr: dbErr,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s, mocks := newTestService(t, "admin.Service.UpdateConnection")
			tc.setupMocks(mocks)

			updated, err := s.UpdateConnection(asAdmin(), tenantA, connA, tc.label, tc.clientSecret)

			if !errors.Is(err, tc.expectedErr) {
				t.Fatalf("expected error %v, got %v", tc.expectedErr, err)
			}
			if tc.expectedErr == nil && updated == nil {
				t.Error("expected the updated connection")
			}
		})
	}
}

// deletes expects the connection locked, unbound from owner's policy and
// deleted, in that order and in one transaction.
func deletes(m *serviceMocks, owner string, unbindErr error) {
	gomock.InOrder(
		m.storage.EXPECT().LockConnections(inTx, []string{connA}).Return([]*types.Connection{connectionOf(owner, true)}, nil),
		m.tenants.EXPECT().RemoveTenantSSOBinding(inTx, owner, connA).Return(unbindErr),
		m.storage.EXPECT().DeleteConnection(inTx, connA).Return(nil),
	)
}

// unbindFails expects the connection locked and the binding not removed:
// nothing is deleted.
func unbindFails(m *serviceMocks, owner string, unbindErr error) {
	gomock.InOrder(
		m.storage.EXPECT().LockConnections(inTx, []string{connA}).Return([]*types.Connection{connectionOf(owner, true)}, nil),
		m.tenants.EXPECT().RemoveTenantSSOBinding(inTx, owner, connA).Return(unbindErr),
	)
}

func TestService_DeleteConnection(t *testing.T) {
	dbErr := errors.New("db error")

	testCases := []struct {
		name        string
		setupMocks  func(*serviceMocks)
		expectedErr error
		// expectedActions are the admin actions recorded: none for a delete
		// that did not happen.
		expectedActions []string
	}{
		{
			name:            "success",
			setupMocks:      func(m *serviceMocks) { deletes(m, tenantA, nil) },
			expectedActions: []string{adminID + " delete_connection " + connA},
		},
		{
			name: "another tenant's connection",
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().LockConnections(inTx, []string{connA}).Return([]*types.Connection{connectionOf(tenantB, true)}, nil)
			},
			expectedErr: ErrConnectionNotFound,
		},
		{
			name: "not found",
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().LockConnections(inTx, []string{connA}).Return([]*types.Connection{}, nil)
			},
			expectedErr: ErrConnectionNotFound,
		},
		{
			name:        "required needs an active binding",
			setupMocks:  func(m *serviceMocks) { unbindFails(m, tenantA, tenants.ErrRequiredNeedsActiveBinding) },
			expectedErr: tenants.ErrRequiredNeedsActiveBinding,
		},
		{
			name:        "tenant not found",
			setupMocks:  func(m *serviceMocks) { unbindFails(m, tenantA, tenants.ErrTenantNotFound) },
			expectedErr: tenants.ErrTenantNotFound,
		},
		{
			name: "tenant-service unavailable",
			setupMocks: func(m *serviceMocks) {
				unbindFails(m, tenantA, unavailable())
				m.monitor.EXPECT().IncrementTenantServiceWriteFailures(map[string]string{"rpc": "RemoveTenantSSOBinding"}).Return(nil)
			},
			expectedErr: tenants.ErrUnavailable,
		},
		{
			name: "row locked by another request",
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().LockConnections(inTx, []string{connA}).Return(nil, storage.ErrBusy)
			},
			expectedErr: storage.ErrBusy,
		},
		{
			name: "storage error",
			setupMocks: func(m *serviceMocks) {
				gomock.InOrder(
					m.storage.EXPECT().LockConnections(inTx, []string{connA}).Return([]*types.Connection{connectionOf(tenantA, true)}, nil),
					m.tenants.EXPECT().RemoveTenantSSOBinding(inTx, tenantA, connA).Return(nil),
					m.storage.EXPECT().DeleteConnection(inTx, connA).Return(dbErr),
				)
			},
			expectedErr: dbErr,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s, mocks := newTestService(t, "admin.Service.DeleteConnection")
			tc.setupMocks(mocks)

			err := s.DeleteConnection(asAdmin(), tenantA, connA)

			if !errors.Is(err, tc.expectedErr) {
				t.Fatalf("expected error %v, got %v", tc.expectedErr, err)
			}
			if !reflect.DeepEqual(mocks.actions, tc.expectedActions) {
				t.Errorf("expected the admin actions %q, got %q", tc.expectedActions, mocks.actions)
			}
		})
	}
}

func TestService_DeleteAnyConnection(t *testing.T) {
	testCases := []struct {
		name        string
		setupMocks  func(*serviceMocks)
		expectedErr error
	}{
		{
			name:       "any owner's connection",
			setupMocks: func(m *serviceMocks) { deletes(m, tenantB, nil) },
		},
		{
			name:       "owner no longer exists",
			setupMocks: func(m *serviceMocks) { deletes(m, tenantB, tenants.ErrTenantNotFound) },
		},
		{
			name: "not found",
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().LockConnections(inTx, []string{connA}).Return([]*types.Connection{}, nil)
			},
			expectedErr: ErrConnectionNotFound,
		},
		{
			name:        "required needs an active binding",
			setupMocks:  func(m *serviceMocks) { unbindFails(m, tenantB, tenants.ErrRequiredNeedsActiveBinding) },
			expectedErr: tenants.ErrRequiredNeedsActiveBinding,
		},
		{
			name: "tenant-service unavailable",
			setupMocks: func(m *serviceMocks) {
				unbindFails(m, tenantB, unavailable())
				m.monitor.EXPECT().IncrementTenantServiceWriteFailures(map[string]string{"rpc": "RemoveTenantSSOBinding"}).Return(nil)
			},
			expectedErr: tenants.ErrUnavailable,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s, mocks := newTestService(t, "admin.Service.DeleteAnyConnection")
			tc.setupMocks(mocks)

			err := s.DeleteAnyConnection(asAdmin(), connA)

			if !errors.Is(err, tc.expectedErr) {
				t.Fatalf("expected error %v, got %v", tc.expectedErr, err)
			}
		})
	}
}

func TestService_StartTestLogin(t *testing.T) {
	decryptErr := errors.New("decrypt error")
	sealErr := errors.New("seal error")
	connection := connectionOf(tenantA, false)

	// checked expects the connection read, its secret decrypted and the
	// identity provider checked with them.
	checked := func(m *serviceMocks, checkErr error) {
		m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(connection, nil)
		m.envelope.EXPECT().Decrypt([]byte("sealed"), connA).Return("secret", nil)
		m.idp.EXPECT().Check(gomock.Any(), connection, "secret", redirectA).Return(checkErr)
	}

	testCases := []struct {
		name        string
		setupMocks  func(*serviceMocks)
		expectedURL string
		expectedErr error
	}{
		{
			name: "success",
			setupMocks: func(m *serviceMocks) {
				checked(m, nil)
				var test *types.TestState
				m.envelope.EXPECT().Seal(types.PurposeTestState, gomock.Any()).DoAndReturn(func(_ string, v any) (string, error) {
					test = v.(*types.TestState)
					if test.ConnectionID != connA || test.Nonce == "" || test.PKCEVerifier == "" || test.Nonce == test.PKCEVerifier ||
						test.ExpiresAt != now.Add(limits.AttemptTTL).Unix() {
						return "", fmt.Errorf("unexpected test state %+v", test)
					}
					return "sealed-state", nil
				})
				m.idp.EXPECT().AuthCodeURL(gomock.Any(), connection, gomock.Any()).DoAndReturn(
					func(_ context.Context, _ *types.Connection, r *idp.AuthRequest) (string, error) {
						want := idp.AuthRequest{RedirectURI: redirectA, State: "sealed-state", Nonce: test.Nonce,
							PKCEVerifier: test.PKCEVerifier, Reauthenticate: true}
						if *r != want {
							return "", fmt.Errorf("unexpected authorization request %+v", r)
						}
						return "https://idp.example/authorize", nil
					})
			},
			expectedURL: "https://idp.example/authorize",
		},
		{
			name: "another tenant's connection",
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(connectionOf(tenantB, false), nil)
			},
			expectedErr: ErrConnectionNotFound,
		},
		{
			name: "decrypt error",
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(connection, nil)
				m.envelope.EXPECT().Decrypt([]byte("sealed"), connA).Return("", decryptErr)
			},
			expectedErr: ErrClientSecretUnreadable,
		},
		{
			name:        "client credentials refused",
			setupMocks:  func(m *serviceMocks) { checked(m, idp.ErrCredentials) },
			expectedErr: ErrIdPCheckFailed,
		},
		{
			name: "seal error",
			setupMocks: func(m *serviceMocks) {
				checked(m, nil)
				m.envelope.EXPECT().Seal(types.PurposeTestState, gomock.Any()).Return("", sealErr)
			},
			expectedErr: sealErr,
		},
		{
			name: "no authorization request",
			setupMocks: func(m *serviceMocks) {
				checked(m, nil)
				m.envelope.EXPECT().Seal(types.PurposeTestState, gomock.Any()).Return("sealed-state", nil)
				m.idp.EXPECT().AuthCodeURL(gomock.Any(), connection, gomock.Any()).Return("", idp.ErrMisconfigured)
			},
			expectedErr: ErrIdPCheckFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s, mocks := newTestService(t, "admin.Service.StartTestLogin")
			tc.setupMocks(mocks)

			url, err := s.StartTestLogin(asAdmin(), tenantA, connA)

			if !errors.Is(err, tc.expectedErr) {
				t.Fatalf("expected error %v, got %v", tc.expectedErr, err)
			}
			if url != tc.expectedURL {
				t.Errorf("expected URL %q, got %q", tc.expectedURL, url)
			}
		})
	}

	t.Run("the identity provider's error stays readable", func(t *testing.T) {
		s, mocks := newTestService(t, "admin.Service.StartTestLogin")
		checked(mocks, idp.ErrCredentials)

		_, err := s.StartTestLogin(asAdmin(), tenantA, connA)

		if !errors.Is(err, idp.ErrCredentials) {
			t.Errorf("expected the error to wrap %v, got %v", idp.ErrCredentials, err)
		}
	})
}

func TestService_GetTenantSSOPolicy(t *testing.T) {
	policy := &v0tenant.TenantSSOPolicy{TenantId: tenantA, Enforcement: v0tenant.Enforcement_ENFORCEMENT_OPTIONAL}

	testCases := []struct {
		name        string
		setupMocks  func(*serviceMocks)
		expectedErr error
	}{
		{
			name: "success",
			setupMocks: func(m *serviceMocks) {
				m.tenants.EXPECT().GetTenantSSOPolicy(gomock.Any(), tenantA).Return(policy, nil)
			},
		},
		{
			name: "personal tenant",
			setupMocks: func(m *serviceMocks) {
				m.tenants.EXPECT().GetTenantSSOPolicy(gomock.Any(), tenantA).Return(nil, tenants.ErrPersonalTenant)
			},
			expectedErr: tenants.ErrPersonalTenant,
		},
		{
			name: "tenant-service unavailable",
			setupMocks: func(m *serviceMocks) {
				m.tenants.EXPECT().GetTenantSSOPolicy(gomock.Any(), tenantA).Return(nil, unavailable())
			},
			expectedErr: tenants.ErrUnavailable,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s, mocks := newTestService(t, "admin.Service.GetTenantSSOPolicy")
			tc.setupMocks(mocks)

			got, err := s.GetTenantSSOPolicy(context.Background(), tenantA)

			if !errors.Is(err, tc.expectedErr) {
				t.Fatalf("expected error %v, got %v", tc.expectedErr, err)
			}
			if tc.expectedErr == nil && got != policy {
				t.Errorf("expected the policy as tenant-service holds it, got %v", got)
			}
		})
	}
}

func TestService_PutTenantSSOPolicy(t *testing.T) {
	required := v0tenant.Enforcement_ENFORCEMENT_REQUIRED
	written := &v0tenant.TenantSSOPolicy{TenantId: tenantA, Enforcement: required}
	active := []*v0tenant.SSOBinding{{ConnectionId: connA, Active: true}}
	inactive := []*v0tenant.SSOBinding{{ConnectionId: connA}}

	// writes expects the bound connections locked, then the policy written
	// to tenant-service while they are, in one transaction.
	writes := func(m *serviceMocks, bindings []*v0tenant.SSOBinding, locked []*types.Connection, writeErr error) {
		ids := make([]string, len(bindings))
		for i, b := range bindings {
			ids[i] = b.ConnectionId
		}
		answer := written
		if writeErr != nil {
			answer = nil
		}
		gomock.InOrder(
			m.storage.EXPECT().LockConnections(inTx, ids).Return(locked, nil),
			m.tenants.EXPECT().PutTenantSSOPolicy(inTx, &v0tenant.PutTenantSSOPolicyRequest{
				TenantId: tenantA, Enforcement: required, AutoJoin: true, Bindings: bindings,
			}).Return(answer, writeErr),
		)
	}

	testCases := []struct {
		name        string
		bindings    []*v0tenant.SSOBinding
		setupMocks  func(*serviceMocks)
		expectedErr error
	}{
		{
			name:     "active binding to a tested connection",
			bindings: active,
			setupMocks: func(m *serviceMocks) {
				writes(m, active, []*types.Connection{connectionOf(tenantA, true)}, nil)
			},
		},
		{
			name:     "inactive binding to a draft",
			bindings: inactive,
			setupMocks: func(m *serviceMocks) {
				writes(m, inactive, []*types.Connection{connectionOf(tenantA, false)}, nil)
			},
		},
		{
			name:     "no bindings",
			bindings: []*v0tenant.SSOBinding{},
			setupMocks: func(m *serviceMocks) {
				writes(m, []*v0tenant.SSOBinding{}, []*types.Connection{}, nil)
			},
		},
		{
			name:     "active binding to a draft",
			bindings: active,
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().LockConnections(inTx, []string{connA}).Return([]*types.Connection{connectionOf(tenantA, false)}, nil)
			},
			expectedErr: ErrConnectionNotTested,
		},
		{
			name:     "another tenant's connection",
			bindings: active,
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().LockConnections(inTx, []string{connA}).Return([]*types.Connection{connectionOf(tenantB, true)}, nil)
			},
			expectedErr: ErrConnectionNotFound,
		},
		{
			name:     "connection not found",
			bindings: active,
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().LockConnections(inTx, []string{connA}).Return([]*types.Connection{}, nil)
			},
			expectedErr: ErrConnectionNotFound,
		},
		{
			name:     "refused by tenant-service",
			bindings: active,
			setupMocks: func(m *serviceMocks) {
				writes(m, active, []*types.Connection{connectionOf(tenantA, true)}, tenants.ErrAutoJoinNeedsRequiredAndDomains)
			},
			expectedErr: tenants.ErrAutoJoinNeedsRequiredAndDomains,
		},
		{
			name:     "tenant-service unavailable",
			bindings: active,
			setupMocks: func(m *serviceMocks) {
				writes(m, active, []*types.Connection{connectionOf(tenantA, true)}, unavailable())
				m.monitor.EXPECT().IncrementTenantServiceWriteFailures(map[string]string{"rpc": "PutTenantSSOPolicy"}).Return(nil)
			},
			expectedErr: tenants.ErrUnavailable,
		},
		{
			name:     "row locked by another request",
			bindings: active,
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().LockConnections(inTx, []string{connA}).Return(nil, storage.ErrBusy)
			},
			expectedErr: storage.ErrBusy,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s, mocks := newTestService(t, "admin.Service.PutTenantSSOPolicy")
			tc.setupMocks(mocks)

			got, err := s.PutTenantSSOPolicy(asAdmin(), tenantA, required, true, tc.bindings)

			if !errors.Is(err, tc.expectedErr) {
				t.Fatalf("expected error %v, got %v", tc.expectedErr, err)
			}
			if tc.expectedErr == nil && got != written {
				t.Errorf("expected the policy as tenant-service wrote it, got %v", got)
			}
		})
	}
}

func TestService_GetTenantDomains(t *testing.T) {
	s, mocks := newTestService(t, "admin.Service.GetTenantDomains")
	mocks.tenants.EXPECT().GetTenantSSOPolicy(gomock.Any(), tenantA).
		Return(&v0tenant.TenantSSOPolicy{TenantId: tenantA, Domains: []string{"acme.example"}}, nil)

	got, err := s.GetTenantDomains(context.Background(), tenantA)

	if err != nil || !reflect.DeepEqual(got, []string{"acme.example"}) {
		t.Errorf("expected the stored domains, got %v %v", got, err)
	}
}

func TestService_SetTenantDomains(t *testing.T) {
	domains := []string{"Acme.Example", "hooli.example"}
	written := &v0tenant.TenantSSOPolicy{TenantId: tenantA, Domains: []string{"acme.example", "hooli.example"}}

	testCases := []struct {
		name        string
		setupMocks  func(*serviceMocks)
		expectedErr error
	}{
		{
			name: "forwarded as given",
			setupMocks: func(m *serviceMocks) {
				m.tenants.EXPECT().SetTenantSSODomains(gomock.Any(), tenantA, domains).Return(written, nil)
			},
		},
		{
			name: "refused by tenant-service",
			setupMocks: func(m *serviceMocks) {
				m.tenants.EXPECT().SetTenantSSODomains(gomock.Any(), tenantA, domains).Return(nil, tenants.ErrAutoJoinNeedsRequiredAndDomains)
			},
			expectedErr: tenants.ErrAutoJoinNeedsRequiredAndDomains,
		},
		{
			name: "tenant-service unavailable",
			setupMocks: func(m *serviceMocks) {
				m.tenants.EXPECT().SetTenantSSODomains(gomock.Any(), tenantA, domains).Return(nil, unavailable())
				m.monitor.EXPECT().IncrementTenantServiceWriteFailures(map[string]string{"rpc": "SetTenantSSODomains"}).Return(nil)
			},
			expectedErr: tenants.ErrUnavailable,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s, mocks := newTestService(t, "admin.Service.SetTenantDomains")
			tc.setupMocks(mocks)

			got, err := s.SetTenantDomains(asAdmin(), tenantA, domains)

			if !errors.Is(err, tc.expectedErr) {
				t.Fatalf("expected error %v, got %v", tc.expectedErr, err)
			}
			if tc.expectedErr == nil && got != written {
				t.Errorf("expected the policy as tenant-service wrote it, got %v", got)
			}
		})
	}
}
