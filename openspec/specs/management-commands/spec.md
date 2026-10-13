# management-commands Specification

## Purpose

There is no console for the management API yet. An operator who registers a tenant's identity provider or changes a tenant's SSO policy needs a way that does not mean writing requests by hand, and the service's charm needs something its actions can run. The `sso-service` binary therefore carries command-line clients of its own management API.

Key decisions:

- **The commands are clients of the API, not a second way in.** They call the running service on its gRPC listener, so every check, log line and error is the API's; nothing reads the database directly.
- **The caller brings the token.** A command sends the token it is given and obtains none itself.
- **A client secret need not be an argument.** It can be passed in the environment, so that it does not show in a process list or in a record of the command.

Non-goals: commands for the sign-in service; getting a token.

## Requirements
### Requirement: Commands are clients of the running service
The `sso-service` binary SHALL offer the management commands `connection`, `policy` and `domains`. Each command SHALL call the API of a running service on its gRPC listener, at the address given by `--grpc-endpoint` (default `localhost:50051`), and SHALL send the value of `--token`, when one is given, as the bearer token of the call. A command SHALL NOT read the database or any of the service's settings.

A command SHALL print the API's response as JSON on standard output and exit with status 0. When the call fails it SHALL print the API's error on standard error and exit with a non-zero status.

#### Scenario: A call the API refuses
- **WHEN** `connection get` names a connection that another tenant owns
- **THEN** the command prints the API's `CONNECTION_NOT_FOUND` error and exits with a non-zero status

#### Scenario: No token
- **WHEN** a management command is run without `--token` against a service that authenticates its callers
- **THEN** the call is refused as unauthenticated and the command exits with a non-zero status

### Requirement: Connection commands
The system SHALL offer:

- `connection list [tenant-id]`: the tenant's connections (`ListConnections`), or every tenant's when no tenant is given (`ListAllConnections`), with `--page-size` and `--page-token`.
- `connection get <tenant-id> <connection-id>` (`GetConnection`).
- `connection create <tenant-id> --label --issuer --client-id` (`CreateConnection`).
- `connection update <tenant-id> <connection-id> [--label]` (`UpdateConnection`): only the label and the client secret that are given change, and the command fails without calling the API when neither is given.
- `connection delete <connection-id>` (`DeleteAnyConnection`): the connection is deleted whichever tenant owns it, also when that tenant no longer exists.
- `connection test <tenant-id> <connection-id>` (`StartTestLogin`): prints the URL of the test sign-in, to open in a browser.

`create` and `update` SHALL take the client secret from `--client-secret` or, when the flag is not set, from the `CLIENT_SECRET` environment variable, so that a caller can keep it out of the command's arguments.

#### Scenario: A secret passed in the environment
- **WHEN** `connection create` is run with `CLIENT_SECRET` set and no `--client-secret`
- **THEN** the connection is created with the secret from the environment

#### Scenario: Changing only the label
- **WHEN** `connection update` is run with `--label` and no client secret
- **THEN** the label changes and the stored client secret is kept

### Requirement: Policy and domain commands
The system SHALL offer:

- `policy get <tenant-id>` (`GetTenantSSOPolicy`).
- `policy set <tenant-id> --enforcement optional|required [--binding <connection-id>[:active|:inactive]]... [--auto-join]` (`PutTenantSSOPolicy`): the bindings given replace the tenant's bindings, a binding being active unless it ends in `:inactive`. Any other `--enforcement` or binding state SHALL fail without calling the API.
- `domains get <tenant-id>` (`GetTenantDomains`).
- `domains set <tenant-id> [domain...]` (`SetTenantDomains`): the domains given replace the tenant's domains; none given removes them all.

#### Scenario: A binding left out
- **WHEN** `policy set` is run for a tenant with two bindings and names one of them
- **THEN** the other binding is removed

#### Scenario: An unknown binding state
- **WHEN** `policy set` is run with `--binding <connection-id>:off`
- **THEN** the command fails and the policy is not changed

