## MODIFIED Requirements

### Requirement: Read a tenant's connections
The system SHALL return a connection of the tenant in the path on `GetConnection` (`GET …/connections/{connection_id}`). The system SHALL list the tenant's connections on `ListConnections` (`GET …/connections`). The system returns a connection with these fields:

- `id`
- `owner_tenant_id`
- `label`
- `issuer`
- `client_id`
- `status` (`CONNECTION_STATUS_DRAFT` or `CONNECTION_STATUS_TESTED`)
- `created_by`
- `create_time`
- `update_time`
- `test_time` (empty until tested)
- `redirect_uri`, the URI to register at the identity provider: `<PUBLIC_URL>/callback/<tenant id>`, the same for all the connections of the tenant

The system SHALL never return the client secret.

The system SHALL give the same answer for a connection that does not exist and for a connection that another tenant owns. This applies on every route that names a connection. The answer is `NOT_FOUND` (404) with the reason `CONNECTION_NOT_FOUND`.

The system orders lists by id and pages them. A request can have a `page_size` from 0 to 100 (0 means 50) and an opaque `page_token`. The answer has a `next_page_token` while there is more. For a malformed token the system answers `INVALID_ARGUMENT`. The list operation does not ask if the tenant exists. Thus an unknown tenant has an empty list.

#### Scenario: Another tenant's path does not reach a connection
- **WHEN** a caller reads, updates, deletes or tests a connection of tenant A under tenant B's path
- **THEN** the system answers `NOT_FOUND` with the reason `CONNECTION_NOT_FOUND`, as for a connection that does not exist
