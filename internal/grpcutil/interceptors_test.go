// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package grpcutil

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
)

func TestDeadlineUnaryInterceptor(t *testing.T) {
	const timeout = 25 * time.Second
	handlerErr := errors.New("handler error")

	// left runs a call through the interceptor and returns how long the
	// handler had left.
	left := func(t *testing.T, ctx context.Context) time.Duration {
		t.Helper()
		var got time.Duration
		_, err := DeadlineUnaryInterceptor(timeout)(ctx, "request", &grpc.UnaryServerInfo{}, func(ctx context.Context, req any) (any, error) {
			if req != "request" {
				t.Errorf("expected the request passed on, got %v", req)
			}
			at, ok := ctx.Deadline()
			if !ok {
				t.Error("expected the handler's context to have a deadline")
			}
			got = time.Until(at)
			return nil, handlerErr
		})
		if !errors.Is(err, handlerErr) {
			t.Errorf("expected the handler's error passed on, got %v", err)
		}
		return got
	}

	t.Run("no caller deadline", func(t *testing.T) {
		if got := left(t, context.Background()); got > timeout || got < timeout-2*time.Second {
			t.Errorf("expected about %s left, got %s", timeout, got)
		}
	})

	t.Run("longer caller deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		if got := left(t, ctx); got > timeout {
			t.Errorf("expected at most %s left, got %s", timeout, got)
		}
	})

	t.Run("shorter caller deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if got := left(t, ctx); got > time.Second {
			t.Errorf("expected the caller's own second kept, got %s", got)
		}
	})

	t.Run("deadline passes", func(t *testing.T) {
		started := time.Now()
		_, err := DeadlineUnaryInterceptor(20*time.Millisecond)(context.Background(), nil, &grpc.UnaryServerInfo{}, func(ctx context.Context, _ any) (any, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * time.Second):
				return nil, nil
			}
		})
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 3*time.Second {
			t.Errorf("expected the deadline to end the handler, got %v after %s", err, time.Since(started))
		}
	})
}
