## Why

A tenant must be able to offer, or require, sign-in through its own OpenID Connect identity provider. Kratos reads its OIDC providers from static configuration, so a provider per customer would mean a Kratos configuration change for every customer.

## What Changes

A new service, the SSO service:

- Stores each tenant's **connections** (issuer, client id, encrypted client secret) in PostgreSQL (`sso_connections`).
- Is the login and consent provider of a second Hydra, `hydra-sso`. Kratos has one provider, `byo-sso`, pointing at it; the service sends each sign-in to the right identity provider and checks the answer.
- Serves three gRPC services of `identity-platform-api` `v0/sso`: `SSOSignInService` (the login UI, gRPC only), `SSOTenantAdminService` and `SSOPlatformAdminService` (both also behind an HTTP gateway).
- Passes a tenant's SSO policy on to the tenant service, after the checks that need connection data.

## Capabilities

### New Capabilities
- `connections`: a tenant's connections.
- `test-sign-in`: the test that makes a connection tested.
- `tenant-sso-policy`: bindings, enforcement, auto-join.
- `platform-administration`: all connections, reading and setting domains, deleting any connection.
- `sign-in-attempts`: options, tickets, `CompleteAttempt`.
- `company-sign-in`: the browser pages `/login`, `/callback/{connection_id}`, `/consent`, `/error`.
- `links`: listing and removing an account's links.
- `identity-provider-requests`: outbound-request guard, discovery, code exchange, id_token checks.
- `secrets`: encrypted client secrets, sealed tokens.
- `caller-authentication`: JWT authentication of callers.
- `api-conventions`: listeners, validation, errors.
- `configuration`: commands and settings.
- `resilience`: deadlines, retries, degraded modes, shutdown.
- `observability`: metrics and logs.

### Modified Capabilities
<!-- None -->

## Non-goals

- SAML, or any protocol other than OIDC.
- A connection shared by several tenants.
- Writing links, accounts or memberships: Kratos and the tenant service do.
- Authorizing callers: the gateway does.
- Storing the SSO policy, or proving that a tenant owns a domain.
- A per-connection circuit breaker; rate limiting inside the service.
- Tooling that re-encrypts stored secrets under a new key.
- Stored sign-in attempts or test results; session revocation; MFA.

## Impact

- **Affected Packages** (all new): `cmd`, `migrations`, `pkg/admin`, `pkg/sso`, `pkg/bridge`, `pkg/web`, `pkg/authentication`, `pkg/status`, `pkg/metrics`, and under `internal/`: `config`, `limits`, `db`, `storage`, `types`, `secrets`, `idp`, `hydra`, `kratos`, `tenants`, `links`, `apierrors`, `grpcutil`, `logging`, `monitoring`, `tracing`.
- **Dependencies**: `identity-platform-api`, the Ory Kratos and Hydra SDKs, `go-oidc`, `x/oauth2`.
- **Infrastructure**: PostgreSQL, `hydra-sso`, Kratos's admin API, the tenant service (gRPC), a client at the platform's Hydra, gateway routes under `/api/v0/sso`.
- **Database**: migration `001_initial_schema.sql` creates `sso_connections`.
