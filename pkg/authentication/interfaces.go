// Copyright 2025 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package authentication

import (
	"context"

	"github.com/coreos/go-oidc/v3/oidc"
)

type ProviderInterface interface {
	// Verifier returns the token verifier associated with the specified OIDC issuer
	Verifier(*oidc.Config) *oidc.IDTokenVerifier
}

type TokenVerifierInterface interface {
	// VerifyToken verifies a raw JWT string and validates authorization claims
	// Returns the subject (user ID) if the token is valid and authorized, otherwise an error
	VerifyToken(ctx context.Context, rawToken string) (string, error)
}
