// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package bridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	v0tenant "github.com/canonical/identity-platform-api/v0/tenant"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/mock/gomock"

	"github.com/canonical/sso-service/internal/hydra"
	"github.com/canonical/sso-service/internal/idp"
	"github.com/canonical/sso-service/internal/kratos"
	"github.com/canonical/sso-service/internal/limits"
	"github.com/canonical/sso-service/internal/secrets"
	"github.com/canonical/sso-service/internal/storage"
	"github.com/canonical/sso-service/internal/types"
)

//go:generate mockgen -build_flags=--mod=mod -package bridge -destination ./mock_bridge.go -source=./interfaces.go
//go:generate mockgen -build_flags=--mod=mod -package bridge -destination ./mock_logger.go -source=../../internal/logging/interfaces.go
//go:generate mockgen -build_flags=--mod=mod -package bridge -destination ./mock_monitor.go -source=../../internal/monitoring/interfaces.go
//go:generate mockgen -build_flags=--mod=mod -package bridge -destination ./mock_tracing.go -source=../../internal/tracing/interfaces.go

const (
	tenantID   = "11111111-1111-4111-8111-111111111111"
	identityID = "6c2f1a8e-1f0a-4c3b-9d7e-2b1d6f0e4a11"
	connA      = "0190a0b0-0000-7000-8000-00000000000a"
	connB      = "0190a0b0-0000-7000-8000-00000000000b"
	email      = "alice@test.example"
	publicURL  = "https://sso.example"
	redirectA  = publicURL + "/callback/" + connA

	// Where hydra-sso sends the browser after a login request is rejected or
	// accepted.
	rejected = "https://kratos/rejected"
	accepted = "https://kratos/accepted"
)

var testNow = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

