## MODIFIED Requirements

### Requirement: Deadlines
The system SHALL bound every wait as this table shows:

| what | bound |
|---|---|
| a whole request, HTTP or gRPC | 25 seconds. A gRPC caller can set a shorter deadline. That deadline applies. |
| one call to an identity provider | 10 seconds |
| everything that one operation asks of an identity provider | 15 seconds |
| one call to the Tenant Service, retries included | `TENANT_SERVICE_GRPC_TIMEOUT` (5 seconds) |
| one call to Kratos or hydra-sso | 5 seconds |
| the fetch of the service's own access token | 5 seconds |
| the wait for a database lock | 2 seconds |
| one database statement | 5 seconds |
| one database transaction | 10 seconds |
| a database transaction that stays idle | 15 seconds |
| `migrate` waiting for a lock | 3 seconds, then the run fails |
| the connection to the database when `serve` starts | 30 seconds |
| one request to the issuer of callers' tokens for its discovery document, when `serve` starts | 10 seconds |
| one request to that issuer for its keys, after `serve` started | 10 seconds |
| a ticket, a binding cookie, the `state` of a test | 30 minutes |
| a receipt cookie | until its ticket expires |

The database bounds are fixed in the code. They are not settings. The 10 seconds of a transaction include the call to the Tenant Service that the service makes inside it. The service holds no database lock across a call to an identity provider.

The calls to the Tenant Service that need the service's own access token at the same time SHALL share one fetch of the token. Each call SHALL wait for that fetch only until its own deadline. Thus a slow token endpoint does not keep a call after its deadline.

#### Scenario: A lock held by another request
- **WHEN** a policy write needs a connection row that another transaction holds for more than 2 seconds
- **THEN** the caller gets `ABORTED`
- **AND** the caller can try again
- **AND** a statement or a transaction that runs out of time rolls back
- **AND** the service answers `UNAVAILABLE` for it

#### Scenario: A migration behind a long transaction
- **WHEN** `migrate` needs a lock that another session holds for more than 3 seconds
- **THEN** the run fails
- **AND** the run can be repeated
- **AND** the statements that queued behind it waited no longer than 3 seconds

#### Scenario: A provider that does not answer
- **WHEN** an identity provider accepts the connection and never answers the token request
- **THEN** the service refuses the sign-in as `unavailable` after 10 seconds
