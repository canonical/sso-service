// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package idp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"

	"github.com/canonical/sso-service/internal/limits"
	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/monitoring"
	"github.com/canonical/sso-service/internal/tracing"
	"github.com/canonical/sso-service/internal/types"
)

const (
	testConnection = "0190a0b0-0000-7000-8000-00000000000a"
	testRedirect   = "https://sso.example/callback/" + testConnection
)

func newTestClient(config Config) *Client {
	logger := logging.NewNoopLogger()

	return NewClient(config, tracing.NewNoopTracer(), monitoring.NewNoopMonitor("sso-service", logger), logger)
}

// production is a client with the production rules whose requests are
// answered by handler instead of the network: what a public https identity
// provider would answer, without one.
func production(handler http.Handler) *Client {
	c := newTestClient(Config{})
	c.http.Transport = schemeGuard{policy: c.policy, next: roundTrip(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Result(), nil
	})}

	return c
}

// clientWithHTTPTimeout is a development client whose HTTP client gives one
// request timeout instead of limits.IdPTimeout.
func clientWithHTTPTimeout(timeout time.Duration) *Client {
	policy := hostPolicy{dev: true}

	return &Client{http: newHTTPClient(policy, timeout), policy: policy, now: time.Now, providers: make(map[string]*provider), tracer: tracing.NewNoopTracer()}
}

