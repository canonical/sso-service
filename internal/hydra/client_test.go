// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package hydra

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/monitoring"
	"github.com/canonical/sso-service/internal/tracing"
)

// fakeAdmin answers hydra-sso's admin API for one path, recording the
// request it got.
type fakeAdmin struct {
	status int
	answer map[string]any

	path      string
	challenge string
	body      map[string]any
}

func (f *fakeAdmin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.path = r.URL.Path
	f.challenge = r.URL.Query().Get("login_challenge") + r.URL.Query().Get("consent_challenge")
	_ = json.NewDecoder(r.Body).Decode(&f.body)
	w.Header().Set("Content-Type", "application/json")
	if f.status != 0 {
		w.WriteHeader(f.status)
	}
	_ = json.NewEncoder(w).Encode(f.answer)
}

func newTestClient(url string) *Client {
	logger := logging.NewNoopLogger()

	return NewClient(url, tracing.NewNoopTracer(), monitoring.NewNoopMonitor("sso-service", logger), logger)
}

// newClient is a client of f; the trailing slash of its admin URL names the
// same API.
func newClient(t *testing.T, f *fakeAdmin) *Client {
	t.Helper()
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	return newTestClient(server.URL + "/")
}

func loginRequest(hint string) map[string]any {
	return map[string]any{
		"challenge": "lc", "oidc_context": map[string]any{"login_hint": hint}, "client": map[string]any{"client_id": "kratos"},
		"request_url": "https://hydra-sso.example/oauth2/auth", "requested_access_token_audience": []string{},
		"requested_scope": []string{"openid", "email"}, "skip": false, "subject": "",
	}
}

func consentRequest() map[string]any {
	return map[string]any{
		"challenge": "cc", "subject": "conn:sub", "requested_scope": []string{"openid", "email"},
		"requested_access_token_audience": []string{}, "context": map[string]any{"email": "a@b.example"},
	}
}

func TestClient_GetLoginRequest(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		f := &fakeAdmin{answer: loginRequest("sealed-ticket")}

		request, err := newClient(t, f).GetLoginRequest(context.Background(), "lc")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.path != "/admin/oauth2/auth/requests/login" || f.challenge != "lc" {
			t.Errorf("expected the login request of lc asked for, got %s for %q", f.path, f.challenge)
		}
		if request.OIDCContext.LoginHint != "sealed-ticket" {
			t.Errorf("expected login_hint sealed-ticket, got %+v", request)
		}
	})

	// hydra-sso answers 410 for a challenge that was already used.
	for _, code := range []int{http.StatusNotFound, http.StatusGone} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			f := &fakeAdmin{status: code, answer: map[string]any{"redirect_to": "https://hydra-sso.example/x"}}

			if _, err := newClient(t, f).GetLoginRequest(context.Background(), "lc"); !errors.Is(err, ErrNotFound) {
				t.Errorf("expected %v, got %v", ErrNotFound, err)
			}
		})
	}

	t.Run("server error", func(t *testing.T) {
		f := &fakeAdmin{status: http.StatusInternalServerError, answer: map[string]any{"error": "server_error"}}

		if _, err := newClient(t, f).GetLoginRequest(context.Background(), "lc"); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("expected an error that is not %v, got %v", ErrNotFound, err)
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		server := httptest.NewServer(http.NotFoundHandler())
		server.Close()

		if _, err := newTestClient(server.URL).GetLoginRequest(context.Background(), "lc"); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("expected an error that is not %v, got %v", ErrNotFound, err)
		}
	})
}

func TestClient_AcceptLogin(t *testing.T) {
	f := &fakeAdmin{answer: map[string]any{"redirect_to": "https://kratos.example/cb"}}

	to, err := newClient(t, f).AcceptLogin(context.Background(), "lc", "conn:sub", map[string]any{"email": "a@b.example"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if to != "https://kratos.example/cb" {
		t.Errorf("expected hydra-sso's redirect, got %q", to)
	}
	if f.path != "/admin/oauth2/auth/requests/login/accept" || f.challenge != "lc" {
		t.Errorf("expected the login request of lc accepted, got %s for %q", f.path, f.challenge)
	}
	// The login is never remembered: every sign-in goes to the identity provider.
	if f.body["subject"] != "conn:sub" || f.body["remember"] != false ||
		!reflect.DeepEqual(f.body["context"], map[string]any{"email": "a@b.example"}) {
		t.Errorf("unexpected body %v", f.body)
	}
}

func TestClient_RejectLogin(t *testing.T) {
	f := &fakeAdmin{answer: map[string]any{"redirect_to": "https://kratos.example/err"}}

	to, err := newClient(t, f).RejectLogin(context.Background(), "lc", "Your company sign-in was refused.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if to != "https://kratos.example/err" {
		t.Errorf("expected hydra-sso's redirect, got %q", to)
	}
	if f.path != "/admin/oauth2/auth/requests/login/reject" || f.challenge != "lc" {
		t.Errorf("expected the login request of lc rejected, got %s for %q", f.path, f.challenge)
	}
	if f.body["error"] != "access_denied" || f.body["error_description"] != "Your company sign-in was refused." {
		t.Errorf("unexpected body %v", f.body)
	}
}

func TestClient_GetConsentRequest(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		f := &fakeAdmin{answer: consentRequest()}

		request, err := newClient(t, f).GetConsentRequest(context.Background(), "cc")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.path != "/admin/oauth2/auth/requests/consent" || f.challenge != "cc" {
			t.Errorf("expected the consent request of cc asked for, got %s for %q", f.path, f.challenge)
		}
		if request.Challenge != "cc" || request.Context["email"] != "a@b.example" ||
			!reflect.DeepEqual(request.RequestedScope, []string{"openid", "email"}) {
			t.Errorf("unexpected request %+v", request)
		}
	})

	t.Run("not found", func(t *testing.T) {
		f := &fakeAdmin{status: http.StatusNotFound, answer: map[string]any{"error": "not_found"}}

		if _, err := newClient(t, f).GetConsentRequest(context.Background(), "cc"); !errors.Is(err, ErrNotFound) {
			t.Errorf("expected %v, got %v", ErrNotFound, err)
		}
	})
}

func TestClient_AcceptConsent(t *testing.T) {
	f := &fakeAdmin{answer: map[string]any{"redirect_to": "https://kratos.example/cb"}}
	request := &ConsentRequest{Challenge: "cc", RequestedScope: []string{"openid", "email"}, RequestedAccessTokenAudience: []string{}}

	to, err := newClient(t, f).AcceptConsent(context.Background(), request, map[string]any{"email": "a@b.example", "email_verified": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if to != "https://kratos.example/cb" {
		t.Errorf("expected hydra-sso's redirect, got %q", to)
	}
	if f.path != "/admin/oauth2/auth/requests/consent/accept" || f.challenge != "cc" {
		t.Errorf("expected the consent request of cc accepted, got %s for %q", f.path, f.challenge)
	}
	session, _ := f.body["session"].(map[string]any)
	if f.body["remember"] != false || !reflect.DeepEqual(f.body["grant_scope"], []any{"openid", "email"}) ||
		!reflect.DeepEqual(session["id_token"], map[string]any{"email": "a@b.example", "email_verified": true}) {
		t.Errorf("unexpected body %v", f.body)
	}
}
