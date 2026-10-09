# tenant-sso-policy Specification

## Purpose

A tenant's SSO policy says which of its connections it uses (bindings, each active or not), whether company sign-in is `OPTIONAL` or `REQUIRED` (enforcement), whether users in the tenant's domains join at their first sign-in (auto-join), and which email domains its company sign-in applies to. The tenant service stores the policy, because every sign-in reads it there, including sign-ins that never touch this service. Tenant admins read and write it through this service (`SSOTenantAdminService`), so that connections and policy are one API.

Key decisions:

- **Each service checks what it stores.** This service checks that a binding names a connection the tenant owns, and an active binding a tested one. The tenant service checks the policy as a whole when it writes it, and its refusals are passed on unchanged.
- **The connection rows are locked across the write**, so a connection cannot be deleted between the check and the write. Letting admins write to the tenant service directly was turned down: it would have to call back here to check the connections, a cycle between two services and a lock across both.
- **No read before a write, no version.** A write replaces the part it names.

Non-goals: storing any part of the policy; setting domains (see `platform-administration`); the tenant's MFA policy.

## Requirements
### Requirement: Read a tenant's SSO policy
The system SHALL return, on `GetTenantSSOPolicy` (`GET /api/v0/sso/tenants/{tenant_id}/policy`), the policy as the tenant service stores it: `tenant_id`, `enforcement`, `auto_join`, `domains` and `bindings` (`connection_id`, `active`). The system SHALL answer `NOT_FOUND` (`no such tenant`) for an unknown tenant, `FAILED_PRECONDITION` with the reason `PERSONAL_TENANT` for a personal tenant, and `UNAVAILABLE` when the tenant service does not answer.

#### Scenario: A tenant that never wrote a policy
- **WHEN** the policy of a new tenant is read
- **THEN** it is returned with `ENFORCEMENT_OFF`, auto-join off, no domains and no bindings

### Requirement: Write bindings, enforcement and auto-join
The system SHALL, on `PutTenantSSOPolicy` (`PUT /api/v0/sso/tenants/{tenant_id}/policy`), write the tenant's `bindings` (the whole set), `enforcement` (`ENFORCEMENT_OPTIONAL` or `ENFORCEMENT_REQUIRED`; anything else is `INVALID_ARGUMENT`) and `auto_join`, and return the policy the tenant service stored. The domains are kept as stored. A successful write is logged as an admin action; a refused write changes nothing.

In one transaction the system SHALL lock the row of every connection the bindings name, in id order and in one statement, check them, and hold the locks until the tenant service has answered:

| check | when it fails |
|---|---|
| every binding names a connection the tenant in the path owns | `NOT_FOUND` (404), `CONNECTION_NOT_FOUND` |
| every active binding names a tested connection | `FAILED_PRECONDITION` (400), `CONNECTION_NOT_TESTED` |

It SHALL then pass the write to the tenant service and pass its refusal on:

| the tenant service says | gRPC code (HTTP) | reason |
|---|---|---|
| `REQUIRED` would be left with no active binding | `FAILED_PRECONDITION` (400) | `REQUIRED_NEEDS_ACTIVE_BINDING` |
| auto-join without `REQUIRED`, or without domains | `FAILED_PRECONDITION` (400) | `AUTO_JOIN_NEEDS_REQUIRED_AND_DOMAINS` |
| a personal tenant | `FAILED_PRECONDITION` (400) | `PERSONAL_TENANT` |
| no such tenant | `NOT_FOUND` (404) | none |
| the request is invalid, or refused for another reason | `INVALID_ARGUMENT` or `FAILED_PRECONDITION` (400) | none; the message quotes the tenant service |
| another request is changing the tenant's policy | `ABORTED` (409) | none |
| no answer, or any other failure | `UNAVAILABLE` (503) | none |

A connection row another request holds for more than 2 seconds is `ABORTED` too.

#### Scenario: A draft cannot be active
- **WHEN** a policy binds a draft connection with `active: true`
- **THEN** the system answers `FAILED_PRECONDITION` with the reason `CONNECTION_NOT_TESTED`; with `active: false` the binding is accepted

#### Scenario: Another tenant's connection cannot be bound
- **WHEN** a policy names a connection another tenant owns, or one that does not exist
- **THEN** the system answers `NOT_FOUND` with the reason `CONNECTION_NOT_FOUND`, and the tenant service is not called

#### Scenario: Required with nothing active
- **WHEN** a tenant admin writes `REQUIRED` with no binding, or with none active
- **THEN** the system answers `FAILED_PRECONDITION` with the reason `REQUIRED_NEEDS_ACTIVE_BINDING`, as the tenant service said

