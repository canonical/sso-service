// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"net/http"
	"time"

	v0sso "github.com/canonical/identity-platform-api/v0/sso"
	chi "github.com/go-chi/chi/v5"
	middleware "github.com/go-chi/chi/v5/middleware"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/canonical/sso-service/internal/http/types"
	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/monitoring"
	"github.com/canonical/sso-service/internal/tracing"
	"github.com/canonical/sso-service/pkg/authentication"
	"github.com/canonical/sso-service/pkg/bridge"
	"github.com/canonical/sso-service/pkg/metrics"
	"github.com/canonical/sso-service/pkg/status"
)

func NewRouter(
	pages *bridge.API,
	adminHandler AdminInterface,
	authMiddleware *authentication.Middleware,
	requestTimeout time.Duration,
	tracer tracing.TracingInterface,
	monitor monitoring.MonitorInterface,
	logger logging.LoggerInterface,
) http.Handler {
	router := chi.NewMux()

	middlewares := make(chi.Middlewares, 0)
	middlewares = append(
		middlewares,
		middleware.RequestID,
		monitoring.NewMiddleware(monitor, logger).ResponseTime(),
		middleware.RequestLogger(logging.NewLogFormatter(logger)),
		middlewareDeadline(requestTimeout),
	)

	gRPCGatewayMux := runtime.NewServeMux(
		runtime.WithForwardResponseRewriter(types.ForwardErrorResponseRewriter),
		runtime.WithDisablePathLengthFallback(),
		// Use proto field names (snake_case) in JSON output instead of lowerCamelCase.
		runtime.WithMarshalerOption(runtime.MIMEWildcard, &runtime.JSONPb{
			MarshalOptions: protojson.MarshalOptions{
				UseProtoNames:   true,
				EmitUnpopulated: true,
			},
		}),
	)
	_ = v0sso.RegisterSSOTenantAdminServiceHandlerServer(context.Background(), gRPCGatewayMux, adminHandler)
	_ = v0sso.RegisterSSOPlatformAdminServiceHandlerServer(context.Background(), gRPCGatewayMux, adminHandler)

	router.Use(middlewares...)

	metrics.NewAPI(logger).RegisterEndpoints(router)
	status.NewAPI(tracer, monitor, logger).RegisterEndpoints(router)
	pages.RegisterEndpoints(router)

	// CORS and the authentication middleware apply to the gRPC Gateway
	// endpoints only: the browser pages registered above bypass both.
	authRouter := chi.NewRouter()
	authRouter.Use(middlewareCORS([]string{"*"}), authMiddleware.Authenticate())
	authRouter.Mount("/", gRPCGatewayMux)

	router.Mount("/api/v0/sso", authRouter)

	return tracing.NewMiddleware(monitor, logger).OpenTelemetry(router)
}
