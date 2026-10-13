## 1. Module, settings and commands

- [x] 1.1 Create `go.mod` with `github.com/canonical/identity-platform-api` (`v0/sso`, `v0/tenant`), `github.com/ory/hydra-client-go/v26`, `github.com/ory/kratos-client-go/v25`, `github.com/coreos/go-oidc/v3`, `golang.org/x/oauth2`, `buf.build/go/protovalidate`, `github.com/jackc/pgx/v5`, `github.com/pressly/goose/v3`, `github.com/Masterminds/squirrel`, `github.com/go-chi/chi/v5` and `github.com/grpc-ecosystem/grpc-gateway/v2`.
- [x] 1.2 Add the settings with their defaults and validation in `internal/config/specs.go`.
- [x] 1.3 Add the fixed limits and timeouts in `internal/limits/limits.go`.
- [x] 1.4 Add the entry point and the `version` command in `main.go`, `cmd/root.go`, `cmd/version.go` and `internal/version/const.go`.
- [x] 1.5 Add the `migrate` command (`up`, `down [version]`, `status`, `check`, `--format json`) in `cmd/migrate.go`.
- [x] 1.6 Add tests in `cmd/migrate_test.go` (arguments) and `cmd/serve_test.go` (settings: defaults, required values, the public URL, the 2 ports, authentication disabled).

## 2. Database

- [x] 2.1 Add the migration `migrations/001_initial_schema.sql` (`sso_connections`, index on `owner_tenant_id`) and embed it in `migrations/migration.go`.
- [x] 2.2 Add the database client in `internal/db/storage.go` and `internal/db/interfaces.go`. It has the pool with `lock_timeout`, `statement_timeout` and `idle_in_transaction_session_timeout`, and `WithTx` with its deadline.
- [x] 2.3 Add the shared types in `internal/types/types.go`: `Connection`, `Ticket`, `SignIn`, `TestState`, the token purposes, the link identifier and hydra-sso subject helpers, the redirect URI per connection, `MaskEmail`.
- [x] 2.4 Add the connection queries in `internal/storage/storage.go`, `converters.go`, `errors.go` and `interfaces.go`. The queries are: create with the limit in the insert, get, list by id, `LockConnections` (`FOR UPDATE`, id order), update, delete and `SetTested`. Also add the mapping of lock timeouts and statement timeouts to `ErrBusy` and `ErrTimeout`.
- [x] 2.5 Add the test helpers `internal/testhelpers/containers.go`, `migrations.go` and `shared.go` (a shared PostgreSQL container, one database per test).
- [x] 2.6 Add tests: `internal/types/types_test.go`, `internal/storage/converters_test.go`, `internal/storage/errors_test.go`, and the integration tests `internal/db/db_integration_test.go` and `internal/storage/storage_integration_test.go` (limit, locks, timeouts, `SetTested`).

## 3. Secrets

- [x] 3.1 Add envelope encryption and sealed tokens in `internal/secrets/secrets.go` and `internal/secrets/errors.go`: `NewEnvelope` (one key, 32 bytes in base64), `Encrypt`, `Decrypt`, `Seal`, `Open`, `Receipt`, `ValidReceipt`, `RandomToken`, `Digest`.
- [x] 3.2 Add tests in `internal/secrets/secrets_test.go`.

## 4. Clients of the platform's services

- [x] 4.1 Add the client of the tenant service in `internal/tenants/client.go`, `grpc.go` and `errors.go`. It has the 5 RPCs (`GetSignInContext` of `TenantSignInService`, the 4 of `TenantSSOPolicyService`) with a deadline per call. It also has the client-credentials token source, the bearer interceptor, retries on `UNAVAILABLE` and the reconnect backoff. It also gives names to the refusals of the tenant service.
- [x] 4.2 Add tests in `internal/tenants/client_test.go`, `grpc_test.go` and `errors_test.go`.
- [x] 4.3 Add the Kratos admin client in `internal/kratos/client.go` and `errors.go`. Its calls are: get an identity with credentials, list by credential identifier, and delete an OIDC identifier. Also add the readers of the address, OIDC providers, password and passkeys of an identity.
- [x] 4.4 Add the hydra-sso admin client in `internal/hydra/client.go` and `errors.go`. Its calls are: get, accept and reject a login request, and get and accept a consent request, never remembered.
- [x] 4.5 Add tests in `internal/kratos/client_test.go` and `internal/hydra/client_test.go`.
- [x] 4.6 Add the link reader in `internal/links/links.go` and `interfaces.go` (`Of`, `At`, `OwnWayIn`) with tests in `internal/links/links_test.go`.

## 5. Identity provider client

- [x] 5.1 Add the outbound-request guard in `internal/idp/safehttp.go`. It has the URL check, the address check at dial time, no proxy and no redirects. It also has the response cap, and it takes 5xx as unavailable.
- [x] 5.2 Add the relying party in `internal/idp/client.go` and `internal/idp/errors.go`. It has issuer validation, discovery with its cache, the authorization URL, the code exchange and the id_token checks. It also has the test probe (`Check`), the 5 kinds of failure, and what a test sign-in says about each.
- [x] 5.3 Add a mock identity provider for tests in `internal/testhelpers/mockidp/mockidp.go`, with `mockidp_test.go`.
- [x] 5.4 Add tests in `internal/idp/safehttp_test.go`, `internal/idp/client_test.go` and `internal/idp/errors_test.go`.

## 6. Authentication, errors, logging and metrics

