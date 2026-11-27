## MODIFIED Requirements

### Requirement: Start the sign-in at /login
On `GET /login?login_challenge=<challenge>`, the system SHALL do these steps:

- read hydra-sso's login request
- open the ticket in the `login_hint` of the login request
- set the browser-binding cookie
- redirect the browser (302) to the identity provider's authorization endpoint

The redirect SHALL have these parameters:

- the connection's client id
- the redirect URI of the tenant that owns the connection
- the scopes `openid email`
- a random `state`
- a nonce
- a PKCE challenge (S256)
- the ticket's address as `login_hint`
- `prompt=login` and `max_age=0`, when the ticket asks for re-authentication

The identity provider can ignore `prompt=login` and `max_age`. The system SHALL NOT check that the identity provider asked the user to sign in again.

| when | answer |
|---|---|
| there is no `login_challenge`, or hydra-sso does not know it, or the challenge is expired | a page, HTTP 400 |
| the system cannot reach hydra-sso | a page, HTTP 500 |
| the `login_hint` holds no ticket that opens, or the ticket is expired | the system rejects the login request: `expired` |
| the ticket's connection is gone or not tested, the system cannot read the database, or the system cannot use the identity provider's discovery document | the system rejects the login request: `unavailable` |

#### Scenario: The browser is sent to the identity provider
- **WHEN** hydra-sso sends a browser to `/login` with a login request whose `login_hint` is a valid ticket
- **THEN** the browser gets the binding cookie and a redirect to the identity provider of the ticket's connection
- **AND** the redirect URI is `<PUBLIC_URL>/callback/<tenant id>`, for the tenant that owns the connection

### Requirement: The checks at the callback, in order
On `GET /callback/{tenant_id}` with a `state` that is not the `state` of a test (see `test-sign-in`), the system SHALL make these checks in this order. The system SHALL stop at the first check that fails. A failure that the system answers with a page leaves the login request and the cookie unchanged. Every other failure rejects hydra-sso's login request with the reason shown.

| # | check | when it fails |
|---|---|---|
| 1 | the answer has a `state` | page, HTTP 400 |
| 2 | the browser sent the binding cookie of this `state` | page, HTTP 400: already completed, expired, replaced by a newer sign-in, or not started in this browser |
| 3 | the cookie opens and holds this `state` | page, HTTP 400: started in another browser |
| 4 | hydra-sso still knows the cookie's login challenge, and the challenge is not expired | page, HTTP 400 (HTTP 500 when the system cannot reach hydra-sso) |
| 5 | the ticket in the `login_hint` of the login request opens | `expired` |
| 6 | neither the cookie nor the ticket is expired | `expired` |
| 7 | the database answers, and the ticket's connection exists | `unavailable` |
| 8 | the tenant in the path owns the ticket's connection | `invalid_token` |
| 9 | the identity provider returned no `error` | `idp_refused` |
| 10 | the client secret decrypts | `unavailable` |
| 11 | the system exchanges the code and the id_token passes its checks (see `identity-provider-requests`) | `unavailable` (the system cannot reach the identity provider or its keys, the identity provider refuses the credentials, or the discovery is not usable), `invalid_token` (the id_token fails a check), `idp_refused` (the code is refused) |
| 12 | the id_token's `email` equals the ticket's address (the comparison ignores case) | `address_mismatch` |
| 13 | the id_token's `email_verified` is not `false` (the system accepts an id_token that has no `email_verified`) | `address_unconfirmed` |
| 14 | Kratos answers which account holds the link `byo-sso:<connection id>:<subject>` | `unavailable` |
| 15 | when an account holds the link: that account's address equals the ticket's address (the comparison ignores case) | `already_linked` |
| 16 | when no account holds the link: the rules of the next requirement | see that requirement |

When check 15 passes, the system accepts the login request and counts the outcome as `linked_existing`.

#### Scenario: A returning user
- **WHEN** a user signs in at the identity provider with the address that the user entered
- **AND** the account of the user holds a link for the subject
- **THEN** the system accepts the login request and does not call the tenant service
- **AND** the system counts the outcome as `linked_existing`

#### Scenario: The answer comes to another tenant's redirect URI
- **WHEN** an answer for a sign-in through a connection of tenant A arrives, with its cookie, at `/callback/<B>`
- **THEN** the system rejects the login request with `invalid_token` before it sends the code anywhere

#### Scenario: Re-authentication is only a request
- **WHEN** a sign-in that asked for re-authentication comes back with an id_token that has no `auth_time`
- **THEN** the system makes the same checks as for any other sign-in

### Requirement: Accept the login and the consent at hydra-sso
The system SHALL accept hydra-sso's login request with the subject `<connection id>:<subject>`, not remembered, and with a context. The context holds the ticket's address. The context holds `email_verified` only when the identity provider said it. Then the system redirects the browser (303) to hydra-sso, with the receipt cookie.

On `GET /consent?consent_challenge=<challenge>` the system SHALL accept the consent request with the scopes and audiences that the request asks for, not remembered. The system SHALL put `email` from that context into the id_token, and also `email_verified` when it is present. Then the system redirects the browser (302). Thus Kratos receives an id_token whose `sub` is `<connection id>:<subject>`. Because nothing is remembered, every company sign-in goes to the identity provider. The system answers a missing, unknown or expired `consent_challenge` with a page, HTTP 400.

#### Scenario: The provider says nothing about the address
- **WHEN** the identity provider's id_token has no `email_verified`
- **THEN** the id_token that hydra-sso issues has `email` and no `email_verified`
