// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package tenants

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"

	"github.com/canonical/sso-service/internal/limits"
)

const (
	keepaliveTime    = 30 * time.Second
	keepaliveTimeout = 10 * time.Second
)

// serviceConfig retries a call that ends UNAVAILABLE. Every RPC named is a
// read, or leaves the same state when it is repeated.
const serviceConfig = `{"methodConfig": [{
	"name": [
		{"service": "identity.platform.api.tenant.TenantSignInService", "method": "GetSignInContext"},
		{"service": "identity.platform.api.tenant.TenantSSOPolicyService", "method": "GetTenantSSOPolicy"},
		{"service": "identity.platform.api.tenant.TenantSSOPolicyService", "method": "PutTenantSSOPolicy"},
		{"service": "identity.platform.api.tenant.TenantSSOPolicyService", "method": "SetTenantSSODomains"},
		{"service": "identity.platform.api.tenant.TenantSSOPolicyService", "method": "RemoveTenantSSOBinding"}
	],
	"retryPolicy": {
		"maxAttempts": 3,
		"initialBackoff": "0.1s",
		"maxBackoff": "1s",
		"backoffMultiplier": 2,
		"retryableStatusCodes": ["UNAVAILABLE"]
	}
}]}`

type Credentials struct {
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scopes       []string
}

// TokenSource fetches a client-credentials token and keeps it while it is
// valid.
func TokenSource(c Credentials) oauth2.TokenSource {
	config := clientcredentials.Config{
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		TokenURL:     c.TokenURL,
		Scopes:       c.Scopes,
	}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Timeout: limits.TokenTimeout})

	return oauth2.ReuseTokenSource(nil, config.TokenSource(ctx))
}

// NewGRPCConn creates a gRPC client connection to address, configured with
// keepalive parameters and optionally TLS transport credentials.
// The caller is responsible for closing the returned connection.
func NewGRPCConn(address string, tlsEnabled bool, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	var creds credentials.TransportCredentials
	if tlsEnabled {
		creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	} else {
		creds = insecure.NewCredentials()
	}

	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                keepaliveTime,
			Timeout:             keepaliveTimeout,
			PermitWithoutStream: true,
		}),
	}

	conn, err := grpc.NewClient(address, append(dialOpts, opts...)...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to tenant-service at %s: %v", address, err)
	}

	return conn, nil
}

// DialOptions authenticate every call with a token from source, retry calls
// that end UNAVAILABLE, and reconnect within seconds instead of gRPC's
// default of up to two minutes.
func DialOptions(source oauth2.TokenSource) []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		grpc.WithChainUnaryInterceptor(bearerInterceptor(source)),
		grpc.WithDefaultServiceConfig(serviceConfig),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           backoff.Config{BaseDelay: 200 * time.Millisecond, Multiplier: 1.6, Jitter: 0.2, MaxDelay: 5 * time.Second},
			MinConnectTimeout: 5 * time.Second,
		}),
	}
}

// bearerInterceptor is used instead of gRPC's per-RPC credentials, which
// refuse to send a token over a connection without TLS.
func bearerInterceptor(source oauth2.TokenSource) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		token, err := source.Token()
		if err != nil {
			return fmt.Errorf("failed to get a service token: %w", err)
		}
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token.AccessToken)

		return invoker(ctx, method, req, reply, cc, opts...)
	}
}
