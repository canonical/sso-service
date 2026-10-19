## MODIFIED Requirements

### Requirement: Metrics
The system SHALL expose Prometheus metrics at `GET /api/v0/metrics`. Each metric SHALL have the label `service="sso-service"`. The metrics are these:

| metric | labels | counts |
|---|---|---|
| `sso_callback_outcomes_total` | `reason` | how a company sign-in ended at `/login` or `/callback`: accepted (`linked_existing`, `account_linking`, `registration`) or rejected (the 8 reasons of `company-sign-in`) |
| `sso_first_sign_ins_total` | `outcome` | subjects with no link that the system let through: `account_linking`, `registration` |
| `sso_attempts_completed_total` | | `CompleteAttempt` calls that matched |
| `sso_complete_attempt_refused_total` | | `CompleteAttempt` calls that the system answered with `NOT_APPLICABLE` |
| `sso_tenant_service_write_failures_total` | `rpc` | policy, domain and binding writes that the tenant service did not answer. The system does not count a refusal |
| `http_response_time_seconds` | `route`, `status` | duration of every HTTP request. `route` is the method, then the pattern of the route that served the request. `route` is never the path of the request. It is `GET/callback/id` for the callback of every tenant, the method and `/api/v0/sso/*` for every route of the 2 admin services, and `unmatched` for a request that no route serves |
| `storage_query_duration_seconds` | `operation`, `status` | duration of every database operation |

The system SHALL also expose the standard gRPC server metrics, with handling-time histograms, for calls on the gRPC listener. `sso_callback_outcomes_total` does not count test sign-ins or the refusals that the system answers with a page.

#### Scenario: A refused sign-in is counted
- **WHEN** the system rejects a sign-in because the address does not match
- **THEN** `sso_callback_outcomes_total{reason="address_mismatch"}` goes up by one

#### Scenario: Requests to paths that do not exist
- **WHEN** requests arrive for many different paths that no route serves, or for the callbacks of many tenants
- **THEN** the system counts all the requests of the first kind under `route="unmatched"`
- **AND** the system counts all the requests of the second kind under `route="GET/callback/id"`
- **AND** no label value comes from a path
