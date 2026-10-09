// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	v0sso "github.com/canonical/identity-platform-api/v0/sso"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/monitoring"
	"github.com/canonical/sso-service/internal/testhelpers"
	"github.com/canonical/sso-service/internal/tracing"
	"github.com/canonical/sso-service/pkg/authentication"
)

// forgedToken has the issuer and the subject of a real token, and is signed
// by a key the issuer does not publish.
func forgedToken(t *testing.T, issuer, subject string) string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate a key: %v", err)
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "forged"}},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		t.Fatalf("failed to create a signer: %v", err)
	}
	now := time.Now()
	claims, err := json.Marshal(map[string]any{
		"iss": issuer,
		"sub": subject,
		"iat": now.Unix(),
		"exp": now.Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("failed to encode the claims: %v", err)
	}
	signed, err := signer.Sign(claims)
	if err != nil {
		t.Fatalf("failed to sign: %v", err)
	}
	token, err := signed.CompactSerialize()
	if err != nil {
		t.Fatalf("failed to serialize: %v", err)
	}

	return token
}

// awaitExpiry returns once the token's own expiry has passed.
func awaitExpiry(t *testing.T, token string) {
	t.Helper()

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected a JWT, got %d parts", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("failed to decode the token: %v", err)
	}
	var claims struct {
		Expiry int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("failed to read the token: %v", err)
	}
	// The token carries whole seconds: one more, and it is in the past.
	remaining := time.Until(time.Unix(claims.Expiry, 0).Add(time.Second))
	if remaining > 10*time.Second {
		t.Fatalf("expected a token that lives for a second, it expires in %s", remaining)
	}
	time.Sleep(remaining)
}

// TestIntegration_Authentication checks which access tokens of a real Hydra
// the service takes, on its HTTP routes and on its gRPC listener.
func TestIntegration_Authentication(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	logger := logging.NewNoopLogger()
	tracer := tracing.NewNoopTracer()
	monitor := monitoring.NewNoopMonitor("sso-service-test", logger)

	hydraEnv := shared.Hydra.Env(t)
	clientID, clientSecret := testhelpers.CreateHydraClient(t, hydraEnv, "Test Client", 0)
	validToken := testhelpers.GetAccessToken(t, hydraEnv, clientID, clientSecret)
	shortID, shortSecret := testhelpers.CreateHydraClient(t, hydraEnv, "Short-lived Client", time.Second)
	expiredToken := testhelpers.GetAccessToken(t, hydraEnv, shortID, shortSecret)
	awaitExpiry(t, expiredToken)

	verifier, err := authentication.NewJWTAuthenticator(ctx, hydraEnv.Issuer, hydraEnv.PublicURL+"/.well-known/jwks.json", nil, "", tracer, monitor, logger)
	if err != nil {
		t.Fatalf("failed to create JWT authenticator: %v", err)
	}
	p := startPlaneWith(t, verifier)

	refused := []struct {
		name        string
		token       string
		httpMessage string
		grpcMessage string
	}{
		{name: "no token", token: "", httpMessage: "missing authorization header", grpcMessage: "authorization token is not provided"},
		{name: "not a token", token: "not-a-token", httpMessage: "invalid token", grpcMessage: "invalid token"},
		{name: "signed by another key", token: forgedToken(t, hydraEnv.Issuer, clientID), httpMessage: "invalid token", grpcMessage: "invalid token"},
		{name: "expired", token: expiredToken, httpMessage: "invalid token", grpcMessage: "invalid token"},
	}

	// One tenant-admin route and one platform-admin route; neither needs
	// anything to exist.
	routes := []string{
		"/api/v0/sso/tenants/" + uuid.NewString() + "/connections",
		"/api/v0/sso/connections",
	}
	get := func(t *testing.T, route, token string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, p.ssoURL+route, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := p.sso.Client().Do(req)
		if err != nil {
			t.Fatalf("failed to execute request: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)

		return resp.StatusCode, body
	}

	// One RPC of each service; neither needs anything to exist.
	calls := map[string]func(ctx context.Context) error{
		"SSOSignInService.ListOptions": func(ctx context.Context) error {
			_, err := p.user.ListOptions(ctx, &v0sso.ListOptionsRequest{})
			return err
		},
		"SSOTenantAdminService.ListConnections": func(ctx context.Context) error {
			_, err := p.admin.ListConnections(ctx, &v0sso.ListConnectionsRequest{TenantId: uuid.NewString()})
			return err
		},
		"SSOPlatformAdminService.ListAllConnections": func(ctx context.Context) error {
			_, err := p.platform.ListAllConnections(ctx, &v0sso.ListAllConnectionsRequest{})
			return err
		},
	}
	bearer := func(token string) context.Context {
		if token == "" {
			return context.Background()
		}
		return metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token)
	}

	t.Run("HTTP", func(t *testing.T) {
		for _, tt := range refused {
			t.Run(tt.name, func(t *testing.T) {
				for _, route := range routes {
					code, body := get(t, route, tt.token)

					var refusal struct {
						Status  int    `json:"status"`
						Message string `json:"message"`
					}
					_ = json.Unmarshal(body, &refusal)
					if code != http.StatusUnauthorized || refusal.Status != http.StatusUnauthorized || refusal.Message != tt.httpMessage {
						t.Errorf("expected 401 %q from %s, got %d: %s", tt.httpMessage, route, code, body)
					}
				}
			})
		}

		t.Run("valid token", func(t *testing.T) {
			for _, route := range routes {
				if code, body := get(t, route, validToken); code != http.StatusOK {
					t.Errorf("expected 200 from %s, got %d: %s", route, code, body)
				}
			}
		})

		t.Run("status needs no token", func(t *testing.T) {
			if code, body := get(t, "/api/v0/status", ""); code != http.StatusOK {
				t.Errorf("expected 200 from the status endpoint, got %d: %s", code, body)
			}
		})
	})

	t.Run("gRPC", func(t *testing.T) {
		for _, tt := range refused {
			t.Run(tt.name, func(t *testing.T) {
				for rpc, call := range calls {
					err := call(bearer(tt.token))
					if status.Code(err) != codes.Unauthenticated || status.Convert(err).Message() != tt.grpcMessage {
						t.Errorf("expected Unauthenticated %q from %s, got %v", tt.grpcMessage, rpc, err)
					}
				}
			})
		}

		t.Run("valid token", func(t *testing.T) {
			for rpc, call := range calls {
				if err := call(bearer(validToken)); err != nil {
					t.Errorf("expected success from %s, got error: %v", rpc, err)
				}
			}
		})
	})
}