func claimsJSON(t *testing.T, claims map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

// fakeIdP is a minimal OP on httptest for the client's HTTP paths.
type fakeIdP struct {
	mu     sync.Mutex
	server *httptest.Server
	mux    *http.ServeMux
	keys   []jose.JSONWebKey
	signer jose.Signer
	claims map[string]any

	// discovery overrides entries of the discovery document; a nil value
	// leaves the entry out.
	discovery     map[string]any
	discoveryCode int
	// tokenCode and tokenError are what the token endpoint answers when it
	// refuses; tokenBody replaces a success answer.
	tokenCode  int
	tokenError string
	tokenBody  map[string]any
	// hang makes the named path never answer (until the test ends).
	hang    string
	release chan struct{}

	// hits counts requests by "host path"; token is the last token request.
	hits  map[string]int
	token struct {
		authorization string
		form          url.Values
	}
}

func newFakeIdP(t *testing.T, tls bool) *fakeIdP {
	t.Helper()
	f := &fakeIdP{tokenCode: http.StatusOK, tokenError: "invalid_grant", hits: map[string]int{}, release: make(chan struct{})}
	f.mux = http.NewServeMux()
	f.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits[r.Host+" "+r.URL.Path]++
		hang := f.hang == r.URL.Path
		f.mu.Unlock()
		if hang {
			select {
			case <-f.release:
			case <-r.Context().Done():
			}
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /.well-known/openid-configuration":
			f.serveDiscovery(w)
		case "GET /jwks":
			f.mu.Lock()
			defer f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: f.keys})
		case "POST /token":
			f.serveToken(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	if tls {
		f.server = httptest.NewTLSServer(f.mux)
	} else {
		f.server = httptest.NewServer(f.mux)
	}
	t.Cleanup(f.server.Close)
	// Runs before the server closes: lets a handler that hangs return.
	t.Cleanup(func() { close(f.release) })

	return f
}

func (f *fakeIdP) serveDiscovery(w http.ResponseWriter) {
	f.mu.Lock()
	defer f.mu.Unlock()
	document := map[string]any{
		"issuer": f.server.URL, "authorization_endpoint": f.server.URL + "/authorize",
		"token_endpoint": f.server.URL + "/token", "jwks_uri": f.server.URL + "/jwks",
	}
	for k, v := range f.discovery {
		if v == nil {
			delete(document, k)
		} else {
			document[k] = v
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if f.discoveryCode != 0 {
		w.WriteHeader(f.discoveryCode)
	}
	_ = json.NewEncoder(w).Encode(document)
}

func (f *fakeIdP) serveToken(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.token.authorization, f.token.form = r.Header.Get("Authorization"), r.PostForm
	w.Header().Set("Content-Type", "application/json")
	if f.tokenCode != http.StatusOK {
		w.WriteHeader(f.tokenCode)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": f.tokenError})

		return
	}
	if f.tokenBody != nil {
		_ = json.NewEncoder(w).Encode(f.tokenBody)

		return
	}
	payload, _ := json.Marshal(f.claims)
	signed, _ := f.signer.Sign(payload)
	raw, _ := signed.CompactSerialize()
	_ = json.NewEncoder(w).Encode(map[string]any{"id_token": raw, "access_token": "x", "token_type": "Bearer"})
}

// set changes the fake under its lock.
func (f *fakeIdP) set(change func(f *fakeIdP)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

func (f *fakeIdP) hitsOf(server *httptest.Server, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[strings.TrimPrefix(strings.TrimPrefix(server.URL, "https://"), "http://")+" "+path]
}

// rotate makes a new key the signer and publishes publish.
func (f *fakeIdP) rotate(t *testing.T, kid string, publish bool) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: kid}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signer = signer
	if publish {
		f.keys = append(f.keys, jose.JSONWebKey{Key: &key.PublicKey, KeyID: kid, Algorithm: "RS256", Use: "sig"})
	}
}

func (f *fakeIdP) connection() *types.Connection {
	return &types.Connection{ID: testConnection, Issuer: f.server.URL, ClientID: "client"}
}

func (f *fakeIdP) setClaims(nonce string, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims = map[string]any{
		"iss": f.server.URL, "aud": "client", "exp": now.Add(time.Minute).Unix(), "iat": now.Unix(),
		"nonce": nonce, "sub": "s-1", "email": "a@b.example",
	}
}

// ready is a fake IdP on plain http on loopback, with a published key and
// claims for nonce "n": what only a development deployment talks to.
func ready(t *testing.T) *fakeIdP {
	t.Helper()
	f := newFakeIdP(t, false)
	f.rotate(t, "k1", true)
	f.setClaims("n", time.Now())

	return f
}

// wire records every request the client sends, by path, with the deadline
// its context carried; refuse makes it fail them before they leave, as a
// network that cannot reach the IdP does (every request, or those to
// refusePath only).
type wire struct {
	next http.RoundTripper

	mu         sync.Mutex
	refuse     bool
	refusePath string
	attempts   map[string]int
	deadlines  map[string]time.Time
}

func tap(c *Client) *wire {
	w := &wire{next: c.http.Transport, attempts: map[string]int{}, deadlines: map[string]time.Time{}}
	c.http.Transport = w

	return w
}

func (w *wire) RoundTrip(r *http.Request) (*http.Response, error) {
	w.mu.Lock()
	w.attempts[r.URL.Path]++
	w.deadlines[r.URL.Path], _ = r.Context().Deadline()
	refuse := w.refuse || (w.refusePath != "" && w.refusePath == r.URL.Path)
	w.mu.Unlock()
	if refuse {
		return nil, errors.New("dial tcp: connect: connection refused")
	}

	return w.next.RoundTrip(r)
}

func (w *wire) attemptsOf(path string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.attempts[path]
}

func (w *wire) refusing(refuse bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.refuse, w.refusePath = refuse, ""
}

// refusingOnly refuses the requests to path and lets the others through.
func (w *wire) refusingOnly(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.refuse, w.refusePath = false, path
}

const discoveryPath = "/.well-known/openid-configuration"

func TestNewClient(t *testing.T) {
	c := newTestClient(Config{})
	if c.http.Timeout != limits.IdPTimeout {
		t.Errorf("expected one request bounded by %s, got %s", limits.IdPTimeout, c.http.Timeout)
	}
	if limits.IdPTimeout >= limits.IdPExchangeTimeout {
		t.Error("expected one request to fit in the deadline of the call that makes it")
	}
}

// Outside a development deployment an identity provider is reached over
// https and at a public address only, whichever call asks.
func TestNewClient_HostPolicy(t *testing.T) {
	calls := map[string]func(c *Client, f *fakeIdP) error{
		"Check": func(c *Client, f *fakeIdP) error {
			return c.Check(context.Background(), f.connection(), "s", testRedirect)
		},
		"AuthCodeURL": func(c *Client, f *fakeIdP) error {
			_, err := c.AuthCodeURL(context.Background(), f.connection(), &AuthRequest{Nonce: "n"})
			return err
		},
		"Exchange": func(c *Client, f *fakeIdP) error {
			_, err := c.Exchange(context.Background(), f.connection(), "s", &AuthRequest{Nonce: "n"}, "code")
			return err
		},
	}

	for name, call := range calls {
		t.Run(name+" http issuer", func(t *testing.T) {
			f := ready(t)

			err := call(newTestClient(Config{}), f)
			if !errors.Is(err, ErrInsecureURL) || !errors.Is(err, ErrMisconfigured) {
				t.Errorf("expected %v as %v, got %v", ErrInsecureURL, ErrMisconfigured, err)
			}
			if got := f.hitsOf(f.server, discoveryPath); got != 0 {
				t.Errorf("expected no request to reach the IdP, got %d", got)
			}
		})

		t.Run(name+" https issuer on loopback", func(t *testing.T) {
			f := newFakeIdP(t, true)
			f.rotate(t, "k1", true)

			err := call(newTestClient(Config{}), f)
			if !errors.Is(err, ErrForbiddenAddress) || !errors.Is(err, ErrMisconfigured) {
				t.Errorf("expected %v as %v, got %v", ErrForbiddenAddress, ErrMisconfigured, err)
			}
			if got := f.hitsOf(f.server, discoveryPath); got != 0 {
				t.Errorf("expected no request to reach the IdP, got %d", got)
			}
		})

		t.Run(name+" dev", func(t *testing.T) {
			f := ready(t)
			f.tokenCode = http.StatusBadRequest

			// The fake refuses the made-up code: it was reached.
			err := call(newTestClient(Config{Dev: true}), f)
			if err != nil && !errors.Is(err, ErrRejected) {
				t.Errorf("unexpected error: %v", err)
			}
			if got := f.hitsOf(f.server, discoveryPath); got != 1 {
				t.Errorf("expected one discovery request to reach the IdP, got %d", got)
			}
		})
	}
}

func TestClient_ValidateIssuer(t *testing.T) {
	testCases := []struct {
		name      string
		dev       bool
		issuer    string
		expectErr bool
	}{
		{name: "https", issuer: "https://idp.example"},
		{name: "port and path", issuer: "https://idp.example:8443/realms/acme"},
		{name: "tenant path", issuer: "https://login.microsoftonline.com/0000-tenant/v2.0"},
		// An issuer shared by many organisations is refused when it is used:
		// its discovery document names another issuer.
		{name: "shared issuer", issuer: "https://login.microsoftonline.com/common/v2.0"},
		{name: "http", issuer: "http://idp.example", expectErr: true},
		{name: "http with port", issuer: "http://dex:5556/dex", expectErr: true},
		{name: "userinfo", issuer: "https://user:pw@idp.example", expectErr: true},
		{name: "query", issuer: "https://idp.example/?a=b", expectErr: true},
		{name: "fragment", issuer: "https://idp.example/#fragment", expectErr: true},
		{name: "no scheme", issuer: "idp.example", expectErr: true},
		{name: "scheme-relative", issuer: "//idp.example", expectErr: true},
		{name: "no host", issuer: "https://", expectErr: true},
		{name: "ftp", issuer: "ftp://idp.example", expectErr: true},
		{name: "empty", issuer: "", expectErr: true},
		{name: "dev http", dev: true, issuer: "http://dex:5556/dex"},
		{name: "dev http loopback", dev: true, issuer: "http://127.0.0.1:5557"},
		{name: "dev https", dev: true, issuer: "https://idp.example"},
		{name: "dev userinfo", dev: true, issuer: "http://user:pw@dex:5556/dex", expectErr: true},
		{name: "dev query", dev: true, issuer: "http://dex:5556/dex?a=b", expectErr: true},
		{name: "dev ftp", dev: true, issuer: "ftp://dex", expectErr: true},
		{name: "dev no scheme", dev: true, issuer: "dex:5556", expectErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := newTestClient(Config{Dev: tc.dev}).ValidateIssuer(tc.issuer)
			if tc.expectErr && err == nil {
				t.Error("expected error but got none")
			}
			if !tc.expectErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestClient_AuthCodeURL(t *testing.T) {
	f := ready(t)
	c := newTestClient(Config{Dev: true})

	t.Run("sign-in", func(t *testing.T) {
		target, err := c.AuthCodeURL(context.Background(), f.connection(), &AuthRequest{
			RedirectURI: testRedirect, State: "st", Nonce: "n", PKCEVerifier: "verifier", LoginHint: "a@b.example"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		u, _ := url.Parse(target)
		q := u.Query()
		if !strings.HasPrefix(target, f.server.URL+"/authorize?") || q.Get("client_id") != "client" || q.Get("response_type") != "code" ||
			q.Get("redirect_uri") != testRedirect || q.Get("state") != "st" || q.Get("nonce") != "n" ||
			q.Get("login_hint") != "a@b.example" || q.Get("code_challenge_method") != "S256" {
			t.Errorf("unexpected authorization request %s", target)
		}
		if got := q.Get("scope"); got != "openid email" {
			t.Errorf("expected scope %q, got %q", "openid email", got)
		}
		if challenge := q.Get("code_challenge"); challenge == "" || challenge == "verifier" {
			t.Errorf("expected the verifier's challenge, not the verifier: %s", target)
		}
		if q.Has("prompt") || q.Has("max_age") || q.Has("client_secret") {
			t.Errorf("unexpected authorization request %s", target)
		}
	})

	t.Run("reauthenticate", func(t *testing.T) {
		target, err := c.AuthCodeURL(context.Background(), f.connection(), &AuthRequest{
			RedirectURI: testRedirect, State: "st", Nonce: "n", PKCEVerifier: "v", Reauthenticate: true})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		u, _ := url.Parse(target)
		if u.Query().Get("prompt") != "login" || u.Query().Get("max_age") != "0" || u.Query().Has("login_hint") {
			t.Errorf("expected prompt=login and max_age=0, got %s", target)
		}
	})
}

// What a discovery document may and may not say.
func TestClient_AuthCodeURL_Discovery(t *testing.T) {
	const issuer = "https://idp.example"
	document := func(overrides map[string]any) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != discoveryPath {
				http.NotFound(w, r)
				return
			}
			doc := map[string]any{
				"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
				"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks",
			}
			for k, v := range overrides {
				if v == nil {
					delete(doc, k)
				} else {
					doc[k] = v
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(doc)
		})
	}
	connection := &types.Connection{ID: testConnection, Issuer: issuer, ClientID: "client"}

	testCases := []struct {
		name      string
		overrides map[string]any
		expectErr bool
	}{
		{name: "as discovered"},
		// The token and key endpoints may be on other hosts than the issuer.
		{name: "token and key endpoints elsewhere", overrides: map[string]any{
			"token_endpoint": "https://oauth2.elsewhere.example/token", "jwks_uri": "https://www.elsewhere.example/certs"}},
		{name: "authorization endpoint elsewhere", overrides: map[string]any{"authorization_endpoint": "https://accounts.elsewhere.example/auth"}},
		{name: "another issuer", overrides: map[string]any{"issuer": "https://other.example"}, expectErr: true},
		{name: "issuer with trailing slash", overrides: map[string]any{"issuer": issuer + "/"}, expectErr: true},
		{name: "http authorization endpoint", overrides: map[string]any{"authorization_endpoint": "http://idp.example/authorize"}, expectErr: true},
		{name: "no authorization endpoint", overrides: map[string]any{"authorization_endpoint": nil}, expectErr: true},
		{name: "no token endpoint", overrides: map[string]any{"token_endpoint": nil}, expectErr: true},
		{name: "auth methods not a list", overrides: map[string]any{"token_endpoint_auth_methods_supported": "client_secret_basic"}, expectErr: true},
		{name: "over the size limit", overrides: map[string]any{"padding": strings.Repeat("x", limits.MaxResponseBytes)}, expectErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			target, err := production(document(tc.overrides)).AuthCodeURL(context.Background(), connection,
				&AuthRequest{RedirectURI: testRedirect, Nonce: "n", PKCEVerifier: "v"})
			if tc.expectErr {
				if !errors.Is(err, ErrMisconfigured) {
					t.Errorf("expected %v, got %v", ErrMisconfigured, err)
				}
				return
			}
			if err != nil || !strings.HasPrefix(target, "https://") {
				t.Errorf("expected an https authorization URL, got %q %v", target, err)
			}
		})
	}

	t.Run("not a document", func(t *testing.T) {
		c := production(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) }))

		if _, err := c.AuthCodeURL(context.Background(), connection, &AuthRequest{Nonce: "n"}); !errors.Is(err, ErrMisconfigured) {
			t.Errorf("expected %v, got %v", ErrMisconfigured, err)
		}
	})

	t.Run("redirect not followed", func(t *testing.T) {
		followed := 0
		c := production(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Host != "idp.example" {
				followed++
			}
			http.Redirect(w, r, "https://internal.example/latest/meta-data", http.StatusFound)
		}))

		// production replaces the transport, not the client's redirect rule.
		_, err := c.AuthCodeURL(context.Background(), connection, &AuthRequest{Nonce: "n"})
		if !errors.Is(err, ErrMisconfigured) || followed != 0 {
			t.Errorf("expected %v and no redirect followed, got %v and %d", ErrMisconfigured, err, followed)
		}
	})
}

