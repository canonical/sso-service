## Context

See `proposal.md` for the motivation. The platform signs users in with Kratos (accounts, sessions, credentials), issues apps their tokens with Hydra, and keeps tenants, memberships and invitations in the tenant service. The login UI drives every sign-in. Authorization of management requests is done at the gateway by the platform's Authorization Service.

This service adds one thing to that picture: a tenant's own OIDC identity provider as a way to sign in. It is written like the platform's other Go services: a handler and a service per API package, PostgreSQL through `internal/db` and `internal/storage`, the shared `pkg/authentication`, the security logger, one Prometheus monitor.

Who talks to the service:

| caller | interface | for |
|---|---|---|
| the login UI | gRPC, `SSOSignInService` | options, tickets, confirming an attempt, links |
| tenant admins, through the gateway | HTTP gateway (or gRPC), `SSOTenantAdminService` | connections, test sign-ins, the tenant's policy |
| platform admins, through the gateway | HTTP gateway (or gRPC), `SSOPlatformAdminService` | all connections, reading and setting domains, deleting any connection |
| users' browsers, sent by hydra-sso and by identity providers | `/login`, `/callback/{connection_id}`, `/consent`, `/error` | the company sign-in itself |

What the service talks to: its PostgreSQL database; the tenant service (gRPC, with its own client-credentials token); Kratos's admin API (read accounts and links, remove a link); hydra-sso's admin API (login and consent requests); the customers' identity providers (discovery, token, keys).

## Goals / Non-Goals

**Goals:**
- Adding, changing or removing a connection never changes Kratos's or Hydra's configuration and never affects another tenant.
- No change to Kratos's or Hydra's code.
- Reuse what Kratos already does (linking, registration, verification); build only what it cannot do.
- An outage of this service, of hydra-sso or of one identity provider breaks only company sign-ins, and for a provider only its own.
- Any number of replicas, with no state shared outside PostgreSQL and the keys.

**Non-Goals:**
- SAML; sharing a connection between tenants; a circuit breaker per connection; rate limiting; re-encrypting secrets under a new key; proof of domain ownership.
- The sign-in screen, tenant choice, sessions, MFA and memberships: the login UI, Kratos and the tenant service keep them.

## Decisions

### Decision 1: A second Hydra behind one Kratos provider
- **Decision**: Kratos gets one static OIDC provider, `byo-sso`, whose issuer is a second Hydra, `hydra-sso`. This service is hydra-sso's login and consent provider and does the OIDC exchange with the tenant's real identity provider. It accepts the hydra-sso login with the subject `<connection id>:<subject>`, so the credential Kratos stores, `byo-sso:<connection id>:<subject>`, is unique per connection.
- **Rationale**: Kratos reads its providers from configuration. Connections are rows in this service's database instead; Kratos and hydra-sso are configured once.
- **Alternatives considered**: A Kratos provider per customer: turned down, because every new or changed connection would be a Kratos configuration change and rollout that touches all tenants, and customers' client secrets would live in Kratos's configuration. Changing Kratos so that providers can be added at run time: turned down, the platform runs Kratos unmodified. The cost of the choice is one more Hydra to run.

### Decision 2: Sealed tickets and a binding cookie instead of stored attempts
- **Decision**: A sign-in attempt is a ticket: the tenant, the connection, the address, the re-authentication flag and an expiry, sealed with the service's key. The per-browser part (`state`, nonce, PKCE verifier, login challenge) is a sealed cookie. Nothing about a sign-in is stored, and neither is the fact that one was completed: the browser holds a receipt for it (Decision 10).
- **Rationale**: The sign-in path writes nothing to the database, there is nothing to clean up, any replica finishes what another started, and the database holds no user data.
- **Alternative considered**: A table of attempts with a cleanup job: turned down. Its one advantage is single use, and the pieces that matter are single use already: the identity provider's code works once, the nonce binds the id_token to this sign-in, and the cookie is cleared after the callback. A replayed ticket only starts another sign-in that still needs the user at the provider.

