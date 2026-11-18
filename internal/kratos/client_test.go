// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package kratos

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/monitoring"
	"github.com/canonical/sso-service/internal/tracing"
)

const identityJSON = `{"id":"i-1","schema_id":"default","schema_url":"http://kratos/schemas/default","traits":{"email":"a@b.example"},
	"credentials":{"oidc":{"type":"oidc","identifiers":["byo-sso:c:s"],"config":{"providers":[{"provider":"byo-sso","subject":"c:s"}]}},
	"password":{"type":"password","identifiers":["a@b.example"]}}}`

func newTestClient(url string) *Client {
	logger := logging.NewNoopLogger()

	return NewClient(url, tracing.NewNoopTracer(), monitoring.NewNoopMonitor("sso-service", logger), logger)
}

// newTestServer answers every request with status and body, and records the
// requests it got as "METHOD path?query".
func newTestServer(t *testing.T, status int, body string) (*httptest.Server, *[]string) {
	t.Helper()
	requests := new([]string)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*requests = append(*requests, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	return server, requests
}

func identityWith(credentials map[string]string) *Identity {
	identity := &Identity{Credentials: map[string]Credential{}}
	for kind, config := range credentials {
		identity.Credentials[kind] = Credential{Config: json.RawMessage(config)}
	}
	return identity
}

func TestClient_GetIdentity(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		server, requests := newTestServer(t, http.StatusOK, identityJSON)

		// A trailing slash in the admin URL names the same API.
		identity, err := newTestClient(server.URL+"/").GetIdentity(context.Background(), "i-1", FirstFactorCredentials...)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "GET /admin/identities/i-1?include_credential=password&include_credential=oidc&include_credential=passkey&include_credential=webauthn"
		if !slices.Equal(*requests, []string{want}) {
			t.Errorf("expected request %q, got %v", want, *requests)
		}
		if identity.ID != "i-1" || identity.Email() != "a@b.example" {
			t.Errorf("expected identity i-1 of a@b.example, got %+v", identity)
		}
		if providers := identity.OIDCProviders(); len(providers) != 1 || providers[0].Subject != "c:s" {
			t.Errorf("expected the oidc credential's config, got %+v", providers)
		}
	})

	t.Run("no credentials asked", func(t *testing.T) {
		server, requests := newTestServer(t, http.StatusOK, identityJSON)

		if _, err := newTestClient(server.URL).GetIdentity(context.Background(), "i-1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := "GET /admin/identities/i-1?"; !slices.Equal(*requests, []string{want}) {
			t.Errorf("expected request %q, got %v", want, *requests)
		}
	})

	t.Run("not found", func(t *testing.T) {
		server, _ := newTestServer(t, http.StatusNotFound, `{"error":{"message":"kratos says no"}}`)

		if _, err := newTestClient(server.URL).GetIdentity(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
			t.Errorf("expected %v, got %v", ErrNotFound, err)
		}
	})

	t.Run("server error", func(t *testing.T) {
		server, _ := newTestServer(t, http.StatusInternalServerError, `{"error":{"message":"kratos says no"}}`)

		if _, err := newTestClient(server.URL).GetIdentity(context.Background(), "broken"); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("expected an error that is not %v, got %v", ErrNotFound, err)
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		server, _ := newTestServer(t, http.StatusOK, identityJSON)
		server.Close()

		if _, err := newTestClient(server.URL).GetIdentity(context.Background(), "i-1"); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("expected an error that is not %v, got %v", ErrNotFound, err)
		}
	})
}

func TestClient_ListByIdentifier(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		server, requests := newTestServer(t, http.StatusOK, "["+identityJSON+"]")

		found, err := newTestClient(server.URL).ListByIdentifier(context.Background(), "a@b.example")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := "GET /admin/identities?credentials_identifier=a%40b.example"; !slices.Equal(*requests, []string{want}) {
			t.Errorf("expected request %q, got %v", want, *requests)
		}
		if len(found) != 1 || found[0].ID != "i-1" || found[0].Email() != "a@b.example" {
			t.Errorf("expected identity i-1 of a@b.example, got %+v", found)
		}
	})

	t.Run("none", func(t *testing.T) {
		server, _ := newTestServer(t, http.StatusOK, `[]`)

		found, err := newTestClient(server.URL).ListByIdentifier(context.Background(), "nobody@b.example")
		if err != nil || len(found) != 0 {
			t.Errorf("expected no identities, got %+v %v", found, err)
		}
	})

	t.Run("server error", func(t *testing.T) {
		server, _ := newTestServer(t, http.StatusInternalServerError, `{"error":{"message":"kratos says no"}}`)

		if _, err := newTestClient(server.URL).ListByIdentifier(context.Background(), "a@b.example"); err == nil {
			t.Error("expected error but got none")
		}
	})

	t.Run("unreachable: the error does not name the address", func(t *testing.T) {
		server, _ := newTestServer(t, http.StatusOK, `[]`)
		url := server.URL
		server.Close()

		_, err := newTestClient(url).ListByIdentifier(context.Background(), "a@b.example")
		if err == nil {
			t.Fatal("expected error but got none")
		}
		if strings.Contains(err.Error(), "a%40b.example") || strings.Contains(err.Error(), "a@b.example") {
			t.Errorf("expected no address in the error, got %v", err)
		}
	})
}

