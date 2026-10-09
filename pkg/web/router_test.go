// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	v0sso "github.com/canonical/identity-platform-api/v0/sso"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/canonical/sso-service/internal/apierrors"
	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/monitoring"
	"github.com/canonical/sso-service/internal/tracing"
	"github.com/canonical/sso-service/pkg/authentication"
	"github.com/canonical/sso-service/pkg/bridge"
)

const (
	requestTimeout = 25 * time.Second
	connection     = "0190a0b0-0000-7000-8000-00000000000a"
)

// admin is both admin services, as the one handler behind them is. It keeps
// the context of the last call.
type admin struct {
	v0sso.UnimplementedSSOTenantAdminServiceServer
	v0sso.UnimplementedSSOPlatformAdminServiceServer

	ctx context.Context
}

func (a *admin) ListAllConnections(ctx context.Context, r *v0sso.ListAllConnectionsRequest) (*v0sso.ListAllConnectionsResponse, error) {
	a.ctx = ctx
	if _, ok := authentication.GetUserID(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "unauthenticated")
	}
	return &v0sso.ListAllConnectionsResponse{Connections: []*v0sso.Connection{{Id: "c", OwnerTenantId: r.OwnerTenantId}}}, nil
}

func (a *admin) ListConnections(ctx context.Context, r *v0sso.ListConnectionsRequest) (*v0sso.ListConnectionsResponse, error) {
	a.ctx = ctx
	if _, ok := authentication.GetUserID(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "unauthenticated")
	}
	return &v0sso.ListConnectionsResponse{Connections: []*v0sso.Connection{{Id: "c", OwnerTenantId: r.TenantId}}}, nil
}

func (a *admin) GetTenantDomains(ctx context.Context, r *v0sso.GetTenantDomainsRequest) (*v0sso.GetTenantDomainsResponse, error) {
	return &v0sso.GetTenantDomainsResponse{Domains: []string{"a.example"}}, nil
}

func (a *admin) SetTenantDomains(ctx context.Context, r *v0sso.SetTenantDomainsRequest) (*v0sso.SetTenantDomainsResponse, error) {
	a.ctx = ctx
	return &v0sso.SetTenantDomainsResponse{}, nil
}

func (a *admin) DeleteAnyConnection(ctx context.Context, r *v0sso.DeleteAnyConnectionRequest) (*v0sso.DeleteAnyConnectionResponse, error) {
	a.ctx = ctx
	return &v0sso.DeleteAnyConnectionResponse{}, nil
}

func (a *admin) GetConnection(ctx context.Context, r *v0sso.GetConnectionRequest) (*v0sso.GetConnectionResponse, error) {
	return nil, apierrors.New(codes.NotFound, apierrors.ConnectionNotFound, "no such connection")
}

// pages is the browser flow. It keeps the context and the callback of the
// last call.
type pages struct {
	ctx      context.Context
	callback *bridge.Callback
}

func (p *pages) StartLogin(ctx context.Context, _ string) (string, *bridge.Binding, error) {
	p.ctx = ctx
	return "https://idp.example/authorize", nil, nil
}

func (p *pages) Callback(ctx context.Context, callback *bridge.Callback) (*bridge.Result, error) {
	p.ctx, p.callback = ctx, callback
	return &bridge.Result{Redirect: "https://kratos.example/next"}, nil
}

func (p *pages) Consent(ctx context.Context, _ string) (string, error) {
	p.ctx = ctx
	return "https://kratos.example/next", nil
}

func newRouter(service bridge.ServiceInterface, server AdminInterface, timeout time.Duration) http.Handler {
	logger := logging.NewNoopLogger()
	tracer := tracing.NewNoopTracer()
	monitor := monitoring.NewNoopMonitor("test", logger)
	authMiddleware := authentication.NewMiddleware(authentication.NewNoopVerifier(), tracer, monitor, logger)

	return NewRouter(bridge.NewAPI(service, tracer, logger), server, authMiddleware, timeout, tracer, monitor, logger)
}

