# company-sign-in Specification

## Purpose

This is the browser side of a company sign-in. Kratos knows one OIDC provider, `byo-sso`, which is `hydra-sso`; this service is hydra-sso's login and consent provider. hydra-sso sends the browser to `/login`, the service sends it on to the tenant's identity provider, the provider sends it back to `/callback/{connection_id}`, and the service decides whether hydra-sso may tell Kratos "this is subject S of connection C, with this address". The pages take no access token: a sign-in is tied together by its ticket and a cookie, and an accepted one leaves the browser a receipt for the login UI.

Key decisions:

- **A redirect URI per connection.** An answer is only taken at the path of the connection the sign-in was started through, so whoever controls one connection's identity provider cannot have its answer handled as another connection's. One shared `/callback` was turned down for that reason.
- **A browser-binding cookie instead of a stored attempt.** The callback takes the nonce, the PKCE verifier and the login challenge from a sealed cookie set at `/login`, never from the URL. An answer opened in another browser does nothing.
- **A receipt cookie for the browser that signed in.** The login UI has to know that the sign-in a ticket started was completed, and in the browser it is talking to. The callback therefore sets a cookie the login UI passes on to `CompleteAttempt`; `sign-in-attempts` says why the proof is kept in the browser and not on a server. The cookie holds nothing about the user.
- **Kratos writes every link.** For a subject with no link this service only decides whether the sign-in may go on; Kratos then links it to the existing account, once the user has signed in to that account another way, or registers a new account. Writing the link from here was turned down: the service would have to prove on its own that the user owns the account.
- **A subject that already has a link is not checked against the tenant here.** The login UI checks membership and the tenant's policy for every session before it accepts an app's login.
- **The user never reads text an identity provider controls.** Every refusal has a fixed message.

Non-goals: the sign-in screen and the choice of tenant; sessions; MFA; memberships; styled or translated pages.

## Requirements
### Requirement: Start the sign-in at /login
The system SHALL, on `GET /login?login_challenge=<challenge>`, read hydra-sso's login request, open the ticket in its `login_hint`, set the browser-binding cookie and redirect the browser (302) to the identity provider's authorization endpoint with the connection's client id and redirect URI, the scopes `openid email`, a random `state`, a nonce, a PKCE challenge (S256), the ticket's address as `login_hint`, and, when the ticket asks for re-authentication, `prompt=login` and `max_age=0`.

| when | answer |
|---|---|
| there is no `login_challenge`, or hydra-sso does not know it | a page, HTTP 400 |
| hydra-sso cannot be reached | a page, HTTP 500 |
| the `login_hint` holds no ticket that opens, or the ticket has expired | the login request is rejected: `expired` |
| the ticket's connection is gone or not tested, the database cannot be read, or the provider's discovery document cannot be used | the login request is rejected: `unavailable` |

#### Scenario: The browser is sent to the identity provider
- **WHEN** hydra-sso sends a browser to `/login` with a login request whose `login_hint` is a valid ticket
- **THEN** the browser gets the binding cookie and a redirect to the provider of the ticket's connection, with the redirect URI `<PUBLIC_URL>/callback/<connection id>`

### Requirement: The browser-binding cookie
The system SHALL bind a sign-in to the browser it started in with a cookie named `__Host-sso_bind_` followed by 16 hex characters of the SHA-256 of the `state`, so that two sign-ins in one browser do not overwrite each other. Its value SHALL be a sealed token (see `secrets`) holding the `state`, the nonce, the PKCE verifier, the login challenge and an expiry 30 minutes ahead. It is host-only with `Path=/`, `Secure`, `HttpOnly`, `SameSite=Lax` and a `Max-Age` of 30 minutes. The system SHALL clear the cookie once the callback has accepted or rejected the login request.

#### Scenario: The answer is opened in another browser
- **WHEN** the identity provider's answer is opened in a browser that did not start the sign-in
- **THEN** the system answers with a page, HTTP 400, saying the sign-in was not started in this browser
- **AND** the browser that started the sign-in can still complete it

#### Scenario: The answer is sent twice
- **WHEN** a browser sends the same answer again after the sign-in was accepted
- **THEN** the cookie is gone and the system answers HTTP 400

