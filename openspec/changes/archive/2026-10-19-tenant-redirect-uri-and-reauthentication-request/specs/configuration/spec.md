## MODIFIED Requirements

### Requirement: Settings
The system SHALL read these environment variables at start:

| variable | default | meaning |
|---|---|---|
| `PUBLIC_URL` | required | the URL at which browsers reach the service: an `https` URL with no query and no fragment, on the host that serves the login UI (see `company-sign-in`). The redirect URI of the connections of a tenant is `<PUBLIC_URL>/callback/<tenant id>` |
| `PORT` | `8080` | HTTP listener |
| `GRPC_PORT` | `50051` | gRPC listener. It must differ from `PORT` |
| `DSN` | required | PostgreSQL connection string |
| `DB_MAX_CONNS`, `DB_MIN_CONNS` | `10`, `1` | size of the connection pool |
| `DB_MAX_CONN_LIFETIME`, `DB_MAX_CONN_IDLE_TIME` | `1h`, `30m` | how long the pool reuses a connection, and how long a pooled connection can stay idle |
| `HYDRA_SSO_ADMIN_URL` | required | admin API of hydra-sso |
| `KRATOS_ADMIN_URL` | required | admin API of Kratos |
| `TENANT_SERVICE_GRPC_ADDRESS` | required | the tenant service |
| `TENANT_SERVICE_GRPC_TIMEOUT` | `5s` | the longest time that one call to the tenant service can take, retries included |
| `TENANT_SERVICE_TLS_ENABLED` | `false` | TLS on the connection to the tenant service |
| `AUTHENTICATION_ENABLED` | `true` | verify callers' tokens |
| `AUTHENTICATION_ISSUER` | required when authentication is enabled | the issuer of callers' tokens |
| `AUTHENTICATION_JWKS_URL` | none | where the issuer's keys are, instead of discovery |
| `AUTHENTICATION_ALLOWED_SUBJECTS` | none | comma-separated subjects that the service admits |
| `AUTHENTICATION_REQUIRED_SCOPE` | none | a scope that admits a token |
| `SERVICE_TOKEN_URL`, `SERVICE_CLIENT_ID`, `SERVICE_CLIENT_SECRET` | required | the token endpoint and the client with which the service gets its own access token, for calls to the tenant service |
| `SERVICE_TOKEN_SCOPES` | none | space-separated scopes to ask for with that token |
| `ENVELOPE_KEY` | required | the encryption key, 32 bytes in base64 (see `secrets`) |
| `DEV` | `false` | allow identity providers over plain `http` and at non-public addresses, and a `PUBLIC_URL` over plain `http` |
| `LOG_LEVEL` | `error` | the level of the service's logs (see `observability`) |
| `TRACING_ENABLED` | `true` | OpenTelemetry tracing. When the service cannot create the exporter of the traces, the service logs the error and runs without tracing (see `observability`) |
| `OTEL_GRPC_ENDPOINT`, `OTEL_HTTP_ENDPOINT` | none | where the service sends traces |
| `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY` | none | the proxy for the requests of the service, and the hosts that the service reaches without it. They apply to all the requests: those to Kratos, hydra-sso and the tenant service too. Thus `NO_PROXY` must name the hosts of those services (see `identity-provider-requests`) |

The system SHALL refuse to start when one of these conditions is true:

- A required variable is missing.
- A URL, a duration, a number or a boolean does not parse.
- `PUBLIC_URL` is not `https` while `DEV` is off.
- `PUBLIC_URL` has a query or a fragment.
- `GRPC_PORT` equals `PORT`.
- The envelope key is not usable.
- The system cannot reach the database.
- Authentication is enabled, `AUTHENTICATION_JWKS_URL` is not set, and the system cannot read the issuer's discovery document.

A database or an issuer that does not answer also stops the start, within the bounds of `resilience`. It does not hold the start.

#### Scenario: A required setting is missing
- **WHEN** `serve` starts without `PUBLIC_URL`, or without `ENVELOPE_KEY`
- **THEN** `serve` exits with an error that names the problem

#### Scenario: A public URL over plain http
- **WHEN** `serve` starts with `PUBLIC_URL=http://sso.example` and `DEV` off
- **THEN** `serve` exits with an error that names `PUBLIC_URL`
- **AND** with such a URL, browsers do not return the binding cookie and no sign-in can finish

#### Scenario: Authentication disabled
- **WHEN** `AUTHENTICATION_ENABLED` is `false` and `AUTHENTICATION_ISSUER` is not set
- **THEN** the system accepts the settings
