## 1. Binding cookies

- [x] 1.1 When `/login` sets a binding cookie, clear the binding cookies that the browser sent, but for the newest one that is still valid.
- [x] 1.2 Test the limit, and the cookies that do not open or are expired.

## 2. Metric names

- [x] 2.1 Rename `sso_attempts_completed_total` to `sso_attempts_verified_total`.
- [x] 2.2 Rename `sso_complete_attempt_refused_total` to `sso_verify_attempt_refused_total`.
