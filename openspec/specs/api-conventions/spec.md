# api-conventions Specification

## Purpose

The service has 2 listeners and 3 kinds of caller. This capability says which interface is for which caller. It also says what every API call has in common: how the service validates requests and how errors look. Thus callers can branch on a refusal.

Key decisions:

- **gRPC is the API, and HTTP is a gateway onto part of it.** The 2 admin services are also reachable over HTTP/JSON. The reason is that admins' tools and the gateway speak HTTP. The HTTP/JSON interface is generated from the same definitions. Calls between services are gRPC.
- **A refusal that a caller can branch on has a reason.** A reason is a stable machine-readable word in upper case. A refusal carries the reason twice, because the HTTP error body has no place for details. It carries the reason as a `google.rpc.ErrorInfo` on the gRPC status, and as the start of the message.
- **The HTTP error body is the platform's**: `{"status", "message"}`, as in the platform's other services.

Non-goals:

- API versioning beyond the `v0` prefix.
- Idempotency keys or version checks on writes.

## Requirements
### Requirement: Two listeners and who they are for
The system SHALL serve these interfaces:

| listener | serves | meant for |
|---|---|---|
| gRPC (`GRPC_PORT`) | `SSOSignInService`, `SSOTenantAdminService`, `SSOPlatformAdminService` | the platform's services, on the internal network. `SSOSignInService` is for the login UI |
| HTTP (`PORT`) | the HTTP/JSON gateway for `SSOTenantAdminService` and `SSOPlatformAdminService` | tenant admins and platform admins, through the gateway that authorizes them |
| HTTP (`PORT`) | the browser pages `/login`, `/callback/{connection_id}`, `/consent`, `/error` | users' browsers, from the public ingress |
| HTTP (`PORT`) | `/api/v0/status`, `/api/v0/version`, `/api/v0/metrics` | the platform's monitoring |

The 2 admin services have these HTTP routes:

| RPC | route |
|---|---|
| `ListConnections`, `CreateConnection` | `GET`, `POST /api/v0/sso/tenants/{tenant_id}/connections` |
| `GetConnection`, `UpdateConnection`, `DeleteConnection` | `GET`, `PATCH`, `DELETE /api/v0/sso/tenants/{tenant_id}/connections/{connection_id}` |
| `StartTestLogin` | `POST /api/v0/sso/tenants/{tenant_id}/connections/{connection_id}/test-logins` |
| `GetTenantSSOPolicy`, `PutTenantSSOPolicy` | `GET`, `PUT /api/v0/sso/tenants/{tenant_id}/policy` |
| `ListAllConnections` | `GET /api/v0/sso/connections` |
| `GetTenantDomains`, `SetTenantDomains` | `GET`, `PUT /api/v0/sso/tenants/{tenant_id}/domains` |
| `DeleteAnyConnection` | `DELETE /api/v0/sso/connections/{connection_id}` |

The routes under `/api/v0/sso` answer cross-origin requests from any origin (`Access-Control-Allow-Origin: *`) and never allow credentials. A caller's token travels in the `Authorization` header. The browser pages, and the status, version and metrics routes, send no CORS headers.

#### Scenario: The same call on both transports
- **WHEN** a caller calls `GetConnection` over gRPC and over its HTTP route with the same tenant and connection
- **THEN** both calls return the same connection

### Requirement: Request validation and JSON
The system SHALL validate every request against the constraints of the API definition (ids are UUIDs, lengths, list sizes, allowed enum values) before it does anything else. When a request breaks a constraint, the system SHALL answer `INVALID_ARGUMENT` (`invalid request: …`). The system compares ids in lower case. The system refuses a request body or a gRPC message over 1 MiB.

Over HTTP, these rules apply:

- JSON fields have the API's snake_case names.
- The system writes out fields with no value.
- The system refuses with HTTP 400 a request body with a field that the API does not define. Thus the system never drops a mistyped field silently.

The body of `UpdateConnection` holds the connection's fields to change, not the whole request (see `connections`).

#### Scenario: A tenant id that is not a UUID
- **WHEN** a caller calls a route with the tenant id `acme`
- **THEN** the system answers `INVALID_ARGUMENT` (HTTP 400)

#### Scenario: A mistyped field in a policy write
- **WHEN** a caller sends `PUT /api/v0/sso/tenants/{tenant_id}/policy` with `binding` in place of `bindings`
- **THEN** the system answers HTTP 400 and the tenant's bindings do not change

### Requirement: Errors and reasons
The system SHALL answer a failed call with a gRPC status. A refusal with a reason SHALL carry the reason as a `google.rpc.ErrorInfo` detail (domain `sso-service`). The refusal SHALL also carry the reason as the start of the message, `<REASON>: <text>`. Over HTTP every error SHALL be `{"status": <HTTP status>, "message": "<message>"}`. This includes the errors of the authentication step and of unknown routes under `/api/v0/sso`.

| reason | gRPC code | HTTP | when |
|---|---|---|---|
| `CONNECTION_NOT_FOUND` | `NOT_FOUND` | 404 | a connection that does not exist or is not a connection of the tenant in the path. Also a binding to a connection that the tenant does not own |
| `CONNECTION_NOT_TESTED` | `FAILED_PRECONDITION` | 400 | an active binding to a draft connection |
| `CONNECTION_LIMIT` | `FAILED_PRECONDITION` | 400 | a sixth connection for a tenant |
| `PERSONAL_TENANT` | `FAILED_PRECONDITION` | 400 | a connection or a policy for a personal tenant |
| `REQUIRED_NEEDS_ACTIVE_BINDING` | `FAILED_PRECONDITION` | 400 | a policy write or a delete that will leave a `REQUIRED` tenant with no active binding |
| `AUTO_JOIN_NEEDS_REQUIRED_AND_DOMAINS` | `FAILED_PRECONDITION` | 400 | auto-join without `REQUIRED` or without domains |
| `IDP_CHECK_FAILED` | `FAILED_PRECONDITION` | 400 | the check that starts a test sign-in failed |
| `NOT_APPLICABLE` | `FAILED_PRECONDITION` | gRPC only | `StartAttempt` for a connection that is not tested. Also a `CompleteAttempt` that does not match |
| `LAST_CREDENTIAL` | `FAILED_PRECONDITION` | gRPC only | a `DeleteLink` that will leave the account with no way in of its own |

These failures have no reason:

| gRPC code | HTTP | when |
|---|---|---|
| `INVALID_ARGUMENT` | 400 | a request that fails validation |
| `UNAUTHENTICATED` | 401 | no valid token (see `caller-authentication`) |
| `NOT_FOUND` | 404 | no such tenant, no such account, no link at the connection, or an unknown route |
| `ABORTED` | 409 | another request holds the resource. The caller can try again at once |
| `UNAVAILABLE` | 503 | the tenant service, Kratos or the database did not answer in time. The caller can try again later |
| `CANCELED` | 499 | the caller went away |
| `INTERNAL` | 500 | anything else. The message is `internal error` and the cause is in the log |

#### Scenario: One refusal on both transports
- **WHEN** a caller reads a connection that does not exist
- **THEN** over HTTP the system answers 404 with `{"status": 404, "message": "CONNECTION_NOT_FOUND: no such connection"}`
- **AND** over gRPC the status is `NOT_FOUND` with the same message
- **AND** the gRPC status has an `ErrorInfo` with the reason `CONNECTION_NOT_FOUND` and the domain `sso-service`

