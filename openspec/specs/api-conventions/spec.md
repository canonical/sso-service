# api-conventions Specification

## Purpose

The service has two listeners and three kinds of caller. This capability says which interface is meant for whom, and what every API call has in common: how requests are validated and how errors look, so that callers can branch on a refusal.

Key decisions:

- **gRPC is the API; HTTP is a gateway onto part of it.** The two admin services are also reachable over HTTP/JSON, generated from the same definitions, because admins' tools and the gateway speak HTTP. Calls between services are gRPC.
- **A refusal a caller may branch on has a reason**, a stable machine-readable word in upper case. It is carried twice, because the HTTP error body has no place for details: as a `google.rpc.ErrorInfo` on the gRPC status, and as the start of the message.
- **The HTTP error body is the platform's**: `{"status", "message"}`, as in the platform's other services.

Non-goals: API versioning beyond the `v0` prefix; idempotency keys or version checks on writes.

## Requirements
### Requirement: Two listeners and who they are for
The system SHALL serve:

| listener | serves | meant for |
|---|---|---|
| gRPC (`GRPC_PORT`) | `SSOSignInService`, `SSOTenantAdminService`, `SSOPlatformAdminService` | the platform's services, on the internal network; `SSOSignInService` is the login UI's |
| HTTP (`PORT`) | the HTTP/JSON gateway for `SSOTenantAdminService` and `SSOPlatformAdminService` | tenant admins and platform admins, through the gateway that authorizes them |
| HTTP (`PORT`) | the browser pages `/login`, `/callback/{connection_id}`, `/consent`, `/error` | users' browsers, from the public ingress |
| HTTP (`PORT`) | `/api/v0/status`, `/api/v0/version`, `/api/v0/metrics` | the platform's monitoring |

The HTTP routes of the two admin services are:

| RPC | route |
|---|---|
| `ListConnections`, `CreateConnection` | `GET`, `POST /api/v0/sso/tenants/{tenant_id}/connections` |
| `GetConnection`, `UpdateConnection`, `DeleteConnection` | `GET`, `PATCH`, `DELETE /api/v0/sso/tenants/{tenant_id}/connections/{connection_id}` |
| `StartTestLogin` | `POST /api/v0/sso/tenants/{tenant_id}/connections/{connection_id}/test-logins` |
| `GetTenantSSOPolicy`, `PutTenantSSOPolicy` | `GET`, `PUT /api/v0/sso/tenants/{tenant_id}/policy` |
| `ListAllConnections` | `GET /api/v0/sso/connections` |
| `GetTenantDomains`, `SetTenantDomains` | `GET`, `PUT /api/v0/sso/tenants/{tenant_id}/domains` |
| `DeleteAnyConnection` | `DELETE /api/v0/sso/connections/{connection_id}` |

The routes under `/api/v0/sso` answer cross-origin requests from any origin (`Access-Control-Allow-Origin: *`) and never allow credentials; a caller's token travels in the `Authorization` header. The browser pages, and the status, version and metrics routes, send no CORS headers.

#### Scenario: The same call on both transports
- **WHEN** `GetConnection` is called over gRPC and over its HTTP route with the same tenant and connection
- **THEN** both return the same connection

### Requirement: Request validation and JSON
The system SHALL validate every request against the constraints of the API definition (ids are UUIDs, lengths, list sizes, allowed enum values) before it does anything else, and answer `INVALID_ARGUMENT` (`invalid request: …`) when one is broken. Ids are compared in lower case. A request body or gRPC message over 1 MiB is refused. Over HTTP, JSON fields have the API's snake_case names, fields with no value are written out, and a request body with a field the API does not define is refused with HTTP 400: a mistyped field is never dropped silently. The body of `UpdateConnection` is the connection's fields to change, not the whole request (see `connections`).

#### Scenario: A tenant id that is not a UUID
- **WHEN** a route is called with the tenant id `acme`
- **THEN** the system answers `INVALID_ARGUMENT` (HTTP 400)

#### Scenario: A mistyped field in a policy write
- **WHEN** `PUT /api/v0/sso/tenants/{tenant_id}/policy` is sent with `binding` in place of `bindings`
- **THEN** the system answers HTTP 400 and the tenant's bindings are unchanged

### Requirement: Errors and reasons
The system SHALL answer a failed call with a gRPC status. A refusal with a reason SHALL carry it as a `google.rpc.ErrorInfo` detail (domain `sso-service`) and as the start of the message, `<REASON>: <text>`. Over HTTP every error SHALL be `{"status": <HTTP status>, "message": "<message>"}`, including those of the authentication step and of unknown routes under `/api/v0/sso`.

| reason | gRPC code | HTTP | when |
|---|---|---|---|
| `CONNECTION_NOT_FOUND` | `NOT_FOUND` | 404 | a connection that does not exist or is not the path tenant's; binding one the tenant does not own |
| `CONNECTION_NOT_TESTED` | `FAILED_PRECONDITION` | 400 | an active binding to a draft connection |
| `CONNECTION_LIMIT` | `FAILED_PRECONDITION` | 400 | a sixth connection for a tenant |
| `PERSONAL_TENANT` | `FAILED_PRECONDITION` | 400 | a connection or a policy for a personal tenant |
| `REQUIRED_NEEDS_ACTIVE_BINDING` | `FAILED_PRECONDITION` | 400 | a policy write or a delete would leave a `REQUIRED` tenant with no active binding |
| `AUTO_JOIN_NEEDS_REQUIRED_AND_DOMAINS` | `FAILED_PRECONDITION` | 400 | auto-join without `REQUIRED` or without domains |
| `IDP_CHECK_FAILED` | `FAILED_PRECONDITION` | 400 | the check that starts a test sign-in failed |
| `NOT_APPLICABLE` | `FAILED_PRECONDITION` | gRPC only | `StartAttempt` for a connection that is not tested; `CompleteAttempt` that does not match |
| `LAST_CREDENTIAL` | `FAILED_PRECONDITION` | gRPC only | `DeleteLink` would leave the account no way in of its own |

Failures without a reason:

| gRPC code | HTTP | when |
|---|---|---|
| `INVALID_ARGUMENT` | 400 | a request that fails validation |
| `UNAUTHENTICATED` | 401 | no valid token (see `caller-authentication`) |
| `NOT_FOUND` | 404 | no such tenant; no such account; no link at the connection; an unknown route |
| `ABORTED` | 409 | another request holds the resource; the caller may try again at once |
| `UNAVAILABLE` | 503 | the tenant service, Kratos or the database did not answer in time; the caller may try again later |
| `CANCELED` | 499 | the caller went away |
| `INTERNAL` | 500 | anything else; the message is `internal error` and the cause is in the log |

#### Scenario: One refusal on both transports
- **WHEN** a connection that does not exist is read
- **THEN** over HTTP the system answers 404 with `{"status": 404, "message": "CONNECTION_NOT_FOUND: no such connection"}`
- **AND** over gRPC the status is `NOT_FOUND` with the same message and an `ErrorInfo` with the reason `CONNECTION_NOT_FOUND` and the domain `sso-service`

