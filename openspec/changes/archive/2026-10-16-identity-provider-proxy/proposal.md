## Why

Some networks have no way out but an HTTP proxy. The service sent its requests to the identity providers without a proxy, so it could not reach a provider in such a network. The other requests of the service already used the proxy that the environment names.

## What Changes

- The service sends a request to an identity provider through the proxy that `HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY` name for its URL.
- Before a request goes to a proxy, the service resolves the host of the request. It refuses the request when one of the addresses is not a public unicast address. Through a proxy, the dial check sees only the proxy.
- The service connects to the address of the proxy without the address check.
- Without a proxy, nothing changes.

## Capabilities

### Modified Capabilities
- `identity-provider-requests`: the outbound-request guard allows a proxy, with a check of the host before the proxy.
- `configuration`: the proxy variables.

## Impact

- `internal/idp/safehttp.go`, `internal/idp/client.go`, and their tests.
- `go.mod`: `golang.org/x/net` is a direct dependency.
