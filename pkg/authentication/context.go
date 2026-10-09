// Copyright 2025 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package authentication

import "context"

// Define a private custom type to avoid collisions
type contextKey struct{}

var userContextKey = contextKey{}

// WithUserID returns a new context with the given user ID derived from the parent context.
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userContextKey, userID)
}

// GetUserID retrieves the user ID from the context.
// Returns an empty string and false if the user ID is not present.
func GetUserID(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(userContextKey).(string)
	return id, ok
}
