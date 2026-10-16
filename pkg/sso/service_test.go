// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package sso

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"
	"go.uber.org/mock/gomock"

	"github.com/canonical/sso-service/internal/kratos"
	"github.com/canonical/sso-service/internal/limits"
	"github.com/canonical/sso-service/internal/secrets"
	"github.com/canonical/sso-service/internal/storage"
	"github.com/canonical/sso-service/internal/types"
)

//go:generate mockgen -build_flags=--mod=mod -package sso -destination ./mock_sso.go -source=./interfaces.go
//go:generate mockgen -build_flags=--mod=mod -package sso -destination ./mock_logger.go -source=../../internal/logging/interfaces.go
//go:generate mockgen -build_flags=--mod=mod -package sso -destination ./mock_monitor.go -source=../../internal/monitoring/interfaces.go
//go:generate mockgen -build_flags=--mod=mod -package sso -destination ./mock_tracing.go -source=../../internal/tracing/interfaces.go

const (
	tenantID   = "11111111-1111-4111-8111-111111111111"
	identityID = "6c2f1a8e-1f0a-4c3b-9d7e-2b1d6f0e4a11"
	connA      = "0190a0b0-0000-7000-8000-00000000000a"
	connB      = "0190a0b0-0000-7000-8000-00000000000b"
	connC      = "0190a0b0-0000-7000-8000-00000000000c"
	email      = "alice@test.example"
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
	mockSecurityLogger.EXPECT().AdminAction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	return mockSecurityLogger
}

type serviceMocks struct {
	storage  *MockStorageInterface
	kratos   *MockKratosClientInterface
	envelope *MockEnvelopeInterface
	monitor  *MockMonitorInterface
	tracer   *MockTracingInterface
}

// newTestService returns a Service on mocks whose span is expected to be
// spanName, and whose clock stands at now.
func newTestService(t *testing.T, spanName string) (*Service, *serviceMocks) {
	t.Helper()
	ctrl := gomock.NewController(t)

	mocks := &serviceMocks{
		storage:  NewMockStorageInterface(ctrl),
		kratos:   NewMockKratosClientInterface(ctrl),
		envelope: NewMockEnvelopeInterface(ctrl),
		monitor:  NewMockMonitorInterface(ctrl),
		tracer:   NewMockTracingInterface(ctrl),
	}
	mockLogger := NewMockLoggerInterface(ctrl)
	setupLoggerMock(ctrl, mockLogger)

	s := NewService(mocks.storage, mocks.kratos, mocks.envelope, mocks.tracer, mocks.monitor, mockLogger)
	s.now = func() time.Time { return now }

	mocks.tracer.EXPECT().Start(gomock.Any(), spanName).Return(context.Background(), trace.SpanFromContext(context.Background()))

	return s, mocks
}

// account is a Kratos identity with the given OIDC providers and other
// credentials.
func account(address, providers string, credentials map[string]string) *kratos.Identity {
	i := &kratos.Identity{
		ID:          identityID,
		Traits:      json.RawMessage(`{"email":"` + address + `"}`),
		Credentials: map[string]kratos.Credential{},
	}
	if providers != "" {
		i.Credentials[kratos.OIDCCredential] = kratos.Credential{Config: json.RawMessage(`{"providers":[` + providers + `]}`)}
	}
	for credentialType, config := range credentials {
		i.Credentials[credentialType] = kratos.Credential{Config: json.RawMessage(config)}
	}
	return i
}

func link(connectionID, subject string) string {
	return `{"provider":"byo-sso","subject":"` + connectionID + `:` + subject + `"}`
}

func connectionIDs(connections []*types.Connection) []string {
	ids := make([]string, len(connections))
	for i, c := range connections {
		ids[i] = c.ID
	}
	return ids
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestService_ListOptions(t *testing.T) {
	dbErr := errors.New("db error")

	testCases := []struct {
		name        string
		ids         []string
		setupMocks  func(*serviceMocks)
		expectedIDs []string
		expectedErr error
	}{
		{
			name: "tested ones in the order asked",
			ids:  []string{connB, connA, connC},
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().GetConnections(gomock.Any(), []string{connB, connA, connC}).Return([]*types.Connection{
					{ID: connA, TestedAt: &testedAt},
					{ID: connB, TestedAt: &testedAt},
					{ID: connC},
				}, nil)
			},
			expectedIDs: []string{connB, connA},
		},
		{
			name: "asked twice, listed once",
			ids:  []string{connA, connA, connB},
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().GetConnections(gomock.Any(), []string{connA, connA, connB}).Return([]*types.Connection{
					{ID: connA, TestedAt: &testedAt},
				}, nil)
			},
			expectedIDs: []string{connA},
		},
		{
			name: "storage error",
			ids:  []string{connA},
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().GetConnections(gomock.Any(), []string{connA}).Return(nil, dbErr)
			},
			expectedErr: dbErr,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s, mocks := newTestService(t, "sso.Service.ListOptions")
			tc.setupMocks(mocks)

			options, err := s.ListOptions(context.Background(), tc.ids)

			if !errors.Is(err, tc.expectedErr) {
				t.Fatalf("expected error %v, got %v", tc.expectedErr, err)
			}
			if got := connectionIDs(options); !equal(got, tc.expectedIDs) {
				t.Errorf("expected %v, got %v", tc.expectedIDs, got)
			}
		})
	}
}

