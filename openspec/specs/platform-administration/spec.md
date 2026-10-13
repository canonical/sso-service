# platform-administration Specification

## Purpose

Some operations cross tenants and belong to platform admins: finding connections whatever tenant owns them, reading and setting the email domains a tenant's company sign-in applies to, and deleting any connection. They are a gRPC service of their own, `SSOPlatformAdminService`, so that the gateway can give their routes a rule of their own.

Key decisions:

- **Who is a platform admin is the gateway's decision.** The service applies the same checks, under the same locks, as on the tenant admins' routes; only the scoping to one tenant's resources is absent.
- **Domains are set by platform admins, not by tenant admins.** A domain decides which addresses a tenant's company sign-in applies to and, with auto-join, whose users become members, so a tenant must not claim one for itself. A platform admin checks ownership outside the platform.
- **A deleted tenant leaves its connections behind.** The tenant service does not tell this service when a tenant is deleted; a platform admin lists such connections by owner and deletes them.

Non-goals: proof of domain ownership; overriding the rule that a tenant requiring company sign-in keeps an active binding.

## Requirements
### Requirement: List connections across tenants
The system SHALL list every connection on `ListAllConnections` (`GET /api/v0/sso/connections`), or only one owner's when `owner_tenant_id` is given, with the fields and paging of a tenant's own list (see `connections`). The owner need not exist any more.

#### Scenario: The connections of a deleted tenant
- **WHEN** a platform admin lists the connections with the `owner_tenant_id` of a tenant that was deleted
- **THEN** the connections that tenant owned are returned

### Requirement: Read a tenant's domains
The system SHALL, on `GetTenantDomains` (`GET /api/v0/sso/tenants/{tenant_id}/domains`), return the `domains` of the policy the tenant service stores for the tenant, for a platform admin to read before `SetTenantDomains` replaces the whole set. It takes no lock and writes nothing. The system SHALL answer `NOT_FOUND` (`no such tenant`) for an unknown tenant, `FAILED_PRECONDITION` with the reason `PERSONAL_TENANT` for a personal tenant, and `UNAVAILABLE` when the tenant service does not answer.

#### Scenario: The domains a platform admin set
- **WHEN** a platform admin reads the domains of a tenant whose domains were set to `acme.example`
- **THEN** the system returns `acme.example`; for a tenant with none it returns an empty list

### Requirement: Set a tenant's domains
The system SHALL, on `SetTenantDomains` (`PUT /api/v0/sso/tenants/{tenant_id}/domains`), pass `domains` (the whole set, at most 50, empty clears it) to the tenant service as given and return the policy it stored. Enforcement, auto-join and the bindings are kept as stored. The system SHALL NOT check the domains itself: the tenant service checks them, and its refusal is passed on with the codes of a policy write (see `tenant-sso-policy`). A successful write is logged as an admin action.

#### Scenario: The domains of an auto-join tenant cannot be cleared
- **WHEN** a platform admin sets an empty list for a tenant with auto-join on
- **THEN** the system answers `FAILED_PRECONDITION` with the reason `AUTO_JOIN_NEEDS_REQUIRED_AND_DOMAINS`, as the tenant service said

### Requirement: Delete any connection
The system SHALL, on `DeleteAnyConnection` (`DELETE /api/v0/sso/connections/{connection_id}`), delete a connection whatever tenant owns it, in the same way as a tenant's own delete (see `connections`): the binding is removed from the owner's policy first, under the connection's row lock. When the owner no longer exists there is nothing to unbind, and the connection is deleted. A connection that does not exist is `NOT_FOUND` with the reason `CONNECTION_NOT_FOUND`. The delete is still refused with `REQUIRED_NEEDS_ACTIVE_BINDING` when it would leave an owner whose enforcement is `REQUIRED` with no active binding. A successful delete is logged as an admin action.

#### Scenario: The connection of a deleted tenant
- **WHEN** a platform admin deletes a connection whose owner was deleted
- **THEN** the connection is gone, and a second delete answers `NOT_FOUND` with the reason `CONNECTION_NOT_FOUND`

#### Scenario: A tenant that requires company sign-in keeps its connection
- **WHEN** a platform admin deletes the connection of the only active binding of a tenant whose enforcement is `REQUIRED`
- **THEN** the system answers `FAILED_PRECONDITION` with the reason `REQUIRED_NEEDS_ACTIVE_BINDING` and nothing is deleted

