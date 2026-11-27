## MODIFIED Requirements

### Requirement: Metrics
The system SHALL expose Prometheus metrics at `GET /api/v0/metrics`. Each metric SHALL have the label `service="sso-service"`. The metrics are these:

| metric | labels | counts |
|---|---|---|
| `sso_callback_outcomes_total` | `reason` | how a company sign-in ended at `/login` or `/callback`: accepted (`linked_existing`, `account_linking`, `registration`) or rejected (the 8 reasons of `company-sign-in`) |
| `sso_first_sign_ins_total` | `outcome` | subjects with no link that the system let through: `account_linking`, `registration` |
| `sso_attempts_verified_total` | | `VerifyAttempt` calls that matched |
| `sso_verify_attempt_refused_total` | | `VerifyAttempt` calls that the system answered with `NOT_APPLICABLE` |
| `sso_tenant_service_write_failures_total` | `rpc` | policy, domain and binding writes that the tenant service did not answer. The system does not count a refusal. The system does not count a write that its caller cancelled |
| `http_response_time_seconds` | `route`, `status` | duration of every HTTP request. `route` is the method, then the pattern of the route that served the request. `route` is never the path of the request. It is `GET/callback/id` for the callback of every tenant, the method and `/api/v0/sso/*` for every route of the 2 admin services, and `unmatched` for a request that no route serves |
| `storage_query_duration_seconds` | `operation`, `status` | duration of every database operation |

The outcomes of an accepted sign-in are these (see `company-sign-in`):

- `linked_existing`: an account holds the link of the subject already.
- `account_linking`: Kratos will link the subject to an existing account, after the user signs in to that account another way.
- `registration`: Kratos will register an account for the address.

The system SHALL also expose the standard gRPC server metrics, with handling-time histograms, for calls on the gRPC listener. `sso_callback_outcomes_total` does not count test sign-ins or the refusals that the system answers with a page.

#### Scenario: A refused sign-in is counted
- **WHEN** the system rejects a sign-in because the address does not match
- **THEN** `sso_callback_outcomes_total{reason="address_mismatch"}` goes up by one

#### Scenario: Requests to paths that do not exist
- **WHEN** requests arrive for many different paths that no route serves, or for the callbacks of many tenants
- **THEN** the system counts all the requests of the first kind under `route="unmatched"`
- **AND** the system counts all the requests of the second kind under `route="GET/callback/id"`
- **AND** no label value comes from a path

#### Scenario: A cancelled policy write is not counted
- **WHEN** a caller cancels a policy write while the system waits for the tenant service
- **THEN** `sso_tenant_service_write_failures_total` does not go up

### Requirement: Sign-in logs
The system SHALL log these events in the service log at level `info`:

- Each attempt started.
- Each attempt that `VerifyAttempt` confirmed or refused.
- Each sign-in accepted or rejected with its reason and a detail of the check that failed.
- Each subject with no link that the system let through.
- The outcome of each test sign-in.

The system logs failures of a dependency at level `error` with their cause. A caller of the service can cancel its call. The system SHALL log at level `debug` the calls to dependencies that fail for that reason. Such a failure does not show a fault of a dependency. The system logs the refusals of an identity provider at `warning`.

In these lines a ticket and an account id SHALL appear only as a short one-way digest. An address SHALL appear only as its first character, `***` and its domain (`a***@example.com`). The system never logs a receipt. The system cuts short the text that an identity provider controls and keeps only its printable characters, or the system quotes that text. The error text of a failed Kratos lookup also does not name the request URL. That URL holds the address or the link identifier that the system looked up.

At level `debug` the system logs every HTTP request with its method, host, path, the address of the caller, status, size and duration. The system never logs the query of the request, which carries authorization codes and addresses. The system logs no headers.

This requirement does not cover traces. With tracing on, that URL is an attribute of the span of every such lookup. Thus anyone who can read traces can read addresses.

#### Scenario: A refused sign-in is logged
- **WHEN** the system rejects a sign-in for `alice@example.com` at the callback
- **THEN** the log line has the reason, a detail, the connection id, the tenant id and `a***@example.com`

#### Scenario: A caller that gives up
- **WHEN** the login UI cancels `VerifyAttempt` while the system waits for Kratos
- **THEN** the system logs the failure at level `debug`, not `error`
