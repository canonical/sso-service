## Purpose

Every management call must come from a caller that the platform knows. The caller is one of 2 kinds:

- A user. The gateway forwards the token of the user.
- A service with a client-credentials token from the Hydra of the platform.

This service verifies the token with the verifier that the other services of the platform use. The service takes the id of the caller from the token for the audit trail. The sign-in RPCs take no token. The Login UI calls them from the internal network while a user signs in.

Key decisions:

- **Authentication here, authorization at the gateway.** The gateway asks the Authorization Service of the platform about every management request. A tenant admin can act on the tenant in the path. A platform admin can act on the routes of platform admins. This service keeps no roles and no list of admins or callers. We turned down a check of roles here also. With that check, 2 places decide the same question and drift apart.
- **The service still scopes every tenant admin route to its tenant.** The resource must belong to the tenant in the path.
- **What no user is allowed to call has no HTTP route and takes no token.** `SSOSignInService` is gRPC only. The Login UI calls it from the internal network of the platform. It calls the tenant lookup and the admin API of the identity provider in the same way. That network is the only control on who calls those RPCs with which account id. We turned down a token for the Login UI. A token gives the Login UI a client, a token endpoint and that endpoint's certificate authority to manage, for calls that never leave the network.
- **The browser pages take no token.** The ticket and the binding cookie protect them instead.

Non-goals:

- Authorization of any kind.
- Mutual TLS.
- A restriction of the internal network. The deployment does that.

## ADDED Requirements

### Requirement: Every management call carries a valid access token
The system SHALL require `Authorization: Bearer <token>` on every gRPC call of `SSOTenantAdminService` and `SSOPlatformAdminService`. The system SHALL also require it on every HTTP route under `/api/v0/sso`. A token is valid only when all of these are true:

- It is a JWT signed with RS256 or ES256 by a key of `AUTHENTICATION_ISSUER`. The service finds the key by OIDC discovery, or at `AUTHENTICATION_JWKS_URL` when that setting is set.
- Its `iss` is that issuer.
- It has not expired.
- It has a subject.

The system does not check the audience.

| when | gRPC | HTTP |
|---|---|---|
| no token, or not a bearer token | `UNAUTHENTICATED` | 401 `{"status": 401, "message": "missing authorization header"}` |
| the token is not valid, or not admitted | `UNAUTHENTICATED` (`invalid token`) | 401 `{"status": 401, "message": "invalid token"}` |

The browser pages `/login`, `/callback/{connection_id}`, `/consent` and `/error` need no token. The routes `/api/v0/status`, `/api/v0/version` and `/api/v0/metrics` also need no token. The RPCs of `SSOSignInService` need no token. They exist on the gRPC listener only.

#### Scenario: A call without a token
- **WHEN** a caller calls `GET /api/v0/sso/tenants/{tenant_id}/connections` without an `Authorization` header
- **THEN** the system answers 401
- **AND** the request reaches no handler

#### Scenario: A sign-in RPC without a token
- **WHEN** a caller calls `StartAttempt` on the gRPC listener without a token
- **THEN** the service serves the call

### Requirement: Which valid tokens are admitted
The system SHALL admit a valid token when at least one of these is true:

- Its subject is in `AUTHENTICATION_ALLOWED_SUBJECTS`.
- Its `scope` (space-separated) or its `scp` (a list) contains `AUTHENTICATION_REQUIRED_SCOPE`.

When neither setting is set, the system SHALL admit every valid token. The system logs a valid token that it does not admit as an authorization failure.

With `AUTHENTICATION_ENABLED=false` the service verifies no token. The service still needs a bearer token and uses its value as the id of the caller. Use that mode for development only.

#### Scenario: Neither setting is set
- **WHEN** a caller presents a valid token of the issuer with any subject and any scope
- **THEN** the service admits the call

### Requirement: The service authorizes no caller
The system SHALL NOT decide what an admitted caller can do. The service has no roles. It does not tell a tenant admin from a platform admin or from a service. The system SHALL answer only for the resources of the tenant that a route names (see `connections`, `tenant-sso-policy`). The service uses the subject of the token for 2 things: `created_by` of a connection when the subject is a UUID, and the actor in the admin-action logs.

#### Scenario: A platform admin route called with any admitted token
- **WHEN** an admitted caller calls `DeleteAnyConnection` on the gRPC listener
- **THEN** the service does the call
- **AND** the gateway and the network must keep away the callers that are not platform admins

#### Scenario: SSOSignInService has no HTTP route
- **WHEN** a caller sends an HTTP request with a valid token
- **AND** the request goes to `POST /api/v0/sso/attempts`, or to any other path made up for an RPC of `SSOSignInService`
- **THEN** the system answers 404, because those RPCs exist on the gRPC listener only