func TestNewRouter(t *testing.T) {
	browser := &pages{}
	router := newRouter(browser, &admin{}, requestTimeout)

	testCases := []struct {
		method, path, token string
		payload             string
		code                int
		body                string
	}{
		{path: "/api/v0/status", code: http.StatusOK},
		{path: "/api/v0/sso/tenants/t/connections", code: http.StatusUnauthorized},
		{path: "/api/v0/sso/tenants/t/connections", token: "x", code: http.StatusOK, body: `"owner_tenant_id":"t"`},
		// The platform admin routes sit next to them, with the same token
		// check.
		{path: "/api/v0/sso/connections?owner_tenant_id=o", code: http.StatusUnauthorized},
		{path: "/api/v0/sso/connections?owner_tenant_id=o", token: "x", code: http.StatusOK, body: `"owner_tenant_id":"o"`},
		{path: "/api/v0/sso/tenants/t/domains", token: "x", code: http.StatusOK, body: `"domains":["a.example"]`},
		{method: http.MethodPut, path: "/api/v0/sso/tenants/t/domains", code: http.StatusUnauthorized},
		{method: http.MethodPut, path: "/api/v0/sso/tenants/t/domains", token: "x", code: http.StatusOK},
		// A mistyped key is refused, not dropped.
		{method: http.MethodPut, path: "/api/v0/sso/tenants/t/domains", token: "x", payload: `{"domain":["a.example"]}`, code: http.StatusBadRequest, body: `"status":400`},
		{method: http.MethodDelete, path: "/api/v0/sso/connections/c", code: http.StatusUnauthorized},
		{method: http.MethodDelete, path: "/api/v0/sso/connections/c", token: "x", code: http.StatusOK},
		// The sign-in API has no HTTP route at all.
		{method: http.MethodPost, path: "/api/v0/sso/attempts", token: "x", code: http.StatusNotFound},
		{path: "/login", code: http.StatusBadRequest},
		{path: "/login?login_challenge=lc", code: http.StatusFound},
		{path: "/consent?consent_challenge=cc", code: http.StatusFound},
		// A connection's callback; there is none without a connection.
		{path: "/callback/" + connection + "?state=st&code=c", code: http.StatusSeeOther},
		{path: "/callback?state=st&code=c", code: http.StatusNotFound},
	}

	for _, tc := range testCases {
		method := tc.method
		if method == "" {
			method = http.MethodGet
		}
		payload := tc.payload
		if payload == "" {
			payload = "{}"
		}
		r := httptest.NewRequest(method, tc.path, strings.NewReader(payload))
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.body) {
			t.Errorf("%s %s: %d %s", method, tc.path, w.Code, w.Body.String())
		}
	}
	if browser.callback == nil || browser.callback.ConnectionID != connection || browser.callback.State != "st" {
		t.Errorf("the callback's connection is the one in its path: %+v", browser.callback)
	}
}

// routes keeps the route label of every request's latency observation.
type routes struct {
	*monitoring.NoopMonitor

	seen []string
}

func (m *routes) SetResponseTimeMetric(tags map[string]string, _ float64) error {
	m.seen = append(m.seen, tags["route"])
	return nil
}

// The latency metric is labelled with the route's pattern, so a caller's
// choice of path adds no series.
func TestNewRouter_RouteLabel(t *testing.T) {
	logger := logging.NewNoopLogger()
	tracer := tracing.NewNoopTracer()
	monitor := &routes{NoopMonitor: monitoring.NewNoopMonitor("test", logger)}
	authMiddleware := authentication.NewMiddleware(authentication.NewNoopVerifier(), tracer, monitor, logger)
	router := NewRouter(bridge.NewAPI(&pages{}, tracer, logger), &admin{}, authMiddleware, requestTimeout, tracer, monitor, logger)

	testCases := []struct {
		name     string
		path     string
		expected string
	}{
		{name: "a page with an id in its path", path: "/callback/" + connection + "?state=st&code=c", expected: "GET/callback/id"},
		{name: "the mounted gateway", path: "/api/v0/sso/tenants/t/connections", expected: "GET/api/v0/sso/*"},
		{name: "no route", path: "/callback/" + connection + "/more", expected: "unmatched"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			monitor.seen = nil
			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, tc.path, nil))

			if !reflect.DeepEqual(monitor.seen, []string{tc.expected}) {
				t.Errorf("expected the route %q, got %q", tc.expected, monitor.seen)
			}
		})
	}
}