var testEnvelope = func() *secrets.Envelope {
	e, err := secrets.NewEnvelope(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
	if err != nil {
		panic(err)
	}
	return e
}()

// setupLoggerMock configures a MockLoggerInterface with AnyTimes() stubs for
// the structured logging methods (w-suffix).
func setupLoggerMock(mockLogger *MockLoggerInterface) {
	mockLogger.EXPECT().Debugw(gomock.Any(), gomock.Any()).AnyTimes()
	mockLogger.EXPECT().Infow(gomock.Any(), gomock.Any()).AnyTimes()
	mockLogger.EXPECT().Errorw(gomock.Any(), gomock.Any()).AnyTimes()
	mockLogger.EXPECT().Warnw(gomock.Any(), gomock.Any()).AnyTimes()
}

type mocks struct {
	storage *MockStorageInterface
	idp     *MockIdPClientInterface
	hydra   *MockHydraClientInterface
	kratos  *MockKratosClientInterface
	tenants *MockTenantsClientInterface
	tracer  *MockTracingInterface
	monitor *MockMonitorInterface
	logger  *MockLoggerInterface

	// errors are the messages logged at level error.
	errors []string
}

func newService(t *testing.T) (*Service, *mocks) {
	t.Helper()
	ctrl := gomock.NewController(t)
	m := &mocks{
		storage: NewMockStorageInterface(ctrl),
		idp:     NewMockIdPClientInterface(ctrl),
		hydra:   NewMockHydraClientInterface(ctrl),
		kratos:  NewMockKratosClientInterface(ctrl),
		tenants: NewMockTenantsClientInterface(ctrl),
		tracer:  NewMockTracingInterface(ctrl),
		monitor: NewMockMonitorInterface(ctrl),
		logger:  NewMockLoggerInterface(ctrl),
	}
	m.logger.EXPECT().Errorw(gomock.Any(), gomock.Any()).Do(func(msg string, _ ...any) { m.errors = append(m.errors, msg) }).AnyTimes()
	setupLoggerMock(m.logger)

	// A trailing slash in the public URL names the same redirect URIs.
	s := NewService(m.storage, m.idp, m.hydra, m.kratos, m.tenants, testEnvelope, publicURL+"/", m.tracer, m.monitor, m.logger)
	s.now = func() time.Time { return testNow }

	return s, m
}

func (m *mocks) expectSpan(name string) {
	m.tracer.EXPECT().Start(gomock.Any(), name).Return(context.Background(), trace.SpanFromContext(context.Background()))
}

// expectLoginRequest is hydra-sso's login request with hint as its
// login_hint.
func (m *mocks) expectLoginRequest(hint string) {
	request := new(hydra.LoginRequest)
	request.OIDCContext.LoginHint = hint
	m.hydra.EXPECT().GetLoginRequest(gomock.Any(), "lc").Return(request, nil)
}

// expectExchange sets up the callback up to the claims the identity provider
// asserts: the code is redeemed with what /login sealed into the cookie, at
// the connection's own redirect URI.
func (m *mocks) expectExchange(t *testing.T, ticket *types.Ticket, claims *idp.Claims) {
	t.Helper()
	connection := newConnection()
	m.expectLoginRequest(seal(t, types.PurposeTicket, ticket))
	m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(connection, nil)
	m.idp.EXPECT().Exchange(gomock.Any(), connection, "secret",
		&idp.AuthRequest{RedirectURI: redirectA, State: "st", Nonce: "n", PKCEVerifier: "v"}, "code").Return(claims, nil)
}

// expectFirstSignIn sets up a subject with no link up to tenant-service's
// answer; existing is the account holding the address, if any.
func (m *mocks) expectFirstSignIn(t *testing.T, claims *idp.Claims, existing *kratos.Identity, signIn *v0tenant.SignInContext) {
	t.Helper()
	m.expectExchange(t, newTicket(), claims)
	m.kratos.EXPECT().ListByIdentifier(gomock.Any(), "byo-sso:"+connA+":"+claims.Subject).Return([]kratos.Identity{}, nil)
	if existing == nil {
		m.kratos.EXPECT().ListByIdentifier(gomock.Any(), email).Return([]kratos.Identity{}, nil)
		m.tenants.EXPECT().GetSignInContext(gomock.Any(), tenantID, email, "").Return(signIn, nil)
		return
	}
	m.kratos.EXPECT().ListByIdentifier(gomock.Any(), email).Return([]kratos.Identity{*existing}, nil)
	m.tenants.EXPECT().GetSignInContext(gomock.Any(), tenantID, "", existing.ID).Return(signIn, nil)
}

func (m *mocks) expectReject(reason string) {
	m.monitor.EXPECT().IncrementCallbackOutcomes(map[string]string{"reason": reason}).Return(nil)
	m.hydra.EXPECT().RejectLogin(gomock.Any(), "lc", Message(reason)).Return(rejected, nil)
}

func (m *mocks) expectAccept(outcome, subject string, emailVerified *bool) {
	loginContext := map[string]any{"email": email}
	if emailVerified != nil {
		loginContext["email_verified"] = *emailVerified
	}
	m.hydra.EXPECT().AcceptLogin(gomock.Any(), "lc", connA+":"+subject, loginContext).Return(accepted, nil)
	m.monitor.EXPECT().IncrementCallbackOutcomes(map[string]string{"reason": outcome}).Return(nil)
}

func seal(t *testing.T, purpose string, v any) string {
	t.Helper()
	token, err := testEnvelope.Seal(purpose, v)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// newTicket is what StartAttempt sealed a minute ago.
func newTicket() *types.Ticket {
	return &types.Ticket{
		TenantID: tenantID, ConnectionID: connA, Email: email,
		IssuedAt: testNow.Add(-time.Minute).Unix(), ExpiresAt: testNow.Add(29 * time.Minute).Unix(),
	}
}

// newSignIn is what /login sealed into the binding cookie.
func newSignIn() *types.SignIn {
	return &types.SignIn{State: "st", Nonce: "n", PKCEVerifier: "v", LoginChallenge: "lc", ExpiresAt: testNow.Add(29 * time.Minute).Unix()}
}

// newConnection is connA, tested, its client secret "secret".
func newConnection() *types.Connection {
	sealed, err := testEnvelope.Encrypt("secret", connA)
	if err != nil {
		panic(err)
	}
	tested := testNow.Add(-24 * time.Hour)
	return &types.Connection{ID: connA, Label: "Acme Okta", TestedAt: &tested, ClientSecret: sealed}
}

// answer is the identity provider's answer to a sign-in through connA, at
// connA's redirect URI, in the browser the sign-in started in.
func answer(t *testing.T) *Callback {
	t.Helper()
	return &Callback{ConnectionID: connA, State: "st", Code: "code", Binding: seal(t, types.PurposeSignIn, newSignIn())}
}

func verified(v bool) *bool { return &v }

// account is a Kratos identity; oidc is the JSON of its OIDC providers.
func account(id, address, oidc string) *kratos.Identity {
	i := &kratos.Identity{ID: id, Traits: json.RawMessage(`{"email":"` + address + `"}`), Credentials: map[string]kratos.Credential{}}
	if oidc != "" {
		i.Credentials["oidc"] = kratos.Credential{Config: json.RawMessage(`{"providers":[` + oidc + `]}`)}
	}
	return i
}

// signInContext is tenant-service's answer; connections are the active
// bindings that apply to the address.
func signInContext(member, autoJoin bool, connections ...string) *v0tenant.SignInContext {
	return &v0tenant.SignInContext{Member: member, AutoJoinAdmits: autoJoin, ConnectionIds: connections}
}

func invited(connections ...string) *v0tenant.SignInContext {
	return &v0tenant.SignInContext{InvitationAdmits: true, ConnectionIds: connections}
}

// runCallback calls Service.Callback with request, or with answer(t) when it
// is nil, over the mocks setupMocks prepares.
func runCallback(t *testing.T, request *Callback, setupMocks func(*testing.T, *mocks)) (*Result, error) {
	t.Helper()
	s, m := newService(t)
	m.expectSpan("bridge.Service.Callback")
	if setupMocks != nil {
		setupMocks(t, m)
	}
	if request == nil {
		request = answer(t)
	}

	return s.Callback(context.Background(), request)
}

// expectOutcome checks a callback's result: an error, or the redirect, with a
// receipt when the sign-in was accepted and none when it was refused.
func expectOutcome(t *testing.T, result *Result, err error, expectedRedirect string, expectedErr error) {
	t.Helper()
	if expectedErr != nil {
		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected error %v, got %v", expectedErr, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Redirect != expectedRedirect || result.Page != nil {
		t.Fatalf("expected a redirect to %s, got %+v", expectedRedirect, result)
	}
	if (result.Receipt != nil) != (expectedRedirect == accepted) {
		t.Fatalf("expected a receipt for an accepted sign-in only, got %+v", result.Receipt)
	}
}

func TestService_StartLogin(t *testing.T) {
	down := errors.New("connection refused")
	expired := newTicket()
	expired.ExpiresAt = testNow.Unix()

	t.Run("success", func(t *testing.T) {
		s, m := newService(t)
		m.expectSpan("bridge.Service.StartLogin")
		ticket := newTicket()
		ticket.Reauthenticate = true
		connection := newConnection()
		m.expectLoginRequest(seal(t, types.PurposeTicket, ticket))
		m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(connection, nil)
		var sent *idp.AuthRequest
		m.idp.EXPECT().AuthCodeURL(gomock.Any(), connection, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ *types.Connection, r *idp.AuthRequest) (string, error) {
				sent = r
				return "https://idp.example/authorize", nil
			})

		target, binding, err := s.StartLogin(context.Background(), "lc")
		if err != nil || target != "https://idp.example/authorize" {
			t.Fatalf("expected the IdP's authorization URL, got %q %v", target, err)
		}
		if sent.RedirectURI != redirectA || sent.LoginHint != email || !sent.Reauthenticate {
			t.Fatalf("unexpected authorization request %+v", sent)
		}
		if sent.State == "" || sent.Nonce == "" || sent.PKCEVerifier == "" || sent.State == sent.Nonce {
			t.Fatalf("expected random values of their own, got %+v", sent)
		}
		// The binding cookie is the sign-in, sealed.
		signIn := new(types.SignIn)
		if err := testEnvelope.Open(types.PurposeSignIn, binding.Value, signIn); err != nil {
			t.Fatalf("the binding does not open: %v", err)
		}
		expected := types.SignIn{State: sent.State, Nonce: sent.Nonce, PKCEVerifier: sent.PKCEVerifier,
			LoginChallenge: "lc", ExpiresAt: testNow.Add(limits.AttemptTTL).Unix()}
		if binding.State != sent.State || *signIn != expected {
			t.Fatalf("expected %+v sealed for state %q, got %+v for %q", expected, sent.State, signIn, binding.State)
		}
		if strings.Contains(binding.Value, sent.PKCEVerifier) {
			t.Fatal("the verifier is readable in the binding")
		}
	})

	testCases := []struct {
		name           string
		setupMocks     func(t *testing.T, m *mocks)
		expectedTarget string
		expectedErr    error
		expectedLog    string
	}{
		{
			name: "no ticket",
			setupMocks: func(t *testing.T, m *mocks) {
				m.expectLoginRequest("")
				m.expectReject(ReasonExpired)
			},
			expectedTarget: rejected,
		},
		{
			name: "expired ticket",
			setupMocks: func(t *testing.T, m *mocks) {
				m.expectLoginRequest(seal(t, types.PurposeTicket, expired))
				m.expectReject(ReasonExpired)
			},
			expectedTarget: rejected,
		},
		{
			name: "forged ticket",
			setupMocks: func(t *testing.T, m *mocks) {
				m.expectLoginRequest("Zm9yZ2Vk")
				m.expectReject(ReasonExpired)
			},
			expectedTarget: rejected,
		},
		{
			name: "sealed for another purpose",
			setupMocks: func(t *testing.T, m *mocks) {
				m.expectLoginRequest(seal(t, types.PurposeTestState, newTicket()))
				m.expectReject(ReasonExpired)
			},
			expectedTarget: rejected,
		},
		{
			name: "connection not tested",
			setupMocks: func(t *testing.T, m *mocks) {
				m.expectLoginRequest(seal(t, types.PurposeTicket, newTicket()))
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(&types.Connection{ID: connA}, nil)
				m.expectReject(ReasonUnavailable)
			},
			expectedTarget: rejected,
		},
		{
			name: "connection deleted",
			setupMocks: func(t *testing.T, m *mocks) {
				m.expectLoginRequest(seal(t, types.PurposeTicket, newTicket()))
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(nil, storage.ErrNotFound)
				m.expectReject(ReasonUnavailable)
			},
			expectedTarget: rejected,
		},
		{
			name: "database down",
			setupMocks: func(t *testing.T, m *mocks) {
				m.expectLoginRequest(seal(t, types.PurposeTicket, newTicket()))
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(nil, storage.ErrTimeout)
				m.expectReject(ReasonUnavailable)
			},
			expectedTarget: rejected,
			expectedLog:    "failed to get connection",
		},
		{
			name: "idp unavailable",
			setupMocks: func(t *testing.T, m *mocks) {
				connection := newConnection()
				m.expectLoginRequest(seal(t, types.PurposeTicket, newTicket()))
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(connection, nil)
				m.idp.EXPECT().AuthCodeURL(gomock.Any(), connection, gomock.Any()).Return("", idp.ErrUnavailable)
				m.expectReject(ReasonUnavailable)
			},
			expectedTarget: rejected,
		},
		{
			name: "reject fails",
			setupMocks: func(t *testing.T, m *mocks) {
				m.expectLoginRequest("")
				m.monitor.EXPECT().IncrementCallbackOutcomes(map[string]string{"reason": ReasonExpired}).Return(nil)
				m.hydra.EXPECT().RejectLogin(gomock.Any(), "lc", gomock.Any()).Return("", down)
			},
			expectedErr: down,
		},
		{
			name: "unknown challenge",
			setupMocks: func(t *testing.T, m *mocks) {
				m.hydra.EXPECT().GetLoginRequest(gomock.Any(), "lc").Return(nil, hydra.ErrNotFound)
			},
			expectedErr: ErrUnknownLogin,
		},
		{
			name: "hydra down",
			setupMocks: func(t *testing.T, m *mocks) {
				m.hydra.EXPECT().GetLoginRequest(gomock.Any(), "lc").Return(nil, down)
			},
			expectedErr: down,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s, m := newService(t)
			m.expectSpan("bridge.Service.StartLogin")
			tc.setupMocks(t, m)

			target, binding, err := s.StartLogin(context.Background(), "lc")

			if tc.expectedErr != nil {
				if !errors.Is(err, tc.expectedErr) {
					t.Fatalf("expected error %v, got %v", tc.expectedErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if target != tc.expectedTarget || binding != nil {
				t.Errorf("expected %s and no binding, got %s %+v", tc.expectedTarget, target, binding)
			}
			if got := strings.Join(m.errors, "; "); got != tc.expectedLog {
				t.Errorf("expected the error log %q, got %q", tc.expectedLog, got)
			}
		})
	}
}

// The binding is checked before anything else: a callback that fails it
// reaches neither hydra-sso nor the identity provider.
func TestService_Callback_Binding(t *testing.T) {
	testCases := []struct {
		name             string
		callback         func(t *testing.T) *Callback
		setupMocks       func(t *testing.T, m *mocks)
		expectedRedirect string
		expectedErr      error
	}{
		{
			name:        "no state",
			callback:    func(t *testing.T) *Callback { return &Callback{ConnectionID: connA, Code: "code"} },
			expectedErr: ErrUnknownState,
		},
		{
			name:        "missing",
			callback:    func(t *testing.T) *Callback { return &Callback{ConnectionID: connA, State: "st", Code: "code"} },
			expectedErr: ErrBindingMissing,
		},
		{
			name: "another browser",
			callback: func(t *testing.T) *Callback {
				other := newSignIn()
				other.State = "other"
				return &Callback{ConnectionID: connA, State: "st", Code: "code", Binding: seal(t, types.PurposeSignIn, other)}
			},
			expectedErr: ErrBindingMismatch,
		},
		{
			name: "forged",
			callback: func(t *testing.T) *Callback {
				return &Callback{ConnectionID: connA, State: "st", Code: "code", Binding: "Zm9yZ2Vk"}
			},
			expectedErr: ErrBindingMismatch,
		},
		{
			name: "sealed for another purpose",
			callback: func(t *testing.T) *Callback {
				return &Callback{ConnectionID: connA, State: "st", Code: "code", Binding: seal(t, types.PurposeTicket, newSignIn())}
			},
			expectedErr: ErrBindingMismatch,
		},
		{
			name: "replayed",
			setupMocks: func(t *testing.T, m *mocks) {
				m.hydra.EXPECT().GetLoginRequest(gomock.Any(), "lc").Return(nil, hydra.ErrNotFound)
			},
			expectedErr: ErrUnknownState,
		},
		{
			name: "expired",
			callback: func(t *testing.T) *Callback {
				expired := newSignIn()
				expired.ExpiresAt = testNow.Unix()
				return &Callback{ConnectionID: connA, State: "st", Code: "code", Binding: seal(t, types.PurposeSignIn, expired)}
			},
			setupMocks: func(t *testing.T, m *mocks) {
				m.expectLoginRequest(seal(t, types.PurposeTicket, newTicket()))
				m.expectReject(ReasonExpired)
			},
			expectedRedirect: rejected,
		},
		{
			name: "no ticket",
			setupMocks: func(t *testing.T, m *mocks) {
				m.expectLoginRequest("")
				m.expectReject(ReasonExpired)
			},
			expectedRedirect: rejected,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var request *Callback
			if tc.callback != nil {
				request = tc.callback(t)
			}

			result, err := runCallback(t, request, tc.setupMocks)

			expectOutcome(t, result, err, tc.expectedRedirect, tc.expectedErr)
		})
	}
}

// An answer is only taken at the redirect URI of the connection the sign-in
// was started with: at any other it is rejected before the connection is
// read or the code redeemed.
func TestService_Callback_RedirectURI(t *testing.T) {
	testCases := []struct {
		name             string
		connectionID     string
		setupMocks       func(t *testing.T, m *mocks)
		expectedRedirect string
	}{
		{
			name:         "another connection",
			connectionID: connB,
			setupMocks: func(t *testing.T, m *mocks) {
				m.expectLoginRequest(seal(t, types.PurposeTicket, newTicket()))
				m.expectReject(ReasonInvalidToken)
			},
			expectedRedirect: rejected,
		},
		{
			name:         "no connection",
			connectionID: "",
			setupMocks: func(t *testing.T, m *mocks) {
				m.expectLoginRequest(seal(t, types.PurposeTicket, newTicket()))
				m.expectReject(ReasonInvalidToken)
			},
			expectedRedirect: rejected,
		},
		{
			name:         "the ticket's connection in upper case",
			connectionID: strings.ToUpper(connA),
			setupMocks: func(t *testing.T, m *mocks) {
				m.expectExchange(t, newTicket(), &idp.Claims{Subject: "s", Email: email})
				m.kratos.EXPECT().ListByIdentifier(gomock.Any(), "byo-sso:"+connA+":s").Return([]kratos.Identity{*account(identityID, email, "")}, nil)
				m.expectAccept(ReasonLinkedExisting, "s", nil)
			},
			expectedRedirect: accepted,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			request := answer(t)
			request.ConnectionID = tc.connectionID

			result, err := runCallback(t, request, tc.setupMocks)

			expectOutcome(t, result, err, tc.expectedRedirect, nil)
		})
	}
}

func TestService_Callback_Refusals(t *testing.T) {
	down := errors.New("connection refused")

	testCases := []struct {
		name        string
		idpError    string
		setupMocks  func(t *testing.T, m *mocks)
		reason      string
		expectedErr error
		expectedLog string
	}{
		{
			name:     "idp error",
			idpError: "access_denied",
			setupMocks: func(t *testing.T, m *mocks) {
				m.expectLoginRequest(seal(t, types.PurposeTicket, newTicket()))
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(newConnection(), nil)
			},
			reason: ReasonIdPRefused,
		},
		{
			name: "another address",
			setupMocks: func(t *testing.T, m *mocks) {
				m.expectExchange(t, newTicket(), &idp.Claims{Subject: "s", Email: "mallory@test.example"})
			},
			reason: ReasonAddressMismatch,
		},
		{
			name: "unconfirmed address",
			setupMocks: func(t *testing.T, m *mocks) {
				m.expectExchange(t, newTicket(), &idp.Claims{Subject: "s", Email: "ALICE@test.example", EmailVerified: verified(false)})
			},
			reason: ReasonAddressUnconfirmed,
		},
		{
			name: "connection deleted",
			setupMocks: func(t *testing.T, m *mocks) {
				m.expectLoginRequest(seal(t, types.PurposeTicket, newTicket()))
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(nil, storage.ErrNotFound)
			},
			reason: ReasonUnavailable,
		},
		{
			name: "database down",
			setupMocks: func(t *testing.T, m *mocks) {
				m.expectLoginRequest(seal(t, types.PurposeTicket, newTicket()))
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(nil, storage.ErrTimeout)
			},
			reason:      ReasonUnavailable,
			expectedLog: "failed to get connection",
		},
		{
			// Nothing is sent to the identity provider without the secret.
			name: "secret that does not decrypt",
			setupMocks: func(t *testing.T, m *mocks) {
				unreadable := newConnection()
				unreadable.ClientSecret = []byte("encrypted under another key")
				m.expectLoginRequest(seal(t, types.PurposeTicket, newTicket()))
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(unreadable, nil)
			},
			reason: ReasonUnavailable,
		},
		{
			name: "reject fails",
			setupMocks: func(t *testing.T, m *mocks) {
				m.expectLoginRequest(seal(t, types.PurposeTicket, newTicket()))
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(nil, storage.ErrNotFound)
				m.monitor.EXPECT().IncrementCallbackOutcomes(map[string]string{"reason": ReasonUnavailable}).Return(nil)
				m.hydra.EXPECT().RejectLogin(gomock.Any(), "lc", gomock.Any()).Return("", hydra.ErrNotFound)
			},
			expectedErr: hydra.ErrNotFound,
		},
		{
			name: "hydra down",
			setupMocks: func(t *testing.T, m *mocks) {
				m.hydra.EXPECT().GetLoginRequest(gomock.Any(), "lc").Return(nil, down)
			},
			expectedErr: down,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			request := answer(t)
			request.Error = tc.idpError

			var used *mocks
			result, err := runCallback(t, request, func(t *testing.T, m *mocks) {
				used = m
				tc.setupMocks(t, m)
				if tc.reason != "" {
					m.expectReject(tc.reason)
				}
			})

			expectOutcome(t, result, err, rejected, tc.expectedErr)
			if tc.expectedLog != "" && strings.Join(used.errors, "; ") != tc.expectedLog {
				t.Errorf("expected the error log %q, got %q", tc.expectedLog, used.errors)
			}
		})
	}
}

