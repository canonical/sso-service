## MODIFIED Requirements

### Requirement: Create a connection
On `CreateConnection` (`POST /api/v0/sso/tenants/{tenant_id}/connections`), the system SHALL create a connection for the tenant in the path from these fields:

- `label` (1 to 100 characters)
- `issuer` (1 to 2048)
- `client_id` (1 to 512)
- `client_secret` (1 to 4096)

The connection gets these values:

- a UUIDv7 id
- the status `CONNECTION_STATUS_DRAFT`
- the tenant as `owner_tenant_id`
- the caller's token subject as `created_by`, when that subject is a UUID (empty for a service client)

The system does not contact the identity provider.

After request validation, the system SHALL check for these cases in this order and SHALL refuse the request in each case:

| when | gRPC code (HTTP) | message |
|---|---|---|
| the tenant service does not know the tenant | `NOT_FOUND` (404) | `no such tenant` |
| the tenant is a personal tenant | `FAILED_PRECONDITION` (400) | `PERSONAL_TENANT: …` |
| the issuer is not an absolute `https` URL, or has userinfo, a query or a fragment, also an empty one (`?` or `#`) | `INVALID_ARGUMENT` (400) | `invalid issuer: …` |
| the tenant already owns 5 connections | `FAILED_PRECONDITION` (400) | `CONNECTION_LIMIT: a tenant owns at most 5 connections` |

The limit is part of the statement that inserts the row, and the system does not serialise creates. Thus creates of one tenant that run at the same moment can pass the limit together.

#### Scenario: A draft connection is created
- **WHEN** a tenant admin creates a connection with a label, an `https` issuer, a client id and a client secret
- **THEN** the system returns the connection as a draft that the tenant owns
- **AND** the answer has the connection's `redirect_uri` and does not have the client secret

#### Scenario: A sixth connection is refused
- **WHEN** a tenant that owns 5 connections creates another connection
- **THEN** the system answers `FAILED_PRECONDITION` with the reason `CONNECTION_LIMIT`
- **AND** another tenant can still create its own connection
