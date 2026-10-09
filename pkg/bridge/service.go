// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

// Package bridge is the browser side of a company sign-in: hydra-sso's login
// and consent provider, and the identity providers' callback.
package bridge

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/canonical/sso-service/internal/hydra"
	"github.com/canonical/sso-service/internal/idp"
	"github.com/canonical/sso-service/internal/kratos"
	"github.com/canonical/sso-service/internal/limits"
	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/monitoring"
	"github.com/canonical/sso-service/internal/secrets"
	"github.com/canonical/sso-service/internal/storage"
	"github.com/canonical/sso-service/internal/tracing"
	"github.com/canonical/sso-service/internal/types"
)

// Result is what the browser gets: a redirect, or a page. An accepted
// sign-in comes with its receipt.
type Result struct {
	Redirect string
	Page     *Page
	Receipt  *Receipt
}

// Binding is the cookie that binds a sign-in to the browser it started in.
type Binding struct {
	State string
	Value string
}

// Receipt is the cookie that shows the login UI that this browser completed
// the ticket's sign-in. It lasts MaxAge seconds: as long as the ticket.
type Receipt struct {
	Ticket string
	Value  string
	MaxAge int
}

type Callback struct {
	ConnectionID     string
	State            string
	Code             string
	Error            string
	ErrorDescription string
	Binding          string
}

// flow is a sign-in at the callback; token is its ticket as it was sealed.
type flow struct {
	token  string
	ticket *types.Ticket
	signIn *types.SignIn
}

type Service struct {
	storage  StorageInterface
	idp      IdPClientInterface
	hydra    HydraClientInterface
	kratos   KratosClientInterface
	tenants  TenantsClientInterface
	envelope EnvelopeInterface

	publicURL string
	now       func() time.Time

	tracer  tracing.TracingInterface
	monitor monitoring.MonitorInterface
	logger  logging.LoggerInterface
}

func NewService(
	storage StorageInterface,
	idpClient IdPClientInterface,
	hydraClient HydraClientInterface,
	kratosClient KratosClientInterface,
	tenantsClient TenantsClientInterface,
	envelope EnvelopeInterface,
	publicURL string,
	tracer tracing.TracingInterface,
	monitor monitoring.MonitorInterface,
	logger logging.LoggerInterface,
) *Service {
	return &Service{
		storage:   storage,
		idp:       idpClient,
		hydra:     hydraClient,
		kratos:    kratosClient,
		tenants:   tenantsClient,
		envelope:  envelope,
		publicURL: strings.TrimRight(publicURL, "/"),
		now:       time.Now,
		tracer:    tracer,
		monitor:   monitor,
		logger:    logger,
	}
}

// recordError records an error on the span and emits a structured error log.
// The "error" key is always appended to keysAndValues automatically.
func (s *Service) recordError(span trace.Span, msg string, err error, keysAndValues ...interface{}) {
	span.RecordError(err)
	span.SetStatus(otelcodes.Error, err.Error())
	s.logger.Errorw(msg, append(keysAndValues, "error", err)...)
}

// StartLogin opens the ticket the login request carries as its login_hint
// and sends the browser to the identity provider, with a cookie that binds
// the sign-in to this browser.
func (s *Service) StartLogin(ctx context.Context, challenge string) (string, *Binding, error) {
	ctx, span := s.tracer.Start(ctx, "bridge.Service.StartLogin")
	defer span.End()

	request, err := s.hydra.GetLoginRequest(ctx, challenge)
	if errors.Is(err, hydra.ErrNotFound) {
		return "", nil, ErrUnknownLogin
	}
	if err != nil {
		s.recordError(span, "failed to get login request", err)

		return "", nil, err
	}

	now := s.now()
	ticket, err := s.openTicket(request.OIDCContext.LoginHint)
	if err != nil {
		return s.rejectAtLogin(ctx, challenge, ReasonExpired, "no readable ticket in login_hint")
	}
	if ticket.Expired(now) {
		return s.rejectAtLogin(ctx, challenge, ReasonExpired, "ticket expired")
	}

	connection, err := s.storage.GetConnection(ctx, ticket.ConnectionID)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		s.recordError(span, "failed to get connection", err, "connection_id", ticket.ConnectionID)

		return s.rejectAtLogin(ctx, challenge, ReasonUnavailable, "storage lookup failed")
	}
	if err != nil || !connection.Tested() {
		return s.rejectAtLogin(ctx, challenge, ReasonUnavailable, "connection gone or not tested")
	}

	values := make([]string, 3)
	for i := range values {
		if values[i], err = secrets.RandomToken(32); err != nil {
			s.recordError(span, "failed to generate random token", err)

			return "", nil, err
		}
	}
	signIn := &types.SignIn{
		State: values[0], Nonce: values[1], PKCEVerifier: values[2],
		LoginChallenge: challenge, ExpiresAt: now.Add(limits.AttemptTTL).Unix(),
	}
	sealed, err := s.envelope.Seal(types.PurposeSignIn, signIn)
	if err != nil {
		s.recordError(span, "failed to seal sign-in", err)

		return "", nil, err
	}

	target, err := s.idp.AuthCodeURL(ctx, connection, &idp.AuthRequest{
		RedirectURI:    types.RedirectURI(s.publicURL, connection.ID),
		State:          signIn.State,
		Nonce:          signIn.Nonce,
		PKCEVerifier:   signIn.PKCEVerifier,
		LoginHint:      ticket.Email,
		Reauthenticate: ticket.Reauthenticate,
	})
	if err != nil {
		s.logger.Warnw("failed to build the authorization request", "connection_id", connection.ID, "error", err)

		return s.rejectAtLogin(ctx, challenge, ReasonUnavailable, "idp unavailable")
	}

	return target, &Binding{State: signIn.State, Value: sealed}, nil
}

