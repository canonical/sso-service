// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"net/http"
	"time"

	cors "github.com/go-chi/cors"
)

func middlewareCORS(origins []string) func(http.Handler) http.Handler {
	return cors.Handler(
		cors.Options{
			AllowedOrigins: origins,
			AllowedMethods: []string{
				http.MethodHead,
				http.MethodGet,
				http.MethodPost,
				http.MethodPut,
				http.MethodPatch,
				http.MethodDelete,
				http.MethodOptions,
			},
			AllowedHeaders: []string{"*"},
			MaxAge:         300, // Maximum value not ignored by any of major browsers
		},
	)
}

// middlewareDeadline gives every request a deadline: whatever a handler does
// with the request's context ends by then.
func middlewareDeadline(timeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
