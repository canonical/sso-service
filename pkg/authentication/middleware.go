// Copyright 2025 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package authentication

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/monitoring"
	"github.com/canonical/sso-service/internal/tracing"
)

type Middleware struct {
	verifier TokenVerifierInterface

	tracer  tracing.TracingInterface
	monitor monitoring.MonitorInterface
	logger  logging.LoggerInterface
}

func (m *Middleware) Authenticate() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, span := m.tracer.Start(r.Context(), "authentication.Middleware.Authenticate")
			defer span.End()

			token, found := m.getBearerToken(r.Header)
			if !found {
				err := errors.New("missing authorization header")
				span.RecordError(err)
				span.SetStatus(otelcodes.Error, err.Error())
				m.unauthorizedResponse(w, "missing authorization header")
				return
			}

			userID, err := m.verifier.VerifyToken(ctx, token)
			if err != nil {
				m.logger.Debugf("JWT verification failed: %v", err)
				span.RecordError(err)
				span.SetStatus(otelcodes.Error, err.Error())
				m.unauthorizedResponse(w, "invalid token")
				return
			}

			// Token is valid, inject user ID into context
			ctx = WithUserID(ctx, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GRPCInterceptor is a unary interceptor for gRPC authentication
func (m *Middleware) GRPCInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	ctx, span := m.tracer.Start(ctx, "authentication.Middleware.GRPCInterceptor")
	defer span.End()

	ctx, err := m.authenticateGRPC(ctx, span)
	if err != nil {
		return nil, err
	}

	resp, err := handler(ctx, req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, err.Error())
	}
	return resp, err
}

// authenticateGRPC extracts and verifies the bearer token from gRPC metadata,
// returning a context enriched with the verified user ID on success.
func (m *Middleware) authenticateGRPC(ctx context.Context, span trace.Span) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		err := errors.New("metadata is not provided")
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, err.Error())
		return ctx, status.Error(codes.Unauthenticated, "metadata is not provided")
	}

	values := md.Get("authorization")
	if len(values) == 0 {
		err := errors.New("authorization token is not provided")
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, err.Error())
		return ctx, status.Error(codes.Unauthenticated, "authorization token is not provided")
	}

	authHeader := values[0]
	if !strings.HasPrefix(authHeader, "Bearer ") {
		err := errors.New("authorization token is not a bearer token")
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, err.Error())
		return ctx, status.Error(codes.Unauthenticated, "authorization token is not a bearer token")
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")
	userID, err := m.verifier.VerifyToken(ctx, token)
	if err != nil {
		m.logger.Debugf("gRPC JWT verification failed: %v", err)
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, err.Error())
		return ctx, status.Error(codes.Unauthenticated, "invalid token")
	}

	return WithUserID(ctx, userID), nil
}

func (m *Middleware) getBearerToken(headers http.Header) (string, bool) {
	bearer := headers.Get("Authorization")
	if bearer == "" {
		return "", false
	}

	// Only support "Bearer <token>" format (RFC 6750)
	if !strings.HasPrefix(bearer, "Bearer ") {
		return "", false
	}

	return strings.TrimPrefix(bearer, "Bearer "), true
}

func (m *Middleware) unauthorizedResponse(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  http.StatusUnauthorized,
		"message": message,
	}); err != nil {
		m.logger.Errorf("failed to encode unauthorized response: %v", err)
	}
}

func NewMiddleware(verifier TokenVerifierInterface, tracer tracing.TracingInterface, monitor monitoring.MonitorInterface, logger logging.LoggerInterface) *Middleware {
	return &Middleware{
		verifier: verifier,
		tracer:   tracer,
		monitor:  monitor,
		logger:   logger,
	}
}