### Decision 3: Kratos writes every link
- **Decision**: The service never writes a credential or an account. For a subject with no link it checks that the address is the one the user entered, that the tenant has an active binding to the connection that applies to the address, and that the address is a member's, invited or admitted by auto-join. Then it lets the sign-in through. Kratos sees a subject it does not know and treats it as a registration: if an account has the address, the registration stops at the conflict and becomes Kratos's account linking, where the user signs in to the account another way before the link is added; if none has, Kratos registers the account.
- **Rationale**: Proof that a user owns an account is Kratos's job and it already does it. A hostile identity provider that asserts someone else's address gains nothing: the link is added only after that account's owner has signed in.
- **Alternative considered**: Writing the credential from here through Kratos's admin API, after proving ownership with a confirmation code of our own: turned down, it would rebuild account linking outside Kratos, with codes, limits and cleanup of its own.

### Decision 4: A redirect URI per connection
- **Decision**: Each connection has its own redirect URI, `<PUBLIC_URL>/callback/<connection id>`, returned as `redirect_uri` for the admin to register at the provider. An answer is taken only when the path's connection is the one the sign-in, or the test, was started through.
- **Rationale**: With one shared callback, an identity provider could answer a sign-in that was started at another one (a mix-up attack). All connections' providers are chosen by tenant admins, so one of them must be assumed hostile.
- **Alternative considered**: One `/callback` with the connection found from the `state`: turned down for that reason.

### Decision 5: The tenant service owns the policy; this service checks only what needs connection data
- **Decision**: Bindings, enforcement, auto-join and domains are stored by the tenant service. Tenant admins write them through this service, which checks, under locks on the connection rows, that each bound connection is the tenant's and each active one is tested, and then passes the write on. The tenant service checks the policy as a whole under its own row lock. Deleting a connection removes its binding first, under the same lock.
- **Rationale**: Every sign-in reads the policy at the tenant service, also sign-ins that never use this service, so it must live there. Only this service knows a connection's owner and status.
- **Alternatives considered**: Storing the policy here: turned down, an outage here would break sign-ins that have nothing to do with it. Admins writing straight to the tenant service, which calls back here to check connections: turned down, a cycle between two services and a lock across both. Versions or read-modify-write between the two: turned down, each write names one part and the storing service checks it against what is stored.

### Decision 6: `CompleteAttempt` only verifies
- **Decision**: `CompleteAttempt` answers whether the ticket's sign-in ended at the given account. It reads the ticket, the account's address and links, and the receipt the browser holds (Decision 10), and writes nothing.
- **Rationale**: The login UI needs one fact to record which connection a session came from. A call with no side effects can be retried and returns the same answer.
- **Alternative considered**: Having it also create the membership of an invited or auto-joined user, and remove the account when the membership is refused: turned down. The login UI is the one that knows when a sign-in has passed all of the tenant's checks, and asks the tenant service for the membership then.
- **Consequence**: If an invitation expires or auto-join is switched off after the callback let a new user through, Kratos has registered an account that ends up with no tenant. Nothing in this service removes it.

### Decision 7: No stored test result
- **Decision**: A test sign-in's outcome is shown on the page it ends on. The only thing kept is `tested_at`, set at the first success and never cleared. The test's context travels sealed in its `state`.
- **Rationale**: Afterwards one fact matters: can this connection be made active.
- **Alternative considered**: A table of test runs and an RPC to read a result: turned down, rows and cleanup for something read once.
- **Consequence**: Reloading the result page sends a spent code and shows a failure; the connection's status tells. A test is not bound to a browser, so whoever holds the URL can finish it; all that does is mark the connection tested.