func TestClient_Exchange(t *testing.T) {
	f := ready(t)
	c := newTestClient(Config{Dev: true})
	now := time.Now()
	c.now = func() time.Time { return now }
	request := &AuthRequest{RedirectURI: testRedirect, Nonce: "n", PKCEVerifier: "v"}
	f.setClaims("n", now)
	// The subject is the id_token's sub, whatever else it carries.
	f.set(func(f *fakeIdP) { f.claims["oid"] = "another-identifier" })

	claims, err := c.Exchange(context.Background(), f.connection(), "secret", request, "code")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claims.Subject != "s-1" || claims.Email != "a@b.example" {
		t.Errorf("expected subject s-1 of a@b.example, got %+v", claims)
	}
	// The code is redeemed with the verifier and the redirect URI of the
	// authorization request, and with the client's credentials in the header.
	form := f.token.form
	if form.Get("grant_type") != "authorization_code" || form.Get("code") != "code" || form.Get("code_verifier") != "v" ||
		form.Get("redirect_uri") != testRedirect || form.Has("client_secret") || !strings.HasPrefix(f.token.authorization, "Basic ") {
		t.Errorf("unexpected token request %v (%s)", form, f.token.authorization)
	}

	t.Run("key rotation", func(t *testing.T) {
		hits := f.hitsOf(f.server, "/jwks")
		f.rotate(t, "k2", true)

		// An unknown kid refetches the keys once.
		if _, err := c.Exchange(context.Background(), f.connection(), "secret", request, "code"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := f.hitsOf(f.server, "/jwks"); got != hits+1 {
			t.Errorf("expected one refetch of the keys, got %d", got-hits)
		}
		if got := f.hitsOf(f.server, discoveryPath); got != 1 {
			t.Errorf("expected discovery read once, got %d", got)
		}
	})
}

// client_secret_basic unless the IdP supports only client_secret_post: read
// from the discovery document, never tried out with the single-use code.
func TestClient_Exchange_AuthMethod(t *testing.T) {
	testCases := []struct {
		name         string
		methods      any
		expectedPost bool
	}{
		{name: "not said"},
		{name: "both", methods: []string{"client_secret_post", "client_secret_basic"}},
		{name: "post only", methods: []string{"client_secret_post", "private_key_jwt"}, expectedPost: true},
		{name: "neither", methods: []string{"private_key_jwt"}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			f := ready(t)
			f.discovery = map[string]any{"token_endpoint_auth_methods_supported": tc.methods}

			if _, err := newTestClient(Config{Dev: true}).Exchange(context.Background(), f.connection(), "s3cret", &AuthRequest{Nonce: "n"}, "code"); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			inForm, inHeader := f.token.form.Get("client_secret") == "s3cret", strings.HasPrefix(f.token.authorization, "Basic ")
			if inForm != tc.expectedPost || inHeader == tc.expectedPost {
				t.Errorf("credentials in the form: %v, in the header: %v", inForm, inHeader)
			}
			if got := f.hitsOf(f.server, "/token"); got != 1 {
				t.Errorf("expected the code sent once, got %d token requests", got)
			}
		})
	}
}

