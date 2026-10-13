## Purpose

Every management call must come from a caller the platform knows: a user whose token the gateway forwards, or a service with a client-credentials token from the platform's Hydra. This service verifies that token, with the verifier the platform's other services use, and takes the caller's id from it for the audit trail. The sign-in RPCs, which the login UI calls from the internal network while a user signs in, take no token.

Key decisions:

- **Authentication here, authorization at the gateway.** The gateway asks the platform's Authorization Service about every management request: a tenant admin may act on the tenant in the path, a platform admin on the platform admins' routes. This service keeps no roles and no list of admins or callers. Checking roles here as well was turned down: two places would decide the same question and drift apart.
- **The service still scopes every tenant admin route to its tenant**: the resource must belong to the tenant in the path.
- **What no user may call has no HTTP route, and takes no token.** `SSOSignInService` is gRPC only. The login UI calls it from the platform's internal network, as it calls the tenant lookup and the identity provider's admin API, and that network is the only control on who calls those RPCs with which account id. A token for the login UI was turned down: it would give the login UI a client, a token endpoint and that endpoint's certificate authority to manage, for calls that never leave the network.
- **The browser pages take no token**; the ticket and the binding cookie protect them instead.

Non-goals: authorization of any kind; mutual TLS; restricting the internal network (the deployment's).

## ADDED Requirements

### Requirement: Every management call carries a valid access token
The system SHALL require `Authorization: Bearer <token>` on every gRPC call of `SSOTenantAdminService` and `SSOPlatformAdminService`, and on every HTTP route under `/api/v0/sso`. A token is valid when it is a JWT signed with RS256 or ES256 by a key of `AUTHENTICATION_ISSUER` (found by OIDC discovery, or at `AUTHENTICATION_JWKS_URL` when that is set), its `iss` is that issuer, it has not expired and it has a subject. The audience is not checked.

| when | gRPC | HTTP |
|---|---|---|
| no token, or not a bearer token | `UNAUTHENTICATED` | 401 `{"status": 401, "message": "missing authorization header"}` |
| the token is not valid, or not admitted | `UNAUTHENTICATED` (`invalid token`) | 401 `{"status": 401, "message": "invalid token"}` |

The browser pages `/login`, `/callback/{connection_id}`, `/consent` and `/error`, and `/api/v0/status`, `/api/v0/version` and `/api/v0/metrics`, need no token. Neither do the RPCs of `SSOSignInService`, which exist on the gRPC listener only.

#### Scenario: A call without a token
- **WHEN** `GET /api/v0/sso/tenants/{tenant_id}/connections` is called without an `Authorization` header
- **THEN** the system answers 401 and the request reaches no handler

#### Scenario: A sign-in RPC without a token
- **WHEN** `StartAttempt` is called on the gRPC listener without a token
- **THEN** the call is served

### Requirement: Which valid tokens are admitted
The system SHALL admit a valid token when its subject is listed in `AUTHENTICATION_ALLOWED_SUBJECTS`, or when its `scope` (space-separated) or `scp` (a list) contains `AUTHENTICATION_REQUIRED_SCOPE`. When neither setting is set, every valid token is admitted. A valid token that is not admitted is logged as an authorization failure.

With `AUTHENTICATION_ENABLED=false` no token is verified: a bearer token is still required and its value is taken as the caller's id. That mode is for development only.

#### Scenario: Neither setting is set
- **WHEN** a caller presents a valid token of the issuer, with any subject and any scope
- **THEN** the call is admitted

### Requirement: The service authorizes no caller
The system SHALL NOT decide what an admitted caller may do: it has no roles and does not tell a tenant admin from a platform admin or from a service. It SHALL only answer for resources of the tenant a route names (see `connections`, `tenant-sso-policy`). The token's subject is used for two things: `created_by` of a connection, when the subject is a UUID, and the actor in admin-action logs.

#### Scenario: A platform admin route called with any admitted token
- **WHEN** an admitted caller calls `DeleteAnyConnection` on the gRPC listener
- **THEN** the service carries it out; keeping callers who are not platform admins away is the gateway's and the network's task

#### Scenario: SSOSignInService has no HTTP route
- **WHEN** an HTTP request with a valid token is sent to `POST /api/v0/sso/attempts`, or to any other path made up for an RPC of `SSOSignInService`
- **THEN** the system answers 404: those RPCs exist on the gRPC listener only
