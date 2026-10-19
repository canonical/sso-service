// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package grpcutil

import (
	"context"
	"time"

	"google.golang.org/grpc"
)

// DeadlineUnaryInterceptor returns a gRPC unary server interceptor that gives
// every call a deadline of timeout; an earlier one the caller set stands.
func DeadlineUnaryInterceptor(timeout time.Duration) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		return handler(ctx, req)
	}
}
