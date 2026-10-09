# sign-in-attempts Specification

## Purpose

`SSOSignInService` is what the login UI calls around a company sign-in: which of a tenant's connections to offer, a ticket to start the sign-in with, and a confirmation when the user comes back with a session. It is gRPC only: no user may call it, so it has no HTTP route for a gateway to expose.

Key decisions:

- **A sign-in attempt is a sealed ticket, not a row.** Nothing is written when a sign-in starts, nothing needs cleaning up, and the database holds no user data; a table of attempts was turned down for those reasons. The cost: a ticket cannot be withdrawn before it expires and is not single use. Replaying one only starts another sign-in, which still needs the user at the identity provider.
- **The proof that a sign-in was completed is held by the browser that completed it.** The callback hands that browser a receipt in a cookie (see `company-sign-in`), and the login UI passes it on with the ticket. A record on a server, in a table here or in the rows hydra-sso keeps, was turned down: it would say that the identity provider answered for the ticket, but not in which browser. Whoever started an attempt could then have another user's browser finish it, and present the ticket with a session made another way.
- **`CompleteAttempt` only verifies.** Having it also create the user's membership, and remove an account whose membership was refused, was turned down: a confirmation would then write two other services' data and could not be repeated safely. The login UI asks the tenant service for the membership.
- **This service does not decide which connections apply to a user.** The login UI passes on the connection ids the tenant service gave it.

Non-goals: tenant lookup; recording which connection a session came from (the login UI keeps that).

## Requirements
### Requirement: List the sign-in options among given connections
The system SHALL return, on `ListOptions` with up to 50 connection ids, an option (`connection_id`, `label`) for each of them that exists and is tested, in the order asked and once each. Drafts and unknown ids are left out without an error. The call reads only the service's own database.

#### Scenario: A draft is not offered
- **WHEN** the login UI asks for the options among a tested connection and a draft
- **THEN** only the tested connection is returned, with its label

### Requirement: Start an attempt
The system SHALL return a ticket on `StartAttempt` with `tenant_id`, `email`, `connection_id` and `reauthenticate`, and SHALL refuse with `FAILED_PRECONDITION` and the reason `NOT_APPLICABLE` when the connection does not exist or is not tested. It stores nothing and calls no other service. The tenant named is the tenant the sign-in is for, which need not own the connection: a user who proves an account during account linking signs in through a connection of another tenant. It therefore does not check that the tenant binds the connection or that the address may use it: the callback checks that for a subject with no link (see `company-sign-in`), and the login UI checks every session against the tenant.

A ticket holds the tenant id, the connection id, the address in lower case, whether the identity provider must authenticate the user afresh, the time it was issued and an expiry 30 minutes later. It is sealed with the service's keys (see `secrets`): callers treat it as opaque, and no one else can read or alter it. The login UI hands it to Kratos as the `login_hint` of the `byo-sso` sign-in; it reaches this service as the `login_hint` of hydra-sso's login request, and again in `CompleteAttempt`.

#### Scenario: A draft connection
- **WHEN** an attempt is started for a connection that is a draft or does not exist
- **THEN** the system answers `FAILED_PRECONDITION` with the reason `NOT_APPLICABLE`

#### Scenario: A connection of another tenant
- **WHEN** an attempt is started for tenant T with a tested connection that tenant U owns
- **THEN** the system returns a ticket naming T and that connection; whether a session through it counts at T is the login UI's check against T's policy

#### Scenario: An altered or expired ticket
- **WHEN** a ticket with one character changed, or one issued more than 30 minutes ago, is presented
- **THEN** `/login` and `/callback` refuse the sign-in as expired, and `CompleteAttempt` answers `NOT_APPLICABLE`

### Requirement: Confirm that an attempt ended at an account
The system SHALL, on `CompleteAttempt` with a ticket, an `identity_id` and a `receipt`, return the ticket's `connection_id` and `tenant_id` when all of these hold: the ticket opens and has not expired; Kratos has the account; the account's address equals the ticket's, ignoring case; the account holds a link to the ticket's connection; the receipt is valid (see `secrets`) for the ticket and for the subject of one of the links the account holds at that connection. Otherwise it SHALL answer `FAILED_PRECONDITION` with the reason `NOT_APPLICABLE`, the same for every cause: a receipt that is empty, malformed, made for another ticket or another subject, or made under another key is one more of them. When Kratos does not answer it SHALL answer `UNAVAILABLE`.

The `receipt` is the value of the cookie the callback set in the browser when it accepted the ticket's sign-in (see `company-sign-in`). The login UI reads the cookie named for the ticket it asks about and sends its value, or nothing when the browser has no such cookie. When it confirms two tickets for one session, as after account linking through another company sign-in, each has a receipt of its own.

The call SHALL NOT store or write anything, here, in Kratos or in the tenant service: it creates no membership and removes no account, and a repeat returns the same answer.

What the receipt does not do:

- It is not single use: the same browser can present it again until the ticket expires, 30 minutes at most. That gives nothing beyond the session the sign-in already produced.
- It proves that this browser completed this ticket's sign-in as a subject the account is linked to, not that this particular Kratos session was produced by it.

#### Scenario: The user comes back signed in
- **WHEN** the login UI confirms a ticket with the account that Kratos signed in through the ticket's connection, and the receipt the browser holds for the ticket
- **THEN** it gets the ticket's connection and tenant, and gets them again when it repeats the call

#### Scenario: A sign-in that never reached the identity provider
- **WHEN** an account linked to the connections of two tenants starts a sign-in at the first, leaves it at the identity provider, signs in through the second, and the first ticket is confirmed with that session
- **THEN** the system answers `NOT_APPLICABLE`: the browser has no receipt for the first ticket, and the receipt of the second sign-in was made for another ticket and another subject

#### Scenario: An attempt finished in another browser
- **WHEN** a sign-in started in one browser is completed at the identity provider in another, and the login UI confirms the ticket for the browser that started it, which got a session of the account another way
- **THEN** the system answers `NOT_APPLICABLE`: the receipt is in the browser that completed the sign-in, and the browser that holds the ticket has none

#### Scenario: An account with two subjects at the connection
- **WHEN** the account holds two links to the ticket's connection and the receipt was made for the second
- **THEN** the attempt is confirmed

#### Scenario: No membership is created
- **WHEN** an invited user's first company sign-in is confirmed
- **THEN** the user is not a member of the tenant until the login UI asks the tenant service to make them one

