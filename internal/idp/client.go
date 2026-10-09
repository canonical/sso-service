// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

// Package idp is the OpenID Connect relying party: the only code that talks
// to the tenants' identity providers.
package idp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/oauth2"

	"github.com/canonical/sso-service/internal/limits"
	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/monitoring"
	"github.com/canonical/sso-service/internal/tracing"
	"github.com/canonical/sso-service/internal/types"
)

// scopes are what every connection asks its identity provider for.
var scopes = []string{oidc.ScopeOpenID, "email"}

type Config struct {
	// Dev allows identity providers over plain http and at private
	// addresses. Never in production: any tenant admin names the URLs this
	// client calls.
	Dev bool
}

// metadata is the part of the discovery document go-oidc does not read.
type metadata struct {
	TokenAuthMethods []string `json:"token_endpoint_auth_methods_supported"`
}

type AuthRequest struct {
	RedirectURI  string
	State        string
	Nonce        string
	PKCEVerifier string
	LoginHint    string
	// Reauthenticate sends prompt=login and max_age=0.
	Reauthenticate bool
}

// Claims are read from a verified id_token.
type Claims struct {
	Subject string
	Email   string
	// EmailVerified is nil when the identity provider did not say.
	EmailVerified *bool
	// AuthTime is nil when the identity provider did not say. It is the only
	// proof that a sign-in asked to re-authenticate really did.
	AuthTime *time.Time
}

type provider struct {
	oidc *oidc.Provider
	meta metadata
	at   time.Time
}

type Client struct {
	http   *http.Client
	policy hostPolicy
	now    func() time.Time

	mu        sync.Mutex
	providers map[string]*provider

	tracer  tracing.TracingInterface
	monitor monitoring.MonitorInterface
	logger  logging.LoggerInterface
}

func NewClient(config Config, tracer tracing.TracingInterface, monitor monitoring.MonitorInterface, logger logging.LoggerInterface) *Client {
	policy := hostPolicy{dev: config.Dev}

	return &Client{
		http:      newHTTPClient(policy, limits.IdPTimeout),
		policy:    policy,
		now:       time.Now,
		providers: make(map[string]*provider),
		tracer:    tracer,
		monitor:   monitor,
		logger:    logger,
	}
}

// ValidateIssuer accepts an absolute https URL with no userinfo, query or
// fragment.
func (c *Client) ValidateIssuer(raw string) error {
	u, err := c.policy.checkURL(raw)
	if err != nil {
		return err
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("the issuer carries a query or a fragment")
	}

	return nil
}

func (c *Client) AuthCodeURL(ctx context.Context, connection *types.Connection, request *AuthRequest) (string, error) {
	ctx, span := c.tracer.Start(ctx, "idp.Client.AuthCodeURL")
	defer span.End()

	ctx, cancel := context.WithTimeout(ctx, limits.IdPExchangeTimeout)
	defer cancel()

	p, err := c.discover(ctx, connection.Issuer, false)
	if err != nil {
		return "", recordError(span, err)
	}

	options := []oauth2.AuthCodeOption{
		oauth2.S256ChallengeOption(request.PKCEVerifier),
		oauth2.SetAuthURLParam("nonce", request.Nonce),
	}
	if request.LoginHint != "" {
		options = append(options, oauth2.SetAuthURLParam("login_hint", request.LoginHint))
	}
	if request.Reauthenticate {
		options = append(options, oauth2.SetAuthURLParam("prompt", "login"), oauth2.SetAuthURLParam("max_age", "0"))
	}

	return c.config(p, connection, "", request.RedirectURI).AuthCodeURL(request.State, options...), nil
}

// Exchange redeems the code and verifies the id_token.
func (c *Client) Exchange(ctx context.Context, connection *types.Connection, clientSecret string, request *AuthRequest, code string) (*Claims, error) {
	ctx, span := c.tracer.Start(ctx, "idp.Client.Exchange")
	defer span.End()

	ctx, cancel := context.WithTimeout(ctx, limits.IdPExchangeTimeout)
	defer cancel()

	p, err := c.discover(ctx, connection.Issuer, false)
	if err != nil {
		return nil, recordError(span, err)
	}

	token, err := c.config(p, connection, clientSecret, request.RedirectURI).
		Exchange(c.context(ctx), code, oauth2.VerifierOption(request.PKCEVerifier))
	if err != nil {
		return nil, recordError(span, exchangeError(err))
	}
	raw, _ := token.Extra("id_token").(string)
	if raw == "" {
		return nil, recordError(span, fmt.Errorf("%w: no id_token in the token response", ErrRejected))
	}

	verifier := p.oidc.Verifier(&oidc.Config{
		ClientID:             connection.ClientID,
		SupportedSigningAlgs: []string{oidc.RS256, oidc.ES256},
		// go-oidc allows no skew on exp.
		Now: func() time.Time { return c.now().Add(-limits.ClockSkew) },
	})
	idToken, err := verifier.Verify(c.context(ctx), raw)
	if err != nil {
		return nil, recordError(span, &said{kind: verifyKind(err), cause: err})
	}
	var payload json.RawMessage
	if err := idToken.Claims(&payload); err != nil {
		return nil, recordError(span, fmt.Errorf("%w: unreadable claims", ErrInvalidToken))
	}

	claims, err := checkClaims(idToken.Subject, payload, connection.ClientID, request.Nonce)
	if err != nil {
		return nil, recordError(span, err)
	}

	return claims, nil
}