func TestClient_DeleteOIDCIdentifier(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		server, requests := newTestServer(t, http.StatusNoContent, "")

		if err := newTestClient(server.URL).DeleteOIDCIdentifier(context.Background(), "i-1", "byo-sso:c:s"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "DELETE /admin/identities/i-1/credentials/oidc?identifier=byo-sso%3Ac%3As"
		if !slices.Equal(*requests, []string{want}) {
			t.Errorf("expected request %q, got %v", want, *requests)
		}
	})

	t.Run("not found", func(t *testing.T) {
		server, _ := newTestServer(t, http.StatusNotFound, `{"error":{"message":"kratos says no"}}`)

		if err := newTestClient(server.URL).DeleteOIDCIdentifier(context.Background(), "i-1", "x"); !errors.Is(err, ErrNotFound) {
			t.Errorf("expected %v, got %v", ErrNotFound, err)
		}
	})

	for _, code := range []int{http.StatusBadRequest, http.StatusConflict, http.StatusInternalServerError} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			server, _ := newTestServer(t, code, `{"error":{"message":"kratos says no"}}`)

			err := newTestClient(server.URL).DeleteOIDCIdentifier(context.Background(), "i-1", "x")
			if err == nil || errors.Is(err, ErrNotFound) {
				t.Errorf("expected an error that is not %v, got %v", ErrNotFound, err)
			}
		})
	}
}

func TestIdentity_Email(t *testing.T) {
	if got := (&Identity{Traits: json.RawMessage(`{"email":"a@b.example"}`)}).Email(); got != "a@b.example" {
		t.Errorf("expected a@b.example, got %q", got)
	}
	if got := (&Identity{}).Email(); got != "" {
		t.Errorf("expected no address without traits, got %q", got)
	}
}

func TestIdentity_OIDCProviders(t *testing.T) {
	testCases := []struct {
		name        string
		credentials map[string]string
		expected    []OIDCProvider
	}{
		{name: "no credential"},
		{name: "no config", credentials: map[string]string{"oidc": ``}},
		{
			name:        "providers",
			credentials: map[string]string{"oidc": `{"providers":[{"provider":"byo-sso","subject":"c:s"},{"provider":"google","subject":"g"}]}`},
			expected:    []OIDCProvider{{Provider: "byo-sso", Subject: "c:s"}, {Provider: "google", Subject: "g"}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := identityWith(tc.credentials).OIDCProviders(); !slices.Equal(got, tc.expected) {
				t.Errorf("expected %+v, got %+v", tc.expected, got)
			}
		})
	}
}

// Kratos gives every identity a password credential for its address: only a
// hash makes it a password.
func TestIdentity_HasPassword(t *testing.T) {
	testCases := []struct {
		name        string
		credentials map[string]string
		expected    bool
	}{
		{name: "no credential"},
		{name: "no config", credentials: map[string]string{"password": ``}},
		{name: "empty config", credentials: map[string]string{"password": `{}`}},
		{name: "empty hash", credentials: map[string]string{"password": `{"hashed_password":""}`}},
		{name: "migration hook only", credentials: map[string]string{"password": `{"use_password_migration_hook":true}`}},
		{name: "config not an object", credentials: map[string]string{"password": `"x"`}},
		{name: "hash", credentials: map[string]string{"password": `{"hashed_password":"$2a$10$h"}`}, expected: true},
		{
			name:        "hash and migration hook",
			credentials: map[string]string{"password": `{"hashed_password":"$2a$10$h","use_password_migration_hook":true}`},
			expected:    true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := identityWith(tc.credentials).HasPassword(); got != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, got)
			}
		})
	}
}

func TestIdentity_HasPasskey(t *testing.T) {
	testCases := []struct {
		name        string
		credentials map[string]string
		expected    bool
	}{
		{name: "none"},
		{name: "passkey", credentials: map[string]string{"passkey": `{"credentials":[{"id":"x"}]}`}, expected: true},
		{name: "passkey credential with no key", credentials: map[string]string{"passkey": `{"credentials":[]}`}},
		{
			name:        "passwordless security key",
			credentials: map[string]string{"webauthn": `{"credentials":[{"is_passwordless":false},{"is_passwordless":true}]}`},
			expected:    true,
		},
		{name: "second-factor security key", credentials: map[string]string{"webauthn": `{"credentials":[{"is_passwordless":false}]}`}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := identityWith(tc.credentials).HasPasskey(); got != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, got)
			}
		})
	}
}
