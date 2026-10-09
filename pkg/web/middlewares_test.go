// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMiddlewareDeadline(t *testing.T) {
	t.Run("context ends at the deadline", func(t *testing.T) {
		var err error
		handler := middlewareDeadline(20 * time.Millisecond)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
				err = r.Context().Err()
			case <-time.After(5 * time.Second):
			}
		}))
		started := time.Now()
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		if err != context.DeadlineExceeded || time.Since(started) > 3*time.Second {
			t.Fatalf("got %v after %s", err, time.Since(started))
		}
	})
	t.Run("shorter deadline kept", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		handler := middlewareDeadline(time.Hour)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if at, ok := r.Context().Deadline(); !ok || time.Until(at) > time.Second {
				t.Errorf("deadline %v %v", at, ok)
			}
		}))
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx))
	})
	t.Run("context released", func(t *testing.T) {
		var seen context.Context
		handler := middlewareDeadline(time.Hour)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = r.Context() }))
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		if seen.Err() != context.Canceled {
			t.Fatalf("got %v", seen.Err())
		}
	})
}
