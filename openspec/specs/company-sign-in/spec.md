# company-sign-in Specification

## Purpose

This capability is the browser side of a company sign-in. Kratos knows one OIDC provider, `byo-sso`, and that provider is `hydra-sso`. This service is the login and consent provider of hydra-sso. A company sign-in has these steps:

- hydra-sso sends the browser to `/login`.
- The service sends the browser on to the tenant's identity provider.
- The identity provider sends the browser back to `/callback/{connection_id}`.
- The service decides if hydra-sso can tell Kratos "this is subject S of connection C, with this address".

The pages take no access token. A sign-in's ticket and a cookie tie the sign-in together. An accepted sign-in leaves a receipt for the login UI in the browser.

Key decisions:

- **A redirect URI per connection.** The service takes an answer only at the path of the connection that the sign-in started through. Thus a person who controls one connection's identity provider cannot make the service handle its answer as another connection's answer. For that reason we turned down one shared `/callback`.
- **A browser-binding cookie instead of a stored attempt.** The service sets a sealed cookie at `/login`. The callback takes the nonce, the PKCE verifier and the login challenge from that cookie, and never from the URL. An answer that a person opens in another browser does nothing.
- **A receipt cookie for the browser that signed in.** The login UI must know that the sign-in that a ticket started is complete. The login UI must also know that the browser that it talks to completed that sign-in. Thus the callback sets a cookie, and the login UI passes that cookie on to `CompleteAttempt`. `sign-in-attempts` says why the service keeps the proof in the browser and not on a server. The cookie holds nothing about the user.
- **Kratos writes every link.** For a subject with no link, this service only decides if the sign-in can continue. Then Kratos registers a new account, or links the subject to the existing account after the user signs in to that account another way. We turned down a design in which this service writes the link. In that design, the service must prove on its own that the user owns the account.
- **This service does not check a subject that already has a link against the tenant.** The login UI does those checks. For every session, it checks membership and the tenant's policy before it accepts an app's login.
- **The user never reads text that an identity provider controls.** Every refusal has a fixed message.

Non-goals:

- the sign-in screen and the choice of tenant
- sessions
- MFA
- memberships
- styled or translated pages

## Requirements
### Requirement: Start the sign-in at /login
On `GET /login?login_challenge=<challenge>`, the system SHALL do these steps:

- read hydra-sso's login request
- open the ticket in the `login_hint` of the login request
- set the browser-binding cookie
- redirect the browser (302) to the identity provider's authorization endpoint

The redirect SHALL have these parameters:

- the connection's client id and redirect URI
- the scopes `openid email`
- a random `state`
- a nonce
- a PKCE challenge (S256)
- the ticket's address as `login_hint`
- `prompt=login` and `max_age=0`, when the ticket asks for re-authentication

| when | answer |
|---|---|
| there is no `login_challenge`, or hydra-sso does not know it | a page, HTTP 400 |
| the system cannot reach hydra-sso | a page, HTTP 500 |
| the `login_hint` holds no ticket that opens, or the ticket is expired | the system rejects the login request: `expired` |
| the ticket's connection is gone or not tested, the system cannot read the database, or the system cannot use the identity provider's discovery document | the system rejects the login request: `unavailable` |

#### Scenario: The browser is sent to the identity provider
- **WHEN** hydra-sso sends a browser to `/login` with a login request whose `login_hint` is a valid ticket
- **THEN** the browser gets the binding cookie and a redirect to the identity provider of the ticket's connection
- **AND** the redirect URI is `<PUBLIC_URL>/callback/<connection id>`

### Requirement: The browser-binding cookie
The system SHALL use a cookie to bind a sign-in to the browser that the sign-in started in. The name of the cookie SHALL be `__Host-sso_bind_` followed by 16 hex characters of the SHA-256 of the `state`. Thus 2 sign-ins in the same browser do not overwrite each other.

The value of the cookie SHALL be a sealed token (see `secrets`). The token holds the `state`, the nonce, the PKCE verifier, the login challenge and an expiry 30 minutes ahead. The cookie is host-only with `Path=/`, `Secure`, `HttpOnly`, `SameSite=Lax` and a `Max-Age` of 30 minutes. The system SHALL clear the cookie after the callback accepts or rejects the login request.

#### Scenario: The answer is opened in another browser
- **WHEN** a person opens the answer of the identity provider in a browser that did not start the sign-in
- **THEN** the system answers with a page, HTTP 400
- **AND** the page says that the sign-in was not started in this browser
- **AND** the browser that started the sign-in can still complete it