func TestService_StartAttempt(t *testing.T) {
	dbErr := errors.New("db error")
	sealErr := errors.New("seal error")
	tested := &types.Connection{ID: connA, OwnerTenantID: tenantID, TestedAt: &testedAt}
	ticket := &types.Ticket{
		TenantID:       tenantID,
		ConnectionID:   connA,
		Email:          email,
		Reauthenticate: true,
		IssuedAt:       now.Unix(),
		ExpiresAt:      now.Add(limits.AttemptTTL).Unix(),
	}

	testCases := []struct {
		name           string
		setupMocks     func(*serviceMocks)
		expectedTicket string
		expectedErr    error
	}{
		{
			name: "success",
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(tested, nil)
				m.envelope.EXPECT().Seal(types.PurposeTicket, ticket).Return("sealed", nil)
			},
			expectedTicket: "sealed",
		},
		{
			name: "draft connection",
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(&types.Connection{ID: connA}, nil)
			},
			expectedErr: ErrConnectionNotUsable,
		},
		{
			name: "connection not found",
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(nil, storage.ErrNotFound)
			},
			expectedErr: ErrConnectionNotUsable,
		},
		{
			// The tenant named is the one the sign-in is for. A user who
			// proves an account during account linking does it through a
			// connection of another tenant.
			name: "connection of another tenant",
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).
					Return(&types.Connection{ID: connA, OwnerTenantID: "22222222-2222-4222-8222-222222222222", TestedAt: &testedAt}, nil)
				m.envelope.EXPECT().Seal(types.PurposeTicket, ticket).Return("sealed", nil)
			},
			expectedTicket: "sealed",
		},
		{
			name: "storage error",
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(nil, dbErr)
			},
			expectedErr: dbErr,
		},
		{
			name: "seal error",
			setupMocks: func(m *serviceMocks) {
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(tested, nil)
				m.envelope.EXPECT().Seal(types.PurposeTicket, gomock.Any()).Return("", sealErr)
			},
			expectedErr: sealErr,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s, mocks := newTestService(t, "sso.Service.StartAttempt")
			tc.setupMocks(mocks)

			got, err := s.StartAttempt(context.Background(), tenantID, email, connA, true)

			if !errors.Is(err, tc.expectedErr) {
				t.Fatalf("expected error %v, got %v", tc.expectedErr, err)
			}
			if got != tc.expectedTicket {
				t.Errorf("expected ticket %q, got %q", tc.expectedTicket, got)
			}
		})
	}
}

