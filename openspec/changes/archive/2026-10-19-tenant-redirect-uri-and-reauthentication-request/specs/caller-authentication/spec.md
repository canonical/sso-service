## MODIFIED Requirements

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

The browser pages `/login`, `/callback/{tenant_id}`, `/consent` and `/error` need no token. The routes `/api/v0/status`, `/api/v0/version` and `/api/v0/metrics` also need no token. The RPCs of `SSOSignInService` need no token. They exist on the gRPC listener only.

#### Scenario: A call without a token
- **WHEN** a caller calls `GET /api/v0/sso/tenants/{tenant_id}/connections` without an `Authorization` header
- **THEN** the system answers 401
- **AND** the request reaches no handler

#### Scenario: A sign-in RPC without a token
- **WHEN** a caller calls `StartAttempt` on the gRPC listener without a token
- **THEN** the service serves the call
