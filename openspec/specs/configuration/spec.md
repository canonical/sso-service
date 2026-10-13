# configuration Specification

## Purpose

How the service is started and what an operator can set. The service is one binary with a command to serve and a command to migrate its database, configured by environment variables only, like the platform's other services.

Key decisions:

- **Migrations are a separate step.** `serve` does not migrate; it refuses to start while a migration is pending. A deployment runs `migrate` first, so that replicas starting together never race to change the schema.
- **Settings are checked at start.** A missing or malformed setting stops the service before it listens, instead of failing the first request that needs it.
- **Limits and timeouts are fixed in the code**, not settings (see `resilience`): the only timeout an operator sets is the one for calls to the tenant service.

Non-goals: configuration files or flags for `serve`; reloading settings without a restart; a command that re-encrypts client secrets.

## Requirements
### Requirement: Commands
The system SHALL provide these commands:

| command | effect |
|---|---|
| `serve` | starts the HTTP and gRPC listeners |
| `migrate --dsn <dsn> [up \| down [<version>] \| status \| check]` | applies every pending migration (`up`, the default), rolls back the last one or down to a version (`down`), lists the migrations (`status`), or reports whether one is pending (`check`); `--format json` prints the result as JSON |
| `version` | prints the service's version |

The first migration creates the table `sso_connections`; rolling it back drops the table.

#### Scenario: Serving on a database that was not migrated
- **WHEN** `serve` is started against a database with a pending migration
- **THEN** it exits with an error that says to run `migrate` first, and opens no listener

### Requirement: Settings
The system SHALL read these environment variables at start:

| variable | default | meaning |
|---|---|---|
| `PUBLIC_URL` | required | the URL browsers reach the service at: `https`, with no query and no fragment, on the host that serves the login UI (see `company-sign-in`); a connection's redirect URI is `<PUBLIC_URL>/callback/<connection id>` |
| `PORT` | `8080` | HTTP listener |
| `GRPC_PORT` | `50051` | gRPC listener; must differ from `PORT` |
| `DSN` | required | PostgreSQL connection string |
| `DB_MAX_CONNS`, `DB_MIN_CONNS` | `10`, `1` | size of the connection pool |
| `DB_MAX_CONN_LIFETIME`, `DB_MAX_CONN_IDLE_TIME` | `1h`, `30m` | how long a pooled connection is reused, and may stay idle |
| `HYDRA_SSO_ADMIN_URL` | required | admin API of hydra-sso |
| `KRATOS_ADMIN_URL` | required | admin API of Kratos |
| `TENANT_SERVICE_GRPC_ADDRESS` | required | the tenant service |
| `TENANT_SERVICE_GRPC_TIMEOUT` | `5s` | the most one call to the tenant service may take, retries included |
| `TENANT_SERVICE_TLS_ENABLED` | `false` | TLS on the connection to the tenant service |
| `AUTHENTICATION_ENABLED` | `true` | verify callers' tokens |
| `AUTHENTICATION_ISSUER` | required when authentication is enabled | the issuer of callers' tokens |
| `AUTHENTICATION_JWKS_URL` | none | where the issuer's keys are, instead of discovery |
| `AUTHENTICATION_ALLOWED_SUBJECTS` | none | comma-separated subjects that are admitted |
| `AUTHENTICATION_REQUIRED_SCOPE` | none | a scope that admits a token |
| `SERVICE_TOKEN_URL`, `SERVICE_CLIENT_ID`, `SERVICE_CLIENT_SECRET` | required | the token endpoint and client the service gets its own access token with, for calls to the tenant service |
| `SERVICE_TOKEN_SCOPES` | none | space-separated scopes to ask for with that token |
| `ENVELOPE_KEY` | required | the encryption key, 32 bytes in base64 (see `secrets`) |
| `DEV` | `false` | allow identity providers over plain `http` and at non-public addresses, and a `PUBLIC_URL` over plain `http` |
| `LOG_LEVEL` | `error` | the level of the service's logs (see `observability`) |
| `TRACING_ENABLED` | `true` | OpenTelemetry tracing; when its exporter cannot be created the service logs the error and runs without tracing (see `observability`) |
| `OTEL_GRPC_ENDPOINT`, `OTEL_HTTP_ENDPOINT` | none | where traces are sent |

The system SHALL refuse to start when a required variable is missing, a URL, duration, number or boolean does not parse, `PUBLIC_URL` is not `https` while `DEV` is off or has a query or a fragment, `GRPC_PORT` equals `PORT`, the envelope key is not usable, the database cannot be reached, or, with authentication enabled and no `AUTHENTICATION_JWKS_URL`, the issuer's discovery document cannot be read. A database or an issuer that does not answer stops the start too, within the bounds of `resilience`, instead of holding it.

#### Scenario: A required setting is missing
- **WHEN** `serve` is started without `PUBLIC_URL`, or without `ENVELOPE_KEY`
- **THEN** it exits with an error that names the problem

#### Scenario: A public URL over plain http
- **WHEN** `serve` is started with `PUBLIC_URL=http://sso.example` and `DEV` off
- **THEN** it exits with an error that names `PUBLIC_URL`: browsers would not return the binding cookie, and no sign-in could finish

#### Scenario: Authentication disabled
- **WHEN** `AUTHENTICATION_ENABLED` is `false` and `AUTHENTICATION_ISSUER` is not set
- **THEN** the settings are accepted