func TestService_CompleteAttempt(t *testing.T) {
	kratosErr := errors.New("connection refused")
	valid := types.Ticket{
		TenantID:     tenantID,
		ConnectionID: connA,
		Email:        email,
		IssuedAt:     now.Add(-time.Minute).Unix(),
		ExpiresAt:    now.Add(time.Minute).Unix(),
	}
	expired := valid
	expired.ExpiresAt = now.Unix()

	opens := func(m *serviceMocks, ticket types.Ticket) *gomock.Call {
		return m.envelope.EXPECT().Open(types.PurposeTicket, "sealed", gomock.Any()).DoAndReturn(
			func(_, _ string, v any) error {
				*v.(*types.Ticket) = ticket
				return nil
			})
	}

	// Receipts are made and checked by a real envelope: the service decides
	// which ticket and subjects one is checked against.
	envelope, err := secrets.NewEnvelope(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatalf("failed to read the envelope key: %v", err)
	}
	receipt := func(ticket, subject string) string {
		made, err := envelope.Receipt(ticket, subject)
		if err != nil {
			t.Fatalf("failed to make the receipt: %v", err)
		}
		return made
	}
	held := receipt("sealed", connA+":s")
	// refusedWithLink is a valid ticket and the account that holds its link:
	// only the receipt is left to refuse.
	refusedWithLink := func(m *serviceMocks) {
		opens(m, valid)
		m.kratos.EXPECT().GetIdentity(gomock.Any(), identityID, kratos.OIDCCredential).
			Return(account(email, link(connA, "s"), nil), nil)
		m.monitor.EXPECT().IncrementCompleteAttemptRefused().Return(nil)
	}

	testCases := []struct {
		name        string
		repeat      bool
		receipt     string
		setupMocks  func(*serviceMocks)
		expectedErr error
	}{
		{
			name:    "account holds the link",
			receipt: held,
			setupMocks: func(m *serviceMocks) {
				opens(m, valid)
				m.kratos.EXPECT().GetIdentity(gomock.Any(), identityID, kratos.OIDCCredential).
					Return(account("Alice@Test.Example", link(connA, "s"), nil), nil)
				m.monitor.EXPECT().IncrementAttemptsCompleted().Return(nil)
			},
		},
		{
			name:    "receipt for the second of two links",
			receipt: receipt("sealed", connA+":t"),
			setupMocks: func(m *serviceMocks) {
				opens(m, valid)
				m.kratos.EXPECT().GetIdentity(gomock.Any(), identityID, kratos.OIDCCredential).
					Return(account(email, link(connA, "s")+","+link(connA, "t"), nil), nil)
				m.monitor.EXPECT().IncrementAttemptsCompleted().Return(nil)
			},
		},
		{
			name:    "repeated",
			repeat:  true,
			receipt: held,
			setupMocks: func(m *serviceMocks) {
				m.tracer.EXPECT().Start(gomock.Any(), "sso.Service.CompleteAttempt").
					Return(context.Background(), trace.SpanFromContext(context.Background()))
				opens(m, valid).Times(2)
				m.kratos.EXPECT().GetIdentity(gomock.Any(), identityID, kratos.OIDCCredential).
					Return(account(email, link(connA, "s"), nil), nil).Times(2)
				m.monitor.EXPECT().IncrementAttemptsCompleted().Return(nil).Times(2)
			},
		},
		{
			name:    "unreadable ticket",
			receipt: held,
			setupMocks: func(m *serviceMocks) {
				m.envelope.EXPECT().Open(types.PurposeTicket, "sealed", gomock.Any()).Return(secrets.ErrUnsealable)
				m.monitor.EXPECT().IncrementCompleteAttemptRefused().Return(nil)
			},
			expectedErr: ErrAttemptNotCompleted,
		},
		{
			name:    "expired ticket",
			receipt: held,
			setupMocks: func(m *serviceMocks) {
				opens(m, expired)
				m.monitor.EXPECT().IncrementCompleteAttemptRefused().Return(nil)
			},
			expectedErr: ErrAttemptNotCompleted,
		},
		{
			name:    "account not found",
			receipt: held,
			setupMocks: func(m *serviceMocks) {
				opens(m, valid)
				m.kratos.EXPECT().GetIdentity(gomock.Any(), identityID, kratos.OIDCCredential).Return(nil, kratos.ErrNotFound)
				m.monitor.EXPECT().IncrementCompleteAttemptRefused().Return(nil)
			},
			expectedErr: ErrAttemptNotCompleted,
		},
		{
			name:    "another address",
			receipt: held,
			setupMocks: func(m *serviceMocks) {
				opens(m, valid)
				m.kratos.EXPECT().GetIdentity(gomock.Any(), identityID, kratos.OIDCCredential).
					Return(account("bob@test.example", link(connA, "s"), nil), nil)
				m.monitor.EXPECT().IncrementCompleteAttemptRefused().Return(nil)
			},
			expectedErr: ErrAttemptNotCompleted,
		},
		{
			name:    "no link to the connection",
			receipt: held,
			setupMocks: func(m *serviceMocks) {
				opens(m, valid)
				m.kratos.EXPECT().GetIdentity(gomock.Any(), identityID, kratos.OIDCCredential).
					Return(account(email, link(connB, "s"), nil), nil)
				m.monitor.EXPECT().IncrementCompleteAttemptRefused().Return(nil)
			},
			expectedErr: ErrAttemptNotCompleted,
		},
		{
			name:        "no receipt",
			setupMocks:  refusedWithLink,
			expectedErr: ErrAttemptNotCompleted,
		},
		{
			name:        "receipt for another ticket",
			receipt:     receipt("another", connA+":s"),
			setupMocks:  refusedWithLink,
			expectedErr: ErrAttemptNotCompleted,
		},
		{
			name:        "receipt for a subject the account does not hold",
			receipt:     receipt("sealed", connA+":t"),
			setupMocks:  refusedWithLink,
			expectedErr: ErrAttemptNotCompleted,
		},
		{
			name:    "kratos error",
			receipt: held,
			setupMocks: func(m *serviceMocks) {
				opens(m, valid)
				m.kratos.EXPECT().GetIdentity(gomock.Any(), identityID, kratos.OIDCCredential).Return(nil, kratosErr)
			},
			expectedErr: ErrKratosUnavailable,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s, mocks := newTestService(t, "sso.Service.CompleteAttempt")
			mocks.envelope.EXPECT().ValidReceipt(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(envelope.ValidReceipt).AnyTimes()
			tc.setupMocks(mocks)

			ticket, err := s.CompleteAttempt(context.Background(), "sealed", identityID, tc.receipt)
			if tc.repeat {
				ticket, err = s.CompleteAttempt(context.Background(), "sealed", identityID, tc.receipt)
			}

			if !errors.Is(err, tc.expectedErr) {
				t.Fatalf("expected error %v, got %v", tc.expectedErr, err)
			}
			if tc.expectedErr == nil && (ticket.ConnectionID != connA || ticket.TenantID != tenantID) {
				t.Errorf("expected the ticket's connection and tenant, got %+v", ticket)
			}
		})
	}
}

