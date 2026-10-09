# observability Specification

## Purpose

Operators need to see whether company sign-ins succeed and why they fail, who changed a tenant's connections or policy, and whether the service answers. Reviewers of a service that handles users' addresses also need to know what reaches the logs.

Key decisions:

- **Sign-in outcomes are counted by reason**, the same reasons users are told. The counters have no tenant or connection label: a customer's identity provider being down shows as `unavailable` outcomes and in the log.
- **Admin actions go to the security log**, a stream of its own in the format the platform's services share.
- **Sign-in log lines identify users only by digest and masked address.**

Non-goals: an audit trail in the database; showing a tenant's admins the health of their identity provider; alerting rules and dashboards.

## Requirements
### Requirement: Metrics
The system SHALL expose Prometheus metrics at `GET /api/v0/metrics`, each with the label `service="sso-service"`:

| metric | labels | counts |
|---|---|---|
| `sso_callback_outcomes_total` | `reason` | how a company sign-in ended at `/login` or `/callback`: accepted (`linked_existing`, `account_linking`, `registration`) or rejected (the nine reasons of `company-sign-in`) |
| `sso_first_sign_ins_total` | `outcome` | subjects with no link that were let through: `account_linking`, `registration` |
| `sso_attempts_completed_total` | | `CompleteAttempt` calls that matched |
| `sso_complete_attempt_refused_total` | | `CompleteAttempt` calls answered `NOT_APPLICABLE` |
| `sso_tenant_service_write_failures_total` | `rpc` | policy, domain and binding writes the tenant service did not answer; a refusal is not counted |
| `http_response_time_seconds` | `route`, `status` | duration of every HTTP request; `route` is the method followed by the pattern of the route that served it, never the request's path: `GET/callback/id` for every connection's callback, the method and `/api/v0/sso/*` for every route of the two admin services, and `unmatched` for a request no route serves |
| `storage_query_duration_seconds` | `operation`, `status` | duration of every database operation |

It SHALL also expose the standard gRPC server metrics, with handling-time histograms, for calls on the gRPC listener. Refusals answered with a page and test sign-ins are not counted in `sso_callback_outcomes_total`.

#### Scenario: A refused sign-in is counted
- **WHEN** a sign-in is rejected because the address does not match
- **THEN** `sso_callback_outcomes_total{reason="address_mismatch"}` goes up by one

#### Scenario: Requests to paths that do not exist
- **WHEN** requests arrive for many different paths that no route serves, or for the callbacks of many connections
- **THEN** the first are all counted under `route="unmatched"` and the second under `route="GET/callback/id"`: no label value comes from a path

### Requirement: Admin-action logs
The system SHALL write one record to the security log, at level `info`, after each of these succeeds, naming the actor, the operation and the resource:

| action | actor | resource |
|---|---|---|
| `create_connection`, `update_connection`, `delete_connection`, `delete_any_connection`, `start_test_login` | the caller's token subject | the connection id |
| `put_tenant_sso_policy`, `set_tenant_domains` | the caller's token subject | the tenant id |
| `delete_link` | the account whose link was removed | the connection id |

A record's `event` is `authz_admin:<actor>,<operation>,<action>`. An operation that is refused or fails writes no such record. Because the records are at level `info`, they are emitted when `LOG_LEVEL` is `debug` or `info`, and not at its default, `error`.

The security log also records the service starting and stopping, and a valid token that was not admitted (`authz_fail`).

#### Scenario: A connection is created
- **WHEN** a tenant admin creates a connection and `LOG_LEVEL` is `info`
- **THEN** the security log has a record with the admin's subject, `create_connection` and the new connection's id; at the default level it has none

#### Scenario: A refused delete
- **WHEN** a delete is refused with `REQUIRED_NEEDS_ACTIVE_BINDING`
- **THEN** no admin-action record is written

### Requirement: Sign-in logs
The system SHALL log, in the service log at level `info`, each attempt started, each attempt confirmed or refused by `CompleteAttempt`, each sign-in accepted or rejected with its reason and a detail of the check that failed, each subject with no link let through, and each test sign-in's outcome. Failures of a dependency are logged at level `error` with their cause, and an identity provider's refusals at `warning`.

In these lines a ticket and an account id SHALL appear only as a short one-way digest, and an address only as its first character, `***` and its domain (`a***@example.com`). A receipt is never logged. Text an identity provider controls is cut short and stripped of control characters, or quoted. The error text of a failed Kratos lookup does not name the request URL either, which holds the address or link identifier that was looked up. At level `debug` every HTTP request is logged with its method, host, path, the caller's address, status, size and duration: never its query, which carries authorization codes and addresses, and no headers. Traces are not covered: with tracing on, that URL is an attribute of the span of every such lookup, so whoever can read traces can read addresses.

#### Scenario: A refused sign-in is logged
- **WHEN** a sign-in for `alice@example.com` is rejected at the callback
- **THEN** the log line has the reason, a detail, the connection id, the tenant id and `a***@example.com`

### Requirement: Status, version and traces
The system SHALL answer `GET /api/v0/status` with `{"status": "ok", "buildInfo": {…}}` and `GET /api/v0/version` with the build's version, commit and name; neither checks a dependency. With `TRACING_ENABLED` it SHALL record OpenTelemetry spans for requests, service operations, database statements and outbound calls, and send them to `OTEL_GRPC_ENDPOINT`, or else `OTEL_HTTP_ENDPOINT`, or else write them to standard output. When the exporter of the spans cannot be created at start, the system SHALL log the error and run without tracing, as with `TRACING_ENABLED` off.

#### Scenario: The trace exporter cannot be created
- **WHEN** `serve` is started with `TRACING_ENABLED` and an `OTEL_GRPC_ENDPOINT` no exporter can be created for
- **THEN** the error is logged, the service starts, and its requests are served without spans

#### Scenario: Status while a dependency is down
- **WHEN** `GET /api/v0/status` is called while Kratos is down
- **THEN** the system answers 200 with the status `ok`

