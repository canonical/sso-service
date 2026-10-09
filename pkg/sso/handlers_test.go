// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package sso

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"buf.build/go/protovalidate"
	v0sso "github.com/canonical/identity-platform-api/v0/sso"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/canonical/sso-service/internal/apierrors"
	"github.com/canonical/sso-service/internal/storage"
	"github.com/canonical/sso-service/internal/types"
)

//go:generate mockgen -build_flags=--mod=mod -package sso -destination ./mock_sso.go -source=./interfaces.go
//go:generate mockgen -build_flags=--mod=mod -package sso -destination ./mock_logger.go -source=../../internal/logging/interfaces.go
//go:generate mockgen -build_flags=--mod=mod -package sso -destination ./mock_monitor.go -source=../../internal/monitoring/interfaces.go
//go:generate mockgen -build_flags=--mod=mod -package sso -destination ./mock_tracing.go -source=../../internal/tracing/interfaces.go

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

	h := NewHandler(mockSvc, testValidator, mockTracer, mockLogger)

	mockTracer.EXPECT().Start(gomock.Any(), spanName).Return(context.Background(), trace.SpanFromContext(context.Background()))

	return h, mockSvc
}

func TestHandler_ListOptions(t *testing.T) {
	tests := []struct {
		name       string
		request    *v0sso.ListOptionsRequest
		setupMocks func(*MockServiceInterface)
		wantCode   codes.Code
	}{
		{
			name:    "success",
			request: &v0sso.ListOptionsRequest{ConnectionIds: []string{strings.ToUpper(connA), connB}},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().ListOptions(gomock.Any(), []string{connA, connB}).
					Return([]*types.Connection{{ID: connA, Label: "Acme"}}, nil)
			},
			wantCode: codes.OK,
		},
		{
			name:       "invalid connection id",
			request:    &v0sso.ListOptionsRequest{ConnectionIds: []string{"not-a-uuid"}},
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name:    "service error",
			request: &v0sso.ListOptionsRequest{ConnectionIds: []string{connA}},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().ListOptions(gomock.Any(), []string{connA}).Return(nil, errors.New("service error"))
			},
			wantCode: codes.Internal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, mockSvc := newTestHandler(t, "sso.Handler.ListOptions")
			tt.setupMocks(mockSvc)

			resp, err := h.ListOptions(context.Background(), tt.request)

			if status.Code(err) != tt.wantCode {
				t.Fatalf("expected code %v, got %v", tt.wantCode, err)
			}
			if tt.wantCode != codes.OK {
				return
			}
			if len(resp.Options) != 1 || resp.Options[0].ConnectionId != connA || resp.Options[0].Label != "Acme" {
				t.Errorf("unexpected options: %v", resp.Options)
			}
		})
	}
}

func TestHandler_StartAttempt(t *testing.T) {
	tests := []struct {
		name       string
		request    *v0sso.StartAttemptRequest
		setupMocks func(*MockServiceInterface)
		wantCode   codes.Code
		wantReason string
	}{
		{
			name: "success",
			request: &v0sso.StartAttemptRequest{
				TenantId:       strings.ToUpper(tenantID),
				Email:          "Alice@Test.Example",
				ConnectionId:   strings.ToUpper(connA),
				Reauthenticate: true,
			},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().StartAttempt(gomock.Any(), tenantID, email, connA, true).Return("sealed", nil)
			},
			wantCode: codes.OK,
		},
		{
			name:       "invalid email",
			request:    &v0sso.StartAttemptRequest{TenantId: tenantID, Email: "alice", ConnectionId: connA},
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name:    "connection not usable",
			request: &v0sso.StartAttemptRequest{TenantId: tenantID, Email: email, ConnectionId: connA},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().StartAttempt(gomock.Any(), tenantID, email, connA, false).Return("", ErrConnectionNotUsable)
			},
			wantCode:   codes.FailedPrecondition,
			wantReason: apierrors.NotApplicable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, mockSvc := newTestHandler(t, "sso.Handler.StartAttempt")
			tt.setupMocks(mockSvc)

			resp, err := h.StartAttempt(context.Background(), tt.request)

			if status.Code(err) != tt.wantCode || apierrors.Reason(err) != tt.wantReason {
				t.Fatalf("expected code %v and reason %q, got %v", tt.wantCode, tt.wantReason, err)
			}
			if tt.wantCode == codes.OK && resp.Ticket != "sealed" {
				t.Errorf("expected the ticket, got %q", resp.Ticket)
			}
		})
	}
}

func TestHandler_CompleteAttempt(t *testing.T) {
	tests := []struct {
		name       string
		request    *v0sso.CompleteAttemptRequest
		setupMocks func(*MockServiceInterface)
		wantCode   codes.Code
		wantReason string
	}{
		{
			name:    "success",
			request: &v0sso.CompleteAttemptRequest{Ticket: "sealed", IdentityId: strings.ToUpper(identityID), Receipt: "Receipt"},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().CompleteAttempt(gomock.Any(), "sealed", identityID, "Receipt").
					Return(&types.Ticket{ConnectionID: connA, TenantID: tenantID}, nil)
			},
			wantCode: codes.OK,
		},
		{
			name:       "invalid identity id",
			request:    &v0sso.CompleteAttemptRequest{Ticket: "sealed", IdentityId: "alice"},
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name:       "missing ticket",
			request:    &v0sso.CompleteAttemptRequest{IdentityId: identityID},
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name:    "attempt not completed",
			request: &v0sso.CompleteAttemptRequest{Ticket: "sealed", IdentityId: identityID},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().CompleteAttempt(gomock.Any(), "sealed", identityID, "").Return(nil, ErrAttemptNotCompleted)
			},
			wantCode:   codes.FailedPrecondition,
			wantReason: apierrors.NotApplicable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, mockSvc := newTestHandler(t, "sso.Handler.CompleteAttempt")
			tt.setupMocks(mockSvc)

			resp, err := h.CompleteAttempt(context.Background(), tt.request)

			if status.Code(err) != tt.wantCode || apierrors.Reason(err) != tt.wantReason {
				t.Fatalf("expected code %v and reason %q, got %v", tt.wantCode, tt.wantReason, err)
			}
			if tt.wantCode == codes.OK && (resp.ConnectionId != connA || resp.TenantId != tenantID) {
				t.Errorf("unexpected response: %v", resp)
			}
		})
	}
}

