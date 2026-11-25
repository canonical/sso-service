## MODIFIED Requirements

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
| the id_token has `email_verified: false` | `Test sign-in failed`: `The identity provider marks this account's email address as not confirmed, so its sign-ins would be refused.` |
| everything passes | `Test sign-in succeeded`, with the connection's label |

The system SHALL fail a test when the id_token has `email_verified: false`. A company sign-in with that answer fails (see `company-sign-in`). Thus a provider that marks every address as not confirmed cannot pass its test. An id_token with no `email_verified` passes.

A failed test changes nothing. A test involves no link, account, session or hydra-sso login.

#### Scenario: A successful test
- **WHEN** an admin opens the `url` and signs in at the identity provider
- **AND** the identity provider sends the admin back to the connection's redirect URI
- **THEN** the page says that the test sign-in succeeded
- **AND** the connection is tested

#### Scenario: The provider marks the address as not confirmed
- **WHEN** an admin finishes a test, and the id_token has `email_verified: false`
- **THEN** the page says that the test sign-in failed
- **AND** the page says that the provider marks the address as not confirmed
- **AND** the status of the connection does not change

#### Scenario: The answer arrives at another tenant's redirect URI
- **WHEN** a person opens the answer of a test at `/callback/<another tenant's id>`
- **THEN** the system answers HTTP 400
- **AND** no connection changes
