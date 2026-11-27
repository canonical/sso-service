## MODIFIED Requirements

### Requirement: Errors and reasons
The system SHALL answer a failed call with a gRPC status. A refusal with a reason SHALL carry the reason as a `google.rpc.ErrorInfo` detail (domain `sso-service`). The refusal SHALL also carry the reason as the start of the message, `<REASON>: <text>`. Over HTTP every error SHALL be `{"status": <HTTP status>, "message": "<message>"}`. This includes the errors of the authentication step and of unknown routes under `/api/v0/sso`.

| reason | gRPC code | HTTP | when |
|---|---|---|---|
| `CONNECTION_NOT_FOUND` | `NOT_FOUND` | 404 | a connection that does not exist or is not a connection of the tenant in the path. Also a binding to a connection that the tenant does not own |
| `CONNECTION_NOT_TESTED` | `FAILED_PRECONDITION` | 400 | an active binding to a draft connection |
| `CONNECTION_LIMIT` | `FAILED_PRECONDITION` | 400 | a sixth connection for a tenant |
| `PERSONAL_TENANT` | `FAILED_PRECONDITION` | 400 | a connection or a policy for a personal tenant |
| `REQUIRED_NEEDS_ACTIVE_BINDING` | `FAILED_PRECONDITION` | 400 | a policy write or a delete that will leave a `REQUIRED` tenant with no active binding |
| `AUTO_JOIN_NEEDS_REQUIRED_AND_DOMAINS` | `FAILED_PRECONDITION` | 400 | auto-join without `REQUIRED` or without domains |
| `IDP_CHECK_FAILED` | `FAILED_PRECONDITION` | 400 | the check that starts a test sign-in failed |
| `NOT_APPLICABLE` | `FAILED_PRECONDITION` | gRPC only | `StartAttempt` for a connection that is not tested. Also a `VerifyAttempt` that does not match |
| `LAST_CREDENTIAL` | `FAILED_PRECONDITION` | gRPC only | a `DeleteLink` that will leave the account with no way in of its own |

These failures have no reason:

| gRPC code | HTTP | when |
|---|---|---|
| `INVALID_ARGUMENT` | 400 | a request that fails validation |
| `UNAUTHENTICATED` | 401 | no valid token (see `caller-authentication`) |
| `NOT_FOUND` | 404 | no such tenant, no such account, no link at the connection, or an unknown route |
| `ABORTED` | 409 | another request holds the resource. The caller can try again at once |
| `UNAVAILABLE` | 503 | the tenant service, Kratos or the database did not answer in time. The caller can try again later |
| `CANCELED` | 499 | the caller cancelled the call. This code also applies when the cancellation stops a call to the tenant service or Kratos. The system then does not answer `UNAVAILABLE` |
| `INTERNAL` | 500 | anything else. The message is `internal error` and the cause is in the log |

#### Scenario: One refusal on both transports
- **WHEN** a caller reads a connection that does not exist
- **THEN** over HTTP the system answers 404 with `{"status": 404, "message": "CONNECTION_NOT_FOUND: no such connection"}`
- **AND** over gRPC the status is `NOT_FOUND` with the same message
- **AND** the gRPC status has an `ErrorInfo` with the reason `CONNECTION_NOT_FOUND` and the domain `sso-service`

#### Scenario: A call that its caller cancels
- **WHEN** a caller cancels a call while the system waits for the tenant service or Kratos
- **THEN** the call ends with `CANCELED`, not `UNAVAILABLE`