// A code exchange that fails is the connection's fault, or the platform's,
// unless the identity provider refused this sign-in.
func TestService_Callback_ExchangeFails(t *testing.T) {
	testCases := []struct {
		name   string
		err    error
		reason string
	}{
		{name: "idp unavailable", err: idp.ErrUnavailable, reason: ReasonUnavailable},
		{name: "invalid id_token", err: idp.ErrInvalidToken, reason: ReasonInvalidToken},
		{name: "code refused", err: idp.ErrRejected, reason: ReasonIdPRefused},
		{name: "credentials refused", err: idp.ErrCredentials, reason: ReasonUnavailable},
		{name: "discovery unusable", err: idp.ErrMisconfigured, reason: ReasonUnavailable},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := runCallback(t, nil, func(t *testing.T, m *mocks) {
				connection := newConnection()
				m.expectLoginRequest(seal(t, types.PurposeTicket, newTicket()))
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(connection, nil)
				m.idp.EXPECT().Exchange(gomock.Any(), connection, "secret", gomock.Any(), "code").Return(nil, tc.err)
				m.expectReject(tc.reason)
			})

			expectOutcome(t, result, err, rejected, nil)
		})
	}
}

// A sign-in asked to re-authenticate needs an auth_time no earlier than its
// ticket, give or take the clock skew.
func TestService_Callback_Reauthentication(t *testing.T) {
	issued := newTicket().Issued()
	at := func(d time.Duration) *time.Time {
		authTime := issued.Add(d)
		return &authTime
	}

	testCases := []struct {
		name     string
		authTime *time.Time
		reason   string
	}{
		{name: "no auth_time", reason: ReasonReauthentication},
		{name: "before the ticket", authTime: at(-limits.ClockSkew - time.Second), reason: ReasonReauthentication},
		{name: "within the skew", authTime: at(-30 * time.Second)},
		{name: "after the ticket", authTime: at(30 * time.Second)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := runCallback(t, nil, func(t *testing.T, m *mocks) {
				ticket := newTicket()
				ticket.Reauthenticate = true
				m.expectExchange(t, ticket, &idp.Claims{Subject: "s", Email: email, AuthTime: tc.authTime})
				if tc.reason != "" {
					m.expectReject(tc.reason)
					return
				}
				m.kratos.EXPECT().ListByIdentifier(gomock.Any(), "byo-sso:"+connA+":s").Return([]kratos.Identity{*account(identityID, email, "")}, nil)
				m.expectAccept(ReasonLinkedExisting, "s", nil)
			})

			expectedRedirect := accepted
			if tc.reason != "" {
				expectedRedirect = rejected
			}
			expectOutcome(t, result, err, expectedRedirect, nil)
		})
	}
}