func TestService_ListLinks(t *testing.T) {
	dbErr := errors.New("db error")
	kratosErr := errors.New("connection refused")

	testCases := []struct {
		name        string
		setupMocks  func(*serviceMocks)
		expectedIDs []string
		expectedErr error
	}{
		{
			name: "existing connections, once each",
			setupMocks: func(m *serviceMocks) {
				providers := link(connA, "s") + "," + link(connA, "t") + "," + link(connB, "s") + `,{"provider":"google","subject":"g"}`
				m.kratos.EXPECT().GetIdentity(gomock.Any(), identityID, kratos.OIDCCredential).Return(account(email, providers, nil), nil)
				m.storage.EXPECT().GetConnections(gomock.Any(), []string{connA, connA, connB}).
					Return([]*types.Connection{{ID: connA, Label: "Acme", OwnerTenantID: tenantID}}, nil)
			},
			expectedIDs: []string{connA},
		},
		{
			name: "no links",
			setupMocks: func(m *serviceMocks) {
				m.kratos.EXPECT().GetIdentity(gomock.Any(), identityID, kratos.OIDCCredential).Return(account(email, "", nil), nil)
				m.storage.EXPECT().GetConnections(gomock.Any(), []string{}).Return([]*types.Connection{}, nil)
			},
			expectedIDs: []string{},
		},
		{
			name: "account not found",
			setupMocks: func(m *serviceMocks) {
				m.kratos.EXPECT().GetIdentity(gomock.Any(), identityID, kratos.OIDCCredential).Return(nil, kratos.ErrNotFound)
			},
			expectedErr: ErrAccountNotFound,
		},
		{
			name: "kratos error",
			setupMocks: func(m *serviceMocks) {
				m.kratos.EXPECT().GetIdentity(gomock.Any(), identityID, kratos.OIDCCredential).Return(nil, kratosErr)
			},
			expectedErr: ErrKratosUnavailable,
		},
		{
			name: "storage error",
			setupMocks: func(m *serviceMocks) {
				m.kratos.EXPECT().GetIdentity(gomock.Any(), identityID, kratos.OIDCCredential).Return(account(email, link(connA, "s"), nil), nil)
				m.storage.EXPECT().GetConnections(gomock.Any(), []string{connA}).Return(nil, dbErr)
			},
			expectedErr: dbErr,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s, mocks := newTestService(t, "sso.Service.ListLinks")
			tc.setupMocks(mocks)

			linked, err := s.ListLinks(context.Background(), identityID)

			if !errors.Is(err, tc.expectedErr) {
				t.Fatalf("expected error %v, got %v", tc.expectedErr, err)
			}
			if got := connectionIDs(linked); !equal(got, tc.expectedIDs) {
				t.Errorf("expected %v, got %v", tc.expectedIDs, got)
			}
		})
	}
}

