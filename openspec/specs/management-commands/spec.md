# management-commands Specification

## Purpose

There is no console for the management API yet. An operator who registers a tenant's identity provider or changes a tenant's SSO policy needs a way to do that without hand-written requests. The charm of the service also needs something that its actions can run. Thus the `sso-service` binary carries command-line clients of its own management API.

Key decisions:

- **The commands are clients of the API, not a second way in.** They call the running service on its gRPC listener. Thus every check, log line and error is that of the API. Nothing reads the database directly.
- **The caller brings the token.** A command sends the token that the caller gives it. A command does not get a token itself.
- **A client secret does not need to be an argument.** The caller can pass it in the environment. Then the client secret does not show in a process list or in a record of the command.

Non-goals:

- Commands for the sign-in service.
- A way to get a token.

## Requirements
### Requirement: Commands are clients of the running service
The `sso-service` binary SHALL offer the management commands `connection`, `policy` and `domains`. Each command SHALL call the API of a running service on its gRPC listener, at the address that `--grpc-endpoint` gives (default `localhost:50051`). When the caller gives `--token`, the command SHALL send its value as the bearer token of the call. A command SHALL NOT read the database or any of the service's settings.

A command SHALL print the response of the API as JSON on standard output and exit with status 0. When the call fails, the command SHALL print the error of the API on standard error and exit with a non-zero status.

#### Scenario: A call the API refuses
- **WHEN** `connection get` names a connection that another tenant owns
- **THEN** the command prints the `CONNECTION_NOT_FOUND` error of the API and exits with a non-zero status

#### Scenario: No token
- **WHEN** a management command runs without `--token` against a service that authenticates its callers
- **THEN** the service refuses the call as unauthenticated
- **AND** the command exits with a non-zero status

### Requirement: Connection commands
The system SHALL offer these commands:

- `connection list [tenant-id]` lists the connections of the tenant (`ListConnections`). When the caller gives no tenant, the command lists the connections of every tenant (`ListAllConnections`). The command takes `--page-size` and `--page-token`.
- `connection get <tenant-id> <connection-id>` (`GetConnection`).
- `connection create <tenant-id> --label --issuer --client-id` (`CreateConnection`).
- `connection update <tenant-id> <connection-id> [--label]` (`UpdateConnection`). Of the label and the client secret, only the values that the caller gives change. When the caller gives neither, the command fails and does not call the API.
- `connection delete <connection-id>` (`DeleteAnyConnection`). The command deletes the connection, whichever tenant owns it. This is also true when that tenant no longer exists.
- `connection test <tenant-id> <connection-id>` (`StartTestLogin`). The command prints the URL of the test sign-in, to open in a browser.

`create` and `update` SHALL take the client secret from `--client-secret`. When the flag is not set, they SHALL take the client secret from the `CLIENT_SECRET` environment variable. Thus a caller can keep the client secret out of the arguments of the command.

#### Scenario: A secret passed in the environment
- **WHEN** `connection create` runs with `CLIENT_SECRET` set and no `--client-secret`
- **THEN** the service creates the connection with the secret from the environment

#### Scenario: Changing only the label
- **WHEN** `connection update` runs with `--label` and no client secret
- **THEN** the label changes and the service keeps the stored client secret

### Requirement: Policy and domain commands
The system SHALL offer these commands:

- `policy get <tenant-id>` (`GetTenantSSOPolicy`).
- `policy set <tenant-id> --enforcement optional|required [--binding <connection-id>[:active|:inactive]]... [--auto-join]` (`PutTenantSSOPolicy`). The bindings that the caller gives replace the bindings of the tenant. A binding is active unless it ends in `:inactive`. For any other `--enforcement` or binding state, the command SHALL fail and SHALL NOT call the API.
- `domains get <tenant-id>` (`GetTenantDomains`).
- `domains set <tenant-id> [domain...]` (`SetTenantDomains`). The domains that the caller gives replace the domains of the tenant. When the caller gives no domain, the command removes all the domains of the tenant.

#### Scenario: A binding left out
- **WHEN** `policy set` runs for a tenant with 2 bindings and names one of them
- **THEN** the command removes the other binding

#### Scenario: An unknown binding state
- **WHEN** `policy set` runs with `--binding <connection-id>:off`
- **THEN** the command fails and the policy does not change

