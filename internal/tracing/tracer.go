// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package tracing

import (
	"context"
	"os"
	"runtime/debug"
	"strings"

	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/version"
	"go.opentelemetry.io/contrib/propagators/jaeger"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.18.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

type Tracer struct {
	tracer   trace.Tracer
	provider *sdktrace.TracerProvider

	logger logging.LoggerInterface
}

func (t *Tracer) init(service string, e sdktrace.SpanExporter) {
	traceProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(e),
		sdktrace.WithResource(
			t.buildResource(service),
		),
	)

	otel.SetTracerProvider(traceProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, jaeger.Jaeger{}))

	t.provider = traceProvider

	t.tracer = otel.Tracer(service)
}

func (t *Tracer) gitRevision(settings []debug.BuildSetting) string {
	for _, setting := range settings {
		if setting.Key == "vcs.revision" {
			return setting.Value
		}
	}

	return "n/a"
}

func (t *Tracer) buildResource(service string) *resource.Resource {
	var res *resource.Resource

	res = resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(service),
		semconv.ServiceVersion(version.Version),
	)

	if info, ok := debug.ReadBuildInfo(); ok {
		if service == "" {
			service = info.Path
		}

		res = resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(service),
			semconv.ServiceVersion(version.Version),
			attribute.String("git_sha", t.gitRevision(info.Settings)),
			attribute.String("app", info.Main.Path),
		)
	}

	// OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES win over the attributes above.
	if merged, err := resource.Merge(res, resource.Environment()); err == nil {
		res = merged
	}

	return res
}

func (t *Tracer) Start(ctx context.Context, spanName string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return t.tracer.Start(ctx, spanName, opts...)
}

// Shutdown exports the spans still buffered and stops the exporter.
func (t *Tracer) Shutdown(ctx context.Context) error {
	if t.provider == nil {
		return nil
	}

	return t.provider.Shutdown(ctx)
}

// plaintext tells whether to export without TLS: the default, unless the
// environment sets the exporter's transport security.
func plaintext() bool {
	for _, name := range []string{
		"OTEL_EXPORTER_OTLP_INSECURE",
		"OTEL_EXPORTER_OTLP_TRACES_INSECURE",
		"OTEL_EXPORTER_OTLP_CERTIFICATE",
		"OTEL_EXPORTER_OTLP_TRACES_CERTIFICATE",
		"OTEL_EXPORTER_OTLP_CLIENT_CERTIFICATE",
		"OTEL_EXPORTER_OTLP_TRACES_CLIENT_CERTIFICATE",
	} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return false
		}
	}

	return true
}

// basic tracer implementation of trace.Tracer, just adding some extra configuration
func NewTracer(cfg *Config) *Tracer {
	t := new(Tracer)

	t.logger = cfg.Logger

	// if tracing disabled skip the config
	if !cfg.Enabled {
		t.tracer = noop.NewTracerProvider().Tracer("github.com/canonical/sso-service")

		return t
	}

	var err error
	var exporter sdktrace.SpanExporter

	if cfg.OtelGRPCEndpoint != "" {
		opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(cfg.OtelGRPCEndpoint)}
		if plaintext() {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}

		exporter, err = otlptrace.New(context.TODO(), otlptracegrpc.NewClient(opts...))
	} else if cfg.OtelHTTPEndpoint != "" {
		opts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(cfg.OtelHTTPEndpoint)}
		if plaintext() {
			opts = append(opts, otlptracehttp.WithInsecure())
		}

		exporter, err = otlptrace.New(context.TODO(), otlptracehttp.NewClient(opts...))
	} else {
		t.logger.Warn("tracing is enabled but no OTLP endpoint is set: spans are not exported")
		return NewNoopTracer()
	}

	if err != nil {
		t.logger.Errorf("unable to initialize tracing exporter due: %v", err)
		return NewNoopTracer()
	}

	// set tracer provider and propagator properly, this is to ensure all
	// instrumentation library could run well
	t.init("github.com/canonical/sso-service", exporter)

	return t
}

func NewNoopTracer() *Tracer {
	return NewTracer(NewNoopConfig())
}