### Decision 8: Authentication here, authorization at the gateway
- **Decision**: The service verifies the caller's JWT and nothing more. The gateway asks the Authorization Service whether the caller may act on the tenant in the path, or is a platform admin. `k8s/istio.yaml` routes `/api/v0/sso/tenants` and `/api/v0/sso/connections` through that check. The service itself only makes sure a resource belongs to the tenant in the path. RPCs no user may call have no HTTP route.
- **Rationale**: One place decides who may do what, for every service of the platform.
- **Alternative considered**: Roles or a list of platform admins in this service: turned down, a second copy of a decision that would drift.
- **Consequence**: On the gRPC listener any admitted token can call any RPC with any account id. The internal network is the control there.

### Decision 9: The outbound-request guard
- **Decision**: Every request to an identity provider goes through one HTTP client that allows only `https`, connects only to public unicast addresses (checked when dialling, after name resolution), uses no proxy, follows no redirects, caps the response at 64 KiB and has deadlines.
- **Rationale**: Tenant admins choose the issuer, and its discovery document chooses the other URLs. The check has to be where the connection is made, or a name that resolves differently the second time gets past it.
- **Alternatives considered**: Checking the issuer's address once at create: turned down, it does not cover later resolutions or the discovered endpoints. Requiring token and key endpoints on the issuer's origin: turned down, real providers host them elsewhere.

### Decision 10: A receipt in the browser shows that the sign-in was completed
- **Decision**: When the callback accepts a sign-in it sets a cookie, named for the ticket, whose value is a receipt: an AES-256-GCM authentication tag, under the service's key, over the ticket and the subject accepted at hydra-sso, with no content. The login UI reads the cookie of the ticket it holds and sends its value in `CompleteAttempt`, which requires a receipt that is valid for the ticket and for the subject of one of the account's links to the ticket's connection.
- **Rationale**: A valid ticket, the account's address and a link do not show that the ticket's sign-in ever reached the identity provider. An account linked to the connections of two tenants could start a sign-in at one, sign in through the other, and have that session confirmed for the first ticket. That is a way in to a tenant that requires its company sign-in, for a user its identity provider no longer knows and for whoever runs the other tenant's identity provider. A receipt exists only once the identity provider has answered for this ticket, and only in the browser the answer came through.
- **Alternatives considered**: A record on a server, in a table of completed attempts here or in the rows hydra-sso keeps for its login flows: turned down. It says that the identity provider answered for the ticket, not in which browser: whoever started an attempt can have another user's browser finish it, silently when that user is signed in at the identity provider, and then present the ticket with a session made another way. A table would also bring stored attempts back (Decision 2). A sealed cookie that holds the connection, the subject and a time: turned down, nothing about a user is to be readable in, or recoverable from, a cookie, and a tag over the same facts proves as much.
- **Consequences**: The service's browser pages and the login UI have to be served on the same host, or the browser does not send the login UI the cookie; the same is already required of Kratos and the login UI. A receipt is not single use: the same browser can present it again until the ticket expires, which gives nothing beyond the session the sign-in already produced. And it proves that this browser completed this ticket's sign-in as a subject the account is linked to, not that this particular Kratos session was produced by it.

## Data model

One table, `sso_connections`:

| column | |
|---|---|
| `id` | UUIDv7, primary key |
| `owner_tenant_id` | the tenant service's id; no foreign key across services; indexed |
| `label`, `issuer`, `client_id` | `issuer` and `client_id` never change |
| `client_secret` | the encrypted secret |
| `created_by` | the creating user's account id; null for a service client |
| `created_at`, `updated_at` | the database's clock; `updated_at` moves with every change of the row |
| `tested_at` | null while a draft; the database's clock |

Links are Kratos credentials. Sign-in attempts are tickets and cookies. hydra-sso keeps a row per login flow, as any Hydra does.

## Flows

