// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package tracing

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/monitoring"
	chi "github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.18.0"
)

func TestOpenTelemetry_SpanNamedAfterRoute(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter)))
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	router := chi.NewRouter()
	router.Get("/callback/{tenant_id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	logger := logging.NewNoopLogger()
	handler := NewMiddleware(monitoring.NewNoopMonitor("test", logger), logger).OpenTelemetry(router)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/callback/abc?code=x", nil))

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected one span, got %d", len(spans))
	}
	if got, want := spans[0].Name, "GET /callback/{tenant_id}"; got != want {
		t.Errorf("span name: got %q, want %q", got, want)
	}

	route := ""
	for _, attr := range spans[0].Attributes {
		if attr.Key == semconv.HTTPRouteKey {
			route = attr.Value.AsString()
		}
	}
	if want := "/callback/{tenant_id}"; route != want {
		t.Errorf("http.route: got %q, want %q", route, want)
	}
}