### Requirement: The receipt cookie
The system SHALL set a receipt cookie in the response of `GET /callback/{connection_id}` that accepts a sign-in, the response that redirects the browser to hydra-sso. Its name is `__Host-sso_receipt_` followed by the first 16 lower-case hex digits of the SHA-256 of the ticket, so that each attempt has a cookie of its own. Its value is the receipt (see `secrets`) of the ticket, as the text in the login request's `login_hint`, and of the subject the login request is accepted with, `<connection id>:<subject>`. It is host-only with `Path=/`, `Secure`, `HttpOnly`, `SameSite=Lax` and a `Max-Age` that ends when the ticket expires.

The system SHALL set it for every accepted sign-in: of a subject that has a link, of one Kratos will link to an account, and of one Kratos will register an account for. It SHALL NOT set it for a refused sign-in or for a test sign-in. The value is a nonce and an authentication tag: nothing about the user can be read in it or recovered from it.

The system never reads the cookie and never clears it. The login UI reads it by its name, sends its value in `CompleteAttempt` (see `sign-in-attempts`) and clears it when it is done with the attempt; otherwise it expires with the ticket.

The deployment has to serve these pages and the login UI on the same host, so that the browser sends the login UI a cookie this service set: cookies are not kept apart by port, and the cookie's path is `/`. The same is already required of Kratos and the login UI.

#### Scenario: An accepted sign-in
- **WHEN** the callback accepts a sign-in
- **THEN** the redirect to hydra-sso sets one receipt cookie, named for the ticket, whose value is valid for the ticket and the accepted subject

#### Scenario: A refused sign-in
- **WHEN** the callback rejects the login request, or answers with a page
- **THEN** no receipt cookie is set

#### Scenario: The login UI is on another host
- **WHEN** the login UI is served on a host other than the one in `PUBLIC_URL`
- **THEN** the browser does not send it the receipt, and every company sign-in ends with `CompleteAttempt` answering `NOT_APPLICABLE`

### Requirement: The checks at the callback, in order
The system SHALL, on `GET /callback/{connection_id}` with a `state` that is not a test's (see `test-sign-in`), make these checks in this order and stop at the first that fails. A failure answered with a page leaves the login request and the cookie untouched; every other failure rejects hydra-sso's login request with the reason shown.

| # | check | when it fails |
|---|---|---|
| 1 | the answer has a `state` | page, HTTP 400 |
| 2 | the browser sent the binding cookie of this `state` | page, HTTP 400: already completed, or not started in this browser |
| 3 | the cookie opens and holds this `state` | page, HTTP 400: started in another browser |
| 4 | hydra-sso still knows the cookie's login challenge | page, HTTP 400 (HTTP 500 when hydra-sso cannot be reached) |
| 5 | the ticket in the login request's `login_hint` opens | `expired` |
| 6 | neither the cookie nor the ticket has expired | `expired` |
| 7 | the ticket's connection is the connection in the path | `invalid_token` |
| 8 | the database answers, and the connection exists | `unavailable` |
| 9 | the provider returned no `error` | `idp_refused` |
| 10 | the client secret decrypts | `unavailable` |
| 11 | the code is exchanged and the id_token passes its checks (see `identity-provider-requests`) | `unavailable` (provider or its keys unreachable, credentials refused, discovery unusable), `invalid_token` (the id_token fails a check), `idp_refused` (the code is refused) |
| 12 | when the ticket asks for re-authentication: `auth_time` is present and not earlier than 60 seconds before the ticket was issued | `reauthentication_not_done` |
| 13 | the id_token's `email` equals the ticket's address, ignoring case | `address_mismatch` |
| 14 | the id_token's `email_verified` is not `false` (absent is accepted) | `address_unconfirmed` |
| 15 | Kratos answers which account holds the link `byo-sso:<connection id>:<subject>` | `unavailable` |
| 16 | an account holds the link: its address equals the ticket's, ignoring case | `already_linked` |
| 17 | no account holds the link: the rules of the next requirement | see there |

#### Scenario: A returning user
- **WHEN** a user whose account holds a link for the subject signs in at the provider with the address they entered
- **THEN** the login request is accepted without a call to the tenant service

#### Scenario: The answer comes to another connection's redirect URI
- **WHEN** an answer for a sign-in started through connection A arrives, with its cookie, at `/callback/<B>`
- **THEN** the login request is rejected with `invalid_token`, before the code is sent anywhere

