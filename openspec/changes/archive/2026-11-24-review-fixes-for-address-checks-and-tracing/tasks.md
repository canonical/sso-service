## 1. Identity provider requests

- [x] 1.1 Refuse an address with an IPv6 zone and 4 more ranges in `internal/idp/safehttp.go`.
- [x] 1.2 Connect to a proxy's address without the check only through that proxy, in `internal/idp/safehttp.go`.
- [x] 1.3 Read the response body in the guard, so that a failed read keeps its kind, in `internal/idp/safehttp.go`.
- [x] 1.4 Send no trace context to an identity provider, in `internal/idp/safehttp.go`.
- [x] 1.5 Sort keys that the service's own rules refuse as misconfigured, in `internal/idp/client.go`.
- [x] 1.6 Refuse an issuer with an empty query or fragment, in `internal/idp/client.go`.

## 2. hydra-sso

- [x] 2.1 Read a 401 `request_unauthorized` for a challenge as an unknown or expired challenge, in `internal/hydra/client.go`.

## 3. Tenant service

- [x] 3.1 Share one token fetch between calls, and stop each call at its deadline, in `internal/tenants/grpc.go`.

## 4. Configuration

- [x] 4.1 Validate `DB_MAX_CONNS`, `DB_MIN_CONNS` and `TENANT_SERVICE_GRPC_TIMEOUT` in `internal/config/specs.go`.

## 5. Tracing

- [x] 5.1 Log a warning and run without tracing when no OTLP endpoint is set, in `internal/tracing/tracer.go`.
- [x] 5.2 Export in plaintext only when no `OTEL_EXPORTER_OTLP_*` TLS setting is set, in `internal/tracing/tracer.go`.
- [x] 5.3 Remove the baggage propagator in `internal/tracing/tracer.go`.
- [x] 5.4 Name server spans after the route, and link the caller's trace context, in `internal/tracing/middleware.go`.
- [x] 5.5 Export the buffered spans when the service stops, in `internal/tracing/tracer.go` and `cmd/serve.go`.

## 6. Tests

- [x] 6.1 Test the zone, the new ranges, the proxy's address and the body limit in `internal/idp/safehttp_test.go`.
- [x] 6.2 Test the issuer, the key endpoint over `http` and the body limit in `internal/idp/client_test.go`.
- [x] 6.3 Test the expired challenge in `internal/hydra/client_test.go`.
- [x] 6.4 Test the shared token fetch and the deadline in `internal/tenants/grpc_test.go`.
- [x] 6.5 Test the span name and `http.route` in `internal/tracing/middleware_test.go`.
