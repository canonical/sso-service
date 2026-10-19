## MODIFIED Requirements

### Requirement: Two listeners and who they are for
The system SHALL serve these interfaces:

| listener | serves | meant for |
|---|---|---|
| gRPC (`GRPC_PORT`) | `SSOSignInService`, `SSOTenantAdminService`, `SSOPlatformAdminService` | the platform's services, on the internal network. `SSOSignInService` is for the login UI |
| HTTP (`PORT`) | the HTTP/JSON gateway for `SSOTenantAdminService` and `SSOPlatformAdminService` | tenant admins and platform admins, through the gateway that authorizes them |
| HTTP (`PORT`) | the browser pages `/login`, `/callback/{tenant_id}`, `/consent`, `/error` | users' browsers, from the public ingress |
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