func (s *Service) rejectAtLogin(ctx context.Context, challenge, reason, detail string) (string, *Binding, error) {
	s.logger.Infow("sign-in refused", "reason", reason, "detail", detail, "stage", "login")
	s.incrementCallbackOutcomes(reason)

	target, err := s.hydra.RejectLogin(ctx, challenge, Message(reason))
	if err != nil {
		s.recordError(trace.SpanFromContext(ctx), "failed to reject login request", err)

		return "", nil, err
	}

	return target, nil, nil
}

// Callback handles the identity provider's answer to a sign-in or to a test
// sign-in. Every refusal of a sign-in rejects the login request, so the
// reason reaches the user on the login flow.
func (s *Service) Callback(ctx context.Context, callback *Callback) (*Result, error) {
	ctx, span := s.tracer.Start(ctx, "bridge.Service.Callback")
	defer span.End()

	if callback.State == "" {
		return nil, ErrUnknownState
	}
	// A test's state is its sealed context. A state that does not open as one
	// belongs to a sign-in, and has to match the binding of the browser.
	test := new(types.TestState)
	if err := s.envelope.Open(types.PurposeTestState, callback.State, test); err == nil {
		if !strings.EqualFold(test.ConnectionID, callback.ConnectionID) {
			return nil, ErrUnknownState
		}

		return s.testCallback(ctx, test, callback)
	}

	// The binding is checked before anything else.
	signIn, err := s.openBinding(callback)
	if err != nil {
		s.logger.Infow("callback refused", "reason", err.Error(), "state", secrets.Digest(callback.State))

		return nil, err
	}
	request, err := s.hydra.GetLoginRequest(ctx, signIn.LoginChallenge)
	if errors.Is(err, hydra.ErrNotFound) {
		return nil, ErrUnknownState
	}
	if err != nil {
		s.recordError(span, "failed to get login request", err)

		return nil, err
	}

	f := &flow{signIn: signIn, token: request.OIDCContext.LoginHint, ticket: new(types.Ticket)}
	ticket, err := s.openTicket(f.token)
	if err != nil {
		return s.reject(ctx, f, ReasonExpired, "no readable ticket in login_hint")
	}
	f.ticket = ticket
	if now := s.now(); signIn.Expired(now) || ticket.Expired(now) {
		return s.reject(ctx, f, ReasonExpired, "sign-in expired")
	}
	if !strings.EqualFold(ticket.ConnectionID, callback.ConnectionID) {
		return s.reject(ctx, f, ReasonInvalidToken, "the answer came to another connection's redirect URI")
	}

	connection, err := s.storage.GetConnection(ctx, ticket.ConnectionID)
	if errors.Is(err, storage.ErrNotFound) {
		return s.reject(ctx, f, ReasonUnavailable, "connection gone")
	}
	if err != nil {
		s.recordError(span, "failed to get connection", err, "connection_id", ticket.ConnectionID)

		return s.reject(ctx, f, ReasonUnavailable, "storage lookup failed")
	}
	if callback.Error != "" {
		s.logger.Warnw("identity provider refused the authorization",
			"connection_id", connection.ID, "error", bounded(callback.Error), "error_description", bounded(callback.ErrorDescription))

		return s.reject(ctx, f, ReasonIdPRefused, "idp error "+bounded(callback.Error))
	}

	secret, err := s.envelope.Decrypt(connection.ClientSecret, connection.ID)
	if err != nil {
		s.recordError(span, "failed to decrypt the client secret", err, "connection_id", connection.ID)

		return s.reject(ctx, f, ReasonUnavailable, "secret not decryptable")
	}

	// The code is single use: a replayed callback fails here.
	claims, err := s.idp.Exchange(ctx, connection, secret, &idp.AuthRequest{
		RedirectURI:  types.RedirectURI(s.publicURL, connection.ID),
		State:        signIn.State,
		Nonce:        signIn.Nonce,
		PKCEVerifier: signIn.PKCEVerifier,
	}, callback.Code)
	if err != nil {
		s.logger.Warnw("company sign-in exchange failed", "connection_id", connection.ID, "error", err)

		return s.reject(ctx, f, exchangeReason(err), "exchange failed")
	}

	if ticket.Reauthenticate {
		if claims.AuthTime == nil || claims.AuthTime.Before(ticket.Issued().Add(-limits.ClockSkew)) {
			return s.reject(ctx, f, ReasonReauthentication, "auth_time missing or before the ticket")
		}
	}
	if !strings.EqualFold(claims.Email, ticket.Email) {
		return s.reject(ctx, f, ReasonAddressMismatch, "id_token email differs from the entered address")
	}
	if claims.EmailVerified != nil && !*claims.EmailVerified {
		return s.reject(ctx, f, ReasonAddressUnconfirmed, "email_verified false")
	}

	return s.decide(ctx, f, connection, claims.Subject, claims.EmailVerified)
}

