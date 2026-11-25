## 1. Browser pages

- [x] 1.1 Fail a test when the id_token has `email_verified: false`, in `pkg/bridge/test_login.go`.
- [x] 1.2 Keep only printable characters, and cut between characters at 120 bytes, in `pkg/bridge/validation.go`.
- [x] 1.3 Log a cancelled call at `debug`, in `pkg/bridge/service.go`.

## 2. Caller authentication

- [x] 2.1 Read the scheme in any case, with one parser for HTTP and gRPC, in `pkg/authentication/middleware.go`.
- [x] 2.2 Send `WWW-Authenticate: Bearer` with each HTTP 401, in `pkg/authentication/middleware.go`.

## 3. Commands

- [x] 3.1 Refuse a `PUBLIC_URL` with no host or with userinfo, in `cmd/serve.go`.
- [x] 3.2 Refuse a `PUBLIC_URL` with an empty query or fragment, or another scheme, in `cmd/serve.go`.
- [x] 3.3 Check `ENVELOPE_KEY` with the other settings, before the database, in `cmd/serve.go`.
- [x] 3.4 Give the export of the last spans its own 3 seconds, in `cmd/serve.go`.
- [x] 3.5 Refuse a `--format` other than `text` or `json`, in `cmd/migrate.go`.

## 4. Tenant service

- [x] 4.1 Read a cancelled call as a cancellation, in `internal/tenants/errors.go`.

## 5. Admin API

- [x] 5.1 Map a cancelled call to `CANCELED` first, in `pkg/admin/handlers.go`.
- [x] 5.2 Log a cancelled call at `debug`, and do not count it as a failed write, in `pkg/admin/service.go`.

## 6. Sign-in API

- [x] 6.1 Map a cancelled call to `CANCELED` first, in `pkg/sso/handlers.go`.
- [x] 6.2 Keep the cause of a Kratos error, and log a cancelled call at `debug`, in `pkg/sso/service.go`.

## 7. Tests

- [x] 7.1 Test the page for `email_verified: false` in `pkg/bridge/test_login_test.go`.
- [x] 7.2 Test the printable text and the cut between characters in `pkg/bridge/validation_test.go`.
- [x] 7.3 Test the scheme, the words of the header and `WWW-Authenticate` in `pkg/authentication/middleware_test.go`.
- [x] 7.4 Test the public URL and the envelope key in `cmd/serve_test.go`.
- [x] 7.5 Test a Kratos call that its caller cancelled in `pkg/sso/handlers_test.go`.
