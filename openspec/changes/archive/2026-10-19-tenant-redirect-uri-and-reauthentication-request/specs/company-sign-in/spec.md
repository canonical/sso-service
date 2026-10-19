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
| there is no `login_challenge`, or hydra-sso does not know it | a page, HTTP 400 |
| the system cannot reach hydra-sso | a page, HTTP 500 |
| the `login_hint` holds no ticket that opens, or the ticket is expired | the system rejects the login request: `expired` |
| the ticket's connection is gone or not tested, the system cannot read the database, or the system cannot use the identity provider's discovery document | the system rejects the login request: `unavailable` |

#### Scenario: The browser is sent to the identity provider
- **WHEN** hydra-sso sends a browser to `/login` with a login request whose `login_hint` is a valid ticket
- **THEN** the browser gets the binding cookie and a redirect to the identity provider of the ticket's connection
- **AND** the redirect URI is `<PUBLIC_URL>/callback/<tenant id>`, for the tenant that owns the connection

### Requirement: The receipt cookie
The system SHALL set a receipt cookie in the response of `GET /callback/{tenant_id}` that accepts a sign-in. That response is the response that redirects the browser to hydra-sso. The name of the cookie is `__Host-sso_receipt_` followed by the first 16 lower-case hex digits of the SHA-256 of the ticket. Thus each attempt has a cookie of its own.

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

#### Scenario: A returning user
- **WHEN** a user signs in at the identity provider with the address that the user entered
- **AND** the account of the user holds a link for the subject
- **THEN** the system accepts the login request and does not call the tenant service

#### Scenario: The answer comes to another tenant's redirect URI
- **WHEN** an answer for a sign-in through a connection of tenant A arrives, with its cookie, at `/callback/<B>`
- **THEN** the system rejects the login request with `invalid_token` before it sends the code anywhere

#### Scenario: Re-authentication is only a request
- **WHEN** a sign-in that asked for re-authentication comes back with an id_token that has no `auth_time`
- **THEN** the system makes the same checks as for any other sign-in

### Requirement: What a refused user is told
The system SHALL reject a login request at hydra-sso with the error `access_denied`. The description of the error SHALL be the fixed message of the reason. The system redirects the browser to the place that hydra-sso gives. The message travels back through hydra-sso and Kratos to the login UI. The messages are the service's own messages and never contain text from an identity provider:

| reason | message |
|---|---|
| `idp_refused` | Your tenant's sign-in refused the request. |
| `invalid_token` | Your tenant's sign-in returned an answer that could not be accepted. |
| `address_mismatch` | The account you used at your tenant's sign-in does not match the email address you entered. |
| `address_unconfirmed` | Your tenant's sign-in says your email address is not confirmed. |
| `not_a_member` | This account cannot sign in to this tenant through its company sign-in. |
| `already_linked` | This account at your tenant's sign-in is already linked to another account. |
| `unavailable` | Company sign-in is unavailable right now. Please try again later. |
| `expired` | This sign-in has expired. Please start again. |

A refusal with no login request to reject is a page titled `Sign-in failed`. The answer to `GET /error` is also a page titled `Sign-in failed`, with HTTP 400. The page is for a browser that hydra-sso sends there with an error that it was not able to give to its client. The system logs that error.

Every page is HTML with `Cache-Control: no-store`, a content security policy that allows no scripts, no forms and no framing, and `X-Frame-Options: DENY`. The pages answer `GET` only. The system answers `/callback` without a tenant id with HTTP 404.

#### Scenario: The provider's error text does not reach the user
- **WHEN** the identity provider sends the browser back with `error=access_denied&error_description=<any text>`
- **THEN** the system rejects the login request with the message of `idp_refused`
- **AND** the identity provider's text appears only in the log
- **AND** the log has that text cut to 120 bytes, with control characters removed
