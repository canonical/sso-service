# sign-in-attempts Specification

## Purpose

The login UI calls `SSOSignInService` around a company sign-in, for these things:

- which connections of a tenant to offer
- a ticket to start the sign-in with
- a confirmation when the user comes back with a session

`SSOSignInService` is gRPC only. A user must not call it, so it has no HTTP route that a gateway can expose.

Key decisions:

- **A sign-in attempt is a sealed ticket, not a row.** The service writes nothing when a sign-in starts. There is nothing to clean up, and the database holds no user data. For those reasons we turned down a table of attempts. The cost is that no one can withdraw a ticket before it expires, and a ticket is not single use. A replay of a ticket only starts another sign-in, and that sign-in still needs the user at the identity provider.
- **The browser that completed a sign-in holds the proof of it.** The callback gives that browser a receipt in a cookie (see `company-sign-in`). The login UI passes the receipt on with the ticket. We turned down a record on a server, in a table of this service or in the rows that hydra-sso keeps. Such a record says that the identity provider answered for the ticket, but it does not say in which browser. With such a record, a person who starts an attempt can make another user's browser finish the attempt. Then that person can present the ticket with a session made another way.
- **`CompleteAttempt` only verifies.** We turned down a design in which it also creates the user's membership, and removes an account whose membership was refused. In that design, a confirmation writes 2 other services' data, and a caller cannot repeat it safely. The login UI asks the tenant service for the membership.
- **This service does not decide which connections apply to a user.** The login UI passes on the connection ids that the tenant service gave it.

Non-goals:

- tenant lookup
- a record of which connection a session came from (the login UI keeps that)

## Requirements
### Requirement: List the sign-in options among given connections
On `ListOptions` with up to 50 connection ids, the system SHALL return an option (`connection_id`, `label`) for each connection that exists and is tested. The options SHALL be in the order of the request, and each option SHALL appear once. The system leaves out drafts and unknown ids without an error. The call reads only the service's own database.

#### Scenario: A draft is not offered
- **WHEN** the login UI asks for the options among a tested connection and a draft
- **THEN** the system returns only the tested connection, with its label

### Requirement: Start an attempt
On `StartAttempt` with `tenant_id`, `email`, `connection_id` and `reauthenticate`, the system SHALL return a ticket. The system SHALL refuse with `FAILED_PRECONDITION` and the reason `NOT_APPLICABLE` when the connection does not exist or is not tested. The system stores nothing and calls no other service.

The named tenant is the tenant that the sign-in is for. That tenant does not need to own the connection. A user who proves an account during account linking signs in through a connection of another tenant. Thus the system does not check that the tenant binds the connection or that the address can use the connection. The callback does those checks for a subject with no link (see `company-sign-in`), and the login UI checks every session against the tenant.

A ticket holds these items:

- the tenant id
- the connection id
- the address in lower case
- if the identity provider must authenticate the user again
- the time when the system issued the ticket
- an expiry 30 minutes later

The system seals the ticket with the service's keys (see `secrets`). Callers treat the ticket as opaque, and no one else can read it or change it. The login UI gives the ticket to Kratos as the `login_hint` of the `byo-sso` sign-in. The ticket reaches this service as the `login_hint` of hydra-sso's login request, and again in `CompleteAttempt`.

#### Scenario: A draft connection
- **WHEN** the login UI starts an attempt for a connection that is a draft or does not exist
- **THEN** the system answers `FAILED_PRECONDITION` with the reason `NOT_APPLICABLE`

#### Scenario: A connection of another tenant
- **WHEN** the login UI starts an attempt for tenant T with a tested connection that tenant U owns
- **THEN** the system returns a ticket that names T and that connection
- **AND** the login UI's check against T's policy decides if a session through that connection counts at T

#### Scenario: An altered or expired ticket
- **WHEN** the system gets a ticket with one character changed, or a ticket issued more than 30 minutes ago
- **THEN** `/login` and `/callback` refuse the sign-in as expired
- **AND** `CompleteAttempt` answers `NOT_APPLICABLE`

### Requirement: Confirm that an attempt ended at an account
On `CompleteAttempt` with a ticket, an `identity_id` and a `receipt`, the system SHALL return the ticket's `connection_id` and `tenant_id` when all these conditions are true:

- the ticket opens and is not expired
- Kratos has the account
- the account's address equals the ticket's address (the comparison ignores case)
- the account holds a link to the ticket's connection
- the receipt is valid (see `secrets`) for the ticket and for the subject of one of the links that the account holds at that connection

In all other cases the system SHALL answer `FAILED_PRECONDITION` with the reason `NOT_APPLICABLE`. The answer is the same for every cause. A receipt that is empty, malformed, made for another ticket or another subject, or made under another key is one more cause. When Kratos does not answer, the system SHALL answer `UNAVAILABLE`.

The `receipt` is the value of a cookie. The callback set that cookie in the browser when it accepted the ticket's sign-in (see `company-sign-in`). The login UI reads the cookie that is named for the ticket that it asks about, and sends the value of that cookie. When the browser has no such cookie, the login UI sends nothing. The login UI can confirm 2 tickets for the same session, for example after account linking through another company sign-in. Then each ticket has a receipt of its own.

The call SHALL NOT store or write anything in this service, in Kratos or in the tenant service. The call creates no membership and removes no account, and a repeat returns the same answer.

The receipt has these limits:

- The receipt is not single use. The same browser can present the receipt again until the ticket expires, which is 30 minutes at most. That gives nothing more than the session that the sign-in already produced.
- The receipt proves that this browser completed this ticket's sign-in as a subject that the account is linked to. The receipt does not prove that this sign-in produced this particular Kratos session.

#### Scenario: The user comes back signed in
- **WHEN** the login UI confirms a ticket with the account that Kratos signed in through the ticket's connection
- **AND** the login UI sends the receipt that the browser holds for the ticket
- **THEN** the login UI gets the ticket's connection and tenant
- **AND** the login UI gets them again when it repeats the call

#### Scenario: A sign-in that never reached the identity provider
- **WHEN** an account has links to the connections of 2 tenants
- **AND** the user of that account starts a sign-in at the first tenant and leaves it at the identity provider
- **AND** the user signs in through the connection of the second tenant
- **AND** the login UI confirms the first ticket with that session
- **THEN** the system answers `NOT_APPLICABLE`
- **AND** the reason is that the browser has no receipt for the first ticket
- **AND** the system made the receipt of the second sign-in for another ticket and another subject

#### Scenario: An attempt finished in another browser
- **WHEN** a sign-in that started in one browser is completed at the identity provider in another browser
- **AND** the browser that started the sign-in got a session of the account another way
- **AND** the login UI confirms the ticket for the browser that started the sign-in
- **THEN** the system answers `NOT_APPLICABLE`
- **AND** the reason is that the receipt is in the browser that completed the sign-in
- **AND** the browser that holds the ticket has no receipt

#### Scenario: An account with two subjects at the connection
- **WHEN** the account holds 2 links to the ticket's connection
- **AND** the system made the receipt for the second link
- **THEN** the system confirms the attempt

#### Scenario: No membership is created
- **WHEN** the system confirms the first company sign-in of an invited user
- **THEN** the user is not a member of the tenant until the login UI asks the tenant service for the membership