- [x] 6.1 Add the platform's JWT authentication package as `pkg/authentication` (`authenticator.go`, `provider.go`, `verifier.go`, `middleware.go`, `context.go`, `noop.go`, `interfaces.go`), with `middleware_test.go` and `verifier_test.go`.
- [x] 6.2 Add status errors with a reason in `internal/apierrors/errors.go` and the HTTP error body in `internal/http/types/gRPC_mappers.go`, with `internal/apierrors/errors_test.go` and `internal/http/types/gRPC_mappers_test.go`.
- [x] 6.3 Add the request deadline interceptor in `internal/grpcutil/interceptors.go`, with `interceptors_test.go`.
- [x] 6.4 Add logging in `internal/logging` (`logger.go`, `security_logger.go`, `grpc_interceptor.go`, `middlewares.go`, `interfaces.go`, `noop.go`), with `grpc_interceptor_test.go` and `middlewares_test.go`.
- [x] 6.5 Add the monitor in `internal/monitoring` (`interfaces.go`, `middlewares.go`, `noop.go`, `prometheus/prometheus.go`) with the sign-in counters. Add tracing in `internal/tracing` (`config.go`, `tracer.go`, `middleware.go`, `interfaces.go`), with `tracer_test.go`. The service runs without tracing when it cannot create the exporter.

## 7. Tenant admin and platform admin API

- [x] 7.1 Add the interfaces and errors of `pkg/admin` in `interfaces.go` and `errors.go`.
- [x] 7.2 Add the service in `pkg/admin/service.go`. It has connections (list, create, get, update, delete, delete any), `StartTestLogin`, `GetTenantSSOPolicy`, and `PutTenantSSOPolicy` under the locks on the connection rows. It also has `GetTenantDomains`, `SetTenantDomains` and the admin-action logs.
- [x] 7.3 Add the handler of `SSOTenantAdminService` and `SSOPlatformAdminService` in `pkg/admin/handlers.go` and `converters.go`. It has request validation, the update mask, and the mapping of errors to codes and reasons.
- [x] 7.4 Add tests in `pkg/admin/service_test.go`, `handlers_test.go` and `converters_test.go`.

## 8. Sign-in API

- [x] 8.1 Add the interfaces and errors of `pkg/sso` in `interfaces.go` and `errors.go`.
- [x] 8.2 Add the service in `pkg/sso/service.go`: `ListOptions`, `StartAttempt`, `CompleteAttempt`, `ListLinks`, `DeleteLink`.
- [x] 8.3 Add the handler of `SSOSignInService` in `pkg/sso/handlers.go` and `converters.go`.
- [x] 8.4 Add tests in `pkg/sso/service_test.go` and `handlers_test.go`.

## 9. Browser side of a company sign-in

- [x] 9.1 Add the interfaces, the refusals answered with a page and the reasons with their messages. They go in `pkg/bridge/interfaces.go`, `errors.go` and `reasons.go`.
- [x] 9.2 Add the sign-in in `pkg/bridge/service.go` and `validation.go`. It has `StartLogin`, `Callback` with its checks in order, and the rules for a subject with no link. It also has accept, with the receipt, and reject at hydra-sso, and `Consent`.
- [x] 9.3 Add the test sign-in's callback in `pkg/bridge/test_login.go`.
- [x] 9.4 Add the routes, the binding cookie, the receipt cookie and the pages in `pkg/bridge/handlers.go` and `pages.go`.
- [x] 9.5 Add tests in `pkg/bridge/service_test.go`, `test_login_test.go`, `handlers_test.go`, `pages_test.go`, `reasons_test.go`, `errors_test.go` and `validation_test.go`.

## 10. Servers

- [x] 10.1 Add the HTTP router in `pkg/web/router.go`, `middlewares.go` and `interfaces.go`. It has the browser pages, and the gateway for the 2 admin services behind CORS and authentication. It also has the request deadline and the error body.
- [x] 10.2 Add the status and version endpoints in `pkg/status/handlers.go` and `build.go`, and the metrics endpoint in `pkg/metrics/handlers.go`.
- [x] 10.3 Add the `serve` command in `cmd/serve.go`. It has the pending-migration check, wiring, and the gRPC server with its interceptors and 3 services. It also has the HTTP server with its timeouts and size limit, and the bounded shutdown.
- [x] 10.4 Add tests in `pkg/web/router_test.go`, `pkg/web/middlewares_test.go` and `pkg/status/handlers_test.go`.
- [x] 10.5 Add the in-process sign-in test `cmd/flow_integration_test.go` with its fakes of Kratos, hydra-sso and the tenant service in `cmd/fakes_test.go`.
- [x] 10.6 Add a Hydra container to `internal/testhelpers` (`containers.go`, `shared.go`, `oauth.go`) and the authentication test `cmd/authentication_integration_test.go`. The test runs over HTTP and gRPC. The service takes a token of a real Hydra. The service refuses a call with no token, or with a string that is no token. It also refuses a token signed by another key, and an expired token.

## 11. Packaging

- [x] 11.1 Add the build and test targets in `Makefile` and the rock in `rockcraft.yaml`. Add the gateway route and authorization policy in `k8s/istio.yaml`, and add `README.md`.

## 12. Verification

- [x] 12.1 Run `make test-unit` (`go generate ./...`, `go vet ./...`, `go test ./... -short`).
- [x] 12.2 Run `make test` (the unit tests and the integration tests, on PostgreSQL and Hydra containers).
- [x] 12.3 Run `openspec validate bring-your-own-sso --strict`.