// Signature, iss, aud and exp are go-oidc's checks; exp gets the clock skew
// through the verifier's Now.
func TestClient_Exchange_Verification(t *testing.T) {
	f := ready(t)
	c := newTestClient(Config{Dev: true})
	now := time.Now()
	c.now = func() time.Time { return now }
	request := &AuthRequest{RedirectURI: testRedirect, Nonce: "n", PKCEVerifier: "v"}

	testCases := []struct {
		name      string
		mutate    func(map[string]any)
		expectErr bool
	}{
		{name: "other issuer", mutate: func(c map[string]any) { c["iss"] = "https://evil.example" }, expectErr: true},
		{name: "other audience", mutate: func(c map[string]any) { c["aud"] = "other" }, expectErr: true},
		{name: "expired beyond skew", mutate: func(c map[string]any) { c["exp"] = now.Add(-2 * time.Minute).Unix() }, expectErr: true},
		{name: "expired within skew", mutate: func(c map[string]any) { c["exp"] = now.Add(-30 * time.Second).Unix() }},
		{name: "another nonce", mutate: func(c map[string]any) { c["nonce"] = "other" }, expectErr: true},
		{name: "no sub", mutate: func(c map[string]any) { delete(c, "sub") }, expectErr: true},
		{name: "sub too long", mutate: func(c map[string]any) { c["sub"] = strings.Repeat("s", limits.MaxSubjectLength+1) }, expectErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			f.setClaims("n", now)
			f.set(func(f *fakeIdP) { tc.mutate(f.claims) })

			_, err := c.Exchange(context.Background(), f.connection(), "secret", request, "code")
			if tc.expectErr && !errors.Is(err, ErrInvalidToken) {
				t.Errorf("expected %v, got %v", ErrInvalidToken, err)
			}
			if !tc.expectErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}

	t.Run("unpublished key", func(t *testing.T) {
		f.setClaims("n", now)
		f.rotate(t, "unpublished", false)

		if _, err := c.Exchange(context.Background(), f.connection(), "secret", request, "code"); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("expected %v, got %v", ErrInvalidToken, err)
		}
	})
}

func TestClient_Exchange_SigningAlgorithms(t *testing.T) {
	f := newFakeIdP(t, false)
	c := newTestClient(Config{Dev: true})
	f.setClaims("n", time.Now())

	t.Run("ES256", func(t *testing.T) {
		ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: jose.JSONWebKey{Key: ec, KeyID: "e"}}, nil)
		f.set(func(f *fakeIdP) {
			f.signer = signer
			f.keys = []jose.JSONWebKey{{Key: &ec.PublicKey, KeyID: "e", Algorithm: "ES256", Use: "sig"}}
		})

		if _, err := c.Exchange(context.Background(), f.connection(), "s", &AuthRequest{Nonce: "n"}, "code"); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("HS256", func(t *testing.T) {
		hs, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte(strings.Repeat("k", 32))}, nil)
		f.set(func(f *fakeIdP) { f.signer = hs })

		if _, err := c.Exchange(context.Background(), f.connection(), "s", &AuthRequest{Nonce: "n"}, "code"); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("expected %v, got %v", ErrInvalidToken, err)
		}
	})
}

