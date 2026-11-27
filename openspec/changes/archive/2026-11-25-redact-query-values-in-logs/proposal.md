## Why

- At level `debug` the request log had only the path of each request. The names of the query parameters were not in it.
- These names help an operator to find the cause of a problem. The values carry authorization codes and addresses.

## What Changes

- The request log has the path and the names of the query parameters, in their order.
- The system replaces the value of each parameter with `REDACTED`. The system also redacts a parameter that a later version adds.

## Non-goals

- A list of parameters whose values the log can show.
- Part of a value, as in `a***@example.com`. The sign-in log lines keep that form.

## Capabilities

### Modified Capabilities
- `observability`: the request log.

## Impact

- `internal/logging`.
- The request log at level `debug`.