// decide follows an existing link, or checks a subject with no link and lets
// it through to Kratos, which writes the link itself.
func (s *Service) decide(ctx context.Context, f *flow, connection *types.Connection, subject string, emailVerified *bool) (*Result, error) {
	linked, err := s.kratos.ListByIdentifier(ctx, types.LinkIdentifier(connection.ID, subject))
	if err != nil {
		s.recordError(trace.SpanFromContext(ctx), "failed to look up the link", err)

		return s.reject(ctx, f, ReasonUnavailable, "kratos lookup failed")
	}
	// Kratos keeps a credential identifier unique, so there is at most one.
	if len(linked) == 0 {
		return s.firstSignIn(ctx, f, connection, subject, emailVerified)
	}

	return s.existingLink(ctx, f, connection, &linked[0], subject, emailVerified)
}

func (s *Service) existingLink(ctx context.Context, f *flow, connection *types.Connection, identity *kratos.Identity, subject string, emailVerified *bool) (*Result, error) {
	if !strings.EqualFold(identity.Email(), f.ticket.Email) {
		return s.reject(ctx, f, ReasonAlreadyLinked, "linked account has another address")
	}

	return s.accept(ctx, f, connection, subject, emailVerified, ReasonLinkedExisting)
}

// firstSignIn lets a subject with no link through when the tenant has an
// active binding to the connection that applies to the address, and the
// address is a member's, or a pending invitation or auto-join admits it.
func (s *Service) firstSignIn(ctx context.Context, f *flow, connection *types.Connection, subject string, emailVerified *bool) (*Result, error) {
	accounts, err := s.kratos.ListByIdentifier(ctx, f.ticket.Email)
	if err != nil {
		s.recordError(trace.SpanFromContext(ctx), "failed to look up the account", err)

		return s.reject(ctx, f, ReasonUnavailable, "kratos lookup failed")
	}
	if len(accounts) > 1 {
		return s.reject(ctx, f, ReasonAlreadyLinked, "several accounts hold the address")
	}

	identityID, email := "", f.ticket.Email
	if len(accounts) == 1 {
		if !strings.EqualFold(accounts[0].Email(), f.ticket.Email) {
			return s.reject(ctx, f, ReasonAddressMismatch, "the account's address differs")
		}
		identityID, email = accounts[0].ID, ""
	}

	signIn, err := s.tenants.GetSignInContext(ctx, f.ticket.TenantID, email, identityID)
	if err != nil {
		s.recordError(trace.SpanFromContext(ctx), "failed to get sign-in context", err)

		return s.reject(ctx, f, ReasonUnavailable, "tenant-service unavailable")
	}
	admitted := signIn.GetInvitationAdmits() || signIn.GetAutoJoinAdmits()
	switch {
	case !admits(signIn, connection.ID):
		return s.reject(ctx, f, ReasonNotAMember, "no active binding to this connection applies to the address")
	case !signIn.GetMember() && !admitted:
		return s.reject(ctx, f, ReasonNotAMember, "neither a member nor admitted by an invitation or auto-join")
	}

	reason := ReasonAccountLinking
	if identityID == "" {
		reason = ReasonRegistration
	}
	s.logger.Infow("first sign-in let through",
		"connection_id", connection.ID, "tenant_id", f.ticket.TenantID, "outcome", reason,
		"identity", secrets.Digest(identityID), "member", signIn.GetMember(),
		"invitation", signIn.GetInvitationAdmits(), "auto_join", signIn.GetAutoJoinAdmits())
	s.incrementFirstSignIns(reason)

	return s.accept(ctx, f, connection, subject, emailVerified, reason)
}