func TestClient_Exchange_Failures(t *testing.T) {
	f := ready(t)
	c := newTestClient(Config{Dev: true})
	request := &AuthRequest{Nonce: "n"}
	exchange := func() error {
		_, err := c.Exchange(context.Background(), f.connection(), "s", request, "code")
		return err
	}

	testCases := []struct {
		name        string
		status      int
		code        string
		expectedErr error
	}{
		{name: "4xx", status: http.StatusBadRequest, code: "invalid_grant", expectedErr: ErrRejected},
		{name: "401", status: http.StatusUnauthorized, expectedErr: ErrCredentials},
		{name: "invalid_client", status: http.StatusBadRequest, code: "invalid_client", expectedErr: ErrCredentials},
		{name: "unauthorized_client", status: http.StatusBadRequest, code: "unauthorized_client", expectedErr: ErrCredentials},
		{name: "5xx", status: http.StatusBadGateway, code: "server_error", expectedErr: ErrUnavailable},
		{name: "5xx naming the client", status: http.StatusServiceUnavailable, code: "invalid_client", expectedErr: ErrUnavailable},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			f.set(func(f *fakeIdP) { f.tokenCode, f.tokenError = tc.status, tc.code })

			if err := exchange(); !errors.Is(err, tc.expectedErr) {
				t.Errorf("expected %v, got %v", tc.expectedErr, err)
			}
		})
	}

	// However often the IdP failed, the next sign-in asks it.
	t.Run("asked again after failures", func(t *testing.T) {
		f.set(func(f *fakeIdP) { f.tokenCode = http.StatusBadGateway })
		for range 5 {
			if err := exchange(); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("expected %v, got %v", ErrUnavailable, err)
			}
		}
		before := f.hitsOf(f.server, "/token")
		f.set(func(f *fakeIdP) { f.tokenCode = http.StatusOK })

		if err := exchange(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if _, err := c.AuthCodeURL(context.Background(), f.connection(), request); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if got := f.hitsOf(f.server, "/token"); got != before+1 {
			t.Errorf("expected the token endpoint asked once, got %d requests", got-before)
		}
	})

	t.Run("no id_token", func(t *testing.T) {
		f.set(func(f *fakeIdP) {
			f.tokenCode, f.tokenBody = http.StatusOK, map[string]any{"access_token": "x", "token_type": "Bearer"}
		})

		if err := exchange(); !errors.Is(err, ErrRejected) {
			t.Errorf("expected %v, got %v", ErrRejected, err)
		}
	})

	t.Run("not a token response", func(t *testing.T) {
		f.set(func(f *fakeIdP) { f.tokenCode, f.tokenBody = http.StatusOK, map[string]any{"hello": "world"} })

		if err := exchange(); !errors.Is(err, ErrRejected) {
			t.Errorf("expected %v, got %v", ErrRejected, err)
		}
	})
}

// Keys that cannot be fetched are the IdP being unavailable, not a token
// that failed its checks.
func TestClient_Exchange_KeysUnavailable(t *testing.T) {
	f := ready(t)
	c := newTestClient(Config{Dev: true})
	tap(c).refusingOnly("/jwks")

	_, err := c.Exchange(context.Background(), f.connection(), "secret", &AuthRequest{Nonce: "n"}, "code")
	if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrInvalidToken) {
		t.Errorf("expected %v, got %v", ErrUnavailable, err)
	}
}

// Token and key endpoints on another origin than the issuer are used as the
// discovery document names them.
func TestClient_Exchange_EndpointsElsewhere(t *testing.T) {
	f := ready(t)
	elsewhere := httptest.NewServer(f.mux)
	t.Cleanup(elsewhere.Close)
	f.discovery = map[string]any{"token_endpoint": elsewhere.URL + "/token", "jwks_uri": elsewhere.URL + "/jwks"}
	c := newTestClient(Config{Dev: true})

	if err := c.Check(context.Background(), f.connection(), "s", testRedirect); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	claims, err := c.Exchange(context.Background(), f.connection(), "s", &AuthRequest{Nonce: "n"}, "code")
	if err != nil || claims.Subject != "s-1" {
		t.Fatalf("expected subject s-1, got %+v %v", claims, err)
	}
	if f.hitsOf(elsewhere, "/token") != 2 || f.hitsOf(elsewhere, "/jwks") != 1 || f.hitsOf(f.server, "/token") != 0 || f.hitsOf(f.server, "/jwks") != 0 {
		t.Errorf("expected the token and key requests at the other origin, got %v", f.hits)
	}
}

// Check asks the token endpoint with the client's credentials and a code it
// never issued, after reading the discovery document anew.
func TestClient_Check(t *testing.T) {
	testCases := []struct {
		name        string
		status      int
		code        string
		expectedErr error
	}{
		{name: "code refused", status: http.StatusBadRequest, code: "invalid_grant"},
		{name: "client refused", status: http.StatusUnauthorized, code: "invalid_client", expectedErr: ErrCredentials},
		{name: "invalid_client with a 400", status: http.StatusBadRequest, code: "invalid_client", expectedErr: ErrCredentials},
		{name: "unauthorized_client", status: http.StatusBadRequest, code: "unauthorized_client", expectedErr: ErrCredentials},
		{name: "token endpoint down", status: http.StatusBadGateway, expectedErr: ErrUnavailable},
		{name: "no OAuth answer", status: http.StatusNotFound, expectedErr: ErrMisconfigured},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			f := ready(t)
			f.tokenCode, f.tokenError = tc.status, tc.code

			err := newTestClient(Config{Dev: true}).Check(context.Background(), f.connection(), "secret", testRedirect)
			if !errors.Is(err, tc.expectedErr) {
				t.Errorf("expected %v, got %v", tc.expectedErr, err)
			}
			if f.token.form.Get("redirect_uri") != testRedirect || f.token.form.Get("code") == "" {
				t.Errorf("expected a probe with the connection's redirect URI, got %v", f.token.form)
			}
		})
	}

	t.Run("refusal with no response", func(t *testing.T) {
		f := ready(t)
		c := newTestClient(Config{Dev: true})
		next := c.http.Transport
		c.http.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/token" {
				return nil, &oauth2.RetrieveError{}
			}
			return next.RoundTrip(r)
		})

		if err := c.Check(context.Background(), f.connection(), "secret", testRedirect); !errors.Is(err, ErrUnavailable) {
			t.Errorf("expected %v, got %v", ErrUnavailable, err)
		}
	})

	t.Run("discovery read every time", func(t *testing.T) {
		f := ready(t)
		f.tokenCode = http.StatusBadRequest
		c := newTestClient(Config{Dev: true})

		for range 3 {
			if err := c.Check(context.Background(), f.connection(), "secret", testRedirect); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}
		if got := f.hitsOf(f.server, discoveryPath); got != 3 {
			t.Errorf("expected discovery read 3 times, got %d", got)
		}
	})
}

// An issuer shared by many organisations names another issuer in its
// discovery document: that refuses it, on every path.
func TestClient_Discover_AnotherIssuer(t *testing.T) {
	f := ready(t)
	f.discovery = map[string]any{"issuer": f.server.URL + "/{tenantid}/v2.0"}
	c := newTestClient(Config{Dev: true})

	if err := c.Check(context.Background(), f.connection(), "s", testRedirect); !errors.Is(err, ErrMisconfigured) {
		t.Errorf("Check: expected %v, got %v", ErrMisconfigured, err)
	}
	if _, err := c.AuthCodeURL(context.Background(), f.connection(), &AuthRequest{Nonce: "n"}); !errors.Is(err, ErrMisconfigured) {
		t.Errorf("AuthCodeURL: expected %v, got %v", ErrMisconfigured, err)
	}
	if _, err := c.Exchange(context.Background(), f.connection(), "s", &AuthRequest{Nonce: "n"}, "code"); !errors.Is(err, ErrMisconfigured) {
		t.Errorf("Exchange: expected %v, got %v", ErrMisconfigured, err)
	}
	if got := f.hitsOf(f.server, "/token"); got != 0 {
		t.Errorf("expected nothing sent to the endpoints such a document names, got %d token requests", got)
	}
}

