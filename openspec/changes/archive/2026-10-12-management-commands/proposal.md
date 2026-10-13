## Why

There is no console for the management API yet. Until there is one, an operator who has to register a tenant's identity provider or change a tenant's SSO policy needs a way that does not mean writing HTTP requests by hand, and the service's charm needs something it can run from its actions, as the tenant service's charm runs `tenant-service tenant …`.

## What Changes

The `sso-service` binary gains management commands. Each one is a client of the running service's own API on its gRPC listener, so the checks, the logging and the errors are the API's:

- `connection list|get|create|update|delete|test`
- `policy get|set`
- `domains get|set`

Two flags apply to all of them: `--grpc-endpoint` (default `localhost:50051`) and `--token`.

Nothing changes in the service or in its API.

## Capabilities

### New Capabilities
- `management-commands`: the command-line clients of the management API.

## Impact

- `cmd/client.go`, `cmd/connection.go`, `cmd/policy.go`, and their tests.
- `README.md`: a section on the commands.
