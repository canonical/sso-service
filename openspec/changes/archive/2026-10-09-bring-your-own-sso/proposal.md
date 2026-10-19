## Why

A tenant must be able to offer, or require, sign-in through its own OpenID Connect identity provider. Kratos reads its OIDC providers from static configuration. Thus a provider per customer needs a change of the Kratos configuration for every customer.

## What Changes

This change adds a new service, the SSO service. The SSO service does these things:

- It stores the **connections** of each tenant (issuer, client id, encrypted client secret) in PostgreSQL (`sso_connections`).
- It is the login and consent provider of a second Hydra, `hydra-sso`. Kratos has one provider, `byo-sso`, that points at `hydra-sso`. The service sends each sign-in to the right identity provider and checks the answer.
- It serves 3 gRPC services of `identity-platform-api` `v0/sso`: `SSOSignInService` (the login UI, gRPC only), `SSOTenantAdminService` and `SSOPlatformAdminService` (both also behind an HTTP gateway).
- It passes the SSO policy of a tenant on to the tenant service, after the checks that need connection data.

## Capabilities

### New Capabilities
- `connections`: a tenant's connections.
- `test-sign-in`: the test that makes a connection tested.
- `tenant-sso-policy`: bindings, enforcement, auto-join.
- `platform-administration`: all connections, how to read and set domains, how to delete any connection.
- `sign-in-attempts`: options, tickets, `CompleteAttempt`.
- `company-sign-in`: the browser pages `/login`, `/callback/{connection_id}`, `/consent`, `/error`.
- `links`: how to list and remove the links of an account.
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
- The write of links, accounts or memberships. Kratos and the tenant service write them.
- The authorization of callers. The gateway authorizes them.
- The storage of the SSO policy, or proof that a tenant owns a domain.
- A circuit breaker per connection, and rate limiting inside the service.
- Tooling that re-encrypts stored secrets under a new key.
- Stored sign-in attempts or test results, session revocation, and MFA.

## Impact

- **Affected Packages** (all new): `cmd`, `migrations`, `pkg/admin`, `pkg/sso`, `pkg/bridge`, `pkg/web`, `pkg/authentication`, `pkg/status`, `pkg/metrics`, and under `internal/`: `config`, `limits`, `db`, `storage`, `types`, `secrets`, `idp`, `hydra`, `kratos`, `tenants`, `links`, `apierrors`, `grpcutil`, `logging`, `monitoring`, `tracing`.
- **Dependencies**: `identity-platform-api`, the Ory Kratos and Hydra SDKs, `go-oidc`, `x/oauth2`.
- **Infrastructure**: PostgreSQL, `hydra-sso`, Kratos's admin API, the tenant service (gRPC), a client at the platform's Hydra, gateway routes under `/api/v0/sso`.
- **Database**: migration `001_initial_schema.sql` creates `sso_connections`.
