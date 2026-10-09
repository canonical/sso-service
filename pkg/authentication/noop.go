// Copyright 2025 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package authentication

import (
	"context"
)

type NoopVerifier struct{}

// NewNoopVerifier returns a no-op token verifier that allows all requests.
func NewNoopVerifier() *NoopVerifier {
	return &NoopVerifier{}
}

// VerifyToken treats the token as the user ID for development purposes.
func (n *NoopVerifier) VerifyToken(ctx context.Context, rawIDToken string) (string, error) {
	return rawIDToken, nil
}
