## Purpose

Some operations cross tenants and belong to platform admins:

- Find connections, whatever tenant owns them.
- Read and set the email domains that the company sign-in of a tenant applies to.
- Delete any connection.

These operations are a gRPC service of their own, `SSOPlatformAdminService`, so that the gateway can give their routes a rule of their own.

Key decisions:

- **The gateway decides who is a platform admin.** The service applies the same checks, under the same locks, as on the routes of tenant admins. Only the scoping to the resources of one tenant is absent.
- **Platform admins set domains, not tenant admins.** A domain decides which addresses the company sign-in of a tenant applies to. With auto-join, a domain also decides whose users become members. For this reason a tenant must not claim a domain for itself. A platform admin checks the ownership outside the platform.
- **A deleted tenant leaves its connections behind.** The Tenant Service does not tell this service when a tenant is deleted. A platform admin lists such connections by owner and deletes them.

Non-goals:

- Proof of domain ownership.
- An override of the rule that a tenant that requires company sign-in keeps an active binding.

## ADDED Requirements

### Requirement: List connections across tenants
The system SHALL list every connection on `ListAllConnections` (`GET /api/v0/sso/connections`). When `owner_tenant_id` is given, the system SHALL list only the connections of that owner. The list SHALL have the fields and the paging of a tenant's own list (see `connections`). The owner does not need to exist any more.

#### Scenario: The connections of a deleted tenant
- **WHEN** a platform admin lists the connections with the `owner_tenant_id` of a tenant that was deleted
- **THEN** the service returns the connections that the tenant owned

### Requirement: Read a tenant's domains
The system SHALL, on `GetTenantDomains` (`GET /api/v0/sso/tenants/{tenant_id}/domains`), return the `domains` of the policy that the Tenant Service stores for the tenant. The answer is for a platform admin to read before `SetTenantDomains` replaces the whole set. The call takes no lock and writes nothing. The system SHALL answer in these cases:

- `NOT_FOUND` (`no such tenant`) for an unknown tenant.
- `FAILED_PRECONDITION` with the reason `PERSONAL_TENANT` for a personal tenant.
- `UNAVAILABLE` when the Tenant Service does not answer.

#### Scenario: The domains a platform admin set
- **WHEN** a platform admin reads the domains of a tenant whose domains were set to `acme.example`
- **THEN** the system returns `acme.example`
- **AND** for a tenant with no domains the system returns an empty list

### Requirement: Set a tenant's domains
The system SHALL, on `SetTenantDomains` (`PUT /api/v0/sso/tenants/{tenant_id}/domains`), pass `domains` to the Tenant Service as given. `domains` is the whole set, at most 50, and an empty set clears it. The system SHALL return the policy that the Tenant Service stored. The enforcement, the auto-join and the bindings stay as stored. The system SHALL NOT check the domains itself. The Tenant Service checks them, and the system passes on its refusal with the codes of a policy write (see `tenant-sso-policy`). The service logs a successful write as an admin action.

#### Scenario: The domains of an auto-join tenant cannot be cleared
- **WHEN** a platform admin sets an empty list for a tenant with auto-join on
- **THEN** the system answers `FAILED_PRECONDITION` with the reason `AUTO_JOIN_NEEDS_REQUIRED_AND_DOMAINS`
- **AND** this is the answer that the Tenant Service gave

### Requirement: Delete any connection
The system SHALL, on `DeleteAnyConnection` (`DELETE /api/v0/sso/connections/{connection_id}`), delete a connection, whatever tenant owns it. The delete SHALL work in the same way as a tenant's own delete of a connection (see `connections`). The service SHALL first remove the binding from the owner's policy, under the connection's row lock. When the owner does not exist any more, there is nothing to unbind, and the service deletes the connection. A connection that does not exist gives `NOT_FOUND` with the reason `CONNECTION_NOT_FOUND`. The system SHALL still refuse the delete with `REQUIRED_NEEDS_ACTIVE_BINDING` when the delete leaves an owner with enforcement `REQUIRED` and no active binding. The service logs a successful delete as an admin action.

#### Scenario: The connection of a deleted tenant
- **WHEN** a platform admin deletes a connection whose owner was deleted
- **THEN** the connection is gone
- **AND** a second delete answers `NOT_FOUND` with the reason `CONNECTION_NOT_FOUND`

#### Scenario: A tenant that requires company sign-in keeps its connection
- **WHEN** a platform admin deletes the connection of the only active binding of a tenant whose enforcement is `REQUIRED`
- **THEN** the system answers `FAILED_PRECONDITION` with the reason `REQUIRED_NEEDS_ACTIVE_BINDING`
- **AND** the service deletes nothing
