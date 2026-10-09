// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package mockidp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/canonical/sso-service/internal/idp"
	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/monitoring"
	"github.com/canonical/sso-service/internal/tracing"
	"github.com/canonical/sso-service/internal/types"
)

func newIdPClient(config idp.Config) *idp.Client {
	logger := logging.NewNoopLogger()

	return idp.NewClient(config, tracing.NewNoopTracer(), monitoring.NewNoopMonitor("sso-service", logger), logger)
}

const (
	clientID     = "c"
	clientSecret = "s+/=" // characters RFC 6749 §2.3.1 form-encodes
	// redirectPrefix is sso-service's callback; a connection's redirect URI
	// ends in its id.
	redirectPrefix = "http://sso.test/callback/"
	connectionID   = "0190a0b0-0000-7000-8000-000000000001"
	redirectURI    = redirectPrefix + connectionID
)

func newProvider(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	srv, err := New(Config{Issuer: "http://" + ts.Listener.Addr().String() + "/", ClientID: clientID, ClientSecret: clientSecret, RedirectURIPrefix: redirectPrefix})
	if err != nil {
		t.Fatal(err)
	}
	ts.Config.Handler = srv.Handler()
	ts.Start()
	t.Cleanup(ts.Close)
	return ts
}

func oauthConfig(t *testing.T, issuer string) (*oauth2.Config, *oidc.IDTokenVerifier) {
	t.Helper()
	provider, err := oidc.NewProvider(context.Background(), issuer)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &oauth2.Config{ClientID: clientID, ClientSecret: clientSecret, Endpoint: provider.Endpoint(), RedirectURL: redirectURI, Scopes: []string{oidc.ScopeOpenID}}
	return cfg, provider.Verifier(&oidc.Config{ClientID: clientID})
}

func putUser(t *testing.T, issuer, login string, u User) {
	t.Helper()
	body, _ := json.Marshal(u)
	req, _ := http.NewRequest(http.MethodPut, issuer+"/admin/users/"+login, bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put user: status %d", resp.StatusCode)
	}
}

// noRedirects keeps the code on the redirect instead of following it.
var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// login submits the form for authURL as login and returns the response.
func login(t *testing.T, issuer, authURL, login string) *http.Response {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	form := u.Query()
	form.Set("login", login)
	resp, err := noRedirects.PostForm(issuer+"/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func codeFrom(t *testing.T, resp *http.Response) string {
	t.Helper()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
	to, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || !strings.HasPrefix(to.String(), redirectURI) {
		t.Fatalf("redirected to %q (%v)", resp.Header.Get("Location"), err)
	}
	return to.Query().Get("code")
}

func sessionCookie(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookie {
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func TestServer_Login(t *testing.T) {
	ts := newProvider(t)
	cfg, verifier := oauthConfig(t, ts.URL)
	unverified := false
	putUser(t, ts.URL, "alice", User{Subject: "sub-1", Email: "changed@test.example", EmailVerified: &unverified})

	pkce := oauth2.GenerateVerifier()
	authURL := cfg.AuthCodeURL("st", oidc.Nonce("n-1"), oauth2.S256ChallengeOption(pkce))
	resp := login(t, ts.URL, authURL, "alice")
	session := sessionCookie(t, resp)

	token, err := cfg.Exchange(context.Background(), codeFrom(t, resp), oauth2.VerifierOption(pkce))
	if err != nil {
		t.Fatal(err)
	}
	if got := token.Extra("id_token"); got == nil {
		t.Fatal("no id_token")
	}
	idToken, err := verifier.Verify(context.Background(), token.Extra("id_token").(string))
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		t.Fatal(err)
	}
	if idToken.Subject != "sub-1" || idToken.Nonce != "n-1" || claims["email"] != "changed@test.example" || claims["email_verified"] != false {
		t.Fatalf("claims %v", claims)
	}

	// The session answers the next authorization without the form.
	again, _ := http.NewRequest(http.MethodGet, authURL, nil)
	again.AddCookie(session)
	resp, err = noRedirects.Do(again)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if codeFrom(t, resp) == "" {
		t.Fatal("the session did not answer")
	}
}

func TestServer_Authorize(t *testing.T) {
	ts := newProvider(t)
	cfg, _ := oauthConfig(t, ts.URL)
	for name, authURL := range map[string]string{
		"another redirect_uri": strings.Replace(cfg.AuthCodeURL("st"), url.QueryEscape(redirectURI), url.QueryEscape("https://evil.example/cb"), 1),
		"redirect_uri prefix of another host": strings.Replace(cfg.AuthCodeURL("st"), url.QueryEscape(redirectURI),
			url.QueryEscape(strings.TrimSuffix(redirectPrefix, "/")+".evil.example/"+connectionID), 1),
		"another client":        strings.Replace(cfg.AuthCodeURL("st"), "client_id="+clientID, "client_id=other", 1),
		"plain code_challenge":  cfg.AuthCodeURL("st", oauth2.SetAuthURLParam("code_challenge", "x"), oauth2.SetAuthURLParam("code_challenge_method", "plain")),
		"no response_type code": strings.Replace(cfg.AuthCodeURL("st"), "response_type=code", "response_type=token", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if resp := login(t, ts.URL, authURL, "alice"); resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("want 400, got %d", resp.StatusCode)
			}
		})
	}
}

