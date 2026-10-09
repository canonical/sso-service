// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package logging

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// LoggingUnaryInterceptor returns a gRPC unary server interceptor that logs each
// RPC call with the method name, status code, and duration. Server-side error
// codes (Internal, Unknown, Unavailable, DataLoss, DeadlineExceeded) are logged at
// Error level; all other outcomes are logged at Debug level.
func LoggingUnaryInterceptor(logger LoggerInterface) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		startTime := time.Now()
		resp, err := handler(ctx, req)
		elapsed := time.Since(startTime)

		code := status.Code(err)
		if isServerErrorCode(code) {
			logger.Errorf("gRPC %s %s in %s", info.FullMethod, code, elapsed)
		} else {
			logger.Debugf("gRPC %s %s in %s", info.FullMethod, code, elapsed)
		}

		return resp, err
	}
}

// isServerErrorCode reports whether a gRPC status code represents a server-side
// failure (as opposed to a client error or an expected condition).
func isServerErrorCode(code codes.Code) bool {
	switch code {
	case codes.Unknown, codes.Internal, codes.Unavailable, codes.DataLoss, codes.DeadlineExceeded:
		return true
	default:
		return false
	}
}
