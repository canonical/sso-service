## MODIFIED Requirements

### Requirement: The browser-binding cookie
The system SHALL use a cookie to bind a sign-in to the browser that the sign-in started in. The name of the cookie SHALL be `__Host-sso_bind_` followed by the first 16 lower-case hex digits of the SHA-256 of the `state`. Thus 2 sign-ins in the same browser do not overwrite each other.

The value of the cookie SHALL be a sealed token (see `secrets`). The token holds the `state`, the nonce, the PKCE verifier, the login challenge and an expiry 30 minutes ahead. The cookie is host-only with `Path=/`, `Secure`, `HttpOnly`, `SameSite=Lax` and a `Max-Age` of 30 minutes. The system SHALL clear the cookie after the callback accepts or rejects the login request.

A browser SHALL keep at most 2 binding cookies. When `/login` sets a binding cookie, the system SHALL clear all the other binding cookies that the browser sent, but for one. The system SHALL keep the cookie whose token opens, is not expired, and expires last. Thus the browser keeps the new cookie and at most 1 older cookie. When `/login` sets no binding cookie, the system SHALL NOT clear a binding cookie.

#### Scenario: The answer is opened in another browser
- **WHEN** a person opens the answer of the identity provider in a browser that did not start the sign-in
- **THEN** the system answers with a page, HTTP 400
- **AND** the page says that the sign-in was not started in this browser
- **AND** the browser that started the sign-in can still complete it

#### Scenario: The answer is sent twice
- **WHEN** a browser sends the same answer again after the system accepted the sign-in
- **THEN** the cookie is gone
- **AND** the system answers HTTP 400

#### Scenario: A third sign-in in the same browser
- **WHEN** a browser that has 2 valid binding cookies starts a third sign-in
- **THEN** the system clears the older of the 2 cookies
- **AND** the browser has 2 binding cookies
- **AND** the answer to the oldest sign-in fails check 2 at the callback

#### Scenario: A binding cookie that is of no use
- **WHEN** a browser sends `/login` a binding cookie that does not open, or whose token is expired
- **THEN** the system clears that cookie when it sets the new binding cookie

### Requirement: The checks at the callback, in order
On `GET /callback/{tenant_id}` with a `state` that is not the `state` of a test (see `test-sign-in`), the system SHALL make these checks in this order. The system SHALL stop at the first check that fails. A failure that the system answers with a page leaves the login request and the cookie unchanged. Every other failure rejects hydra-sso's login request with the reason shown.

| # | check | when it fails |
|---|---|---|
| 1 | the answer has a `state` | page, HTTP 400 |
| 2 | the browser sent the binding cookie of this `state` | page, HTTP 400: already completed, or not started in this browser |
| 3 | the cookie opens and holds this `state` | page, HTTP 400: started in another browser |
| 4 | hydra-sso still knows the cookie's login challenge | page, HTTP 400 (HTTP 500 when the system cannot reach hydra-sso) |
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