// A subject Kratos already holds a link for signs in: tenant-service is not
// asked and nothing is written.
func TestService_Callback_ExistingLink(t *testing.T) {
	link := "byo-sso:" + connA + ":s"

	testCases := []struct {
		name             string
		setupMocks       func(t *testing.T, m *mocks)
		expectedRedirect string
		expectedErr      error
	}{
		{
			name: "success",
			setupMocks: func(t *testing.T, m *mocks) {
				m.expectExchange(t, newTicket(), &idp.Claims{Subject: "s", Email: email})
				m.kratos.EXPECT().ListByIdentifier(gomock.Any(), link).Return([]kratos.Identity{*account(identityID, email, "")}, nil)
				m.expectAccept(ReasonLinkedExisting, "s", nil)
			},
			expectedRedirect: accepted,
		},
		{
			name: "email_verified passed on",
			setupMocks: func(t *testing.T, m *mocks) {
				m.expectExchange(t, newTicket(), &idp.Claims{Subject: "s", Email: email, EmailVerified: verified(true)})
				m.kratos.EXPECT().ListByIdentifier(gomock.Any(), link).Return([]kratos.Identity{*account(identityID, email, "")}, nil)
				m.expectAccept(ReasonLinkedExisting, "s", verified(true))
			},
			expectedRedirect: accepted,
		},
		{
			name: "account with another address",
			setupMocks: func(t *testing.T, m *mocks) {
				m.expectExchange(t, newTicket(), &idp.Claims{Subject: "s", Email: email})
				m.kratos.EXPECT().ListByIdentifier(gomock.Any(), link).Return([]kratos.Identity{*account(identityID, "other@test.example", "")}, nil)
				m.expectReject(ReasonAlreadyLinked)
			},
			expectedRedirect: rejected,
		},
		{
			name: "kratos down",
			setupMocks: func(t *testing.T, m *mocks) {
				m.expectExchange(t, newTicket(), &idp.Claims{Subject: "s", Email: email})
				m.kratos.EXPECT().ListByIdentifier(gomock.Any(), link).Return(nil, errors.New("connection refused"))
				m.expectReject(ReasonUnavailable)
			},
			expectedRedirect: rejected,
		},
		{
			name: "accept fails",
			setupMocks: func(t *testing.T, m *mocks) {
				m.expectExchange(t, newTicket(), &idp.Claims{Subject: "s", Email: email})
				m.kratos.EXPECT().ListByIdentifier(gomock.Any(), link).Return([]kratos.Identity{*account(identityID, email, "")}, nil)
				m.hydra.EXPECT().AcceptLogin(gomock.Any(), "lc", gomock.Any(), gomock.Any()).Return("", hydra.ErrNotFound)
			},
			expectedErr: hydra.ErrNotFound,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := runCallback(t, nil, tc.setupMocks)

			expectOutcome(t, result, err, tc.expectedRedirect, tc.expectedErr)
		})
	}
}

