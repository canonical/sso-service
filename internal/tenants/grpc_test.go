// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package tenants

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	v0tenant "github.com/canonical/identity-platform-api/v0/tenant"
	"golang.org/x/oauth2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type failingSource struct{}

func (failingSource) Token() (*oauth2.Token, error) { return nil, errors.New("hydra down") }

func TestTokenSource(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_ = r.ParseForm()
		id, secret, _ := r.BasicAuth()
		if id != "sso-service" || secret != "s3cret" || r.PostForm.Get("grant_type") != "client_credentials" || r.PostForm.Get("scope") != "tenant-service other" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "svc-token", "token_type": "bearer", "expires_in": 3600})
	}))
	defer server.Close()

	t.Run("token kept while valid", func(t *testing.T) {
		source := TokenSource(Credentials{TokenURL: server.URL, ClientID: "sso-service", ClientSecret: "s3cret", Scopes: []string{"tenant-service", "other"}})

		for range 3 {
			got, err := source.Token()
			if err != nil || got.AccessToken != "svc-token" {
				t.Fatalf("expected svc-token, got %v %v", got, err)
			}
		}
		if requests != 1 {
			t.Errorf("expected one request to the token endpoint, got %d", requests)
		}
	})

	t.Run("wrong secret", func(t *testing.T) {
		source := TokenSource(Credentials{TokenURL: server.URL, ClientID: "sso-service", ClientSecret: "wrong"})

		if _, err := source.Token(); err == nil {
			t.Error("expected error but got none")
		}
	})
}

func TestNewGRPCConn(t *testing.T) {
	for name, tlsEnabled := range map[string]bool{"plaintext": false, "tls": true} {
		t.Run(name, func(t *testing.T) {
			conn, err := NewGRPCConn("tenant-service.example:50051", tlsEnabled)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			_ = conn.Close()
		})
	}
}

func TestDialOptions_Token(t *testing.T) {
	t.Run("sent with every call", func(t *testing.T) {
		f := &fakeTenantService{context: &v0tenant.SignInContext{}}

		if _, err := dial(t, f, token, callTimeout).GetSignInContext(context.Background(), "t", "", "i"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !slices.Equal(f.tokens, []string{"Bearer svc-token"}) {
			t.Errorf("expected the service's token, got %v", f.tokens)
		}
	})

	t.Run("no token no call", func(t *testing.T) {
		f := &fakeTenantService{context: &v0tenant.SignInContext{}}

		if _, err := dial(t, f, failingSource{}, callTimeout).GetSignInContext(context.Background(), "t", "", "i"); !errors.Is(err, ErrUnavailable) {
			t.Errorf("expected %v, got %v", ErrUnavailable, err)
		}
		if f.reached() != 0 {
			t.Errorf("expected no call to tenant-service, it saw %d", f.reached())
		}
	})
}

// The service config parses and names the RPCs as tenant-service serves
// them: a call answered UNAVAILABLE is tried again without its caller seeing
// it.
func TestDialOptions_Retry(t *testing.T) {
	unavailable := status.Error(codes.Unavailable, "restarting")
	calls := map[string]func(c *Client) error{
		"GetSignInContext": func(c *Client) error {
			_, err := c.GetSignInContext(context.Background(), "t", "", "i")
			return err
		},
		"GetTenantSSOPolicy": func(c *Client) error {
			_, err := c.GetTenantSSOPolicy(context.Background(), "t")
			return err
		},
		"PutTenantSSOPolicy": func(c *Client) error {
			_, err := c.PutTenantSSOPolicy(context.Background(), &v0tenant.PutTenantSSOPolicyRequest{TenantId: "t"})
			return err
		},
		"SetTenantSSODomains": func(c *Client) error {
			_, err := c.SetTenantSSODomains(context.Background(), "t", []string{"a.example"})
			return err
		},
		"RemoveTenantSSOBinding": func(c *Client) error {
			return c.RemoveTenantSSOBinding(context.Background(), "t", "c1")
		},
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			f := &fakeTenantService{context: &v0tenant.SignInContext{}, failures: []error{unavailable, unavailable}}

			started := time.Now()
			if err := call(dial(t, f, token, callTimeout)); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !slices.Equal(f.tokens, []string{"Bearer svc-token", "Bearer svc-token", "Bearer svc-token"}) {
				t.Errorf("expected three attempts, each with the token, got %v", f.tokens)
			}
			if took := time.Since(started); took > callTimeout {
				t.Errorf("expected the retries within the call's timeout, took %s", took)
			}
		})
	}

	t.Run("always unavailable", func(t *testing.T) {
		f := &fakeTenantService{err: unavailable}

		if _, err := dial(t, f, token, callTimeout).GetTenantSSOPolicy(context.Background(), "t"); !errors.Is(err, ErrUnavailable) {
			t.Errorf("expected %v, got %v", ErrUnavailable, err)
		}
		if f.reached() != 3 {
			t.Errorf("expected three attempts, tenant-service saw %d", f.reached())
		}
	})

	refusals := map[codes.Code]error{
		codes.FailedPrecondition: ErrRefused,
		codes.InvalidArgument:    ErrInvalid,
		codes.Aborted:            ErrBusy,
		codes.NotFound:           ErrTenantNotFound,
		codes.Internal:           ErrUnavailable,
		codes.DeadlineExceeded:   ErrUnavailable,
	}
	for code, expectedErr := range refusals {
		t.Run("no retry of "+code.String(), func(t *testing.T) {
			f := &fakeTenantService{err: status.Error(code, "no")}

			_, err := dial(t, f, token, callTimeout).PutTenantSSOPolicy(context.Background(), &v0tenant.PutTenantSSOPolicyRequest{TenantId: "t"})
			if !errors.Is(err, expectedErr) {
				t.Errorf("expected %v, got %v", expectedErr, err)
			}
			if f.reached() != 1 {
				t.Errorf("expected one attempt, tenant-service saw %d", f.reached())
			}
		})
	}
}