### A company sign-in
1. The user enters their address in the login UI, which asks the tenant service which tenants and bindings apply, and this service for the labels (`ListOptions`). On the user's pick it calls `StartAttempt` and gets a ticket.
2. The login UI submits Kratos's `byo-sso` sign-in with the ticket as `login_hint`. Kratos redirects the browser to hydra-sso, which redirects it to `/login?login_challenge=…`.
3. `/login` reads the login request from hydra-sso, opens the ticket, reads the connection and the provider's discovery document, sets the binding cookie and redirects the browser to the identity provider.
4. The user signs in there. The provider redirects the browser to `/callback/{connection_id}` with a code.
5. The callback opens the cookie, reads the login request and the ticket again, compares the path's connection with the ticket's, exchanges the code, verifies the id_token and applies the address checks. It asks Kratos who holds the link. For a subject with no link it asks Kratos who has the address and the tenant service whether the address may sign in. It accepts the hydra-sso login, clears the cookie and sets the receipt cookie.
6. hydra-sso sends the browser to `/consent`, which accepts with the address as id_token claims. hydra-sso returns the browser to Kratos with a code, and Kratos gets an id_token with `sub`, `email` and, when the provider said it, `email_verified`.
7. Kratos finds the credential and signs the user in, or links, or registers (Decision 3).
8. Back in the login UI with a session, it calls `CompleteAttempt` with the ticket, the account and the receipt it reads from the browser's cookie. It then applies the tenant's rules and, for an invited or auto-joined user, asks the tenant service for the membership.

A refusal in step 3 or 5 rejects the hydra-sso login with a fixed message, which the user reads in the login UI.

### A test sign-in
1. A tenant admin calls `StartTestLogin`. The service decrypts the secret, reads the discovery document afresh, probes the token endpoint with a made-up code, and returns the authorization URL with a sealed `state`.
2. The admin opens the URL and signs in. The provider redirects to `/callback/{connection_id}`.
3. The callback opens the `state`, exchanges the code, verifies the id_token and requires an `email`. On success it sets `tested_at` if it is not set, and shows the result page.

### A policy write
1. `PutTenantSSOPolicy` opens a transaction and locks the rows of all bound connections (`FOR UPDATE`, in id order, one statement).
2. It checks owner and status, then calls the tenant service's policy write inside the transaction.
3. The tenant service locks the tenant's row, checks the policy as a whole, stores it and answers. The transaction ends and the locks go.

`SetTenantDomains` involves no connection: it is passed straight on. `GetTenantDomains` reads the policy at the tenant service and returns its domains.

### Deleting a connection
1. `DeleteConnection` or `DeleteAnyConnection` opens a transaction and locks the connection's row.
2. It asks the tenant service to remove the connection's binding from the owner's policy. That is refused when a tenant that requires company sign-in would be left with no active binding. On the platform admins' route, an owner that no longer exists has nothing to unbind.
3. It deletes the row.

## Failure handling

- **A dependency is down**: see the table in `specs/resilience/spec.md`. On the API the caller gets `UNAVAILABLE`; in the browser flow the login request is rejected as `unavailable`, or, when hydra-sso itself is down, the service shows a page.
- **A sign-in is abandoned** at the provider or during account linking: nothing was stored, so nothing is left here. The ticket and the cookies expire within 30 minutes.
- **The browser does not return the receipt** (the cookie was removed or refused, or the login UI is on another host): `CompleteAttempt` answers `NOT_APPLICABLE`, and the user starts again from the login UI.
- **The callback fails after the code was exchanged** (for instance hydra-sso does not accept): the code is spent and the user starts again from the login UI.
- **A delete removed the binding but not the row** (the database failed in between): the policy no longer binds the connection and the connection still exists. Repeating the delete finishes it, because removing a binding that is not there changes nothing.
- **A policy write was stored but its answer was lost**: repeating it stores the same policy.
- **Two admins write at once**: the later write wins. A write that would wait for a lock longer than 2 seconds is answered `ABORTED`.
- **A replica stops mid sign-in**: another replica handles the next request; nothing was in memory but the discovery cache.
- **The envelope key is changed**: the stored client secrets no longer decrypt, so the connections cannot be used until the old key is back or each admin sets the secret anew; sign-ins in progress fail and start again.

