## Purpose

A connection is a tenant's registration of its OIDC identity provider: the issuer, and the client id and client secret the provider gave the tenant. Connections are the only thing this service stores (table `sso_connections`). Tenant admins manage them through `SSOTenantAdminService`, over gRPC or HTTP.

Key decisions:

- **One owner per connection.** Only the tenant that created a connection uses it; a company with several tenants registers its provider once per tenant. Sharing was turned down: with one owner, every route is scoped by the tenant in its path.
- **`issuer` and `client_id` are fixed at create.** Another provider or app registration asserts other subjects, so it is a new connection, not an edit that strands every link.
- **At most five per tenant**, to bound what one tenant admin can make the service store and call.
- **Writes are last-write-wins**: no version check, no idempotency key.

Non-goals: contacting the provider at create (the test sign-in does); a scope list or a subject claim per connection (always `openid email` and the id_token's `sub`).

## ADDED Requirements

### Requirement: Create a connection
The system SHALL create a connection for the tenant in the path on `CreateConnection` (`POST /api/v0/sso/tenants/{tenant_id}/connections`) from `label` (1 to 100 characters), `issuer` (1 to 2048), `client_id` (1 to 512) and `client_secret` (1 to 4096). The connection gets a UUIDv7 id, the status `CONNECTION_STATUS_DRAFT`, the tenant as `owner_tenant_id`, and as `created_by` the caller's token subject when that is a UUID (empty for a service client). The identity provider is not contacted.

After request validation the system SHALL refuse, checking in this order:

| when | gRPC code (HTTP) | message |
|---|---|---|
| the tenant service does not know the tenant | `NOT_FOUND` (404) | `no such tenant` |
| the tenant is a personal tenant | `FAILED_PRECONDITION` (400) | `PERSONAL_TENANT: …` |
| the issuer is not an absolute `https` URL, or has userinfo, a query or a fragment | `INVALID_ARGUMENT` (400) | `invalid issuer: …` |
| the tenant already owns five connections | `FAILED_PRECONDITION` (400) | `CONNECTION_LIMIT: a tenant owns at most 5 connections` |

The limit is part of the statement that inserts the row, and creates are not serialised: creates of one tenant that run at the same moment can pass the limit together.

#### Scenario: A draft connection is created
- **WHEN** a tenant admin creates a connection with a label, an `https` issuer, a client id and a client secret
- **THEN** the connection is returned as a draft owned by the tenant, with its `redirect_uri` and without the client secret

#### Scenario: A sixth connection is refused
- **WHEN** a tenant that owns five connections creates another
- **THEN** the system answers `FAILED_PRECONDITION` with the reason `CONNECTION_LIMIT`, and another tenant can still create its own

### Requirement: Read a tenant's connections
The system SHALL return a connection of the tenant in the path on `GetConnection` (`GET …/connections/{connection_id}`) and list the tenant's connections on `ListConnections` (`GET …/connections`). A connection is returned as `id`, `owner_tenant_id`, `label`, `issuer`, `client_id`, `status` (`CONNECTION_STATUS_DRAFT` or `CONNECTION_STATUS_TESTED`), `created_by`, `create_time`, `update_time`, `test_time` (empty until tested) and `redirect_uri`, the URI to register at the identity provider: `<PUBLIC_URL>/callback/<connection id>`. The client secret SHALL never be returned.

A connection that does not exist and a connection another tenant owns SHALL get the same answer on every route that names a connection: `NOT_FOUND` (404) with the reason `CONNECTION_NOT_FOUND`.

Lists are ordered by id and paged: `page_size` from 0 to 100 (0 means 50) and an opaque `page_token`; the answer carries `next_page_token` while there is more. A malformed token is `INVALID_ARGUMENT`. Listing does not ask whether the tenant exists: an unknown tenant has an empty list.

#### Scenario: Another tenant's path does not reach a connection
- **WHEN** a caller reads, updates, deletes or tests tenant A's connection under tenant B's path
- **THEN** the system answers `NOT_FOUND` with the reason `CONNECTION_NOT_FOUND`, as for a connection that does not exist

### Requirement: Update the label and the client secret
The system SHALL change a connection's `label`, its `client_secret` or both on `UpdateConnection` (`PATCH …/connections/{connection_id}`), as named by `update_mask`, and nothing else. A mask that names nothing, a path other than `label` and `client_secret`, a label that is not 1 to 100 characters and a secret that is not 1 to 4096 characters are `INVALID_ARGUMENT`, the limits `CreateConnection` has.

Over HTTP the request body is the fields to change, `{"label": …, "client_secret": …}`, and the fields present make the mask: a body with `label` only changes the label only. A body with no field, and a body with a field other than these two, are refused with HTTP 400.

The update SHALL set `update_time` and keep the status: a tested connection stays tested when its secret changes.

#### Scenario: A new secret keeps the status
- **WHEN** a tenant admin replaces the client secret of a tested connection
- **THEN** the connection is still tested and the next sign-in uses the new secret

### Requirement: Delete a connection
The system SHALL delete a connection of the tenant in the path on `DeleteConnection` (`DELETE …/connections/{connection_id}`). In one transaction, with the connection's row locked, it SHALL first have the tenant service remove the connection's binding from the tenant's SSO policy and then delete the row. When the tenant service refuses, nothing is deleted.

| when | gRPC code (HTTP) | message |
|---|---|---|
| removing the binding would leave a tenant whose enforcement is `REQUIRED` with no active binding | `FAILED_PRECONDITION` (400) | `REQUIRED_NEEDS_ACTIVE_BINDING: …` |
| the tenant no longer exists | `NOT_FOUND` (404) | `no such tenant`; a platform admin deletes such a connection (see `platform-administration`) |
| another request holds the row for more than 2 seconds | `ABORTED` (409) | try again |
| the tenant service does not answer | `UNAVAILABLE` (503) | |

Links to a deleted connection stay in Kratos. They never match again, because connection ids are not reused, and `ListLinks` does not show them.

#### Scenario: A bound connection is unbound, then deleted
- **WHEN** a tenant admin deletes a connection that the tenant's policy binds, and the tenant's enforcement is `OPTIONAL`
- **THEN** the policy no longer holds the binding and the connection is gone

#### Scenario: The only active binding of a tenant that requires company sign-in
- **WHEN** a tenant admin deletes the connection of the only active binding of a tenant whose enforcement is `REQUIRED`
- **THEN** the system answers `FAILED_PRECONDITION` with the reason `REQUIRED_NEEDS_ACTIVE_BINDING`, and the connection and the policy are unchanged

#### Scenario: A delete and a policy write that name the same connection
- **WHEN** a policy write that binds a connection and a delete of that connection arrive together
- **THEN** the later one waits for the first, or is answered `ABORTED` after 2 seconds, and the policy never ends up binding a connection that is gone
