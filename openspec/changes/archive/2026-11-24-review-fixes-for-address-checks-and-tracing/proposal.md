## Why

The code had these gaps:

- The address check accepted an IPv6 address with a zone, and 4 ranges that lead inside a network.
- The service did not check the address of a proxy, also for a request that did not go through it.
- Keys that the guard refused counted as an unavailable provider. A response body over the size limit lost its kind.
- The service accepted an issuer with an empty query or fragment.
- An expired challenge got the page of an unavailable service, HTTP 500.
- Calls that waited for the service's token ran past their deadlines.
- With no trace endpoint, spans went into the logs. Identity providers got trace context. The trace context of an HTTP caller was the parent of its span.
- The service started with a pool of no connections, or a timeout of zero for the tenant service.

## What Changes

- The guard refuses an address with an IPv6 zone, and `0.0.0.0/8`, `::/96`, `::ffff:0:0:0/96` and `2001::/32`.
- The service skips the check of a proxy's address only for a request through that proxy.
- The kind of a failure follows its cause, also for keys and response bodies.
- Requests to identity providers carry no trace context.
- The system refuses an issuer with an empty query or fragment.
- An expired login or consent challenge gets the page of an unknown challenge.
- Calls that need the service's token share one fetch, and each waits only until its own deadline.
- With no trace endpoint, the service runs without tracing. Server spans have the name of their route. An HTTP request starts a new trace that links to the caller's trace context. No baggage. The export follows the exporter's TLS settings. The service exports the last spans when it stops.
- The service refuses to start with pool sizes or a tenant service timeout that cannot work.

## Non-goals

- A setting that sends trace context to an identity provider.
- A check of the addresses that a proxy connects to.
- An HTTP caller's trace context as the parent of a span.

## Capabilities

### Modified Capabilities
- `identity-provider-requests`: refused addresses, proxies, failure kinds, trace context.
- `connections`: the issuer.
- `company-sign-in`: an expired challenge.
- `resilience`: the token fetch.
- `observability`: traces.
- `configuration`: start checks and `TRACING_ENABLED`.

## Impact

- `internal/idp`, `internal/hydra`, `internal/tenants`, `internal/config`, `internal/tracing`, `cmd`.
- With tracing on and no endpoint, a deployment now has no traces. Before, the spans went to standard output.