#### Scenario: The answer is sent twice
- **WHEN** a browser sends the same answer again after the system accepted the sign-in
- **THEN** the cookie is gone
- **AND** the system answers HTTP 400

### Requirement: The receipt cookie
The system SHALL set a receipt cookie in the response of `GET /callback/{connection_id}` that accepts a sign-in. That response is the response that redirects the browser to hydra-sso. The name of the cookie is `__Host-sso_receipt_` followed by the first 16 lower-case hex digits of the SHA-256 of the ticket. Thus each attempt has a cookie of its own.

The value of the cookie is the receipt (see `secrets`) of the ticket and of the subject. For the receipt, the ticket is the text in the `login_hint` of the login request. The subject is the subject that the system accepts the login request with, `<connection id>:<subject>`. The cookie is host-only with `Path=/`, `Secure`, `HttpOnly`, `SameSite=Lax` and a `Max-Age` that ends when the ticket expires.

The system SHALL set the receipt cookie for every accepted sign-in. That includes the sign-in of each of these subjects:

- a subject that has a link
- a subject that Kratos will link to an account
- a subject that Kratos will register an account for

The system SHALL NOT set the receipt cookie for a refused sign-in or for a test sign-in. The value is a nonce and an authentication tag. A person cannot read anything about the user in the value, and cannot recover anything about the user from the value.

The system never reads the receipt cookie and never clears it. The login UI reads the cookie by its name and sends its value in `CompleteAttempt` (see `sign-in-attempts`). The login UI clears the cookie when it is done with the attempt. If the login UI does not clear the cookie, the cookie expires with the ticket.

The deployment must serve these pages and the login UI on the same host. As a result, the browser sends the login UI a cookie that this service set. Browsers do not keep cookies apart by port, and the cookie's path is `/`. The same requirement already applies to Kratos and the login UI.

#### Scenario: An accepted sign-in
- **WHEN** the callback accepts a sign-in
- **THEN** the redirect to hydra-sso sets one receipt cookie, named for the ticket
- **AND** the value of the cookie is valid for the ticket and the accepted subject

#### Scenario: A refused sign-in
- **WHEN** the callback rejects the login request, or answers with a page
- **THEN** the system sets no receipt cookie

#### Scenario: The login UI is on another host
- **WHEN** the deployment serves the login UI on a host that is not the host in `PUBLIC_URL`
- **THEN** the browser does not send the receipt to the login UI
- **AND** every company sign-in ends with the answer `NOT_APPLICABLE` from `CompleteAttempt`

### Requirement: The checks at the callback, in order
On `GET /callback/{connection_id}` with a `state` that is not the `state` of a test (see `test-sign-in`), the system SHALL make these checks in this order. The system SHALL stop at the first check that fails. A failure that the system answers with a page leaves the login request and the cookie unchanged. Every other failure rejects hydra-sso's login request with the reason shown.

| # | check | when it fails |
|---|---|---|
| 1 | the answer has a `state` | page, HTTP 400 |
| 2 | the browser sent the binding cookie of this `state` | page, HTTP 400: already completed, or not started in this browser |
| 3 | the cookie opens and holds this `state` | page, HTTP 400: started in another browser |
| 4 | hydra-sso still knows the cookie's login challenge | page, HTTP 400 (HTTP 500 when the system cannot reach hydra-sso) |
| 5 | the ticket in the `login_hint` of the login request opens | `expired` |
| 6 | neither the cookie nor the ticket is expired | `expired` |
| 7 | the ticket's connection is the connection in the path | `invalid_token` |
| 8 | the database answers, and the connection exists | `unavailable` |
| 9 | the identity provider returned no `error` | `idp_refused` |
| 10 | the client secret decrypts | `unavailable` |
| 11 | the system exchanges the code and the id_token passes its checks (see `identity-provider-requests`) | `unavailable` (the system cannot reach the identity provider or its keys, the identity provider refuses the credentials, or the discovery is not usable), `invalid_token` (the id_token fails a check), `idp_refused` (the code is refused) |
| 12 | when the ticket asks for re-authentication: `auth_time` is present and not earlier than 60 seconds before the ticket was issued | `reauthentication_not_done` |
| 13 | the id_token's `email` equals the ticket's address (the comparison ignores case) | `address_mismatch` |
| 14 | the id_token's `email_verified` is not `false` (the system accepts an id_token that has no `email_verified`) | `address_unconfirmed` |
| 15 | Kratos answers which account holds the link `byo-sso:<connection id>:<subject>` | `unavailable` |
| 16 | when an account holds the link: that account's address equals the ticket's address (the comparison ignores case) | `already_linked` |
| 17 | when no account holds the link: the rules of the next requirement | see that requirement |