// The API answers any origin and allows no credentials; the browser pages
// send no CORS headers at all.
func TestNewRouter_CORS(t *testing.T) {
	router := newRouter(&pages{}, &admin{}, requestTimeout)

	get := func(path string) http.Header {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Origin", "https://other.example")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)

		return w.Header()
	}

	api := get("/api/v0/sso/connections")
	if api.Get("Access-Control-Allow-Origin") != "*" || api.Get("Access-Control-Allow-Credentials") != "" {
		t.Errorf("unexpected CORS headers on the API: %v", api)
	}
	if page := get("/login?login_challenge=lc"); page.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("expected no CORS headers on a page, got %v", page)
	}
}

// Every HTTP error is {"status", "message"}; a reason is the start of the
// message.
func TestNewRouter_Errors(t *testing.T) {
	router := newRouter(&pages{}, &admin{}, requestTimeout)

	testCases := []struct {
		name         string
		path         string
		token        string
		expectedCode int
		expectedBody string
	}{
		{
			name:         "no token",
			path:         "/api/v0/sso/tenants/t/connections",
			expectedCode: http.StatusUnauthorized,
			expectedBody: `{"message":"missing authorization header","status":401}`,
		},
		{
			name:         "refusal with a reason",
			path:         "/api/v0/sso/tenants/t/connections/c",
			token:        "x",
			expectedCode: http.StatusNotFound,
			expectedBody: `{"status":404,"message":"CONNECTION_NOT_FOUND: no such connection"}`,
		},
		{
			name:         "not implemented",
			path:         "/api/v0/sso/tenants/t/policy",
			token:        "x",
			expectedCode: http.StatusNotImplemented,
			expectedBody: `{"status":501,"message":"method GetTenantSSOPolicy not implemented"}`,
		},
		{
			name:         "no such route",
			path:         "/api/v0/sso/attempts",
			token:        "x",
			expectedCode: http.StatusNotFound,
			expectedBody: `{"status":404,"message":"Not Found"}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)

			var got, want map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatalf("expected a JSON body, got %q: %v", w.Body.String(), err)
			}
			if err := json.Unmarshal([]byte(tc.expectedBody), &want); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.expectedCode || !reflect.DeepEqual(got, want) {
				t.Errorf("expected %d %s, got %d %s", tc.expectedCode, tc.expectedBody, w.Code, w.Body.String())
			}
		})
	}
}

// Every request runs under one deadline: what a handler does with the
// request's context ends by then, whatever the client asked for.
func TestNewRouter_Deadline(t *testing.T) {
	browser, server := &pages{}, &admin{}
	router := newRouter(browser, server, requestTimeout)

	bounded := func(t *testing.T, ctx context.Context, started time.Time) {
		t.Helper()
		if ctx == nil {
			t.Fatal("the handler was not reached")
		}
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("the handler's context has no deadline")
		}
		if deadline.After(started.Add(requestTimeout+time.Second)) || deadline.Before(started.Add(requestTimeout-time.Second)) {
			t.Fatalf("expected a deadline %s from the request's start, got %s", requestTimeout, deadline.Sub(started))
		}
	}

	t.Run("browser page", func(t *testing.T) {
		started := time.Now()
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/callback/"+connection+"?state=st&code=c", nil))
		bounded(t, browser.ctx, started)
	})
	t.Run("tenant admin route", func(t *testing.T) {
		started := time.Now()
		r := httptest.NewRequest(http.MethodGet, "/api/v0/sso/tenants/t/connections", nil)
		r.Header.Set("Authorization", "Bearer x")
		router.ServeHTTP(httptest.NewRecorder(), r)
		bounded(t, server.ctx, started)
	})
	t.Run("platform admin route", func(t *testing.T) {
		started := time.Now()
		r := httptest.NewRequest(http.MethodGet, "/api/v0/sso/connections", nil)
		r.Header.Set("Authorization", "Bearer x")
		router.ServeHTTP(httptest.NewRecorder(), r)
		bounded(t, server.ctx, started)
	})
	t.Run("grpc-timeout header ignored", func(t *testing.T) {
		started := time.Now()
		r := httptest.NewRequest(http.MethodGet, "/api/v0/sso/tenants/t/connections", nil)
		r.Header.Set("Authorization", "Bearer x")
		r.Header.Set("Grpc-Timeout", "10H")
		router.ServeHTTP(httptest.NewRecorder(), r)
		bounded(t, server.ctx, started)
	})
}
