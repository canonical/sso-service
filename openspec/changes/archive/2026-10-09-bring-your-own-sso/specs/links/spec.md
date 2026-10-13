## Purpose

A link maps a subject at a connection's identity provider to an account. This service does not store a link. A link is the Kratos OIDC credential of the `byo-sso` provider with the identifier `byo-sso:<connection id>:<subject>`. Kratos writes every link. This service reads links, and removes a link when a user disconnects a company sign-in from that user's account. The login UI calls both RPCs, on `SSOSignInService`, for the signed-in user.

Key decisions:

- **Per-connection removal, built here.** Kratos's own unlink works per provider, and every connection is the one provider `byo-sso`. Thus Kratos's own unlink removes all of an account's company sign-ins at once.
- **The service refuses to remove the last way in.** Kratos's email code does not count as a way in. Every account with an address has the email code, so the rule is empty if the email code counts.
- **Links to deleted connections stay in Kratos.** These links never match again, and the service does not list them. To remove these links, the service needs a write to Kratos for every linked account.

Non-goals:

- to write a link
- to decide if the user's session is fresh enough to change credentials (the login UI decides that)

## ADDED Requirements

### Requirement: List an account's links
On `ListLinks` with an `identity_id`, the system SHALL return one entry for each connection that the account holds a link to and that still exists. An entry has the `connection_id`, the `label` of the connection, and the `tenant_id`, which is the tenant that owns the connection. The system lists a connection once when the account holds several subjects at that connection. For an unknown account the system answers `NOT_FOUND` (`no such account`). When Kratos does not answer, the system answers `UNAVAILABLE`.

#### Scenario: A link to a deleted connection
- **WHEN** an account holds links to connection A, which exists, and to connection B, which was deleted
- **THEN** the system lists only A

### Requirement: Remove an account's link at one connection
On `DeleteLink` with an `identity_id` and a `connection_id`, the system SHALL remove from Kratos every credential identifier that the account holds at that connection. The system SHALL NOT remove any other credential identifier. The system logs a removal as an admin action of the account.

| when | gRPC code | message |
|---|---|---|
| Kratos does not have the account | `NOT_FOUND` | `no such account` |
| the account holds no link at the connection | `NOT_FOUND` | `the account has no link at this connection` |
| the removal leaves the account with no way to sign in of its own | `FAILED_PRECONDITION` | `LAST_CREDENTIAL: …` |
| Kratos does not answer, or refuses the removal | `UNAVAILABLE` | `Kratos is unavailable` |

An account has a way to sign in of its own when it has at least one of these:

- a password
- a passkey
- a passwordless WebAuthn key
- a sign-in through an OIDC provider that is not `byo-sso`
- a link to another connection that still exists

The links that the system removes do not count. Kratos's email code and a WebAuthn key used only for MFA also do not count.

#### Scenario: The account has a password
- **WHEN** a user with a password removes their only link
- **THEN** the system removes the link from Kratos and does not list it any more

#### Scenario: The only way in
- **WHEN** a user asks to remove a link that is their only credential
- **THEN** the system answers `FAILED_PRECONDITION` with the reason `LAST_CREDENTIAL`
- **AND** the link stays
- **AND** the result is the same when the user's only other link is to a deleted connection

### Requirement: The service never writes a link
The system SHALL NOT create or change a credential, an account or a session in Kratos. Its only write to Kratos is the removal above.

#### Scenario: A first company sign-in
- **WHEN** the callback lets a subject with no link through
- **THEN** the service made no write to Kratos
- **AND** the link exists only after Kratos links or registers the account
