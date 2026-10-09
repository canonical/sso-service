// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"buf.build/go/protovalidate"
	v0sso "github.com/canonical/identity-platform-api/v0/sso"
	v0tenant "github.com/canonical/identity-platform-api/v0/tenant"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/canonical/sso-service/internal/apierrors"
	"github.com/canonical/sso-service/internal/db"
	"github.com/canonical/sso-service/internal/grpcutil"
	"github.com/canonical/sso-service/internal/hydra"
	"github.com/canonical/sso-service/internal/idp"
	"github.com/canonical/sso-service/internal/kratos"
	"github.com/canonical/sso-service/internal/limits"
	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/monitoring"
	"github.com/canonical/sso-service/internal/secrets"
	"github.com/canonical/sso-service/internal/storage"
	"github.com/canonical/sso-service/internal/tenants"
	"github.com/canonical/sso-service/internal/testhelpers"
	"github.com/canonical/sso-service/internal/testhelpers/mockidp"
	"github.com/canonical/sso-service/internal/tracing"
	"github.com/canonical/sso-service/internal/types"
	"github.com/canonical/sso-service/pkg/admin"
	"github.com/canonical/sso-service/pkg/authentication"
	"github.com/canonical/sso-service/pkg/bridge"
	"github.com/canonical/sso-service/pkg/sso"
	"github.com/canonical/sso-service/pkg/web"
)

// shared is the one Postgres container of this package's integration tests;
// every test takes a database of its own in it.
var shared testhelpers.SharedContainers

func TestMain(m *testing.M) {
	code := m.Run()
	shared.Close()
	os.Exit(code)
}

// tokens the fake verifier knows.
var ownerID = uuid.NewString()

// staticVerifier maps a token to its subject.
type staticVerifier map[string]string

func (v staticVerifier) VerifyToken(_ context.Context, rawToken string) (string, error) {
	if subject, ok := v[rawToken]; ok {
		return subject, nil
	}
	return "", errors.New("unknown token")
}

// plane is sso-service in process, wired as serve wires it: its store on a
// real Postgres, its gRPC and HTTP listeners, the mock IdP, and fakes for
// hydra-sso, Kratos and tenant-service.
type plane struct {
	kratos  *fakeKratos
	hydra   *fakeHydra
	tenants *fakeTenants
	idp     *mockidp.Server
	idpURL  string
	ssoURL  string
	sso     *httptest.Server
	user    v0sso.SSOSignInServiceClient
	// admin and platform are the two admin services: one handler behind both.
	admin    v0sso.SSOTenantAdminServiceClient
	platform v0sso.SSOPlatformAdminServiceClient
}

func startPlane(t *testing.T) *plane {
	t.Helper()

	return startPlaneWith(t, staticVerifier{
		"login-ui":       "login-ui",
		"owner":          ownerID,
		"platform-admin": "platform-admin-tool",
	})
}

// startPlaneWith is startPlane with the caller's verifier of access tokens.
func startPlaneWith(t *testing.T, verifier authentication.TokenVerifierInterface) *plane {
	t.Helper()
	logger := logging.NewNoopLogger()
	tracer := tracing.NewNoopTracer()
	monitor := monitoring.NewNoopMonitor("sso-service-test", logger)

	dsn := shared.Postgres.IsolatedDB(t)
	if err := checkMigrations(context.Background(), dsn); err != nil {
		t.Fatalf("the database is not migrated: %v", err)
	}
	client, err := db.NewDBClient(context.Background(), db.Config{
		DSN: dsn, MaxConns: 10, MinConns: 1, MaxConnLifetime: time.Hour, MaxConnIdleTime: 30 * time.Minute,
	}, tracer, monitor, logger)
	if err != nil {
		t.Fatalf("failed to create the DB client: %v", err)
	}
	t.Cleanup(client.Close)
	store := storage.NewStorage(client, tracer, monitor, logger)

	p := &plane{kratos: newFakeKratos(), hydra: newFakeHydra()}
	p.tenants = newFakeTenants(p.kratos)

	kratosServer := httptest.NewServer(p.kratos)
	t.Cleanup(kratosServer.Close)
	hydraServer := httptest.NewServer(p.hydra)
	t.Cleanup(hydraServer.Close)

	// tenant-service over real gRPC, checking sso-service's token.
	tenantListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tenantServer := grpc.NewServer(grpc.UnaryInterceptor(requireToken))
	v0tenant.RegisterTenantSignInServiceServer(tenantServer, p.tenants)
	v0tenant.RegisterTenantSSOPolicyServiceServer(tenantServer, p.tenants)
	go func() { _ = tenantServer.Serve(tenantListener) }()
	t.Cleanup(tenantServer.Stop)
	tokenSource := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "svc-token"})
	conn, err := tenants.NewGRPCConn(tenantListener.Addr().String(), false, tenants.DialOptions(tokenSource)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	tenantsClient := tenants.NewClient(v0tenant.NewTenantSignInServiceClient(conn), v0tenant.NewTenantSSOPolicyServiceClient(conn),
		5*time.Second, tracer, monitor, logger)

	// sso-service's HTTP listener over TLS: the binding cookie is __Host-.
	p.sso = httptest.NewUnstartedServer(nil)
	p.ssoURL = "https://" + p.sso.Listener.Addr().String()

	// The company IdP, over plain http on loopback: only a development
	// deployment (DEV) talks to it. Its one client has every connection's
	// redirect URI: sso-service's callback, which ends in the connection's id.
	idpServer := httptest.NewUnstartedServer(nil)
	p.idpURL = "http://" + idpServer.Listener.Addr().String()
	p.idp, err = mockidp.New(mockidp.Config{Issuer: p.idpURL, ClientID: "mock-client", ClientSecret: "mock-secret",
		RedirectURIPrefix: p.ssoURL + types.CallbackPath + "/"})
	if err != nil {
		t.Fatal(err)
	}
	idpServer.Config.Handler = p.idp.Handler()
	idpServer.Start()
	t.Cleanup(idpServer.Close)

	envelope, err := secrets.NewEnvelope(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("e", 32))))
	if err != nil {
		t.Fatal(err)
	}
	validator, err := protovalidate.New()
	if err != nil {
		t.Fatal(err)
	}
	idpClient := idp.NewClient(idp.Config{Dev: true}, tracer, monitor, logger)
	kratosClient := kratos.NewClient(kratosServer.URL, tracer, monitor, logger)
	hydraClient := hydra.NewClient(hydraServer.URL, tracer, monitor, logger)

	adminHandler := admin.NewHandler(
		admin.NewService(store, tenantsClient, idpClient, envelope, p.ssoURL, tracer, monitor, logger),
		validator, p.ssoURL, tracer, logger)
	ssoHandler := sso.NewHandler(sso.NewService(store, kratosClient, envelope, tracer, monitor, logger), validator, tracer, logger)
	bridgeService := bridge.NewService(store, idpClient, hydraClient, kratosClient, tenantsClient, envelope,
		p.ssoURL, tracer, monitor, logger)

	// Authentication only: any valid token reaches any RPC.
	authMiddleware := authentication.NewMiddleware(verifier, tracer, monitor, logger)
	p.sso.Config.Handler = http.MaxBytesHandler(web.NewRouter(bridge.NewAPI(bridgeService, tracer, logger), adminHandler, authMiddleware,
		limits.RequestTimeout, tracer, monitor, logger), maxRequestBytes)
	p.sso.StartTLS()
	t.Cleanup(p.sso.Close)

	grpcListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxRequestBytes),
		grpc.ChainUnaryInterceptor(
			logging.LoggingUnaryInterceptor(logger),
			grpcutil.DeadlineUnaryInterceptor(limits.RequestTimeout),
			authMiddleware.GRPCInterceptor,
		),
	)
	v0sso.RegisterSSOSignInServiceServer(grpcServer, ssoHandler)
	v0sso.RegisterSSOTenantAdminServiceServer(grpcServer, adminHandler)
	v0sso.RegisterSSOPlatformAdminServiceServer(grpcServer, adminHandler)
	go func() { _ = grpcServer.Serve(grpcListener) }()
	t.Cleanup(grpcServer.Stop)
	grpcClient, err := grpc.NewClient(grpcListener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = grpcClient.Close() })
	p.user = v0sso.NewSSOSignInServiceClient(grpcClient)
	p.admin = v0sso.NewSSOTenantAdminServiceClient(grpcClient)
	p.platform = v0sso.NewSSOPlatformAdminServiceClient(grpcClient)

	return p
}