// One provider is kept per issuer for DiscoveryTTL. A refresh that cannot
// reach the IdP keeps the one in hand for another DiscoveryTTL: one failed
// fetch, not one per sign-in.
func TestClient_Discover_Cache(t *testing.T) {
	f := ready(t)
	c := newTestClient(Config{Dev: true})
	net := tap(c)
	now := time.Now()
	c.now = func() time.Time { return now }
	expect := func(what string, expectedErr error, fetches int) {
		t.Helper()
		_, err := c.AuthCodeURL(context.Background(), f.connection(), &AuthRequest{Nonce: "n"})
		if !errors.Is(err, expectedErr) {
			t.Fatalf("%s: expected %v, got %v", what, expectedErr, err)
		}
		if got := net.attemptsOf(discoveryPath); got != fetches {
			t.Fatalf("%s: expected %d discovery fetches so far, got %d", what, fetches, got)
		}
	}

	expect("first sign-in", nil, 1)
	expect("second sign-in", nil, 1)
	now = now.Add(limits.DiscoveryTTL - time.Second)
	expect("just before the TTL", nil, 1)
	now = now.Add(2 * time.Second)
	expect("past the TTL", nil, 2)

	// The IdP's discovery goes down.
	now = now.Add(limits.DiscoveryTTL + time.Second)
	net.refusing(true)
	expect("refresh while unreachable", nil, 3)
	expect("right after", nil, 3)
	now = now.Add(limits.DiscoveryTTL - time.Second)
	expect("a TTL later, less a second", nil, 3)
	now = now.Add(2 * time.Second)
	expect("a TTL later", nil, 4)

	// Check never answers from what is kept: with discovery down and the
	// token endpoint up it fails on discovery and sends nothing on.
	net.refusingOnly(discoveryPath)
	if err := c.Check(context.Background(), f.connection(), "s", testRedirect); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected %v, got %v", ErrUnavailable, err)
	}
	if got := net.attemptsOf("/token"); got != 0 {
		t.Fatalf("expected no token request with a document not just read, got %d", got)
	}

	// A document that is back but not usable any more is not served from
	// what is kept.
	net.refusing(false)
	f.set(func(f *fakeIdP) { f.discovery = map[string]any{"issuer": "https://other.example"} })
	now = now.Add(limits.DiscoveryTTL + time.Second)
	expect("refresh finds the document unusable", ErrMisconfigured, 6)

	// With nothing kept, an unreachable IdP is unavailable.
	net.refusing(true)
	other := f.connection()
	other.Issuer = "http://127.0.0.1:1/never-discovered"
	if _, err := c.AuthCodeURL(context.Background(), other, &AuthRequest{Nonce: "n"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected %v, got %v", ErrUnavailable, err)
	}
}

