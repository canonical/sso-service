## Purpose

A link maps a subject at a connection's identity provider to an account. It is not stored here: it is the Kratos OIDC credential of the `byo-sso` provider with the identifier `byo-sso:<connection id>:<subject>`. Kratos writes every link; this service reads them, and removes one when a user disconnects a company sign-in from their account. The login UI calls both RPCs, on `SSOSignInService`, for the signed-in user.

Key decisions:

- **Per-connection removal, built here.** Kratos's own unlink works per provider, and every connection is the one provider `byo-sso`: it would remove all of an account's company sign-ins at once.
- **The last way in cannot be removed.** Kratos's email code does not count as a way in: every account with an address has it, so counting it would make the rule empty.
- **Links to deleted connections are left in Kratos.** They never match again and are not listed; removing them would mean a write to Kratos for every linked account.

Non-goals: writing a link; deciding whether the user's session is fresh enough to change credentials (the login UI does).

## ADDED Requirements

### Requirement: List an account's links
The system SHALL return, on `ListLinks` with an `identity_id`, one entry per connection the account holds a link to and that still exists: `connection_id`, the connection's `label`, and `tenant_id`, the tenant that owns the connection. A connection the account holds several subjects at is listed once. An unknown account is `NOT_FOUND` (`no such account`); Kratos not answering is `UNAVAILABLE`.

#### Scenario: A link to a deleted connection
- **WHEN** an account holds links to connection A, which exists, and to connection B, which was deleted
- **THEN** only A is listed

### Requirement: Remove an account's link at one connection
The system SHALL, on `DeleteLink` with an `identity_id` and a `connection_id`, remove from Kratos every credential identifier the account holds at that connection, and no other. A removal is logged as an admin action of the account.

| when | gRPC code | message |
|---|---|---|
| Kratos does not have the account | `NOT_FOUND` | `no such account` |
| the account holds no link at the connection | `NOT_FOUND` | `the account has no link at this connection` |
| the account would be left with no way to sign in of its own | `FAILED_PRECONDITION` | `LAST_CREDENTIAL: …` |
| Kratos does not answer, or refuses the removal | `UNAVAILABLE` | `Kratos is unavailable` |

An account has a way to sign in of its own, apart from the links being removed, when it has a password, a passkey, a passwordless WebAuthn key, a sign-in through another OIDC provider than `byo-sso`, or a link to another connection that still exists. Kratos's email code and a WebAuthn key used only for MFA do not count.

#### Scenario: The account has a password
- **WHEN** a user with a password removes their only link
- **THEN** the link is removed from Kratos and no longer listed

#### Scenario: The only way in
- **WHEN** a user whose only credential is one link, or whose only other link is to a deleted connection, asks to remove it
- **THEN** the system answers `FAILED_PRECONDITION` with the reason `LAST_CREDENTIAL` and the link stays

### Requirement: The service never writes a link
The system SHALL NOT create or change a credential, an account or a session in Kratos. Its only write to Kratos is the removal above.

#### Scenario: A first company sign-in
- **WHEN** a subject with no link is let through at the callback
- **THEN** the service has made no write to Kratos, and the link exists only once Kratos has linked or registered the account