// The receipt of an accepted sign-in is made for its ticket and the subject
// accepted at hydra-sso, and lasts as long as the ticket.
func TestService_Callback_Receipt(t *testing.T) {
	ticket := seal(t, types.PurposeTicket, newTicket())

	result, err := runCallback(t, nil, func(t *testing.T, m *mocks) {
		connection := newConnection()
		m.expectLoginRequest(ticket)
		m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(connection, nil)
		m.idp.EXPECT().Exchange(gomock.Any(), connection, "secret", gomock.Any(), "code").Return(&idp.Claims{Subject: "s", Email: email}, nil)
		m.kratos.EXPECT().ListByIdentifier(gomock.Any(), "byo-sso:"+connA+":s").Return([]kratos.Identity{*account(identityID, email, "")}, nil)
		m.expectAccept(ReasonLinkedExisting, "s", nil)
	})

	expectOutcome(t, result, err, accepted, nil)
	receipt := result.Receipt
	if receipt.Ticket != ticket || receipt.MaxAge != 29*60 || !testEnvelope.ValidReceipt(receipt.Value, ticket, connA+":s") {
		t.Errorf("expected a receipt for the ticket and the subject %s:s that lasts 29 minutes, got %+v", connA, receipt)
	}
}

// A subject with no link is let through, and nothing is written: Kratos
// links it to the account that holds the address, or registers one.
func TestService_Callback_FirstSignIn(t *testing.T) {
	member := account(identityID, email, "")
	relinked := account(identityID, email, `{"provider":"byo-sso","subject":"`+connA+`:old"}`)

	testCases := []struct {
		name    string
		claims  *idp.Claims
		account *kratos.Identity
		signIn  *v0tenant.SignInContext
		// outcome is how the subject is let through, reason why it is not.
		outcome string
		reason  string
	}{
		{
			name:    "member",
			claims:  &idp.Claims{Subject: "s", Email: email, EmailVerified: verified(true)},
			account: member,
			signIn:  signInContext(true, false, connA),
			outcome: ReasonAccountLinking,
		},
		{
			name:    "email_verified not said",
			claims:  &idp.Claims{Subject: "s", Email: email},
			account: member,
			signIn:  signInContext(true, false, connA),
			outcome: ReasonAccountLinking,
		},
		{
			name:    "new subject of a linked account",
			claims:  &idp.Claims{Subject: "new", Email: email, EmailVerified: verified(true)},
			account: relinked,
			signIn:  signInContext(true, false, connA),
			outcome: ReasonAccountLinking,
		},
		{
			name:    "invited, no account",
			claims:  &idp.Claims{Subject: "s", Email: email, EmailVerified: verified(true)},
			signIn:  invited(connA),
			outcome: ReasonRegistration,
		},
		{
			name:    "invited account",
			claims:  &idp.Claims{Subject: "s", Email: email, EmailVerified: verified(true)},
			account: member,
			signIn:  invited(connA),
			outcome: ReasonAccountLinking,
		},
		{
			name:    "auto-join, no account",
			claims:  &idp.Claims{Subject: "s", Email: email},
			signIn:  signInContext(false, true, connA),
			outcome: ReasonRegistration,
		},
		{
			name:    "auto-join account",
			claims:  &idp.Claims{Subject: "s", Email: email, EmailVerified: verified(true)},
			account: member,
			signIn:  signInContext(false, true, connA),
			outcome: ReasonAccountLinking,
		},
		{
			name:    "account not admitted",
			claims:  &idp.Claims{Subject: "s", Email: email, EmailVerified: verified(true)},
			account: member,
			signIn:  signInContext(false, false, connA),
			reason:  ReasonNotAMember,
		},
		{
			name:   "address not admitted",
			claims: &idp.Claims{Subject: "s", Email: email, EmailVerified: verified(true)},
			signIn: signInContext(false, false, connA),
			reason: ReasonNotAMember,
		},
		{
			name:    "binding of another connection",
			claims:  &idp.Claims{Subject: "s", Email: email, EmailVerified: verified(true)},
			account: member,
			signIn:  signInContext(true, false, connB),
			reason:  ReasonNotAMember,
		},
		{
			name:   "invited, no binding applies",
			claims: &idp.Claims{Subject: "s", Email: email, EmailVerified: verified(true)},
			signIn: invited(),
			reason: ReasonNotAMember,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := runCallback(t, nil, func(t *testing.T, m *mocks) {
				m.expectFirstSignIn(t, tc.claims, tc.account, tc.signIn)
				if tc.reason != "" {
					m.expectReject(tc.reason)
					return
				}
				m.monitor.EXPECT().IncrementFirstSignIns(map[string]string{"outcome": tc.outcome}).Return(nil)
				m.expectAccept(tc.outcome, tc.claims.Subject, tc.claims.EmailVerified)
			})

			expectedRedirect := accepted
			if tc.reason != "" {
				expectedRedirect = rejected
			}
			expectOutcome(t, result, err, expectedRedirect, nil)
		})
	}
}