func TestService_DeleteLink(t *testing.T) {
	dbErr := errors.New("db error")
	kratosErr := errors.New("connection refused")
	password := map[string]string{"password": `{"hashed_password":"h"}`}
	identifier := func(subject string) string { return "byo-sso:" + connA + ":" + subject }

	testCases := []struct {
		name        string
		setupMocks  func(*serviceMocks)
		expectedErr error
	}{
		{
			name: "password remains",
			setupMocks: func(m *serviceMocks) {
				m.kratos.EXPECT().GetIdentity(gomock.Any(), identityID, kratos.FirstFactorCredentials).
					Return(account(email, link(connA, "s"), password), nil)
				m.kratos.EXPECT().DeleteOIDCIdentifier(gomock.Any(), identityID, identifier("s")).Return(nil)
			},
		},
		{
			name: "another link remains",
			setupMocks: func(m *serviceMocks) {
				m.kratos.EXPECT().GetIdentity(gomock.Any(), identityID, kratos.FirstFactorCredentials).
					Return(account(email, link(connA, "s")+","+link(connB, "t"), nil), nil)
				m.storage.EXPECT().GetConnections(gomock.Any(), []string{connB}).Return([]*types.Connection{{ID: connB}}, nil)
				m.kratos.EXPECT().DeleteOIDCIdentifier(gomock.Any(), identityID, identifier("s")).Return(nil)
			},
		},
		{
			name: "every subject, one already gone",
			setupMocks: func(m *serviceMocks) {
				m.kratos.EXPECT().GetIdentity(gomock.Any(), identityID, kratos.FirstFactorCredentials).
					Return(account(email, link(connA, "s")+","+link(connA, "t"), password), nil)
				m.kratos.EXPECT().DeleteOIDCIdentifier(gomock.Any(), identityID, identifier("s")).Return(kratos.ErrNotFound)
				m.kratos.EXPECT().DeleteOIDCIdentifier(gomock.Any(), identityID, identifier("t")).Return(nil)
			},
		},
		{
			name: "last way in",
			setupMocks: func(m *serviceMocks) {
				// A password credential with no hash and an email code are no way in.
				m.kratos.EXPECT().GetIdentity(gomock.Any(), identityID, kratos.FirstFactorCredentials).
					Return(account(email, link(connA, "s"), map[string]string{"password": `{}`, "code": `{}`}), nil)
			},
			expectedErr: ErrLastCredential,
		},
		{
			name: "other link to a deleted connection",
			setupMocks: func(m *serviceMocks) {
				m.kratos.EXPECT().GetIdentity(gomock.Any(), identityID, kratos.FirstFactorCredentials).
					Return(account(email, link(connA, "s")+","+link(connB, "s"), nil), nil)
				m.storage.EXPECT().GetConnections(gomock.Any(), []string{connB}).Return([]*types.Connection{}, nil)
			},
			expectedErr: ErrLastCredential,
		},
		{
			name: "no link at the connection",
			setupMocks: func(m *serviceMocks) {
				m.kratos.EXPECT().GetIdentity(gomock.Any(), identityID, kratos.FirstFactorCredentials).
					Return(account(email, link(connB, "s"), password), nil)
			},
			expectedErr: ErrLinkNotFound,
		},
		{
			name: "account not found",
			setupMocks: func(m *serviceMocks) {
				m.kratos.EXPECT().GetIdentity(gomock.Any(), identityID, kratos.FirstFactorCredentials).Return(nil, kratos.ErrNotFound)
			},
			expectedErr: ErrAccountNotFound,
		},
		{
			name: "kratos error",
			setupMocks: func(m *serviceMocks) {
				m.kratos.EXPECT().GetIdentity(gomock.Any(), identityID, kratos.FirstFactorCredentials).Return(nil, kratosErr)
			},
			expectedErr: ErrKratosUnavailable,
		},
		{
			name: "storage error, nothing removed",
			setupMocks: func(m *serviceMocks) {
				m.kratos.EXPECT().GetIdentity(gomock.Any(), identityID, kratos.FirstFactorCredentials).
					Return(account(email, link(connA, "s")+","+link(connB, "s"), nil), nil)
				m.storage.EXPECT().GetConnections(gomock.Any(), []string{connB}).Return(nil, dbErr)
			},
			expectedErr: dbErr,
		},
		{
			name: "kratos refuses the removal",
			setupMocks: func(m *serviceMocks) {
				m.kratos.EXPECT().GetIdentity(gomock.Any(), identityID, kratos.FirstFactorCredentials).
					Return(account(email, link(connA, "s"), password), nil)
				m.kratos.EXPECT().DeleteOIDCIdentifier(gomock.Any(), identityID, identifier("s")).Return(kratosErr)
			},
			expectedErr: ErrKratosUnavailable,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s, mocks := newTestService(t, "sso.Service.DeleteLink")
			tc.setupMocks(mocks)

			err := s.DeleteLink(context.Background(), identityID, connA)

			if !errors.Is(err, tc.expectedErr) {
				t.Fatalf("expected error %v, got %v", tc.expectedErr, err)
			}
		})
	}
}