// Check reads the discovery document anew and asks the token endpoint to
// redeem a made-up code with the client's credentials: only a refusal of the
// client itself is an error.
func (c *Client) Check(ctx context.Context, connection *types.Connection, clientSecret, redirectURI string) error {
	ctx, span := c.tracer.Start(ctx, "idp.Client.Check")
	defer span.End()

	ctx, cancel := context.WithTimeout(ctx, limits.IdPExchangeTimeout)
	defer cancel()

	p, err := c.discover(ctx, connection.Issuer, true)
	if err != nil {
		return recordError(span, err)
	}

	_, err = c.config(p, connection, clientSecret, redirectURI).Exchange(c.context(ctx), "sso-service-test-probe")
	var refused *oauth2.RetrieveError
	switch {
	case err == nil:
		return nil
	case !errors.As(err, &refused) || refused.Response == nil:
		return recordError(span, exchangeError(err))
	case refused.Response.StatusCode == http.StatusUnauthorized || refused.ErrorCode == "invalid_client" || refused.ErrorCode == "unauthorized_client":
		return recordError(span, ErrCredentials)
	case refused.ErrorCode == "":
		return recordError(span, fmt.Errorf("%w: token endpoint gave no OAuth answer", ErrMisconfigured))
	}

	return nil
}

func recordError(span trace.Span, err error) error {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())

	return err
}

// config picks how the client authenticates from the discovery document:
// x/oauth2's own trial and error would spend the single-use code on a wrong
// guess.
func (c *Client) config(p *provider, connection *types.Connection, clientSecret, redirectURI string) *oauth2.Config {
	endpoint := p.oidc.Endpoint()
	endpoint.AuthStyle = oauth2.AuthStyleInHeader
	if len(p.meta.TokenAuthMethods) > 0 && !contains(p.meta.TokenAuthMethods, "client_secret_basic") &&
		contains(p.meta.TokenAuthMethods, "client_secret_post") {
		endpoint.AuthStyle = oauth2.AuthStyleInParams
	}

	return &oauth2.Config{
		ClientID:     connection.ClientID,
		ClientSecret: clientSecret,
		Endpoint:     endpoint,
		RedirectURL:  redirectURI,
		Scopes:       scopes,
	}
}

// context makes go-oidc and x/oauth2 use this client's HTTP client.
func (c *Client) context(ctx context.Context) context.Context {
	return oidc.ClientContext(context.WithValue(ctx, oauth2.HTTPClient, c.http), c.http)
}

// discover keeps one provider per issuer, because go-oidc keeps none and the
// provider holds the issuer's keys, and replaces it after DiscoveryTTL. A
// refresh that fails because the issuer is unreachable keeps the one in hand
// for another DiscoveryTTL, so that costs one failed fetch, not one per
// sign-in.
func (c *Client) discover(ctx context.Context, issuer string, fresh bool) (*provider, error) {
	c.mu.Lock()
	cached := c.providers[issuer]
	c.mu.Unlock()
	if !fresh && cached != nil && c.now().Sub(cached.at) < limits.DiscoveryTTL {
		return cached, nil
	}

	p, err := c.fetchProvider(ctx, issuer)
	if err != nil {
		if cached == nil || fresh || errors.Is(err, ErrMisconfigured) {
			return nil, err
		}
		p = &provider{oidc: cached.oidc, meta: cached.meta, at: c.now()}
	}
	c.mu.Lock()
	c.providers[issuer] = p
	// A provider nobody asked for during a DiscoveryTTL after it expired is
	// dropped, so the issuers of deleted connections do not stay.
	for kept, old := range c.providers {
		if c.now().Sub(old.at) >= 2*limits.DiscoveryTTL {
			delete(c.providers, kept)
		}
	}
	c.mu.Unlock()

	return p, nil
}