func TestService_Callback_FirstSignInRefusals(t *testing.T) {
	link := "byo-sso:" + connA + ":s"
	existing := account(identityID, email, "")

	testCases := []struct {
		name       string
		setupMocks func(m *mocks)
		reason     string
	}{
		{
			name: "several accounts hold the address",
			setupMocks: func(m *mocks) {
				m.kratos.EXPECT().ListByIdentifier(gomock.Any(), email).Return([]kratos.Identity{*existing, *existing}, nil)
			},
			reason: ReasonAlreadyLinked,
		},
		{
			name: "account holds another address",
			setupMocks: func(m *mocks) {
				m.kratos.EXPECT().ListByIdentifier(gomock.Any(), email).Return([]kratos.Identity{*account(identityID, "other@test.example", "")}, nil)
			},
			reason: ReasonAddressMismatch,
		},
		{
			name: "kratos down",
			setupMocks: func(m *mocks) {
				m.kratos.EXPECT().ListByIdentifier(gomock.Any(), email).Return(nil, errors.New("connection refused"))
			},
			reason: ReasonUnavailable,
		},
		{
			name: "tenant-service down",
			setupMocks: func(m *mocks) {
				m.kratos.EXPECT().ListByIdentifier(gomock.Any(), email).Return([]kratos.Identity{}, nil)
				m.tenants.EXPECT().GetSignInContext(gomock.Any(), tenantID, email, "").Return(nil, errors.New("unavailable"))
			},
			reason: ReasonUnavailable,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := runCallback(t, nil, func(t *testing.T, m *mocks) {
				m.expectExchange(t, newTicket(), &idp.Claims{Subject: "s", Email: email})
				m.kratos.EXPECT().ListByIdentifier(gomock.Any(), link).Return([]kratos.Identity{}, nil)
				tc.setupMocks(m)
				m.expectReject(tc.reason)
			})

			expectOutcome(t, result, err, rejected, nil)
		})
	}
}

