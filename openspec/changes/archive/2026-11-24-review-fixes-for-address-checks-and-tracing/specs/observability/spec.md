## MODIFIED Requirements

### Requirement: Status, version and traces
The system SHALL answer `GET /api/v0/status` with `{"status": "ok", "buildInfo": {…}}`. The system SHALL answer `GET /api/v0/version` with the version, the commit and the name of the build. Neither route checks a dependency.

With `TRACING_ENABLED` the system SHALL record OpenTelemetry spans for requests, service operations, database statements and outbound calls. The system SHALL send the spans to `OTEL_GRPC_ENDPOINT`, or else to `OTEL_HTTP_ENDPOINT`. When neither endpoint is set, the system SHALL log a warning and run without tracing. When the system cannot create the exporter of the spans at start, it SHALL log the error and run without tracing. Both cases are the same as `TRACING_ENABLED` off.

The export is plaintext by default. When one of the TLS settings of the exporter is set, the system SHALL use those settings for the export. These settings are `OTEL_EXPORTER_OTLP_INSECURE`, `OTEL_EXPORTER_OTLP_CERTIFICATE`, `OTEL_EXPORTER_OTLP_CLIENT_CERTIFICATE` and their `OTEL_EXPORTER_OTLP_TRACES_*` forms.

The system SHALL name the span of an HTTP request after the method and the pattern of its route, for example `GET /callback/{tenant_id}`. The span SHALL have that pattern as `http.route`. Callers reach every HTTP route through the gateway. Thus the span of an HTTP request SHALL start a new trace. When the request carries a trace context, the span SHALL only link to it. The system SHALL NOT read or send baggage.

When the service stops, the system SHALL export the spans that it still holds, after the listeners stopped (see `resilience`).

#### Scenario: The trace exporter cannot be created
- **WHEN** `serve` starts with `TRACING_ENABLED` and an `OTEL_GRPC_ENDPOINT` for which the service cannot create an exporter
- **THEN** the service logs the error and starts
- **AND** the service serves its requests without spans

#### Scenario: Status while a dependency is down
- **WHEN** a caller calls `GET /api/v0/status` while Kratos is down
- **THEN** the system answers 200 with the status `ok`