// fetchProvider checks the authorization endpoint, where the browser is sent.
// The token and key endpoints may be on other hosts than the issuer: the
// HTTP client's rules apply to them as to any request.
func (c *Client) fetchProvider(ctx context.Context, issuer string) (*provider, error) {
	discovered, err := oidc.NewProvider(c.context(ctx), issuer)
	if err != nil {
		var transport *url.Error
		if errors.As(err, &transport) {
			return nil, transportError(err)
		}

		return nil, &said{kind: ErrMisconfigured, cause: err}
	}
	p := &provider{oidc: discovered, at: c.now()}
	if err := discovered.Claims(&p.meta); err != nil {
		return nil, fmt.Errorf("%w: unreadable discovery document", ErrMisconfigured)
	}
	if _, err := c.policy.checkURL(discovered.Endpoint().AuthURL); err != nil {
		return nil, fmt.Errorf("%w: authorization_endpoint", ErrMisconfigured)
	}
	if discovered.Endpoint().TokenURL == "" {
		return nil, fmt.Errorf("%w: no token_endpoint", ErrMisconfigured)
	}

	return p, nil
}

// verifyKind tells a token that failed verification from keys that could not
// be fetched. go-oidc reports both as one error and drops the cause from its
// chain, so its text is all there is to go by.
func verifyKind(err error) error {
	if strings.Contains(err.Error(), "fetching keys") {
		return ErrUnavailable
	}

	return ErrInvalidToken
}

func exchangeError(err error) error {
	var refused *oauth2.RetrieveError
	if errors.As(err, &refused) && refused.Response != nil {
		return tokenError(refused.Response.StatusCode, refused.ErrorCode)
	}
	var transport *url.Error
	if errors.As(err, &transport) {
		return transportError(err)
	}

	return &said{kind: ErrRejected, cause: err}
}

// transportError is a request that got no answer: the identity provider is
// unavailable, unless this service refused the destination.
func transportError(err error) error {
	if errors.Is(err, ErrForbiddenAddress) || errors.Is(err, ErrInsecureURL) || errors.Is(err, ErrResponseTooLarge) {
		return fmt.Errorf("%w: %w", ErrMisconfigured, err)
	}

	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}

// checkClaims makes the checks go-oidc leaves to its caller: azp, the nonce,
// and the subject.
func checkClaims(subject string, payload []byte, clientID, nonce string) (*Claims, error) {
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.UseNumber()
	claims := make(map[string]any)
	if err := decoder.Decode(&claims); err != nil {
		return nil, fmt.Errorf("%w: unreadable claims", ErrInvalidToken)
	}

	audiences := audience(claims["aud"])
	azp, hasAZP := claims["azp"].(string)
	if (len(audiences) > 1 && !hasAZP) || (hasAZP && azp != clientID) {
		return nil, fmt.Errorf("%w: azp", ErrInvalidToken)
	}
	if got, _ := claims["nonce"].(string); nonce == "" || got != nonce {
		return nil, fmt.Errorf("%w: nonce", ErrInvalidToken)
	}
	if err := validSubject(subject); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}

	out := &Claims{Subject: subject}
	out.Email, _ = claims["email"].(string)
	switch v := claims["email_verified"].(type) {
	case bool:
		out.EmailVerified = &v
	case string:
		// Some identity providers send "true" or "false".
		if b := strings.EqualFold(v, "true"); b || strings.EqualFold(v, "false") {
			out.EmailVerified = &b
		}
	}
	if authTime, ok := unixTime(claims["auth_time"]); ok {
		out.AuthTime = &authTime
	}

	return out, nil
}

func validSubject(subject string) error {
	if subject == "" || len(subject) > limits.MaxSubjectLength {
		return fmt.Errorf("the subject is empty or longer than %d characters", limits.MaxSubjectLength)
	}
	for i := 0; i < len(subject); i++ {
		if subject[i] < 0x20 || subject[i] > 0x7e {
			return errors.New("the subject is not printable ASCII")
		}
	}

	return nil
}

// tokenError tells a refusal of the client's credentials, which is the
// connection's fault, from a rejection of this sign-in.
func tokenError(status int, code string) error {
	if status == http.StatusUnauthorized || code == "invalid_client" || code == "unauthorized_client" {
		return fmt.Errorf("%w: token endpoint answered %d %.40q", ErrCredentials, status, code)
	}

	// The error code is the identity provider's text and ends up in a log
	// line: quoted and cut short, it cannot forge or flood one.
	return fmt.Errorf("%w: token endpoint answered %d %.40q", ErrRejected, status, code)
}

func audience(v any) []string {
	switch aud := v.(type) {
	case string:
		return []string{aud}
	case []any:
		out := make([]string, 0, len(aud))
		for _, a := range aud {
			if s, ok := a.(string); ok {
				out = append(out, s)
			}
		}

		return out
	}

	return nil
}

func unixTime(v any) (time.Time, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return time.Time{}, false
	}
	f, err := n.Float64()
	if err != nil {
		return time.Time{}, false
	}

	return time.Unix(int64(f), 0), true
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}

	return false
}