func TestServer_Token(t *testing.T) {
	ts := newProvider(t)
	cfg, _ := oauthConfig(t, ts.URL)
	pkce := oauth2.GenerateVerifier()
	code := codeFrom(t, login(t, ts.URL, cfg.AuthCodeURL("st", oauth2.S256ChallengeOption(pkce)), "alice"))

	if _, err := cfg.Exchange(context.Background(), code, oauth2.VerifierOption("not-the-verifier")); !isOAuthError(err, "invalid_grant") {
		t.Fatalf("a PKCE mismatch is invalid_grant: %v", err)
	}
	if _, err := cfg.Exchange(context.Background(), code, oauth2.VerifierOption(pkce)); !isOAuthError(err, "invalid_grant") {
		t.Fatalf("a code is single use, even after a failed attempt: %v", err)
	}

	// Every connection has a redirect URI of its own under the prefix: a code
	// is redeemed only with the one it was issued for.
	other := *cfg
	other.RedirectURL = redirectPrefix + "0190a0b0-0000-7000-8000-000000000002"
	resp := login(t, ts.URL, other.AuthCodeURL("st", oauth2.S256ChallengeOption(pkce)), "alice")
	if to := resp.Header.Get("Location"); resp.StatusCode != http.StatusFound || !strings.HasPrefix(to, other.RedirectURL+"?") {
		t.Fatalf("a second connection's redirect URI is the client's too: %d %q", resp.StatusCode, to)
	}
	back, _ := url.Parse(resp.Header.Get("Location"))
	if _, err := cfg.Exchange(context.Background(), back.Query().Get("code"), oauth2.VerifierOption(pkce)); !isOAuthError(err, "invalid_grant") {
		t.Fatalf("a code issued for another redirect URI is invalid_grant: %v", err)
	}

	wrong := *cfg
	wrong.ClientSecret = "wrong"
	if _, err := wrong.Exchange(context.Background(), "never-issued"); !isOAuthError(err, "invalid_client") {
		t.Fatalf("a wrong secret is invalid_client: %v", err)
	}
	post := *cfg
	post.Endpoint.AuthStyle = oauth2.AuthStyleInParams
	if _, err := post.Exchange(context.Background(), "never-issued"); !isOAuthError(err, "invalid_grant") {
		t.Fatalf("client_secret_post authenticates, then the unknown code is invalid_grant: %v", err)
	}
}

func isOAuthError(err error, code string) bool {
	var re *oauth2.RetrieveError
	return errors.As(err, &re) && re.ErrorCode == code
}

// The service's own client against the mock: the test sign-in's pre-checks
// pass, and an exchange leaves an unstated email_verified unstated and
// carries auth_time.
func TestServer_Handler_IdPClient(t *testing.T) {
	ts := newProvider(t)
	putUser(t, ts.URL, "entra", User{Subject: "sub-2", Email: "entra@test.example"})

	// The mock listens on loopback, over plain http: only a development
	// deployment (DEV) reaches it.
	client := newIdPClient(idp.Config{Dev: true})
	connection := &types.Connection{ID: connectionID, Issuer: ts.URL, ClientID: clientID}

	if err := client.Check(context.Background(), connection, clientSecret, redirectURI); err != nil {
		t.Fatalf("pre-checks: %v", err)
	}
	if err := client.Check(context.Background(), connection, "wrong", redirectURI); !errors.Is(err, idp.ErrCredentials) {
		t.Fatalf("a wrong secret must be refused, got %v", err)
	}

	request := &idp.AuthRequest{RedirectURI: redirectURI, State: "st", Nonce: "n-2", PKCEVerifier: oauth2.GenerateVerifier(), Reauthenticate: true}
	authURL, err := client.AuthCodeURL(context.Background(), connection, request)
	if err != nil {
		t.Fatal(err)
	}
	if u, _ := url.Parse(authURL); u.Query().Get("prompt") != "login" || u.Query().Get("max_age") != "0" {
		t.Fatalf("re-authentication not asked: %s", authURL)
	}
	before := time.Now().Add(-time.Second)
	claims, err := client.Exchange(context.Background(), connection, clientSecret, request, codeFrom(t, login(t, ts.URL, authURL, "entra")))
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "sub-2" || claims.Email != "entra@test.example" || claims.EmailVerified != nil {
		t.Fatalf("claims %+v", claims)
	}
	if claims.AuthTime == nil || claims.AuthTime.Before(before.Truncate(time.Second)) {
		t.Fatalf("auth_time %v", claims.AuthTime)
	}
}

// Outside a development deployment the mock, on plain http and on loopback,
// is not an identity provider the service talks to.
func TestServer_Handler_IdPClientNotDev(t *testing.T) {
	ts := newProvider(t)
	client := newIdPClient(idp.Config{})
	connection := &types.Connection{ID: connectionID, Issuer: ts.URL, ClientID: clientID}
	err := client.Check(context.Background(), connection, clientSecret, redirectURI)
	if !errors.Is(err, idp.ErrMisconfigured) || !errors.Is(err, idp.ErrInsecureURL) {
		t.Fatalf("expected the http issuer to be refused, got %v", err)
	}
}
