## Purpose

A connection is a draft until someone has signed in through it once. The test sign-in is that proof and the only way a connection becomes tested: it shows that the identity provider's discovery document, the client credentials, the redirect URI and the id_token all work, and that the provider asserts an email address. Only a tested connection can be offered to users or be active in a tenant's policy.

Key decisions:

- **No stored test result.** `tested_at` holds the one fact that matters afterwards; the outcome is shown on the page the test ends on and nowhere else. A table of test runs with an RPC to read one back was turned down: rows and cleanup for a result that is read once.
- **The test is sealed into its OAuth `state`; there is no cookie.** A test is started by an API call, so no response of this service reaches the browser before the identity provider does. Whoever holds the URL can finish the test, in any browser; all a finished test does is mark the connection tested.
- **Tested is permanent.** A provider that breaks later fails its own sign-ins; the status does not go back.

Non-goals: testing as a particular user; checking `email_verified` or the address during a test.

## ADDED Requirements

### Requirement: Start a test sign-in
The system SHALL, on `StartTestLogin` (`POST /api/v0/sso/tenants/{tenant_id}/connections/{connection_id}/test-logins`) for a connection of the tenant in the path, check the identity provider and return the `url` to open in a browser. Nothing is stored and the connection's status does not change. A started test is logged as an admin action.

The check reads the provider's discovery document afresh, never from the cache, and asks its token endpoint to redeem a made-up code with the connection's client credentials. A token endpoint that refuses the code but not the client passes. A failed check is `FAILED_PRECONDITION` (400) with the reason `IDP_CHECK_FAILED` and one of the service's own messages, never the provider's text:

| what failed | message |
|---|---|
| the provider could not be reached, timed out or answered 5xx | `The identity provider could not be reached, or answered with an error.` |
| the token endpoint answered 401, `invalid_client` or `unauthorized_client` | `The identity provider refused the client credentials.` |
| the discovery document names another issuer, has no usable authorization or token endpoint or sits at a forbidden address, or the token endpoint gave no OAuth answer | `The identity provider's discovery document or keys are not usable (issuer, endpoints, JWKS).` |

Before the check, a stored client secret that does not decrypt (the envelope key is not the one it was encrypted under) is answered the same way, with the message `The connection's client secret cannot be read: set it again.`, and the provider is not contacted.

The `url` is the provider's authorization endpoint with the connection's redirect URI, the scopes `openid email`, PKCE (S256), a nonce, `prompt=login`, `max_age=0`, and as `state` a sealed token holding the connection id, the nonce, the PKCE verifier and an expiry 30 minutes ahead.

#### Scenario: The provider and the credentials are usable
- **WHEN** a tenant admin starts a test of a connection whose provider answers and accepts the client
- **THEN** the system returns a `url` at the provider's authorization endpoint, and the connection is still a draft

#### Scenario: A wrong client secret is found at once
- **WHEN** a test is started for a connection whose client secret the provider refuses
- **THEN** the system answers `FAILED_PRECONDITION` with `IDP_CHECK_FAILED: The identity provider refused the client credentials.`

#### Scenario: A client secret stored under another key
- **WHEN** a test is started for a connection whose stored client secret does not decrypt
- **THEN** the system answers `FAILED_PRECONDITION` with `IDP_CHECK_FAILED: The connection's client secret cannot be read: set it again.`

### Requirement: Finish a test sign-in at the connection's redirect URI
The system SHALL treat an answer at `GET /callback/{connection_id}` whose `state` opens as a test's sealed state as a test's. It SHALL answer with a page, HTTP 400, when the state names another connection than the path, has expired, or names a connection that is gone, and with a page, HTTP 500, when the database cannot be read. A `state` that does not open as a test's (altered, or sealed for something else) is taken for a company sign-in's, and is refused there for want of the browser's binding.

Otherwise it SHALL exchange the code and verify the id_token as for a company sign-in (see `identity-provider-requests`) and show the outcome on a page (HTTP 200):

| outcome | page |
|---|---|
| the provider returned an `error` | `Test sign-in failed`: the identity provider refused the sign-in |
| the client secret cannot be decrypted | `Test sign-in failed`: the client secret could not be read |
| the exchange or the id_token checks fail | `Test sign-in failed`, with the service's message for the kind of failure |
| the id_token has no `email` | `Test sign-in failed`: the provider asserted no email address, so members cannot be matched to their accounts |
| everything passes | `Test sign-in succeeded`, naming the connection's label, with a warning when the id_token carries no `auth_time`: apps that ask for a fresh sign-in will fail through such a connection |

A failed test changes nothing. No link, account, session or hydra-sso login is involved in a test.

#### Scenario: A successful test
- **WHEN** an admin opens the `url`, signs in at the identity provider and is sent back to the connection's redirect URI
- **THEN** the page says the test sign-in succeeded and the connection is tested

#### Scenario: The answer arrives at another connection's redirect URI
- **WHEN** a test's answer is opened at `/callback/<another connection's id>`
- **THEN** the system answers HTTP 400 and neither connection changes

### Requirement: A connection is tested once and stays tested
The system SHALL set `tested_at` at the first successful test of a connection and never change or clear it afterwards. The status only moves from draft to tested: a later failed test, a replayed answer, another successful test and a changed client secret all leave `test_time` at the time of the first success.

#### Scenario: The page of a finished test is reloaded
- **WHEN** the browser sends the answer of a successful test a second time
- **THEN** the provider refuses the spent code and the page says the test failed, and that a code works once
- **AND** the connection is still tested, with the `test_time` of the first success