// A provider nobody asked for during a DiscoveryTTL after it expired is
// dropped when another is written.
func TestClient_Discover_Forgets(t *testing.T) {
	unused, used := ready(t), ready(t)
	c := newTestClient(Config{Dev: true})
	now := time.Now()
	c.now = func() time.Time { return now }
	signIn := func(f *fakeIdP) {
		t.Helper()
		if _, err := c.AuthCodeURL(context.Background(), f.connection(), &AuthRequest{Nonce: "n"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	kept := func(f *fakeIdP) bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.providers[f.server.URL] != nil
	}

	signIn(unused)
	now = now.Add(2*limits.DiscoveryTTL - time.Second)
	signIn(used)
	if !kept(unused) {
		t.Fatal("expected a provider expired for less than a DiscoveryTTL kept")
	}
	now = now.Add(limits.DiscoveryTTL + time.Second)
	signIn(used)
	if kept(unused) || !kept(used) {
		t.Fatalf("expected only the provider in use kept, got unused %v and used %v", kept(unused), kept(used))
	}
}

// A 5xx from the discovery endpoint is the IdP being unavailable: a refresh
// that gets one keeps the provider in hand, and with nothing kept the
// sign-in is refused as unavailable, not as a misconfiguration.
func TestClient_Discover_ServerError(t *testing.T) {
	f := ready(t)
	c := newTestClient(Config{Dev: true})
	net := tap(c)
	now := time.Now()
	c.now = func() time.Time { return now }
	signIn := func() error {
		_, err := c.AuthCodeURL(context.Background(), f.connection(), &AuthRequest{Nonce: "n"})
		return err
	}

	f.set(func(f *fakeIdP) { f.discoveryCode = http.StatusServiceUnavailable })
	if err := signIn(); !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrMisconfigured) {
		t.Fatalf("expected %v with nothing kept, got %v", ErrUnavailable, err)
	}

	f.set(func(f *fakeIdP) { f.discoveryCode = 0 })
	if err := signIn(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	fetched := net.attemptsOf(discoveryPath)

	now = now.Add(limits.DiscoveryTTL + time.Second)
	f.set(func(f *fakeIdP) { f.discoveryCode = http.StatusBadGateway })
	if err := signIn(); err != nil {
		t.Fatalf("expected the provider in hand kept, got %v", err)
	}
	if err := signIn(); err != nil || net.attemptsOf(discoveryPath) != fetched+1 {
		t.Fatalf("expected no second fetch, got %v and %d fetches (had %d)", err, net.attemptsOf(discoveryPath), fetched)
	}
}

// What an IdP says about a failure is logged with the error: it is quoted
// and cut, and the kind stays first.
func TestClient_Discover_ErrorQuoted(t *testing.T) {
	f := ready(t)
	c := newTestClient(Config{Dev: true})
	forged := "x\n{\"level\":\"error\",\"msg\":\"forged\"}" + strings.Repeat("A", 5000)
	f.set(func(f *fakeIdP) { f.discovery = map[string]any{"issuer": forged} })

	_, err := c.AuthCodeURL(context.Background(), f.connection(), &AuthRequest{Nonce: "n"})
	if !errors.Is(err, ErrMisconfigured) {
		t.Fatalf("expected %v, got %v", ErrMisconfigured, err)
	}
	text := err.Error()
	if len(text) > 400 || strings.ContainsAny(text, "\n\r") || !strings.HasPrefix(text, ErrMisconfigured.Error()) {
		t.Errorf("expected one short quoted line starting with the kind, got %d bytes: %.300s", len(text), text)
	}
}

// Everything one call asks of the IdP shares one deadline,
// limits.IdPExchangeTimeout from when the call began. The HTTP client's own
// timeout for one request, which is shorter and would hide it, is out of the
// way here.
func TestClient_Timeout(t *testing.T) {
	near := func(t *testing.T, what string, deadline, started time.Time) {
		t.Helper()
		if deadline.IsZero() {
			t.Fatalf("%s: the request carried no deadline", what)
		}
		if deadline.Before(started.Add(limits.IdPExchangeTimeout-time.Second)) || deadline.After(started.Add(limits.IdPExchangeTimeout+time.Second)) {
			t.Fatalf("%s: expected a deadline %s after the call began, got %s", what, limits.IdPExchangeTimeout, deadline.Sub(started))
		}
	}

	t.Run("Exchange", func(t *testing.T) {
		f := ready(t)
		c := clientWithHTTPTimeout(time.Hour)
		net := tap(c)

		started := time.Now()
		if _, err := c.Exchange(context.Background(), f.connection(), "s", &AuthRequest{Nonce: "n"}, "code"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		near(t, "discovery", net.deadlines[discoveryPath], started)
		near(t, "token", net.deadlines["/token"], started)
		if !net.deadlines[discoveryPath].Equal(net.deadlines["/token"]) {
			t.Errorf("expected one deadline, got %s for discovery and %s for the token request", net.deadlines[discoveryPath], net.deadlines["/token"])
		}
	})

	t.Run("Check", func(t *testing.T) {
		f := ready(t)
		f.tokenCode = http.StatusBadRequest
		c := clientWithHTTPTimeout(time.Hour)
		net := tap(c)

		started := time.Now()
		if err := c.Check(context.Background(), f.connection(), "s", testRedirect); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		near(t, "discovery", net.deadlines[discoveryPath], started)
		if !net.deadlines[discoveryPath].Equal(net.deadlines["/token"]) {
			t.Errorf("expected one deadline, got %s for discovery and %s for the probe", net.deadlines[discoveryPath], net.deadlines["/token"])
		}
	})

	t.Run("AuthCodeURL", func(t *testing.T) {
		f := ready(t)
		c := clientWithHTTPTimeout(time.Hour)
		net := tap(c)

		started := time.Now()
		if _, err := c.AuthCodeURL(context.Background(), f.connection(), &AuthRequest{Nonce: "n"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		near(t, "discovery", net.deadlines[discoveryPath], started)
	})

	t.Run("shorter caller deadline", func(t *testing.T) {
		f := ready(t)
		c := clientWithHTTPTimeout(time.Hour)
		net := tap(c)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		if _, err := c.AuthCodeURL(ctx, f.connection(), &AuthRequest{Nonce: "n"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if left := time.Until(net.deadlines[discoveryPath]); left > time.Second {
			t.Errorf("expected the caller's own second kept, got %s left", left)
		}
	})

	// As deployed, one request is cut sooner: by the HTTP client's timeout.
	t.Run("one request", func(t *testing.T) {
		f := ready(t)
		c := newTestClient(Config{Dev: true})
		net := tap(c)

		started := time.Now()
		if _, err := c.AuthCodeURL(context.Background(), f.connection(), &AuthRequest{Nonce: "n"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		deadline := net.deadlines[discoveryPath]
		if deadline.Before(started.Add(limits.IdPTimeout-time.Second)) || deadline.After(started.Add(limits.IdPTimeout+time.Second)) {
			t.Errorf("expected %s for one request, got %s", limits.IdPTimeout, deadline.Sub(started))
		}
	})
}

// An IdP that never answers costs a caller its deadline and no more, and
// reads as unavailable.
func TestClient_Timeout_NoAnswer(t *testing.T) {
	const patience = 300 * time.Millisecond
	within := func(t *testing.T, started time.Time) {
		t.Helper()
		if took := time.Since(started); took < patience/2 || took > limits.IdPTimeout/2 {
			t.Errorf("returned after %s with %s to wait", took, patience)
		}
	}

	t.Run("token endpoint", func(t *testing.T) {
		f := ready(t)
		c := newTestClient(Config{Dev: true})
		if _, err := c.AuthCodeURL(context.Background(), f.connection(), &AuthRequest{Nonce: "n"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		f.set(func(f *fakeIdP) { f.hang = "/token" })
		ctx, cancel := context.WithTimeout(context.Background(), patience)
		defer cancel()

		started := time.Now()
		if _, err := c.Exchange(ctx, f.connection(), "s", &AuthRequest{Nonce: "n"}, "code"); !errors.Is(err, ErrUnavailable) {
			t.Errorf("expected %v, got %v", ErrUnavailable, err)
		}
		within(t, started)
	})

	t.Run("discovery", func(t *testing.T) {
		f := ready(t)
		f.hang = discoveryPath
		ctx, cancel := context.WithTimeout(context.Background(), patience)
		defer cancel()

		started := time.Now()
		if err := newTestClient(Config{Dev: true}).Check(ctx, f.connection(), "s", testRedirect); !errors.Is(err, ErrUnavailable) {
			t.Errorf("expected %v, got %v", ErrUnavailable, err)
		}
		within(t, started)
	})

	t.Run("keys", func(t *testing.T) {
		f := ready(t)
		f.hang = "/jwks"
		ctx, cancel := context.WithTimeout(context.Background(), patience)
		defer cancel()

		started := time.Now()
		if _, err := newTestClient(Config{Dev: true}).Exchange(ctx, f.connection(), "s", &AuthRequest{Nonce: "n"}, "code"); err == nil {
			t.Error("expected error but got none")
		}
		within(t, started)
	})

	// The HTTP client's timeout for one request, with no deadline from the
	// caller at all.
	t.Run("token endpoint, client timeout", func(t *testing.T) {
		f := ready(t)
		f.hang = "/token"

		started := time.Now()
		_, err := clientWithHTTPTimeout(patience).Exchange(context.Background(), f.connection(), "s", &AuthRequest{Nonce: "n"}, "code")
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("expected %v, got %v", ErrUnavailable, err)
		}
		within(t, started)
	})

	t.Run("discovery, client timeout", func(t *testing.T) {
		f := ready(t)
		f.hang = discoveryPath

		started := time.Now()
		_, err := clientWithHTTPTimeout(patience).AuthCodeURL(context.Background(), f.connection(), &AuthRequest{Nonce: "n"})
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("expected %v, got %v", ErrUnavailable, err)
		}
		within(t, started)
	})
}

func TestCheckClaims(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	base := func() map[string]any {
		return map[string]any{
			"iss": "https://idp.example", "aud": "client", "exp": now.Add(time.Minute).Unix(),
			"iat": now.Unix(), "nonce": "n", "sub": "subject-1",
			"email": "a@b.example", "auth_time": now.Add(-time.Second).Unix(),
		}
	}
	verified, unverified := true, false

	testCases := []struct {
		name                  string
		nonce                 string
		mutate                func(map[string]any)
		expectedEmailVerified *bool
		expectErr             bool
	}{
		{name: "valid", nonce: "n"},
		{name: "audience list with azp", nonce: "n",
			mutate: func(c map[string]any) { c["aud"] = []string{"client", "x"}; c["azp"] = "client" }},
		{name: "audience list without azp", nonce: "n",
			mutate: func(c map[string]any) { c["aud"] = []string{"client", "x"} }, expectErr: true},
		{name: "azp for another client", nonce: "n",
			mutate: func(c map[string]any) { c["azp"] = "other" }, expectErr: true},
		{name: "iat ahead", nonce: "n",
			mutate: func(c map[string]any) { c["iat"] = now.Add(2 * time.Minute).Unix() }},
		{name: "no iat", nonce: "n", mutate: func(c map[string]any) { delete(c, "iat") }},
		{name: "no nonce", nonce: "n", mutate: func(c map[string]any) { delete(c, "nonce") }, expectErr: true},
		{name: "empty nonce asked", nonce: "", mutate: func(c map[string]any) { c["nonce"] = "" }, expectErr: true},
		{name: "email_verified true", nonce: "n",
			mutate: func(c map[string]any) { c["email_verified"] = true }, expectedEmailVerified: &verified},
		{name: "email_verified false", nonce: "n",
			mutate: func(c map[string]any) { c["email_verified"] = false }, expectedEmailVerified: &unverified},
		{name: "email_verified as a string", nonce: "n",
			mutate: func(c map[string]any) { c["email_verified"] = "true" }, expectedEmailVerified: &verified},
		{name: "email_verified as an upper-case string", nonce: "n",
			mutate: func(c map[string]any) { c["email_verified"] = "FALSE" }, expectedEmailVerified: &unverified},
		{name: "email_verified unreadable", nonce: "n",
			mutate: func(c map[string]any) { c["email_verified"] = "maybe" }},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			if tc.mutate != nil {
				tc.mutate(c)
			}

			claims, err := checkClaims("subject-1", claimsJSON(t, c), "client", tc.nonce)
			if tc.expectErr {
				if !errors.Is(err, ErrInvalidToken) {
					t.Errorf("expected %v, got %v", ErrInvalidToken, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if claims.Subject != "subject-1" || claims.Email != "a@b.example" || claims.AuthTime == nil || !claims.AuthTime.Equal(now.Add(-time.Second)) {
				t.Errorf("unexpected claims %+v", claims)
			}
			switch {
			case tc.expectedEmailVerified == nil && claims.EmailVerified != nil:
				t.Errorf("expected email_verified unsaid, got %v", *claims.EmailVerified)
			case tc.expectedEmailVerified != nil && (claims.EmailVerified == nil || *claims.EmailVerified != *tc.expectedEmailVerified):
				t.Errorf("expected email_verified %v, got %v", *tc.expectedEmailVerified, claims.EmailVerified)
			}
		})
	}

	t.Run("not JSON", func(t *testing.T) {
		if _, err := checkClaims("subject-1", []byte("{"), "client", "n"); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("expected %v, got %v", ErrInvalidToken, err)
		}
	})
}

func TestValidSubject(t *testing.T) {
	testCases := []struct {
		name      string
		subject   string
		expectErr bool
	}{
		{name: "at the limit", subject: strings.Repeat("s", limits.MaxSubjectLength)},
		{name: "not ASCII", subject: "sübject", expectErr: true},
		{name: "control character", subject: "sub\nject", expectErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := validSubject(tc.subject)
			if tc.expectErr && err == nil {
				t.Error("expected error but got none")
			}
			if !tc.expectErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// The token endpoint's error code is the IdP's text and ends up in a log
// line: it is quoted, so it cannot start a line of its own, and cut short,
// so it cannot flood one.
func TestTokenError(t *testing.T) {
	const kept = `invalid_grant` + "\n" + `level=error msg="forged" a` // 40 characters
	if len(kept) != 40 {
		t.Fatalf("the test's own arithmetic: %d", len(kept))
	}
	code := kept + "CUT-FROM-HERE" + strings.Repeat("A", 4096)

	testCases := []struct {
		name        string
		status      int
		code        string
		expectedErr error
	}{
		{name: "rejected", status: http.StatusBadRequest, code: code, expectedErr: ErrRejected},
		{name: "credentials refused", status: http.StatusUnauthorized, code: code, expectedErr: ErrCredentials},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := tokenError(tc.status, tc.code)
			if !errors.Is(err, tc.expectedErr) {
				t.Errorf("expected %v, got %v", tc.expectedErr, err)
			}
			text := err.Error()
			if strings.ContainsAny(text, "\n\r") {
				t.Errorf("expected one line, got %q", text)
			}
			quoted := strconv.Quote(tc.code)
			if tc.code == code {
				quoted = strconv.Quote(kept)
			}
			if !strings.HasSuffix(text, strconv.Itoa(tc.status)+" "+quoted) {
				t.Errorf("expected the status and the first 40 characters of the code, quoted, got %q", text)
			}
			if strings.Contains(text, "CUT-FROM-HERE") || len(text) > 200 {
				t.Errorf("expected the code cut, got %d characters", len(text))
			}
		})
	}
}
