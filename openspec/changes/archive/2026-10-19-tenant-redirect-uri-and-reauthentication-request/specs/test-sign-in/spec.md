## MODIFIED Requirements

### Requirement: Start a test sign-in
On `StartTestLogin` (`POST /api/v0/sso/tenants/{tenant_id}/connections/{connection_id}/test-logins`) for a connection of the tenant in the path, the system SHALL check the identity provider. The system SHALL then return the `url` to open in a browser. The system stores nothing, and the connection's status does not change. The system logs a started test as an admin action.

The check always gets the identity provider's discovery document, and never from the cache. The check also asks the identity provider's token endpoint to redeem a made-up code with the connection's client credentials. A token endpoint that refuses the code but not the client passes the check. For a failed check, the system answers `FAILED_PRECONDITION` (400) with the reason `IDP_CHECK_FAILED` and one of the service's own messages. The answer never has the text of the identity provider:

| what failed | message |
|---|---|
| the system was not able to reach the identity provider, or the identity provider timed out or answered 5xx | `The identity provider could not be reached, or answered with an error.` |
| the token endpoint answered 401, `invalid_client` or `unauthorized_client` | `The identity provider refused the client credentials.` |
| the discovery document names another issuer, has no usable authorization or token endpoint or sits at a forbidden address, or the token endpoint gave no OAuth answer | `The identity provider's discovery document or keys are not usable (issuer, endpoints, JWKS).` |

A stored client secret does not decrypt when the envelope key is not the key that the secret was encrypted under. Before the check, the system gives the same answer for such a client secret, with the message `The connection's client secret cannot be read: set it again.`, and does not contact the identity provider.

The `url` is the identity provider's authorization endpoint with these parameters:

- the redirect URI of the tenant that owns the connection
- the scopes `openid email`
- PKCE (S256)
- a nonce
- a sealed token as `state`

The sealed token holds the connection id, the nonce, the PKCE verifier and an expiry 30 minutes ahead.

#### Scenario: The provider and the credentials are usable
- **WHEN** a tenant admin starts a test of a connection whose identity provider answers and accepts the client
- **THEN** the system returns a `url` at the identity provider's authorization endpoint
- **AND** the connection is still a draft

#### Scenario: A wrong client secret is found at once
- **WHEN** someone starts a test for a connection whose client secret the identity provider refuses
- **THEN** the system answers `FAILED_PRECONDITION` with `IDP_CHECK_FAILED: The identity provider refused the client credentials.`

#### Scenario: A client secret stored under another key
- **WHEN** someone starts a test for a connection whose stored client secret does not decrypt
- **THEN** the system answers `FAILED_PRECONDITION` with `IDP_CHECK_FAILED: The connection's client secret cannot be read: set it again.`

### Requirement: Finish a test sign-in at the connection's redirect URI
The system SHALL treat an answer at `GET /callback/{tenant_id}` as the answer of a test when its `state` opens as a test's sealed state. The system SHALL answer with a page, HTTP 400, when the state has one of these faults:

- the state is expired
- the state names a connection that is gone
- the tenant in the path does not own the state's connection

The system SHALL answer with a page, HTTP 500, when it cannot read the database. A `state` that does not open as a test's state is altered, or is sealed for something else. The system takes such a `state` for the `state` of a company sign-in. The system refuses it there, because the browser's binding is missing.

In all other cases the system SHALL exchange the code and verify the id_token as for a company sign-in (see `identity-provider-requests`). The system SHALL show the outcome on a page (HTTP 200):

| outcome | page |
|---|---|
| the identity provider returned an `error` | `Test sign-in failed`: the identity provider refused the sign-in |
| the system cannot decrypt the client secret | `Test sign-in failed`: the client secret could not be read |
| the exchange or the id_token checks fail | `Test sign-in failed`, with the service's message for the kind of failure |
| the id_token has no `email` | `Test sign-in failed`: the provider asserted no email address, so members cannot be matched to their accounts |
| everything passes | `Test sign-in succeeded`, with the connection's label |

A failed test changes nothing. A test involves no link, account, session or hydra-sso login.

#### Scenario: A successful test
- **WHEN** an admin opens the `url` and signs in at the identity provider
- **AND** the identity provider sends the admin back to the connection's redirect URI
- **THEN** the page says that the test sign-in succeeded
- **AND** the connection is tested

#### Scenario: The answer arrives at another tenant's redirect URI
- **WHEN** a person opens the answer of a test at `/callback/<another tenant's id>`
- **THEN** the system answers HTTP 400
- **AND** no connection changes
