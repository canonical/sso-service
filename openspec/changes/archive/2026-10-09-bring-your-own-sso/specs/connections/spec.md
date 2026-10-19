## Purpose

A connection is a tenant's registration of its OIDC identity provider. A connection holds the issuer, and the client id and the client secret that the identity provider gave the tenant. Connections are the only thing that this service stores (table `sso_connections`). Tenant admins manage connections through `SSOTenantAdminService`, over gRPC or HTTP.

Key decisions:

- **One owner per connection.** Only the tenant that created a connection uses it. A company with several tenants registers its identity provider once for each tenant. We turned down shared connections because, with one owner, the tenant in the path scopes every route.
- **`issuer` and `client_id` are fixed at create.** Another identity provider or another app registration asserts other subjects. Thus it is a new connection, and not an edit that breaks every link.
- **At most 5 per tenant**, to bound what one tenant admin can make the service store and call.
- **Writes are last-write-wins.** There is no version check and no idempotency key.

Non-goals:

- contact with the identity provider at create (the test sign-in does that)
- a scope list or a subject claim per connection (always `openid email` and the id_token's `sub`)

## ADDED Requirements

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
| the issuer is not an absolute `https` URL, or has userinfo, a query or a fragment | `INVALID_ARGUMENT` (400) | `invalid issuer: …` |
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
- `redirect_uri`, the URI to register at the identity provider: `<PUBLIC_URL>/callback/<connection id>`

The system SHALL never return the client secret.

The system SHALL give the same answer for a connection that does not exist and for a connection that another tenant owns. This applies on every route that names a connection. The answer is `NOT_FOUND` (404) with the reason `CONNECTION_NOT_FOUND`.

The system orders lists by id and pages them. A request can have a `page_size` from 0 to 100 (0 means 50) and an opaque `page_token`. The answer has a `next_page_token` while there is more. For a malformed token the system answers `INVALID_ARGUMENT`. The list operation does not ask if the tenant exists. Thus an unknown tenant has an empty list.

#### Scenario: Another tenant's path does not reach a connection
- **WHEN** a caller reads, updates, deletes or tests a connection of tenant A under tenant B's path
- **THEN** the system answers `NOT_FOUND` with the reason `CONNECTION_NOT_FOUND`, as for a connection that does not exist

### Requirement: Update the label and the client secret
On `UpdateConnection` (`PATCH …/connections/{connection_id}`), the system SHALL change the `label` of a connection, its `client_secret` or both, as `update_mask` names them. The system SHALL NOT change anything else. The system answers `INVALID_ARGUMENT` in each of these cases:

- the mask names nothing
- the mask names a path that is not `label` and not `client_secret`
- the label is not 1 to 100 characters
- the secret is not 1 to 4096 characters

These limits are the limits that `CreateConnection` has.

Over HTTP the request body is the fields to change, `{"label": …, "client_secret": …}`. The fields that are present make the mask. Thus a body with only `label` changes only the label. The system refuses, with HTTP 400, a body with no field and a body with a field that is not one of these 2 fields.

The update SHALL set `update_time` and keep the status. A tested connection stays tested when its secret changes.

#### Scenario: A new secret keeps the status
- **WHEN** a tenant admin replaces the client secret of a tested connection
- **THEN** the connection is still tested
- **AND** the next sign-in uses the new secret

### Requirement: Delete a connection
The system SHALL delete a connection of the tenant in the path on `DeleteConnection` (`DELETE …/connections/{connection_id}`). The system SHALL do the delete in one transaction, with the connection's row locked. First the system SHALL make the tenant service remove the connection's binding from the tenant's SSO policy. Then the system SHALL delete the row. When the tenant service refuses, the system deletes nothing.

| when | gRPC code (HTTP) | message |
|---|---|---|
| the removal of the binding leaves a tenant whose enforcement is `REQUIRED` with no active binding | `FAILED_PRECONDITION` (400) | `REQUIRED_NEEDS_ACTIVE_BINDING: …` |
| the tenant no longer exists | `NOT_FOUND` (404) | `no such tenant`. A platform admin deletes such a connection (see `platform-administration`) |
| another request holds the row for more than 2 seconds | `ABORTED` (409) | try again |
| the tenant service does not answer | `UNAVAILABLE` (503) | |

Links to a deleted connection stay in Kratos. These links never match again, because the system does not reuse connection ids. `ListLinks` does not show these links.

#### Scenario: A bound connection is unbound, then deleted
- **WHEN** a tenant admin deletes a connection that the tenant's policy binds
- **AND** the tenant's enforcement is `OPTIONAL`
- **THEN** the policy no longer holds the binding
- **AND** the connection is gone

#### Scenario: The only active binding of a tenant that requires company sign-in
- **WHEN** a tenant admin deletes the connection of the only active binding of a tenant whose enforcement is `REQUIRED`
- **THEN** the system answers `FAILED_PRECONDITION` with the reason `REQUIRED_NEEDS_ACTIVE_BINDING`
- **AND** the connection and the policy are unchanged

#### Scenario: A delete and a policy write that name the same connection
- **WHEN** a policy write that binds a connection and a delete of that connection arrive together
- **THEN** the later request waits for the first request, or the system answers it with `ABORTED` after 2 seconds
- **AND** the result is never a policy that binds a connection that is gone