## Risks / Trade-offs

- **[Risk] The service holds admin access to Kratos and hydra-sso.** If compromised it could accept hydra-sso logins for any subject and remove credentials. → hydra-sso's admin API should be reachable from this service only, Kratos's from the platform's services only.
- **[Risk] The gRPC listener trusts the internal network** (Decision 8). → Network policy in the deployment; `AUTHENTICATION_ALLOWED_SUBJECTS` or `AUTHENTICATION_REQUIRED_SCOPE` to narrow which tokens are admitted.
- **[Risk] A tenant admin can register an account for an address that has none.** The tenant invites the address and its own identity provider asserts it; Kratos registers the account, linked to the tenant's connection. → Accepted: tenant admins are customers the gateway authorizes, and domains, which admit whole ranges of addresses, are set by platform admins only.
- **[Trade-off] Tickets cannot be withdrawn and are not single use** (Decision 2).
- **[Trade-off] A receipt is not single use, and proves the browser and the ticket, not the session** (Decision 10). For as long as a ticket lives, a browser that completed its sign-in can have another session of the same account confirmed for it.
- **[Trade-off] Tested is permanent.** A connection whose secret was changed is still tested, and can be active with a secret nobody has tried. Accepted: rotating a secret must not take a tenant's sign-in down until someone runs a test, and a wrong secret fails that tenant's sign-ins only.
- **[Trade-off] The limit of five connections is not serialised.** Creates of one tenant at the same moment can pass it together.
- **[Trade-off] Links to deleted connections stay in Kratos**, unused.
- **[Trade-off] Discovery documents are cached for 10 minutes per replica**, so a provider's change of endpoints takes that long to be seen.
- **[Trade-off] One more Hydra to run** (Decision 1).
- **[Trade-off] hydra-sso keeps a record of every completed sign-in.** Hydra stores a row for each completed authorization, with the claims it issued, so hydra-sso's rows hold the address each sign-in was for; its janitor removes unfinished and rejected flows, not completed ones. Nothing in this change removes them, and nothing reads them again once the sign-in is over: hydra-sso's database grows with every company sign-in. Accepted for now. Removing old rows is left to the deployment; a row must not be removed in the 30 minutes after its sign-in, while hydra-sso would still accept that sign-in's consent verifier.
- **[Trade-off] Traces carry addresses.** A lookup of an account by address puts the address in the request URL to Kratos, which the HTTP client's span records. Log lines mask addresses; traces do not. Accepted for now: the trace store has to be treated as holding personal data.

## Migration Plan

Nothing exists before this change, so there is no data to migrate.

1. The tenant service must serve the RPCs this service calls: `GetSignInContext` of `TenantSignInService`, and `GetTenantSSOPolicy`, `PutTenantSSOPolicy`, `SetTenantSSODomains` and `RemoveTenantSSOBinding` of `TenantSSOPolicyService`.
2. Create the database and run `migrate --dsn <dsn> up`.
3. Create the service's client at the platform's Hydra (client credentials) and generate an envelope key (32 random bytes).
4. Deploy hydra-sso with its login, consent and error URLs set to this service's `/login`, `/consent` and `/error`, and register Kratos as its client.
5. Add the one provider to Kratos: `byo-sso`, generic, issuer hydra-sso, scopes `openid email`, PKCE `auto`, and a mapper that takes the address from `email` and marks it verified only when `email_verified` is true.
6. Deploy the service with `DEV` off. Route the four browser pages on the public ingress, on the host that serves the login UI (Decision 10), `/api/v0/sso/tenants` and `/api/v0/sso/connections` through the gateway with external authorization (`k8s/istio.yaml`), and keep the gRPC port on the internal network.
7. Company sign-in reaches users only when the login UI starts offering it: without a ticket, `/login` rejects every request.

Rollback: stop offering company sign-in in the login UI and remove the routes. Links already written stay in Kratos, unused. `migrate down` drops `sso_connections`, and with it every connection.
