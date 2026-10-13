## 1. Commands

- [x] 1.1 Add the `--grpc-endpoint` and `--token` flags, the gRPC clients and the JSON output in `cmd/client.go`.
- [x] 1.2 Add `connection list|get|create|update|delete|test` in `cmd/connection.go`, with the client secret taken from `--client-secret` or the `CLIENT_SECRET` environment variable.
- [x] 1.3 Add `policy get|set` and `domains get|set` in `cmd/policy.go`.
- [x] 1.4 Add tests in `cmd/admin_test.go`: the bindings a `--binding` flag names, and the update mask of `connection update`.

## 2. Documentation

- [x] 2.1 Describe the commands in `README.md`.
