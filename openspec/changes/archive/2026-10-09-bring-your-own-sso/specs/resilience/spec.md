## Purpose

The service sits on the sign-in path and calls five other systems: its database, the tenant service, Kratos, hydra-sso and the customers' identity providers, which the platform does not control. This capability says how long anything may take, what is tried again, what an outage of each dependency breaks, and how the service stops.

Key decisions:

- **Nothing waits without a bound.** A dependency that is slow or down becomes an answer the caller can act on: try again now (`ABORTED`), or try again later (`UNAVAILABLE`, or the `unavailable` message to a user).
- **Only what is safe to repeat is retried**: calls to the tenant service. A code exchange is not; the code works once.
- **No circuit breaker.** A breaker per connection was turned down: it would add state to every replica to save calls that are already bounded.
- **No state between requests that another replica needs.** Tickets and cookies open on any replica with the same keys.
- **Rate limiting is not here.** It is left to the layer in front of the service.

Non-goals: retrying on behalf of callers; queuing work; a readiness check that probes dependencies.

## ADDED Requirements

### Requirement: Deadlines
The system SHALL bound every wait:

| what | bound |
|---|---|
| a whole request, HTTP or gRPC | 25 seconds; a gRPC caller's own shorter deadline stands |
| one call to an identity provider; everything one operation asks of it | 10 seconds; 15 seconds |
| one call to the tenant service, retries included | `TENANT_SERVICE_GRPC_TIMEOUT` (5 seconds) |
| one call to Kratos or hydra-sso | 5 seconds |
| fetching the service's own access token | 5 seconds |
| waiting for a database lock; one statement; one transaction; a transaction left idle | 2; 5; 10; 15 seconds |
| `migrate` waiting for a lock | 3 seconds, then the run fails |
| reaching the database when `serve` starts | 30 seconds |
| one request to the issuer of callers' tokens: its discovery document when `serve` starts, its keys later | 10 seconds |
| a ticket, a binding cookie, a test's `state` | 30 minutes |
| a receipt cookie | until its ticket expires |

The database bounds are fixed in the code, not settings. A transaction's 10 seconds include the call to the tenant service made inside it. No database lock is held across a call to an identity provider.

#### Scenario: A lock held by another request
- **WHEN** a policy write needs a connection row another transaction has held for more than 2 seconds
- **THEN** the caller gets `ABORTED` and may try again; a statement or transaction that runs out of time is rolled back and answered `UNAVAILABLE`

#### Scenario: A migration behind a long transaction
- **WHEN** `migrate` needs a lock that another session has held for more than 3 seconds
- **THEN** the run fails and can be repeated, and the statements that queued behind it waited no longer than that

#### Scenario: A provider that does not answer
- **WHEN** an identity provider accepts the connection and never answers the token request
- **THEN** the sign-in is refused as `unavailable` after 10 seconds

### Requirement: Retries
The system SHALL retry a call to the tenant service that ends `UNAVAILABLE`, up to three attempts in all, with a backoff from 0.1 to 1 second, inside that call's deadline. It SHALL reconnect to the tenant service with a backoff from 0.2 to at most 5 seconds. It SHALL NOT retry calls to identity providers, Kratos or hydra-sso.

A write whose answer was lost may have been applied. Policy writes, domain writes and deletes can be repeated: each leaves the same state when applied twice, and a delete that removed the binding but not the connection finishes when repeated.

#### Scenario: The tenant service restarts
- **WHEN** a call to the tenant service fails once with `UNAVAILABLE` and the tenant service is back within the call's deadline
- **THEN** the call succeeds and the caller sees no error

### Requirement: What an outage breaks
The system SHALL keep working, as far as it does not need the system that is down:

| down | fails | keeps working |
|---|---|---|
| one identity provider | sign-ins and tests through its connections | everything else |
| the tenant service | creating and deleting connections, reading and writing policy and domains (`UNAVAILABLE`); the first sign-in of a subject with no link (`unavailable`) | sign-ins of subjects that have a link; the `SSOSignInService` RPCs; reading, updating and testing connections |
| Kratos | every company sign-in at the callback (`unavailable`); `CompleteAttempt`, `ListLinks`, `DeleteLink` (`UNAVAILABLE`) | the admin APIs; test sign-ins |
| hydra-sso | `/login`, `/callback` of a sign-in and `/consent` (a page, HTTP 500) | the APIs; test sign-ins |
| the database | everything that reads or writes a connection | `/api/v0/status`, `/api/v0/metrics`, `CompleteAttempt` |

#### Scenario: The tenant service is down
- **WHEN** a user whose account already holds a link signs in while the tenant service is down
- **THEN** the callback accepts the login request

### Requirement: Shutdown
The system SHALL, on `SIGINT` or `SIGTERM`, or when one of its listeners fails, stop accepting requests on both listeners, let the requests in flight finish for up to 20 seconds, cut whatever is still running, and exit.

#### Scenario: A request in flight at shutdown
- **WHEN** the service is told to stop while a request is being handled
- **THEN** that request is answered if it finishes within 20 seconds, and no new request is accepted
