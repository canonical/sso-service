## Why

A review of the specification found three gaps and one risk:

- The name of the binding cookie did not say which 16 hex digits of the hash it uses.
- 2 metric names said "complete", but the RPC is `VerifyAttempt` since the review of the API.
- The specification used the outcome `linked_existing`, but did not define it.
- Each `/login` set a binding cookie of about 2.5 kB, and only the callback cleared it. Thus the cookies of abandoned sign-ins stayed for 30 minutes. The browser sends all the cookies of the host with each request to the host, also to Kratos and to the login UI. A few abandoned sign-ins could make the Cookie header too large for a proxy, which often allows 8 kB. Then every request to the host fails.

## What Changes

- The name of the binding cookie uses the first 16 lower-case hex digits of the SHA-256 of the `state`. The code did this already.
- When `/login` sets a binding cookie, it clears the other binding cookies, but for the newest one that is still valid. Thus the browser keeps the new cookie and at most 1 older cookie.
- **BREAKING** for dashboards and alerts: `sso_attempts_completed_total` is now `sso_attempts_verified_total`, and `sso_complete_attempt_refused_total` is now `sso_verify_attempt_refused_total`. No release has the old names.
- The specification defines the outcomes of an accepted sign-in, `linked_existing` included.

## Capabilities

### Modified Capabilities
- `company-sign-in`: the name of the binding cookie, the limit on binding cookies, and the outcome `linked_existing`.
- `observability`: the names of 2 metrics, and the outcomes of an accepted sign-in.

## Impact

- `internal/limits`, `internal/monitoring`, `pkg/bridge`, `pkg/sso`, and their tests.
- The Grafana dashboard of the charm uses the 2 metric names.
