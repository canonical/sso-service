## Why

There is no console for the management API yet. Until there is one, an operator needs a way to register a tenant's identity provider or change a tenant's SSO policy without hand-written HTTP requests. The charm of the service also needs something that it can run from its actions. The charm of the tenant service runs `tenant-service tenant …` in the same way.

## What Changes

The `sso-service` binary gets management commands. Each command is a client of the running service's own API on its gRPC listener. Thus the checks, the logging and the errors are those of the API. The commands are these:

- `connection list|get|create|update|delete|test`
- `policy get|set`
- `domains get|set`

These 2 flags apply to all the commands: `--grpc-endpoint` (default `localhost:50051`) and `--token`.

Nothing changes in the service or in its API.

## Capabilities

### New Capabilities
- `management-commands`: the command-line clients of the management API.

## Impact

- `cmd/client.go`, `cmd/connection.go`, `cmd/policy.go`, and their tests.
- `README.md`: a section on the commands.