func as(token string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token)
}

// browser is one user's browser for one sign-in: its own cookies, and
// redirects followed by hand.
type browser struct {
	t      *testing.T
	client *http.Client
}

func (p *plane) browser(t *testing.T) *browser {
	jar, _ := cookiejar.New(nil)
	// A copy: httptest hands out one shared client.
	client := new(http.Client)
	*client = *p.sso.Client()
	client.Jar = jar
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &browser{t: t, client: client}
}

// get answers with the body read, kept readable for the caller.
func (b *browser) get(target string) *http.Response {
	b.t.Helper()
	resp, err := b.client.Get(target)
	if err != nil {
		b.t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp
}

func (b *browser) post(target string, form url.Values) *http.Response {
	b.t.Helper()
	resp, err := b.client.PostForm(target, form)
	if err != nil {
		b.t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

// answerOf logs in at the mock IdP from its authorization URL and returns
// the callback URL the IdP sends the browser back to: its answer, on its way
// to the connection's redirect URI.
func (b *browser) answerOf(p *plane, authorizeURL, login string) string {
	b.t.Helper()
	back := b.post(p.idpURL+"/authorize", formFrom(authorizeURL, login))
	if back.StatusCode != http.StatusFound || !strings.HasPrefix(back.Header.Get("Location"), p.ssoURL+"/callback/") {
		b.t.Fatalf("the IdP did not send the browser back: %d %s", back.StatusCode, back.Header.Get("Location"))
	}
	return back.Header.Get("Location")
}

// atIdP logs in at the mock IdP from its authorization URL and follows the
// redirect back to sso-service's callback.
func (b *browser) atIdP(p *plane, authorizeURL, login string) *http.Response {
	b.t.Helper()
	return b.get(b.answerOf(p, authorizeURL, login))
}

// toIdP opens /login for a hydra-sso challenge and returns the IdP's
// authorization URL the browser is sent to.
func (b *browser) toIdP(p *plane, challenge string) string {
	b.t.Helper()
	toIdP := b.get(p.ssoURL + "/login?login_challenge=" + challenge)
	if toIdP.StatusCode != http.StatusFound || !strings.HasPrefix(toIdP.Header.Get("Location"), p.idpURL) {
		b.t.Fatalf("/login did not send the browser to the IdP: %d %s", toIdP.StatusCode, toIdP.Header.Get("Location"))
	}
	return toIdP.Header.Get("Location")
}

// signIn runs /login → IdP → /callback for ticket and returns the callback's
// answer and the hydra-sso challenge.
func (p *plane) signIn(t *testing.T, b *browser, ticket, login string) (*http.Response, string) {
	t.Helper()
	challenge := p.hydra.challenge(ticket)
	return b.atIdP(p, b.toIdP(p, challenge), login), challenge
}

// receipt is the receipt the browser holds for ticket, found as the login UI
// finds it: by the name of its cookie. "" when the browser holds none.
func (b *browser) receipt(p *plane, ticket string) string {
	sum := sha256.Sum256([]byte(ticket))
	name := "__Host-sso_receipt_" + hex.EncodeToString(sum[:])[:16]
	ssoURL, _ := url.Parse(p.ssoURL)
	for _, cookie := range b.client.Jar.Cookies(ssoURL) {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

// complete is the login UI's question when b comes back with a session of
// identity: did the sign-in ticket started end at that account, in b.
func (p *plane) complete(b *browser, ticket, identity string) (*v0sso.CompleteAttemptResponse, error) {
	return p.user.CompleteAttempt(as("login-ui"), &v0sso.CompleteAttemptRequest{Ticket: ticket, IdentityId: identity, Receipt: b.receipt(p, ticket)})
}

func (p *plane) putIdPUser(t *testing.T, login string, u mockidp.User) {
	t.Helper()
	body := `{"sub":"` + u.Subject + `","email":"` + u.Email + `"`
	if u.EmailVerified != nil {
		if *u.EmailVerified {
			body += `,"email_verified":true`
		} else {
			body += `,"email_verified":false`
		}
	}
	req, _ := http.NewRequest(http.MethodPut, p.idpURL+"/admin/users/"+login, strings.NewReader(body+"}"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("put IdP user: %v %v", resp, err)
	}
	resp.Body.Close()
}

// tested registers a connection of tenant at the mock IdP's issuer and
// passes its test sign-in.
func (p *plane) tested(t *testing.T, tenant, label string) *v0sso.Connection {
	t.Helper()
	created, err := p.admin.CreateConnection(as("owner"), &v0sso.CreateConnectionRequest{
		TenantId: tenant, Label: label, Issuer: p.idpURL, ClientId: "mock-client", ClientSecret: "mock-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	test, err := p.admin.StartTestLogin(as("owner"), &v0sso.StartTestLoginRequest{TenantId: tenant, ConnectionId: created.Connection.Id})
	if err != nil {
		t.Fatal(err)
	}
	if page := p.browser(t).atIdP(p, test.Url, "tester"); page.StatusCode != http.StatusOK {
		t.Fatalf("test page %d", page.StatusCode)
	}
	got, err := p.admin.GetConnection(as("owner"), &v0sso.GetConnectionRequest{TenantId: tenant, ConnectionId: created.Connection.Id})
	if err != nil || got.Connection.Status != v0sso.ConnectionStatus_CONNECTION_STATUS_TESTED {
		t.Fatalf("%v %v", got, err)
	}
	return got.Connection
}

// elsewhere is the IdP's answer for one connection's redirect URI,
// addressed to another connection's.
func elsewhere(t *testing.T, answer, from, to string) string {
	t.Helper()
	if !strings.Contains(answer, "/callback/"+from+"?") {
		t.Fatalf("the answer is not addressed to %s: %s", from, answer)
	}
	return strings.Replace(answer, "/callback/"+from+"?", "/callback/"+to+"?", 1)
}

// bindingsOf lists a policy's bindings as "<connection id> active|inactive".
func bindingsOf(policy *v0tenant.TenantSSOPolicy) []string {
	out := make([]string, 0, len(policy.GetBindings()))
	for _, b := range policy.GetBindings() {
		state := "inactive"
		if b.GetActive() {
			state = "active"
		}
		out = append(out, b.GetConnectionId()+" "+state)
	}
	slices.Sort(out)
	return out
}

func expectReason(t *testing.T, err error, reason string) {
	t.Helper()
	if apierrors.Reason(err) != reason {
		t.Fatalf("expected %s, got %v", reason, err)
	}
}

// TestIntegration_CompanySignIn is a whole company sign-in over sso-service's
// real listeners: a tenant admin registers and tests a connection, a platform admin sets
// the domains and the tenant its policy; a member's first sign-in, a pending
// invitation, an account with a password (account linking) and auto-join are
// let through to Kratos, which writes every link; and the connection is
// deleted by the tenant and by a platform admin.
func TestIntegration_CompanySignIn(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	p := startPlane(t)
	yes := true

	acme := p.tenants.tenant("Acme")
	p.tenants.join(acme, ownerID)

	// A tenant admin registers the connection: a draft, with a redirect URI
	// of its own to register at the IdP.
	created, err := p.admin.CreateConnection(as("owner"), &v0sso.CreateConnectionRequest{
		TenantId: acme, Label: "Acme Mock", Issuer: p.idpURL,
		ClientId: "mock-client", ClientSecret: "mock-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	conn := created.Connection
	if conn.Status != v0sso.ConnectionStatus_CONNECTION_STATUS_DRAFT || conn.RedirectUri != p.ssoURL+"/callback/"+conn.Id || conn.CreatedBy != ownerID {
		t.Fatalf("connection %v", conn)
	}
	// Another tenant's path sees none of Acme's connections (the gateway
	// decides who may use which path; sso-service only scopes by it).
	other := p.tenants.tenant("Other")
	if listed, err := p.admin.ListConnections(as("owner"), &v0sso.ListConnectionsRequest{TenantId: other}); err != nil || len(listed.Connections) != 0 {
		t.Fatalf("expected none, got %v %v", listed, err)
	}
	_, err = p.admin.GetConnection(as("owner"), &v0sso.GetConnectionRequest{TenantId: other, ConnectionId: conn.Id})
	expectReason(t, err, apierrors.ConnectionNotFound)
	// Any valid token is enough; an invalid one is not.
	if _, err := p.admin.ListConnections(as("garbage"), &v0sso.ListConnectionsRequest{TenantId: acme}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}

	// A platform admin sets the domains (the tenant's own routes cannot). The
	// tenant never wrote its enforcement, so company sign-in stays off.
	domains, err := p.platform.SetTenantDomains(as("platform-admin"), &v0sso.SetTenantDomainsRequest{TenantId: acme, Domains: []string{"test.example"}})
	if err != nil || !slices.Equal(domains.Policy.Domains, []string{"test.example"}) || len(domains.Policy.Bindings) != 0 ||
		domains.Policy.Enforcement != v0tenant.Enforcement_ENFORCEMENT_OFF || domains.Policy.AutoJoin {
		t.Fatalf("%v %v", domains, err)
	}

	// A draft cannot be active, nor offered.
	required := &v0sso.PutTenantSSOPolicyRequest{TenantId: acme, Enforcement: v0tenant.Enforcement_ENFORCEMENT_REQUIRED,
		Bindings: []*v0tenant.SSOBinding{{ConnectionId: conn.Id, Active: true}}}
	_, err = p.admin.PutTenantSSOPolicy(as("owner"), required)
	expectReason(t, err, apierrors.ConnectionNotTested)
	if _, err := p.user.StartAttempt(as("login-ui"), &v0sso.StartAttemptRequest{TenantId: acme, Email: "a@test.example", ConnectionId: conn.Id}); apierrors.Reason(err) != apierrors.NotApplicable {
		t.Fatalf("a draft is not applicable: %v", err)
	}

	// The test sign-in, through the IdP and back, sets tested.
	p.putIdPUser(t, "tester", mockidp.User{Subject: "tester-sub", Email: "tester@test.example", EmailVerified: &yes})
	test, err := p.admin.StartTestLogin(as("owner"), &v0sso.StartTestLoginRequest{TenantId: acme, ConnectionId: conn.Id})
	if err != nil {
		t.Fatal(err)
	}
	if u, _ := url.Parse(test.Url); u.Query().Get("prompt") != "login" || u.Query().Get("max_age") != "0" || u.Query().Get("redirect_uri") != conn.RedirectUri {
		t.Fatalf("a test sign-in asks for a fresh sign-in, answered at the connection's redirect URI: %s", test.Url)
	}
	testBrowser := p.browser(t)
	answer := testBrowser.answerOf(p, test.Url, "tester")
	if page := testBrowser.get(answer); page.StatusCode != http.StatusOK || !strings.Contains(readBody(t, page), "Test sign-in succeeded") {
		t.Fatalf("test page %d", page.StatusCode)
	}
	got, err := p.admin.GetConnection(as("owner"), &v0sso.GetConnectionRequest{TenantId: acme, ConnectionId: conn.Id})
	if err != nil || got.Connection.Status != v0sso.ConnectionStatus_CONNECTION_STATUS_TESTED || got.Connection.TestTime == "" {
		t.Fatalf("%v %v", got, err)
	}
	// The answer again: its code is spent, the page says so, and the
	// connection stays tested, since when it was.
	if page := testBrowser.get(answer); page.StatusCode != http.StatusOK || !strings.Contains(readBody(t, page), "Test sign-in failed") {
		t.Fatalf("test page %d", page.StatusCode)
	}
	again, err := p.admin.GetConnection(as("owner"), &v0sso.GetConnectionRequest{TenantId: acme, ConnectionId: conn.Id})
	if err != nil || again.Connection.Status != v0sso.ConnectionStatus_CONNECTION_STATUS_TESTED || again.Connection.TestTime != got.Connection.TestTime {
		t.Fatalf("%v %v", again, err)
	}

	// The tenant writes its policy; the domains a platform admin set are kept.
	written, err := p.admin.PutTenantSSOPolicy(as("owner"), required)
	if err != nil || written.Policy.Enforcement != v0tenant.Enforcement_ENFORCEMENT_REQUIRED || written.Policy.AutoJoin ||
		!slices.Equal(bindingsOf(written.Policy), []string{conn.Id + " active"}) || !slices.Equal(written.Policy.Domains, []string{"test.example"}) {
		t.Fatalf("%v %v", written, err)
	}
	options, err := p.user.ListOptions(as("login-ui"), &v0sso.ListOptionsRequest{ConnectionIds: []string{conn.Id}})
	if err != nil || len(options.Options) != 1 || options.Options[0].Label != "Acme Mock" {
		t.Fatalf("%v %v", options, err)
	}

	t.Run("member's first sign-in", func(t *testing.T) {
		alice := p.kratos.add("alice@test.example", false)
		p.tenants.join(acme, alice)
		p.putIdPUser(t, "alice", mockidp.User{Subject: "alice-sub", Email: "Alice@Test.example", EmailVerified: &yes})

		start, err := p.user.StartAttempt(as("login-ui"), &v0sso.StartAttemptRequest{TenantId: acme, Email: "alice@test.example", ConnectionId: conn.Id})
		if err != nil {
			t.Fatal(err)
		}
		b := p.browser(t)
		challenge := p.hydra.challenge(start.Ticket)
		authorize := b.toIdP(p, challenge)
		if u, _ := url.Parse(authorize); u.Query().Get("redirect_uri") != conn.RedirectUri || u.Query().Get("login_hint") != "alice@test.example" {
			t.Fatalf("the IdP is asked to answer at the connection's redirect URI: %s", authorize)
		}
		back := b.atIdP(p, authorize, "alice")
		if back.StatusCode != http.StatusSeeOther || p.hydra.acceptedFor(challenge) != conn.Id+":alice-sub" {
			t.Fatalf("not accepted: %d %v", back.StatusCode, p.hydra.rejectedAll())
		}
		if links := p.kratos.links(alice); len(links) != 0 || p.kratos.writeCount() != 0 {
			t.Fatalf("sso-service writes no link: %v, %d writes", links, p.kratos.writeCount())
		}

		// Before Kratos links the account, the attempt did not end at it.
		_, err = p.complete(b, start.Ticket, alice)
		expectReason(t, err, apierrors.NotApplicable)

		// Kratos's account linking writes the link.
		p.kratos.link(alice, conn.Id, "alice-sub")
		done, err := p.complete(b, start.Ticket, alice)
		if err != nil || done.ConnectionId != conn.Id || done.TenantId != acme {
			t.Fatalf("%v %v", done, err)
		}
		if _, err := p.complete(b, start.Ticket, alice); err != nil {
			t.Fatalf("a repeat for the same account: %v", err)
		}
		_, err = p.complete(b, start.Ticket, uuid.NewString())
		expectReason(t, err, apierrors.NotApplicable)
		// The receipt is in the browser that signed in at the IdP: to a
		// browser that only holds the ticket, the attempt confirms nothing.
		_, err = p.complete(p.browser(t), start.Ticket, alice)
		expectReason(t, err, apierrors.NotApplicable)

		// Nothing is stored, so the ticket is not spent: until it
		// expires it can start another company sign-in, which still needs
		// the user at the IdP.
		if rejected := p.loginWith(t, start.Ticket); rejected != "" {
			t.Fatalf("a ticket starts a sign-in until it expires, got %q", rejected)
		}
		if rejected := p.loginWith(t, altered(start.Ticket)); rejected != bridge.Message(bridge.ReasonExpired) {
			t.Fatalf("an altered ticket must be refused, got %q", rejected)
		}

		links, err := p.user.ListLinks(as("login-ui"), &v0sso.ListLinksRequest{IdentityId: alice})
		if err != nil || len(links.Links) != 1 || links.Links[0].Label != "Acme Mock" {
			t.Fatalf("%v %v", links, err)
		}
		_, err = p.user.DeleteLink(as("login-ui"), &v0sso.DeleteLinkRequest{ConnectionId: conn.Id, IdentityId: alice})
		expectReason(t, err, apierrors.LastCredential)

		// The next sign-in follows the link.
		next, _ := p.user.StartAttempt(as("login-ui"), &v0sso.StartAttemptRequest{TenantId: acme, Email: "alice@test.example", ConnectionId: conn.Id})
		b = p.browser(t)
		_, challenge = p.signIn(t, b, next.Ticket, "alice")
		if p.hydra.acceptedFor(challenge) != conn.Id+":alice-sub" {
			t.Fatalf("not accepted: %v", p.hydra.rejectedAll())
		}
		if _, err := p.complete(b, next.Ticket, alice); err != nil {
			t.Fatalf("an existing link completes at once: %v", err)
		}
	})

	t.Run("replayed callback", func(t *testing.T) {
		start, _ := p.user.StartAttempt(as("login-ui"), &v0sso.StartAttemptRequest{TenantId: acme, Email: "alice@test.example", ConnectionId: conn.Id})
		b := p.browser(t)
		challenge := p.hydra.challenge(start.Ticket)
		authorize := b.toIdP(p, challenge)
		ssoURL, _ := url.Parse(p.ssoURL)
		cookies := b.client.Jar.Cookies(ssoURL)
		callback := b.answerOf(p, authorize, "alice")
		if first := b.get(callback); first.StatusCode != http.StatusSeeOther || p.hydra.acceptedFor(challenge) != conn.Id+":alice-sub" {
			t.Fatalf("not accepted: %d %v", first.StatusCode, p.hydra.rejectedAll())
		}
		if again := b.get(callback); again.StatusCode != http.StatusBadRequest {
			t.Fatalf("the binding cookie is cleared after use: %d", again.StatusCode)
		}

		// hydra-sso (25.x) would accept its login challenge again: the
		// IdP's code is single use.
		replay := p.browser(t)
		replay.client.Jar.SetCookies(ssoURL, cookies)
		replay.get(callback)
		if p.hydra.rejectedFor(challenge) == "" {
			t.Fatal("a replayed callback must be refused")
		}
	})

	// Every connection has a redirect URI of its own, and an answer is only
	// taken at the one of the connection it was asked through: an identity
	// provider (or whoever controls a tenant's connection) cannot have its
	// answer handled as another connection's.
	t.Run("answer at another connection's callback", func(t *testing.T) {
		// Another tenant's connection, at the same identity provider.
		theirs := p.tested(t, other, "Other Mock")

		t.Run("sign-in", func(t *testing.T) {
			start, err := p.user.StartAttempt(as("login-ui"), &v0sso.StartAttemptRequest{TenantId: acme, Email: "alice@test.example", ConnectionId: conn.Id})
			if err != nil {
				t.Fatal(err)
			}
			b := p.browser(t)
			challenge := p.hydra.challenge(start.Ticket)
			authorize := b.toIdP(p, challenge)
			ssoURL, _ := url.Parse(p.ssoURL)
			cookies := b.client.Jar.Cookies(ssoURL)
			answer := b.answerOf(p, authorize, "alice")

			misdelivered := b.get(elsewhere(t, answer, conn.Id, theirs.Id))
			if misdelivered.StatusCode != http.StatusSeeOther || p.hydra.rejectedFor(challenge) != bridge.Message(bridge.ReasonInvalidToken) {
				t.Fatalf("expected the sign-in rejected as an answer that cannot be accepted: %d, rejected %q", misdelivered.StatusCode, p.hydra.rejectedFor(challenge))
			}
			if accepted := p.hydra.acceptedFor(challenge); accepted != "" {
				t.Fatalf("accepted as %q", accepted)
			}

			// The code was not spent on the way: the same answer at its own
			// redirect URI, in a browser that still holds the binding, is
			// taken. (The fake hydra-sso accepts a challenge it rejected; the
			// real one does not.)
			again := p.browser(t)
			again.client.Jar.SetCookies(ssoURL, cookies)
			if rightful := again.get(answer); rightful.StatusCode != http.StatusSeeOther || p.hydra.acceptedFor(challenge) != conn.Id+":alice-sub" {
				t.Fatalf("the code was redeemed at the wrong redirect URI: %d %v", rightful.StatusCode, p.hydra.rejectedAll())
			}
		})

		t.Run("another tenant's sign-in", func(t *testing.T) {
			// The other tenant binds its connection and has a member of its own.
			if _, err := p.admin.PutTenantSSOPolicy(as("owner"), &v0sso.PutTenantSSOPolicyRequest{TenantId: other, Enforcement: v0tenant.Enforcement_ENFORCEMENT_OPTIONAL,
				Bindings: []*v0tenant.SSOBinding{{ConnectionId: theirs.Id, Active: true}}}); err != nil {
				t.Fatal(err)
			}
			eve := p.kratos.add("eve@test.example", false)
			p.tenants.join(other, eve)
			p.putIdPUser(t, "eve", mockidp.User{Subject: "eve-sub", Email: "eve@test.example", EmailVerified: &yes})

			start, err := p.user.StartAttempt(as("login-ui"), &v0sso.StartAttemptRequest{TenantId: other, Email: "eve@test.example", ConnectionId: theirs.Id})
			if err != nil {
				t.Fatal(err)
			}
			b := p.browser(t)
			challenge := p.hydra.challenge(start.Ticket)
			answer := b.answerOf(p, b.toIdP(p, challenge), "eve")
			b.get(elsewhere(t, answer, theirs.Id, conn.Id))
			if p.hydra.rejectedFor(challenge) != bridge.Message(bridge.ReasonInvalidToken) || p.hydra.acceptedFor(challenge) != "" {
				t.Fatalf("rejected %q, accepted %q", p.hydra.rejectedFor(challenge), p.hydra.acceptedFor(challenge))
			}
		})

		t.Run("test sign-in", func(t *testing.T) {
			fresh, err := p.admin.CreateConnection(as("owner"), &v0sso.CreateConnectionRequest{
				TenantId: acme, Label: "Acme Second", Issuer: p.idpURL, ClientId: "mock-client", ClientSecret: "mock-secret",
			})
			if err != nil {
				t.Fatal(err)
			}
			id := fresh.Connection.Id
			test, err := p.admin.StartTestLogin(as("owner"), &v0sso.StartTestLoginRequest{TenantId: acme, ConnectionId: id})
			if err != nil {
				t.Fatal(err)
			}
			b := p.browser(t)
			answer := b.answerOf(p, test.Url, "tester")

			for name, to := range map[string]string{"another tenant's connection": theirs.Id, "the tenant's other connection": conn.Id} {
				if misdelivered := b.get(elsewhere(t, answer, id, to)); misdelivered.StatusCode != http.StatusBadRequest {
					t.Fatalf("%s: expected the answer refused, got %d", name, misdelivered.StatusCode)
				}
			}
			if got, _ := p.admin.GetConnection(as("owner"), &v0sso.GetConnectionRequest{TenantId: acme, ConnectionId: id}); got.GetConnection().GetStatus() != v0sso.ConnectionStatus_CONNECTION_STATUS_DRAFT {
				t.Fatalf("still a draft: %v", got)
			}
			// Nor did it test the connection it was delivered to.
			if theirsNow, _ := p.admin.GetConnection(as("owner"), &v0sso.GetConnectionRequest{TenantId: other, ConnectionId: theirs.Id}); theirsNow.GetConnection().GetTestTime() != theirs.TestTime {
				t.Fatalf("the other connection's test changed: %v", theirsNow)
			}

			// At its own redirect URI it is taken: the code was not spent.
			if page := b.get(answer); page.StatusCode != http.StatusOK {
				t.Fatalf("test page %d", page.StatusCode)
			}
			if got, _ := p.admin.GetConnection(as("owner"), &v0sso.GetConnectionRequest{TenantId: acme, ConnectionId: id}); got.GetConnection().GetStatus() != v0sso.ConnectionStatus_CONNECTION_STATUS_TESTED {
				t.Fatalf("tested by its own answer: %v", got)
			}
			if _, err := p.admin.DeleteConnection(as("owner"), &v0sso.DeleteConnectionRequest{TenantId: acme, ConnectionId: id}); err != nil {
				t.Fatalf("a connection the policy does not bind is deleted: %v", err)
			}
		})

		t.Run("callback without a connection", func(t *testing.T) {
			if bare := p.browser(t).get(p.ssoURL + "/callback?state=st&code=c"); bare.StatusCode != http.StatusNotFound {
				t.Fatalf("expected 404, got %d", bare.StatusCode)
			}
		})
	})

	t.Run("pending invitation", func(t *testing.T) {
		p.tenants.invite(acme, "iris@test.example")
		p.putIdPUser(t, "iris", mockidp.User{Subject: "iris-sub", Email: "iris@test.example", EmailVerified: &yes})

		start, _ := p.user.StartAttempt(as("login-ui"), &v0sso.StartAttemptRequest{TenantId: acme, Email: "iris@test.example", ConnectionId: conn.Id})
		b := p.browser(t)
		_, challenge := p.signIn(t, b, start.Ticket, "iris")
		if p.hydra.acceptedFor(challenge) != conn.Id+":iris-sub" {
			t.Fatalf("not accepted: %v", p.hydra.rejectedAll())
		}
		if p.kratos.find("iris@test.example") != nil {
			t.Fatal("sso-service creates no account")
		}

		iris := p.kratos.register("iris@test.example", conn.Id, "iris-sub")
		if _, err := p.complete(b, start.Ticket, iris); err != nil {
			t.Fatal(err)
		}
		// The membership is not sso-service's to create, nor the account its
		// to remove.
		if p.tenants.isMember(acme, iris) || !p.kratos.exists(iris) || p.kratos.writeCount() != 0 {
			t.Fatalf("member %v, account kept %v, %d writes to Kratos", p.tenants.isMember(acme, iris), p.kratos.exists(iris), p.kratos.writeCount())
		}
	})

	t.Run("account with a password", func(t *testing.T) {
		bob := p.kratos.add("bob@test.example", true)
		p.tenants.join(acme, bob)
		p.putIdPUser(t, "bob", mockidp.User{Subject: "bob-sub", Email: "bob@test.example", EmailVerified: &yes})

		start, _ := p.user.StartAttempt(as("login-ui"), &v0sso.StartAttemptRequest{TenantId: acme, Email: "bob@test.example", ConnectionId: conn.Id})
		b := p.browser(t)
		_, challenge := p.signIn(t, b, start.Ticket, "bob")
		if p.hydra.acceptedFor(challenge) != conn.Id+":bob-sub" {
			t.Fatalf("not accepted: %v", p.hydra.rejectedAll())
		}
		p.kratos.link(bob, conn.Id, "bob-sub")
		if _, err := p.complete(b, start.Ticket, bob); err != nil {
			t.Fatal(err)
		}
		if _, err := p.user.DeleteLink(as("login-ui"), &v0sso.DeleteLinkRequest{ConnectionId: conn.Id, IdentityId: bob}); err != nil {
			t.Fatalf("a password remains: %v", err)
		}
		if len(p.kratos.links(bob)) != 0 {
			t.Fatalf("links %v", p.kratos.links(bob))
		}
	})

	t.Run("address mismatch", func(t *testing.T) {
		carol := p.kratos.add("carol@test.example", false)
		p.tenants.join(acme, carol)
		p.putIdPUser(t, "carol", mockidp.User{Subject: "carol-sub", Email: "mallory@test.example", EmailVerified: &yes})
		start, _ := p.user.StartAttempt(as("login-ui"), &v0sso.StartAttemptRequest{TenantId: acme, Email: "carol@test.example", ConnectionId: conn.Id})
		_, challenge := p.signIn(t, p.browser(t), start.Ticket, "carol")
		if p.hydra.rejectedFor(challenge) != bridge.Message(bridge.ReasonAddressMismatch) {
			t.Fatalf("rejected %q", p.hydra.rejectedFor(challenge))
		}
	})

	t.Run("address not admitted", func(t *testing.T) {
		p.putIdPUser(t, "mallory", mockidp.User{Subject: "mallory-sub", Email: "mallory@test.example", EmailVerified: &yes})
		start, _ := p.user.StartAttempt(as("login-ui"), &v0sso.StartAttemptRequest{TenantId: acme, Email: "mallory@test.example", ConnectionId: conn.Id})
		_, challenge := p.signIn(t, p.browser(t), start.Ticket, "mallory")
		if p.hydra.rejectedFor(challenge) != bridge.Message(bridge.ReasonNotAMember) {
			t.Fatalf("rejected %q", p.hydra.rejectedFor(challenge))
		}
	})

	t.Run("auto-join", func(t *testing.T) {
		hooli := p.tenants.tenant("Hooli")
		p.tenants.join(hooli, ownerID)
		hc := p.tested(t, hooli, "Hooli Mock")
		if _, err := p.platform.SetTenantDomains(as("platform-admin"), &v0sso.SetTenantDomainsRequest{TenantId: hooli, Domains: []string{"hooli.example"}}); err != nil {
			t.Fatal(err)
		}
		autoJoin := &v0sso.PutTenantSSOPolicyRequest{TenantId: hooli, Enforcement: v0tenant.Enforcement_ENFORCEMENT_REQUIRED,
			AutoJoin: true, Bindings: []*v0tenant.SSOBinding{{ConnectionId: hc.Id, Active: true}}}
		if _, err := p.admin.PutTenantSSOPolicy(as("owner"), autoJoin); err != nil {
			t.Fatal(err)
		}

		p.putIdPUser(t, "hank", mockidp.User{Subject: "hank-sub", Email: "hank@hooli.example", EmailVerified: &yes})
		start, _ := p.user.StartAttempt(as("login-ui"), &v0sso.StartAttemptRequest{TenantId: hooli, Email: "hank@hooli.example", ConnectionId: hc.Id})
		b := p.browser(t)
		_, challenge := p.signIn(t, b, start.Ticket, "hank")
		if p.hydra.acceptedFor(challenge) != hc.Id+":hank-sub" {
			t.Fatalf("not accepted: %v", p.hydra.rejectedAll())
		}
		if p.kratos.find("hank@hooli.example") != nil {
			t.Fatal("no account before Kratos registers him")
		}
		hank := p.kratos.register("hank@hooli.example", hc.Id, "hank-sub")

		// Auto-join turned off after the IdP: the attempt still ended at his
		// account, and what becomes of the membership is not decided here.
		autoJoin.AutoJoin = false
		if _, err := p.admin.PutTenantSSOPolicy(as("owner"), autoJoin); err != nil {
			t.Fatal(err)
		}
		if _, err := p.complete(b, start.Ticket, hank); err != nil {
			t.Fatal(err)
		}
		if p.tenants.isMember(hooli, hank) || !p.kratos.exists(hank) || p.kratos.writeCount() != 0 {
			t.Fatalf("member %v, account kept %v, %d writes to Kratos", p.tenants.isMember(hooli, hank), p.kratos.exists(hank), p.kratos.writeCount())
		}

		// The next user's address is admitted by nothing any more.
		p.putIdPUser(t, "gwen", mockidp.User{Subject: "gwen-sub", Email: "gwen@hooli.example", EmailVerified: &yes})
		late, _ := p.user.StartAttempt(as("login-ui"), &v0sso.StartAttemptRequest{TenantId: hooli, Email: "gwen@hooli.example", ConnectionId: hc.Id})
		_, challenge = p.signIn(t, p.browser(t), late.Ticket, "gwen")
		if p.hydra.rejectedFor(challenge) != bridge.Message(bridge.ReasonNotAMember) {
			t.Fatalf("rejected %q", p.hydra.rejectedFor(challenge))
		}
	})

	// The policy is one document in tenant-service, written in parts: a
	// platform admin sets its domains, the tenant's admin its bindings,
	// enforcement and auto-join, and a delete removes one binding. No
	// write undoes another's part, and one that would break a rule of the
	// policy as a whole changes nothing.
	t.Run("policy writes", func(t *testing.T) {
		globex := p.tenants.tenant("Globex")
		first := p.tested(t, globex, "Globex Mock")
		second := p.tested(t, globex, "Globex Second")
		spare := p.tested(t, globex, "Globex Spare")
		required, optional := v0tenant.Enforcement_ENFORCEMENT_REQUIRED, v0tenant.Enforcement_ENFORCEMENT_OPTIONAL
		// holds checks the policy tenant-service holds for the tenant.
		holds := func(t *testing.T, enforcement v0tenant.Enforcement, autoJoin bool, domains []string, bindings ...string) {
			t.Helper()
			slices.Sort(bindings)
			policy := p.tenants.policy(globex)
			if policy.GetEnforcement() != enforcement || policy.GetAutoJoin() != autoJoin ||
				!slices.Equal(policy.GetDomains(), domains) || !slices.Equal(bindingsOf(policy), bindings) {
				t.Fatalf("expected %s, auto-join %v, domains %v, bindings %v, got %v", enforcement, autoJoin, domains, bindings, policy)
			}
		}
		// answered checks that a write answered the policy as it is held
		// after it, the parts it did not write included.
		answered := func(t *testing.T, policy *v0tenant.TenantSSOPolicy) {
			t.Helper()
			if !proto.Equal(policy, p.tenants.policy(globex)) {
				t.Fatalf("the write answered %v, tenant-service holds %v", policy, p.tenants.policy(globex))
			}
		}

		set, err := p.platform.SetTenantDomains(as("platform-admin"), &v0sso.SetTenantDomainsRequest{TenantId: globex, Domains: []string{"globex.example"}})
		if err != nil {
			t.Fatal(err)
		}
		// The domains alone turn nothing on: the enforcement was never written.
		holds(t, v0tenant.Enforcement_ENFORCEMENT_OFF, false, []string{"globex.example"})
		answered(t, set.Policy)

		t.Run("bindings keep the domains", func(t *testing.T) {
			bound, err := p.admin.PutTenantSSOPolicy(as("owner"), &v0sso.PutTenantSSOPolicyRequest{TenantId: globex, Enforcement: required, AutoJoin: true,
				Bindings: []*v0tenant.SSOBinding{{ConnectionId: first.Id, Active: true}, {ConnectionId: second.Id}, {ConnectionId: spare.Id, Active: true}}})
			if err != nil {
				t.Fatal(err)
			}
			holds(t, required, true, []string{"globex.example"}, first.Id+" active", second.Id+" inactive", spare.Id+" active")
			answered(t, bound.Policy)
		})

		domains := []string{"globex.example", "globex.test"}
		t.Run("domains keep the rest", func(t *testing.T) {
			set, err := p.platform.SetTenantDomains(as("platform-admin"), &v0sso.SetTenantDomainsRequest{TenantId: globex, Domains: domains})
			if err != nil {
				t.Fatal(err)
			}
			holds(t, required, true, domains, first.Id+" active", second.Id+" inactive", spare.Id+" active")
			answered(t, set.Policy)
		})

		t.Run("refused write changes nothing", func(t *testing.T) {
			// The domains are the platform admin's part, auto-join the tenant
			// admin's: taking the domains away is refused for what the other
			// wrote.
			_, err := p.platform.SetTenantDomains(as("platform-admin"), &v0sso.SetTenantDomainsRequest{TenantId: globex})
			expectReason(t, err, apierrors.AutoJoinNeedsRequiredAndDomains)
			_, err = p.admin.PutTenantSSOPolicy(as("owner"), &v0sso.PutTenantSSOPolicyRequest{TenantId: globex, Enforcement: required,
				Bindings: []*v0tenant.SSOBinding{{ConnectionId: first.Id}}})
			expectReason(t, err, apierrors.RequiredNeedsActiveBinding)
			_, err = p.admin.PutTenantSSOPolicy(as("owner"), &v0sso.PutTenantSSOPolicyRequest{TenantId: globex, Enforcement: optional, AutoJoin: true,
				Bindings: []*v0tenant.SSOBinding{{ConnectionId: first.Id, Active: true}}})
			expectReason(t, err, apierrors.AutoJoinNeedsRequiredAndDomains)
			holds(t, required, true, domains, first.Id+" active", second.Id+" inactive", spare.Id+" active")
		})

		t.Run("delete removes its binding", func(t *testing.T) {
			if _, err := p.admin.DeleteConnection(as("owner"), &v0sso.DeleteConnectionRequest{TenantId: globex, ConnectionId: second.Id}); err != nil {
				t.Fatal(err)
			}
			holds(t, required, true, domains, first.Id+" active", spare.Id+" active")
			_, err := p.admin.GetConnection(as("owner"), &v0sso.GetConnectionRequest{TenantId: globex, ConnectionId: second.Id})
			expectReason(t, err, apierrors.ConnectionNotFound)

			// A platform admin's, too.
			if _, err := p.platform.DeleteAnyConnection(as("platform-admin"), &v0sso.DeleteAnyConnectionRequest{ConnectionId: spare.Id}); err != nil {
				t.Fatal(err)
			}
			holds(t, required, true, domains, first.Id+" active")
			_, err = p.admin.GetConnection(as("owner"), &v0sso.GetConnectionRequest{TenantId: globex, ConnectionId: spare.Id})
			expectReason(t, err, apierrors.ConnectionNotFound)
		})

		t.Run("delete refused for required", func(t *testing.T) {
			_, err := p.admin.DeleteConnection(as("owner"), &v0sso.DeleteConnectionRequest{TenantId: globex, ConnectionId: first.Id})
			expectReason(t, err, apierrors.RequiredNeedsActiveBinding)
			_, err = p.platform.DeleteAnyConnection(as("platform-admin"), &v0sso.DeleteAnyConnectionRequest{ConnectionId: first.Id})
			expectReason(t, err, apierrors.RequiredNeedsActiveBinding)
			holds(t, required, true, domains, first.Id+" active")
			if kept, err := p.admin.GetConnection(as("owner"), &v0sso.GetConnectionRequest{TenantId: globex, ConnectionId: first.Id}); err != nil || kept.Connection.Id != first.Id {
				t.Fatalf("the connection of a refused delete stays: %v %v", kept, err)
			}
		})

		t.Run("delete of an unbound connection", func(t *testing.T) {
			unbound := p.tested(t, globex, "Globex Unbound")
			if _, err := p.admin.DeleteConnection(as("owner"), &v0sso.DeleteConnectionRequest{TenantId: globex, ConnectionId: unbound.Id}); err != nil {
				t.Fatal(err)
			}
			holds(t, required, true, domains, first.Id+" active")
			_, err := p.admin.GetConnection(as("owner"), &v0sso.GetConnectionRequest{TenantId: globex, ConnectionId: unbound.Id})
			expectReason(t, err, apierrors.ConnectionNotFound)
		})
	})

	t.Run("delete of the only active binding", func(t *testing.T) {
		_, err := p.admin.DeleteConnection(as("owner"), &v0sso.DeleteConnectionRequest{TenantId: acme, ConnectionId: conn.Id})
		expectReason(t, err, apierrors.RequiredNeedsActiveBinding)
		if status.Convert(err).Message() != "REQUIRED_NEEDS_ACTIVE_BINDING: a tenant that requires company sign-in needs an active binding" {
			t.Fatalf("the reason starts the message: %v", err)
		}
	})

	t.Run("connection of a deleted tenant", func(t *testing.T) {
		gone := p.tenants.tenant("Gone")
		orphan, err := p.admin.CreateConnection(as("owner"), &v0sso.CreateConnectionRequest{
			TenantId: gone, Label: "Gone Mock", Issuer: p.idpURL, ClientId: "mock-client", ClientSecret: "mock-secret",
		})
		if err != nil {
			t.Fatal(err)
		}
		id := orphan.Connection.Id
		// Another tenant can neither bind nor delete it.
		_, err = p.admin.PutTenantSSOPolicy(as("owner"), &v0sso.PutTenantSSOPolicyRequest{TenantId: other, Enforcement: v0tenant.Enforcement_ENFORCEMENT_OPTIONAL,
			Bindings: []*v0tenant.SSOBinding{{ConnectionId: id}}})
		expectReason(t, err, apierrors.ConnectionNotFound)
		_, err = p.admin.DeleteConnection(as("owner"), &v0sso.DeleteConnectionRequest{TenantId: other, ConnectionId: id})
		expectReason(t, err, apierrors.ConnectionNotFound)

		p.tenants.remove(gone)
		// The tenant's own route has no tenant to unbind it from.
		if _, err := p.admin.DeleteConnection(as("owner"), &v0sso.DeleteConnectionRequest{TenantId: gone, ConnectionId: id}); status.Code(err) != codes.NotFound || apierrors.Reason(err) != "" {
			t.Fatalf("expected the tenant not found, got %v", err)
		}
		listed, err := p.platform.ListAllConnections(as("platform-admin"), &v0sso.ListAllConnectionsRequest{OwnerTenantId: gone})
		if err != nil || len(listed.Connections) != 1 || listed.Connections[0].Id != id {
			t.Fatalf("%v %v", listed, err)
		}
		if _, err := p.platform.DeleteAnyConnection(as("platform-admin"), &v0sso.DeleteAnyConnectionRequest{ConnectionId: id}); err != nil {
			t.Fatal(err)
		}
		if listed, _ := p.platform.ListAllConnections(as("platform-admin"), &v0sso.ListAllConnectionsRequest{OwnerTenantId: gone}); len(listed.GetConnections()) != 0 {
			t.Fatalf("not deleted: %v", listed)
		}
	})

	// The two admin services behind the HTTP gateway: what a console or a
	// platform-admin tool calls.
	t.Run("HTTP routes", func(t *testing.T) {
		call := func(method, path, token, body string) (int, map[string]any) {
			t.Helper()
			req, _ := http.NewRequest(method, p.ssoURL+path, strings.NewReader(body))
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			resp, err := p.sso.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			answer := map[string]any{}
			_ = json.NewDecoder(resp.Body).Decode(&answer)
			return resp.StatusCode, answer
		}

		code, unauthorized := call(http.MethodGet, "/api/v0/sso/tenants/"+acme+"/connections", "", "")
		if code != http.StatusUnauthorized || unauthorized["status"] != float64(http.StatusUnauthorized) || unauthorized["message"] != "missing authorization header" {
			t.Fatalf("no token: %d %v", code, unauthorized)
		}
		code, listed := call(http.MethodGet, "/api/v0/sso/tenants/"+acme+"/connections/"+conn.Id, "owner", "")
		connection, _ := listed["connection"].(map[string]any)
		if code != http.StatusOK || connection["redirect_uri"] != conn.RedirectUri || connection["status"] != "CONNECTION_STATUS_TESTED" {
			t.Fatalf("%d %v", code, listed)
		}
		if _, leaked := connection["client_secret"]; leaked {
			t.Fatal("the client secret is never returned")
		}
		if code, _ := call(http.MethodGet, "/api/v0/sso/connections?owner_tenant_id="+acme, "", ""); code != http.StatusUnauthorized {
			t.Fatalf("no token: %d", code)
		}
		code, all := call(http.MethodGet, "/api/v0/sso/connections?owner_tenant_id="+acme, "platform-admin", "")
		if connections, _ := all["connections"].([]any); code != http.StatusOK || len(connections) != 1 {
			t.Fatalf("%d %v", code, all)
		}
		code, set := call(http.MethodPut, "/api/v0/sso/tenants/"+acme+"/domains", "platform-admin", `{"domains":["test.example","acme.example"]}`)
		policy, _ := set["policy"].(map[string]any)
		if domains, _ := policy["domains"].([]any); code != http.StatusOK || len(domains) != 2 {
			t.Fatalf("%d %v", code, set)
		}
		// An error is {"status", "message"}, its reason the start of the message.
		code, missing := call(http.MethodGet, "/api/v0/sso/tenants/"+other+"/connections/"+conn.Id, "owner", "")
		if code != http.StatusNotFound || len(missing) != 2 || missing["status"] != float64(http.StatusNotFound) ||
			missing["message"] != "CONNECTION_NOT_FOUND: no such connection" {
			t.Fatalf("%d %v", code, missing)
		}
	})
}

// readBody returns the page a browser was answered with.
func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// altered changes one character in the middle of a sealed token.
func altered(token string) string {
	b := []byte(token)
	if b[len(b)/2] == 'A' {
		b[len(b)/2] = 'B'
	} else {
		b[len(b)/2] = 'A'
	}
	return string(b)
}

// loginWith opens /login for ticket and returns hydra-sso's rejection, if any.
func (p *plane) loginWith(t *testing.T, ticket string) string {
	challenge := p.hydra.challenge(ticket)
	p.browser(t).get(p.ssoURL + "/login?login_challenge=" + challenge)
	return p.hydra.rejectedFor(challenge)
}
