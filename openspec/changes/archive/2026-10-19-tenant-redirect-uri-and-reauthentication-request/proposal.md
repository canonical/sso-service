## Why

Each connection had its own redirect URI, `<PUBLIC_URL>/callback/<connection id>`. A tenant admin knew the URI only after the connection existed. Many identity providers ask for the redirect URI when the admin registers the client. Thus the admin had to go back to the provider after the connection was made.

When an app asked for a fresh sign-in, the service sent `prompt=login` and `max_age=0` to the identity provider. It refused the answer unless the id_token had an `auth_time` from after the attempt started. Many providers ignore these parameters or send no `auth_time`, for example Google and dex. Through such a provider, every sign-in that asked for re-authentication failed. That included the sign-in again before a privileged settings change. The service cannot make an external provider ask the user to sign in again, so it must not claim to.

## What Changes

- All the connections of a tenant share one redirect URI, `<PUBLIC_URL>/callback/<tenant id>`. The admin knows it before the connection exists.
- The service takes an answer only at the redirect URI of the tenant that owns the connection of the sign-in or the test. The check moves after the service reads the connection.
- The service still sends `prompt=login` and `max_age=0` when the ticket asks for re-authentication. It does not check `auth_time`, and it does not read `auth_time` any more.
- The reason `reauthentication_not_done` and its message are removed.
- The test sign-in page does not warn about a missing `auth_time` any more.
- A test sign-in does not send `prompt=login` and `max_age=0` any more. It asks what a user's sign-in asks.
- **BREAKING** for a deployment: each connection made before this change needs the new redirect URI at its identity provider.

## Capabilities

### Modified Capabilities
- `company-sign-in`: the redirect URI per tenant, the order of the callback checks, re-authentication as a request, one reason less.
- `test-sign-in`: the redirect URI per tenant, no `prompt=login` and `max_age=0`, and no `auth_time` warning.
- `identity-provider-requests`: the system does not read `auth_time`.
- `connections`: the `redirect_uri` of a connection.
- `configuration`: the redirect URI that `PUBLIC_URL` gives.
- `api-conventions`, `caller-authentication`: the path of the callback.
- `observability`: the number of reasons, and the route label of the callback.

## Impact

- `internal/types/types.go`, `internal/idp/client.go`, `pkg/bridge`, `pkg/admin`, and their tests.
- `README.md` and `k8s/istio.yaml`: the path of the callback.