#### Scenario: Re-authentication the provider cannot prove
- **WHEN** a sign-in that asked for re-authentication comes back with an id_token that has no `auth_time`
- **THEN** the login request is rejected with `reauthentication_not_done`

### Requirement: A subject with no link
The system SHALL let a subject with no link through only when all of these hold, and SHALL reject the login request otherwise:

| check | when it fails |
|---|---|
| Kratos answers which accounts have the ticket's address as an identifier | `unavailable` |
| at most one account has it | `already_linked` |
| that account's address equals the ticket's | `address_mismatch` |
| the tenant service answers with the sign-in context of the ticket's tenant, for that account or, with no account, for the address | `unavailable` |
| an active binding of the tenant to this connection applies to the address | `not_a_member` |
| the account is a member of the tenant, or a pending invitation or auto-join admits the address | `not_a_member` |

The system SHALL NOT write a link, an account or a membership. Once it has accepted the login request, Kratos adds the link to the existing account after the user has signed in to it another way (counted as `account_linking`), or registers an account for an address that has none (`registration`).

#### Scenario: A member's first company sign-in
- **WHEN** a member of the tenant with an account signs in through the tenant's active connection for the first time
- **THEN** the login request is accepted and the outcome is counted as `account_linking`

#### Scenario: An invited address with no account
- **WHEN** an address with a pending invitation to the tenant and no account signs in through the tenant's active connection
- **THEN** the login request is accepted and the outcome is counted as `registration`

#### Scenario: Neither a member nor admitted
- **WHEN** the address is no member's and no invitation or auto-join admits it, or the tenant has no active binding to the connection that applies to it
- **THEN** the login request is rejected with `not_a_member` and no account is created

### Requirement: Accept the login and the consent at hydra-sso
The system SHALL accept hydra-sso's login request with the subject `<connection id>:<subject>`, not remembered, and a context holding the ticket's address and, only when the provider said it, `email_verified`; it then redirects the browser (303) to hydra-sso, with the receipt cookie. On `GET /consent?consent_challenge=<challenge>` it SHALL accept the consent request with the scopes and audiences asked for, not remembered, and put `email`, and `email_verified` when present, from that context into the id_token; it then redirects the browser (302). Kratos so receives an id_token whose `sub` is `<connection id>:<subject>`. Because nothing is remembered, every company sign-in goes to the identity provider. A missing or unknown `consent_challenge` is a page, HTTP 400.

#### Scenario: The provider says nothing about the address
- **WHEN** the provider's id_token has no `email_verified`
- **THEN** the id_token hydra-sso issues has `email` and no `email_verified`

### Requirement: What a refused user is told
The system SHALL reject a login request at hydra-sso with the error `access_denied` and, as its description, the fixed message of the reason; the browser is redirected to where hydra-sso says, and the message travels back through hydra-sso and Kratos to the login UI. The messages are the service's own and never contain text from an identity provider:

| reason | message |
|---|---|
| `idp_refused` | Your tenant's sign-in refused the request. |
| `invalid_token` | Your tenant's sign-in returned an answer that could not be accepted. |
| `reauthentication_not_done` | Your tenant's sign-in did not ask you to sign in again, as this app requires. |
| `address_mismatch` | The account you used at your tenant's sign-in does not match the email address you entered. |
| `address_unconfirmed` | Your tenant's sign-in says your email address is not confirmed. |
| `not_a_member` | This account cannot sign in to this tenant through its company sign-in. |
| `already_linked` | This account at your tenant's sign-in is already linked to another account. |
| `unavailable` | Company sign-in is unavailable right now. Please try again later. |
| `expired` | This sign-in has expired. Please start again. |

A refusal with no login request to reject is a page titled `Sign-in failed`. So is `GET /error`, where hydra-sso sends a browser with an error it could not give its client: HTTP 400, with the error logged. Every page is HTML with `Cache-Control: no-store`, a content security policy that allows no scripts, no forms and no framing, and `X-Frame-Options: DENY`. The pages answer `GET` only, and `/callback` without a connection id is HTTP 404.

#### Scenario: The provider's error text does not reach the user
- **WHEN** the provider sends the browser back with `error=access_denied&error_description=<any text>`
- **THEN** the login request is rejected with the message of `idp_refused`, and the provider's text appears only in the log, cut to 120 bytes with control characters removed

