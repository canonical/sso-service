# observability Specification

## Purpose

Operators need to see these things:

- If company sign-ins succeed, and why they fail.
- Who changed the connections or the policy of a tenant.
- If the service answers.

Reviewers of a service that handles the addresses of users also need to know what reaches the logs.

Key decisions:

- **The service counts sign-in outcomes by reason.** The reasons are the same reasons that users are told. The counters have no tenant label and no connection label. When the identity provider of a customer is down, that shows as `unavailable` outcomes and in the log.
- **Admin actions go to the security log.** This log is a stream of its own. It has the format that the services of the platform share.
- **Sign-in log lines identify users only by digest and masked address.**

Non-goals:

- An audit trail in the database.
- A way to show the admins of a tenant the health of their identity provider.
- Alerting rules and dashboards.

## Requirements
### Requirement: Metrics
The system SHALL expose Prometheus metrics at `GET /api/v0/metrics`. Each metric SHALL have the label `service="sso-service"`. The metrics are these:

| metric | labels | counts |
|---|---|---|
| `sso_callback_outcomes_total` | `reason` | how a company sign-in ended at `/login` or `/callback`: accepted (`linked_existing`, `account_linking`, `registration`) or rejected (the 9 reasons of `company-sign-in`) |
| `sso_first_sign_ins_total` | `outcome` | subjects with no link that the system let through: `account_linking`, `registration` |
| `sso_attempts_completed_total` | | `CompleteAttempt` calls that matched |
| `sso_complete_attempt_refused_total` | | `CompleteAttempt` calls that the system answered with `NOT_APPLICABLE` |
| `sso_tenant_service_write_failures_total` | `rpc` | policy, domain and binding writes that the tenant service did not answer. The system does not count a refusal |
| `http_response_time_seconds` | `route`, `status` | duration of every HTTP request. `route` is the method, then the pattern of the route that served the request. `route` is never the path of the request. It is `GET/callback/id` for the callback of every connection, the method and `/api/v0/sso/*` for every route of the 2 admin services, and `unmatched` for a request that no route serves |
| `storage_query_duration_seconds` | `operation`, `status` | duration of every database operation |

The system SHALL also expose the standard gRPC server metrics, with handling-time histograms, for calls on the gRPC listener. `sso_callback_outcomes_total` does not count test sign-ins or the refusals that the system answers with a page.

#### Scenario: A refused sign-in is counted
- **WHEN** the system rejects a sign-in because the address does not match
- **THEN** `sso_callback_outcomes_total{reason="address_mismatch"}` goes up by one

#### Scenario: Requests to paths that do not exist
- **WHEN** requests arrive for many different paths that no route serves, or for the callbacks of many connections
- **THEN** the system counts all the requests of the first kind under `route="unmatched"`
- **AND** the system counts all the requests of the second kind under `route="GET/callback/id"`
- **AND** no label value comes from a path

### Requirement: Admin-action logs
The system SHALL write one record to the security log, at level `info`, after each of the actions below succeeds. The record SHALL name the actor, the operation and the resource.

| action | actor | resource |
|---|---|---|
| `create_connection`, `update_connection`, `delete_connection`, `delete_any_connection`, `start_test_login` | the subject of the caller's token | the connection id |
| `put_tenant_sso_policy`, `set_tenant_domains` | the subject of the caller's token | the tenant id |
| `delete_link` | the account whose link was removed | the connection id |

The `event` of a record is `authz_admin:<actor>,<operation>,<action>`. An operation that is refused or that fails writes no such record. The records are at level `info`. Thus the system emits them when `LOG_LEVEL` is `debug` or `info`, and not at the default of `LOG_LEVEL`, `error`.

The security log also records the start and the stop of the service, and a valid token that the service did not admit (`authz_fail`).

#### Scenario: A connection is created
- **WHEN** a tenant admin creates a connection and `LOG_LEVEL` is `info`
- **THEN** the security log has a record with the admin's subject, `create_connection` and the new connection's id
- **AND** at the default level the security log has no such record

#### Scenario: A refused delete
- **WHEN** a delete is refused with `REQUIRED_NEEDS_ACTIVE_BINDING`
- **THEN** the system writes no admin-action record

### Requirement: Sign-in logs
The system SHALL log these events in the service log at level `info`:

- Each attempt started.
- Each attempt that `CompleteAttempt` confirmed or refused.
- Each sign-in accepted or rejected with its reason and a detail of the check that failed.
- Each subject with no link that the system let through.
- The outcome of each test sign-in.

The system logs failures of a dependency at level `error` with their cause. The system logs the refusals of an identity provider at `warning`.

In these lines a ticket and an account id SHALL appear only as a short one-way digest. An address SHALL appear only as its first character, `***` and its domain (`a***@example.com`). The system never logs a receipt. The system cuts short the text that an identity provider controls and strips its control characters, or the system quotes that text. The error text of a failed Kratos lookup also does not name the request URL. That URL holds the address or the link identifier that the system looked up.

At level `debug` the system logs every HTTP request with its method, host, path, the address of the caller, status, size and duration. The system never logs the query of the request, which carries authorization codes and addresses. The system logs no headers.

This requirement does not cover traces. With tracing on, that URL is an attribute of the span of every such lookup. Thus anyone who can read traces can read addresses.

#### Scenario: A refused sign-in is logged
- **WHEN** the system rejects a sign-in for `alice@example.com` at the callback
- **THEN** the log line has the reason, a detail, the connection id, the tenant id and `a***@example.com`

### Requirement: Status, version and traces
The system SHALL answer `GET /api/v0/status` with `{"status": "ok", "buildInfo": {…}}`. The system SHALL answer `GET /api/v0/version` with the version, the commit and the name of the build. Neither route checks a dependency.

With `TRACING_ENABLED` the system SHALL record OpenTelemetry spans for requests, service operations, database statements and outbound calls. The system SHALL send the spans to `OTEL_GRPC_ENDPOINT`, or else to `OTEL_HTTP_ENDPOINT`, or else write them to standard output. When the system cannot create the exporter of the spans at start, it SHALL log the error and run without tracing. This is the same as with `TRACING_ENABLED` off.

#### Scenario: The trace exporter cannot be created
- **WHEN** `serve` starts with `TRACING_ENABLED` and an `OTEL_GRPC_ENDPOINT` for which the service cannot create an exporter
- **THEN** the service logs the error and starts
- **AND** the service serves its requests without spans

#### Scenario: Status while a dependency is down
- **WHEN** a caller calls `GET /api/v0/status` while Kratos is down
- **THEN** the system answers 200 with the status `ok`