func TestHandler_ListLinks(t *testing.T) {
	tests := []struct {
		name       string
		request    *v0sso.ListLinksRequest
		setupMocks func(*MockServiceInterface)
		wantCode   codes.Code
	}{
		{
			name:    "success",
			request: &v0sso.ListLinksRequest{IdentityId: strings.ToUpper(identityID)},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().ListLinks(gomock.Any(), identityID).
					Return([]*types.Connection{{ID: connA, Label: "Acme", OwnerTenantID: tenantID}}, nil)
			},
			wantCode: codes.OK,
		},
		{
			name:       "invalid identity id",
			request:    &v0sso.ListLinksRequest{IdentityId: "alice"},
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name:    "account not found",
			request: &v0sso.ListLinksRequest{IdentityId: identityID},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().ListLinks(gomock.Any(), identityID).Return(nil, ErrAccountNotFound)
			},
			wantCode: codes.NotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, mockSvc := newTestHandler(t, "sso.Handler.ListLinks")
			tt.setupMocks(mockSvc)

			resp, err := h.ListLinks(context.Background(), tt.request)

			if status.Code(err) != tt.wantCode {
				t.Fatalf("expected code %v, got %v", tt.wantCode, err)
			}
			if tt.wantCode != codes.OK {
				return
			}
			if len(resp.Links) != 1 || resp.Links[0].ConnectionId != connA || resp.Links[0].Label != "Acme" || resp.Links[0].TenantId != tenantID {
				t.Errorf("unexpected links: %v", resp.Links)
			}
		})
	}
}

func TestHandler_DeleteLink(t *testing.T) {
	tests := []struct {
		name       string
		request    *v0sso.DeleteLinkRequest
		setupMocks func(*MockServiceInterface)
		wantCode   codes.Code
		wantReason string
	}{
		{
			name:    "success",
			request: &v0sso.DeleteLinkRequest{ConnectionId: strings.ToUpper(connA), IdentityId: strings.ToUpper(identityID)},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().DeleteLink(gomock.Any(), identityID, connA).Return(nil)
			},
			wantCode: codes.OK,
		},
		{
			name:       "invalid connection id",
			request:    &v0sso.DeleteLinkRequest{ConnectionId: "c", IdentityId: identityID},
			setupMocks: func(mockSvc *MockServiceInterface) {},
			wantCode:   codes.InvalidArgument,
		},
		{
			name:    "last credential",
			request: &v0sso.DeleteLinkRequest{ConnectionId: connA, IdentityId: identityID},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().DeleteLink(gomock.Any(), identityID, connA).Return(ErrLastCredential)
			},
			wantCode:   codes.FailedPrecondition,
			wantReason: apierrors.LastCredential,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, mockSvc := newTestHandler(t, "sso.Handler.DeleteLink")
			tt.setupMocks(mockSvc)

			_, err := h.DeleteLink(context.Background(), tt.request)

			if status.Code(err) != tt.wantCode || apierrors.Reason(err) != tt.wantReason {
				t.Fatalf("expected code %v and reason %q, got %v", tt.wantCode, tt.wantReason, err)
			}
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
			name:        "connection not usable",
			err:         ErrConnectionNotUsable,
			wantCode:    codes.FailedPrecondition,
			wantReason:  apierrors.NotApplicable,
			wantMessage: "NOT_APPLICABLE: the connection does not exist or is not tested",
		},
		{
			name:        "attempt not completed",
			err:         ErrAttemptNotCompleted,
			wantCode:    codes.FailedPrecondition,
			wantReason:  apierrors.NotApplicable,
			wantMessage: "NOT_APPLICABLE: the attempt did not end at this account, or has expired",
		},
		{
			name:        "last credential",
			err:         ErrLastCredential,
			wantCode:    codes.FailedPrecondition,
			wantReason:  apierrors.LastCredential,
			wantMessage: "LAST_CREDENTIAL: the account would have no way to sign in of its own; set a password first",
		},
		{
			name:        "account not found",
			err:         ErrAccountNotFound,
			wantCode:    codes.NotFound,
			wantMessage: "no such account",
		},
		{
			name:        "link not found",
			err:         ErrLinkNotFound,
			wantCode:    codes.NotFound,
			wantMessage: "the account has no link at this connection",
		},
		{
			name:        "kratos unavailable",
			err:         fmt.Errorf("%w: dial tcp 10.1.2.3:4434", ErrKratosUnavailable),
			wantCode:    codes.Unavailable,
			wantMessage: "Kratos is unavailable",
		},
		{
			name:        "lock held too long",
			err:         fmt.Errorf("failed to get connections: %w", fmt.Errorf("%w: canceling statement due to lock timeout", storage.ErrBusy)),
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
			h := NewHandler(NewMockServiceInterface(ctrl), testValidator, NewMockTracingInterface(ctrl), mockLogger)

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
		h := NewHandler(NewMockServiceInterface(ctrl), testValidator, NewMockTracingInterface(ctrl), NewMockLoggerInterface(ctrl))
		if err := h.mapErrorToStatus(nil, "test"); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})
}