func (s *Service) accept(ctx context.Context, f *flow, connection *types.Connection, subject string, emailVerified *bool, reason string) (*Result, error) {
	loginContext := map[string]any{"email": f.ticket.Email}
	if emailVerified != nil {
		loginContext["email_verified"] = *emailVerified
	}
	hydraSubject := types.HydraSubject(connection.ID, subject)
	receipt, err := s.envelope.Receipt(f.token, hydraSubject)
	if err != nil {
		s.recordError(trace.SpanFromContext(ctx), "failed to make the receipt", err)

		return s.reject(ctx, f, ReasonUnavailable, "receipt not made")
	}
	target, err := s.hydra.AcceptLogin(ctx, f.signIn.LoginChallenge, hydraSubject, loginContext)
	if err != nil {
		s.recordError(trace.SpanFromContext(ctx), "failed to accept login request", err)

		return nil, fmt.Errorf("failed to accept login request: %w", err)
	}
	s.incrementCallbackOutcomes(reason)
	s.logger.Infow("company sign-in", "outcome", reason, "connection_id", connection.ID, "tenant_id", f.ticket.TenantID)

	return &Result{Redirect: target, Receipt: &Receipt{
		Ticket: f.token,
		Value:  receipt,
		MaxAge: int(time.Unix(f.ticket.ExpiresAt, 0).Sub(s.now()).Seconds()),
	}}, nil
}

func (s *Service) reject(ctx context.Context, f *flow, reason, detail string) (*Result, error) {
	s.incrementCallbackOutcomes(reason)
	s.logger.Infow("sign-in refused",
		"reason", reason, "detail", detail, "connection_id", f.ticket.ConnectionID,
		"tenant_id", f.ticket.TenantID, "email", types.MaskEmail(f.ticket.Email))

	target, err := s.hydra.RejectLogin(ctx, f.signIn.LoginChallenge, Message(reason))
	if err != nil {
		s.recordError(trace.SpanFromContext(ctx), "failed to reject login request", err)

		return nil, fmt.Errorf("failed to reject login request: %w", err)
	}

	return &Result{Redirect: target}, nil
}

// Consent never remembers the consent, and copies the address, and
// email_verified only when the identity provider said it, into the id_token.
func (s *Service) Consent(ctx context.Context, challenge string) (string, error) {
	ctx, span := s.tracer.Start(ctx, "bridge.Service.Consent")
	defer span.End()

	request, err := s.hydra.GetConsentRequest(ctx, challenge)
	if errors.Is(err, hydra.ErrNotFound) {
		return "", ErrUnknownLogin
	}
	if err != nil {
		s.recordError(span, "failed to get consent request", err)

		return "", err
	}

	idToken := map[string]any{}
	if email, ok := request.Context["email"].(string); ok {
		idToken["email"] = email
	}
	if verified, ok := request.Context["email_verified"].(bool); ok {
		idToken["email_verified"] = verified
	}

	target, err := s.hydra.AcceptConsent(ctx, request, idToken)
	if err != nil {
		s.recordError(span, "failed to accept consent request", err)

		return "", err
	}

	return target, nil
}

func (s *Service) openBinding(callback *Callback) (*types.SignIn, error) {
	if callback.Binding == "" {
		return nil, ErrBindingMissing
	}
	signIn := new(types.SignIn)
	if err := s.envelope.Open(types.PurposeSignIn, callback.Binding, signIn); err != nil ||
		subtle.ConstantTimeCompare([]byte(signIn.State), []byte(callback.State)) != 1 {
		return nil, ErrBindingMismatch
	}

	return signIn, nil
}

func (s *Service) openTicket(token string) (*types.Ticket, error) {
	ticket := new(types.Ticket)
	if err := s.envelope.Open(types.PurposeTicket, token, ticket); err != nil {
		return nil, err
	}

	return ticket, nil
}

func (s *Service) incrementCallbackOutcomes(reason string) {
	if err := s.monitor.IncrementCallbackOutcomes(map[string]string{"reason": reason}); err != nil {
		s.logger.Warnf("failed to increment callback outcomes counter: %v", err)
	}
}

func (s *Service) incrementFirstSignIns(outcome string) {
	if err := s.monitor.IncrementFirstSignIns(map[string]string{"outcome": outcome}); err != nil {
		s.logger.Warnf("failed to increment first sign-ins counter: %v", err)
	}
}