func TestService_Consent(t *testing.T) {
	down := errors.New("connection refused")

	testCases := []struct {
		name           string
		setupMocks     func(m *mocks)
		expectedTarget string
		expectedErr    error
	}{
		{
			name: "address into the id_token",
			setupMocks: func(m *mocks) {
				request := &hydra.ConsentRequest{Challenge: "cc", RequestedScope: []string{"openid", "email"}, Context: map[string]any{"email": email}}
				m.hydra.EXPECT().GetConsentRequest(gomock.Any(), "cc").Return(request, nil)
				m.hydra.EXPECT().AcceptConsent(gomock.Any(), request, map[string]any{"email": email}).Return("https://kratos/cb", nil)
			},
			expectedTarget: "https://kratos/cb",
		},
		{
			name: "email_verified as the idp said",
			setupMocks: func(m *mocks) {
				request := &hydra.ConsentRequest{Challenge: "cc", Context: map[string]any{"email": email, "email_verified": false}}
				m.hydra.EXPECT().GetConsentRequest(gomock.Any(), "cc").Return(request, nil)
				m.hydra.EXPECT().AcceptConsent(gomock.Any(), request, map[string]any{"email": email, "email_verified": false}).Return("https://kratos/cb", nil)
			},
			expectedTarget: "https://kratos/cb",
		},
		{
			name: "unknown challenge",
			setupMocks: func(m *mocks) {
				m.hydra.EXPECT().GetConsentRequest(gomock.Any(), "cc").Return(nil, hydra.ErrNotFound)
			},
			expectedErr: ErrUnknownLogin,
		},
		{
			name: "hydra down",
			setupMocks: func(m *mocks) {
				m.hydra.EXPECT().GetConsentRequest(gomock.Any(), "cc").Return(nil, down)
			},
			expectedErr: down,
		},
		{
			name: "accept fails",
			setupMocks: func(m *mocks) {
				request := &hydra.ConsentRequest{Challenge: "cc"}
				m.hydra.EXPECT().GetConsentRequest(gomock.Any(), "cc").Return(request, nil)
				m.hydra.EXPECT().AcceptConsent(gomock.Any(), request, map[string]any{}).Return("", down)
			},
			expectedErr: down,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s, m := newService(t)
			m.expectSpan("bridge.Service.Consent")
			tc.setupMocks(m)

			target, err := s.Consent(context.Background(), "cc")

			if tc.expectedErr != nil {
				if !errors.Is(err, tc.expectedErr) {
					t.Fatalf("expected error %v, got %v", tc.expectedErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if target != tc.expectedTarget {
				t.Errorf("expected %s, got %s", tc.expectedTarget, target)
			}
		})
	}
}
