## 1. Commands

- [x] 1.1 Add the `--grpc-endpoint` and `--token` flags, the gRPC clients and the JSON output in `cmd/client.go`.
- [x] 1.2 Add `connection list|get|create|update|delete|test` in `cmd/connection.go`. The commands take the client secret from `--client-secret` or the `CLIENT_SECRET` environment variable.
- [x] 1.3 Add `policy get|set` and `domains get|set` in `cmd/policy.go`.
- [x] 1.4 Add tests in `cmd/admin_test.go` for the bindings that a `--binding` flag names, and for the update mask of `connection update`.

## 2. Documentation

- [x] 2.1 Describe the commands in `README.md`.
