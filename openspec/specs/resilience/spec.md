# resilience Specification

## Purpose

The service sits on the sign-in path. It calls 5 other systems: its database, the Tenant Service, Kratos, hydra-sso and the identity providers of the customers. The platform does not control the identity providers. This capability says these things:

- How long anything can take.
- What the service tries again.
- What an outage of each dependency breaks.
- How the service stops.

Key decisions:

- **Nothing waits without a bound.** A dependency that is slow or down becomes an answer that the caller can act on. The answer says to try again now (`ABORTED`), or to try again later (`UNAVAILABLE`, or the `unavailable` message to a user).
- **The service tries again only what is safe to repeat.** That is the calls to the Tenant Service. A code exchange is not safe to repeat, because the code works once.
- **No circuit breaker.** We turned down a breaker for each connection. It adds state to every replica to save calls that already have a bound.
- **No state between requests that another replica needs.** Tickets and cookies open on any replica that has the same keys.
- **Rate limiting is not here.** It is left to the layer in front of the service.

Non-goals:

- A retry on behalf of a caller.
- A queue of work.
- A readiness check that probes the dependencies.

## Requirements
### Requirement: Deadlines
The system SHALL bound every wait as this table shows:

| what | bound |
|---|---|
| a whole request, HTTP or gRPC | 25 seconds. A gRPC caller can set a shorter deadline. That deadline applies. |
| one call to an identity provider | 10 seconds |
| everything that one operation asks of an identity provider | 15 seconds |
| one call to the Tenant Service, retries included | `TENANT_SERVICE_GRPC_TIMEOUT` (5 seconds) |
| one call to Kratos or hydra-sso | 5 seconds |
| the fetch of the service's own access token | 5 seconds |
| the wait for a database lock | 2 seconds |
| one database statement | 5 seconds |
| one database transaction | 10 seconds |
| a database transaction that stays idle | 15 seconds |
| `migrate` waiting for a lock | 3 seconds, then the run fails |
| the connection to the database when `serve` starts | 30 seconds |
| one request to the issuer of callers' tokens for its discovery document, when `serve` starts | 10 seconds |
| one request to that issuer for its keys, after `serve` started | 10 seconds |
| a ticket, a binding cookie, the `state` of a test | 30 minutes |
| a receipt cookie | until its ticket expires |

The database bounds are fixed in the code. They are not settings. The 10 seconds of a transaction include the call to the Tenant Service that the service makes inside it. The service holds no database lock across a call to an identity provider.

#### Scenario: A lock held by another request
- **WHEN** a policy write needs a connection row that another transaction holds for more than 2 seconds
- **THEN** the caller gets `ABORTED`
- **AND** the caller can try again
- **AND** a statement or a transaction that runs out of time rolls back
- **AND** the service answers `UNAVAILABLE` for it

#### Scenario: A migration behind a long transaction
- **WHEN** `migrate` needs a lock that another session holds for more than 3 seconds
- **THEN** the run fails
- **AND** the run can be repeated
- **AND** the statements that queued behind it waited no longer than 3 seconds

#### Scenario: A provider that does not answer
- **WHEN** an identity provider accepts the connection and never answers the token request
- **THEN** the service refuses the sign-in as `unavailable` after 10 seconds

### Requirement: Retries
The system SHALL retry a call to the Tenant Service that ends `UNAVAILABLE`. It SHALL make up to 3 attempts in all, with a backoff from 0.1 to 1 second. All the attempts SHALL be inside that call's deadline. The system SHALL reconnect to the Tenant Service with a backoff from 0.2 to at most 5 seconds. The system SHALL NOT retry calls to identity providers, Kratos or hydra-sso.

When the answer to a write is lost, the service possibly applied the write. A caller can repeat policy writes, domain writes and deletes. Each of them leaves the same state when applied twice. A delete that removed the binding but not the connection finishes when the caller repeats it.

#### Scenario: The tenant service restarts
- **WHEN** a call to the Tenant Service fails once with `UNAVAILABLE`
- **AND** the Tenant Service is back within the deadline of the call
- **THEN** the call succeeds
- **AND** the caller sees no error

### Requirement: What an outage breaks
The system SHALL keep working as far as it does not need the system that is down. The table shows what fails and what keeps working:

| down | fails | keeps working |
|---|---|---|
| one identity provider | sign-ins and tests through its connections | everything else |
| the Tenant Service | creating and deleting connections, reading and writing policy and domains (`UNAVAILABLE`), and the first sign-in of a subject with no link (`unavailable`) | sign-ins of subjects that have a link, the `SSOSignInService` RPCs, and reading, updating and testing connections |
| Kratos | every company sign-in at the callback (`unavailable`), and `CompleteAttempt`, `ListLinks` and `DeleteLink` (`UNAVAILABLE`) | the admin APIs and test sign-ins |
| hydra-sso | `/login`, `/callback` of a sign-in and `/consent` (a page, HTTP 500) | the APIs and test sign-ins |
| the database | everything that reads or writes a connection | `/api/v0/status`, `/api/v0/metrics` and `CompleteAttempt` |

#### Scenario: The tenant service is down
- **WHEN** a user whose account already holds a link signs in while the Tenant Service is down
- **THEN** the callback accepts the login request

### Requirement: Shutdown
The system SHALL stop on `SIGINT` or `SIGTERM`, or when one of its listeners fails. To stop, the system SHALL do these steps in order:

1. Accept no more requests on either listener.
2. Let the requests in flight finish for up to 20 seconds.
3. Cut whatever is still running.
4. Exit.

#### Scenario: A request in flight at shutdown
- **WHEN** a signal tells the service to stop while the service handles a request
- **THEN** the service answers that request if it finishes within 20 seconds
- **AND** the service accepts no new request

