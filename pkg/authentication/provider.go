// Copyright 2025 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package authentication

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// requestTimeout bounds one request to the issuer: discovery at start-up,
// and its keys whenever they are fetched.
const requestTimeout = 10 * time.Second

var (
	otelHTTPClient = http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport), Timeout: requestTimeout}
)

// NewProvider creates an OIDC provider using the issuer's well-known configuration
func NewProvider(ctx context.Context, issuer string) (*oidc.Provider, error) {
	// Use otel-instrumented HTTP client
	ctx = oidc.ClientContext(ctx, &otelHTTPClient)

	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("failed to create OIDC provider: %v", err)
	}

	return provider, nil
}

// NewProviderWithJWKS creates a verifier for the issuer's tokens from the key set at jwksURL,
// without discovery; both are required (NewJWTAuthenticator uses discovery when jwksURL is empty).
func NewProviderWithJWKS(ctx context.Context, issuer, jwksURL string) (*oidc.IDTokenVerifier, error) {
	if issuer == "" || jwksURL == "" {
		return nil, fmt.Errorf("issuer and JWKS URL are required")
	}

	ctx = oidc.ClientContext(ctx, &otelHTTPClient)

	keySet := oidc.NewRemoteKeySet(ctx, jwksURL)

	verifier := oidc.NewVerifier(issuer, keySet, &oidc.Config{
		SkipClientIDCheck:    true,
		SkipIssuerCheck:      false,
		SupportedSigningAlgs: []string{oidc.RS256, oidc.ES256},
	})

	return verifier, nil
}
