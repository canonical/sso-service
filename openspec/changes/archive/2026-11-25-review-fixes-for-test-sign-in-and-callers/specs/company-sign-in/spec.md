## MODIFIED Requirements

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
- **AND** the log keeps only the printable characters of that text
- **AND** the log cuts that text to at most 120 bytes, between 2 characters
