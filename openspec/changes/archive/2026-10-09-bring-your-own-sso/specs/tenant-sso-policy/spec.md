## Purpose

The SSO policy of a tenant has these parts:

- The connections that the tenant uses (bindings). Each binding is active or not.
- The enforcement. Company sign-in is `OPTIONAL` or `REQUIRED`.
- The auto-join. It says if users in the domains of the tenant join at their first sign-in.
- The email domains that the company sign-in of the tenant applies to.

The Tenant Service stores the policy, because every sign-in reads it there. This includes sign-ins that never touch this service. Tenant admins read and write the policy through this service (`SSOTenantAdminService`), so that connections and policy are one API.

Key decisions:

- **Each service checks what it stores.** This service checks that a binding names a connection that the tenant owns. It also checks that an active binding names a tested connection. The Tenant Service checks the policy as a whole when it writes the policy. This service passes on the Tenant Service's refusals unchanged.
- **The service locks the connection rows across the write.** For this reason nobody can delete a connection between the check and the write. We turned down a direct write from admins to the Tenant Service. The Tenant Service then needs to call back here to check the connections. That is a cycle between 2 services and a lock across both.
- **No read before a write and no version.** A write replaces the part that it names.

Non-goals:

- Storage of any part of the policy.
- The setting of domains (see `platform-administration`).
- The MFA policy of the tenant.

## ADDED Requirements

### Requirement: Read a tenant's SSO policy
The system SHALL return the policy on `GetTenantSSOPolicy` (`GET /api/v0/sso/tenants/{tenant_id}/policy`), as the Tenant Service stores it. The policy has `tenant_id`, `enforcement`, `auto_join`, `domains` and `bindings` (`connection_id`, `active`). The system SHALL answer in these cases:

- `NOT_FOUND` (`no such tenant`) for an unknown tenant.
- `FAILED_PRECONDITION` with the reason `PERSONAL_TENANT` for a personal tenant.
- `UNAVAILABLE` when the Tenant Service does not answer.

#### Scenario: A tenant that never wrote a policy
- **WHEN** a caller reads the policy of a new tenant
- **THEN** the service returns it with `ENFORCEMENT_OFF`, auto-join off, no domains and no bindings

### Requirement: Write bindings, enforcement and auto-join
The system SHALL, on `PutTenantSSOPolicy` (`PUT /api/v0/sso/tenants/{tenant_id}/policy`), write these parts of the tenant's policy:

- `bindings`, the whole set.
- `enforcement`, which is `ENFORCEMENT_OPTIONAL` or `ENFORCEMENT_REQUIRED`. Any other value gives `INVALID_ARGUMENT`.
- `auto_join`.

The system SHALL return the policy that the Tenant Service stored. The domains stay as stored. The service logs a successful write as an admin action. A refused write changes nothing.

The system SHALL do the next steps in one transaction. It SHALL lock the row of every connection that the bindings name, in id order and in one statement. It SHALL check the connections as this table shows. It SHALL hold the locks until the Tenant Service has answered.

| check | when it fails |
|---|---|
| every binding names a connection that the tenant in the path owns | `NOT_FOUND` (404), `CONNECTION_NOT_FOUND` |
| every active binding names a tested connection | `FAILED_PRECONDITION` (400), `CONNECTION_NOT_TESTED` |

The system SHALL then pass the write to the Tenant Service. It SHALL pass on the refusal of the Tenant Service as this table shows:

| the Tenant Service says | gRPC code (HTTP) | reason |
|---|---|---|
| the write leaves `REQUIRED` with no active binding | `FAILED_PRECONDITION` (400) | `REQUIRED_NEEDS_ACTIVE_BINDING` |
| auto-join without `REQUIRED`, or without domains | `FAILED_PRECONDITION` (400) | `AUTO_JOIN_NEEDS_REQUIRED_AND_DOMAINS` |
| a personal tenant | `FAILED_PRECONDITION` (400) | `PERSONAL_TENANT` |
| no such tenant | `NOT_FOUND` (404) | none |
| the request is invalid, or refused for another reason | `INVALID_ARGUMENT` or `FAILED_PRECONDITION` (400) | none, and the message quotes the Tenant Service |
| another request changes the policy of the tenant | `ABORTED` (409) | none |
| no answer, or any other failure | `UNAVAILABLE` (503) | none |

A connection row that another request holds for more than 2 seconds also gives `ABORTED`.

#### Scenario: A draft cannot be active
- **WHEN** a policy binds a draft connection with `active: true`
- **THEN** the system answers `FAILED_PRECONDITION` with the reason `CONNECTION_NOT_TESTED`
- **AND** with `active: false` the system accepts the binding

#### Scenario: Another tenant's connection cannot be bound
- **WHEN** a policy names a connection that another tenant owns, or one that does not exist
- **THEN** the system answers `NOT_FOUND` with the reason `CONNECTION_NOT_FOUND`
- **AND** the service does not call the Tenant Service

#### Scenario: Required with nothing active
- **WHEN** a tenant admin writes `REQUIRED` with no binding, or with none active
- **THEN** the system answers `FAILED_PRECONDITION` with the reason `REQUIRED_NEEDS_ACTIVE_BINDING`
- **AND** this is the answer that the Tenant Service gave
