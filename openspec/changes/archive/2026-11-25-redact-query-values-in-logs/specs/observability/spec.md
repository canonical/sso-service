## MODIFIED Requirements

### Requirement: Sign-in logs
The system SHALL log these events in the service log at level `info`:

- Each attempt started.
- Each attempt that `VerifyAttempt` confirmed or refused.
- Each sign-in accepted or rejected with its reason and a detail of the check that failed.
- Each subject with no link that the system let through.
- The outcome of each test sign-in.

The system logs failures of a dependency at level `error` with their cause. A caller of the service can cancel its call. The system SHALL log at level `debug` the calls to dependencies that fail for that reason. Such a failure does not show a fault of a dependency. The system logs the refusals of an identity provider at `warning`.

In these lines a ticket and an account id SHALL appear only as a short one-way digest. An address SHALL appear only as its first character, `***` and its domain (`a***@example.com`). The system never logs a receipt. The system cuts short the text that an identity provider controls and keeps only its printable characters, or the system quotes that text. The error text of a failed Kratos lookup also does not name the request URL. That URL holds the address or the link identifier that the system looked up.

At level `debug` the system logs every HTTP request with its method, host, path, the address of the caller, status, size and duration. The line also has the names of the query parameters, in their order. The system SHALL replace the value of each query parameter with `REDACTED`, because a query carries authorization codes and addresses. The system logs no headers.

This requirement does not cover traces. With tracing on, that URL is an attribute of the span of every such lookup. Thus anyone who can read traces can read addresses.

#### Scenario: A refused sign-in is logged
- **WHEN** the system rejects a sign-in for `alice@example.com` at the callback
- **THEN** the log line has the reason, a detail, the connection id, the tenant id and `a***@example.com`

#### Scenario: A caller that gives up
- **WHEN** the login UI cancels `VerifyAttempt` while the system waits for Kratos
- **THEN** the system logs the failure at level `debug`, not `error`

#### Scenario: A callback in the request log
- **WHEN** the system logs the request `/callback/c?code=x&state=y` at level `debug`
- **THEN** the line has `/callback/c?code=REDACTED&state=REDACTED`
- **AND** the line has neither the code nor the state