#### Scenario: A returning user
- **WHEN** a user signs in at the identity provider with the address that the user entered
- **AND** the account of the user holds a link for the subject
- **THEN** the system accepts the login request and does not call the tenant service

#### Scenario: The answer comes to another connection's redirect URI
- **WHEN** an answer for a sign-in that started through connection A arrives, with its cookie, at `/callback/<B>`
- **THEN** the system rejects the login request with `invalid_token` before it sends the code anywhere

#### Scenario: Re-authentication the provider cannot prove
- **WHEN** a sign-in that asked for re-authentication comes back with an id_token that has no `auth_time`
- **THEN** the system rejects the login request with `reauthentication_not_done`

### Requirement: A subject with no link
The system SHALL let a subject with no link through only when all of these checks pass. In all other cases the system SHALL reject the login request:

| check | when it fails |
|---|---|
| Kratos answers which accounts have the ticket's address as an identifier | `unavailable` |
| at most one account has that address | `already_linked` |
| that account's address equals the ticket's address | `address_mismatch` |
| the tenant service answers with the sign-in context of the ticket's tenant, for that account or, when there is no account, for the address | `unavailable` |
| an active binding of the tenant to this connection applies to the address | `not_a_member` |
| the account is a member of the tenant, or a pending invitation or auto-join admits the address | `not_a_member` |

The system SHALL NOT write a link, an account or a membership. After the system accepts the login request, Kratos does one of these actions:

- Kratos adds the link to the existing account, after the user signs in to that account another way. The system counts this outcome as `account_linking`.
- Kratos registers an account for an address that has no account. The system counts this outcome as `registration`.

#### Scenario: A member's first company sign-in
- **WHEN** a member of the tenant with an account signs in through the tenant's active connection for the first time
- **THEN** the system accepts the login request and counts the outcome as `account_linking`

#### Scenario: An invited address with no account
- **WHEN** an address with a pending invitation to the tenant and no account signs in through the tenant's active connection
- **THEN** the system accepts the login request and counts the outcome as `registration`

#### Scenario: Neither a member nor admitted
- **WHEN** the address is not the address of a member, and no invitation or auto-join admits the address
- **THEN** the system rejects the login request with `not_a_member`
- **AND** no account is created
- **AND** the result is the same when no active binding of the tenant to the connection applies to the address

### Requirement: Accept the login and the consent at hydra-sso
The system SHALL accept hydra-sso's login request with the subject `<connection id>:<subject>`, not remembered, and with a context. The context holds the ticket's address. The context holds `email_verified` only when the identity provider said it. Then the system redirects the browser (303) to hydra-sso, with the receipt cookie.

On `GET /consent?consent_challenge=<challenge>` the system SHALL accept the consent request with the scopes and audiences that the request asks for, not remembered. The system SHALL put `email` from that context into the id_token, and also `email_verified` when it is present. Then the system redirects the browser (302). Thus Kratos receives an id_token whose `sub` is `<connection id>:<subject>`. Because nothing is remembered, every company sign-in goes to the identity provider. The system answers a missing or unknown `consent_challenge` with a page, HTTP 400.

#### Scenario: The provider says nothing about the address
- **WHEN** the identity provider's id_token has no `email_verified`
- **THEN** the id_token that hydra-sso issues has `email` and no `email_verified`

### Requirement: What a refused user is told
The system SHALL reject a login request at hydra-sso with the error `access_denied`. The description of the error SHALL be the fixed message of the reason. The system redirects the browser to the place that hydra-sso gives. The message travels back through hydra-sso and Kratos to the login UI. The messages are the service's own messages and never contain text from an identity provider:

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

A refusal with no login request to reject is a page titled `Sign-in failed`. The answer to `GET /error` is also a page titled `Sign-in failed`, with HTTP 400. The page is for a browser that hydra-sso sends there with an error that it was not able to give to its client. The system logs that error.

Every page is HTML with `Cache-Control: no-store`, a content security policy that allows no scripts, no forms and no framing, and `X-Frame-Options: DENY`. The pages answer `GET` only. The system answers `/callback` without a connection id with HTTP 404.

#### Scenario: The provider's error text does not reach the user
- **WHEN** the identity provider sends the browser back with `error=access_denied&error_description=<any text>`
- **THEN** the system rejects the login request with the message of `idp_refused`
- **AND** the identity provider's text appears only in the log
- **AND** the log has that text cut to 120 bytes, with control characters removed

