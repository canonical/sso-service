## Why

The code had these gaps:

- A test passed when the provider marked the address as not confirmed. Then each sign-in through the connection failed.
- The scheme `Bearer` had one case only. Over gRPC, a header with no token passed. A 401 had no `WWW-Authenticate`.
- `serve` accepted a `PUBLIC_URL` with no host, with userinfo, or with an empty query or fragment. With `DEV`, any scheme passed.
- `serve` checked `ENVELOPE_KEY` after it reached the database. `migrate` accepted any `--format`.
- Provider text reached the log with characters that are not printable. The cut at 120 bytes split some characters.
- A cancelled call looked like an outage: `UNAVAILABLE`, an `error` log and a failed write.
- At shutdown, the last export had only the time that the drain left.

## What Changes

- A test fails on `email_verified: false`. An id_token with no `email_verified` still passes.
- The service reads `Bearer` in any case. A header with no token, or with more than one word after the scheme, is not a bearer token. Each HTTP 401 has `WWW-Authenticate: Bearer`.
- `serve` refuses a `PUBLIC_URL` with no host, with userinfo, or with a query or fragment, also an empty one. With `DEV`, the scheme can be `https` or `http` only.
- `serve` checks `ENVELOPE_KEY` before the database. `migrate` refuses a `--format` other than `text` or `json`.
- The log keeps only printable characters of provider text, and cuts between 2 characters.
- A cancelled call ends with `CANCELED`, a `debug` log and no failed write.
- The export of the last spans has its own 3 seconds.

## Non-goals

- A check of the address of an account in a test.
- A test that fails on an id_token with no `email_verified`.
- A scheme other than `Bearer`.
- A metric for cancelled calls.

## Capabilities

### Modified Capabilities
- `test-sign-in`: `email_verified: false`.
- `caller-authentication`: the scheme and `WWW-Authenticate`.
- `configuration`: `PUBLIC_URL`, `ENVELOPE_KEY`, `migrate --format`.
- `company-sign-in`: provider text in the log.
- `observability`: cancelled calls.
- `api-conventions`: `CANCELED`.
- `resilience`: the last export.

## Impact

- `pkg/bridge`, `pkg/authentication`, `pkg/admin`, `pkg/sso`, `internal/tenants`, `cmd`.
- A `PUBLIC_URL` with an empty `?` or `#` stops the start. With `DEV`, so does a scheme other than `http` or `https`.
- A connection whose provider marks every address as not confirmed fails its test.
