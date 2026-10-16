## Context

See `proposal.md` for the motivation. The platform signs users in with Kratos (accounts, sessions, credentials). It issues tokens to apps with Hydra. It keeps tenants, memberships and invitations in the tenant service. The login UI drives every sign-in. The Authorization Service of the platform authorizes management requests at the gateway.

This service adds one thing to that picture: a tenant's own OIDC identity provider as a way to sign in. We wrote the service like the other Go services of the platform:

- a handler and a service per API package
- PostgreSQL through `internal/db` and `internal/storage`
- the shared `pkg/authentication`
- the security logger
- one Prometheus monitor

These callers talk to the service:

| caller | interface | for |
|---|---|---|
| the login UI | gRPC, `SSOSignInService` | options, tickets, the confirmation of an attempt, links |
| tenant admins, through the gateway | HTTP gateway (or gRPC), `SSOTenantAdminService` | connections, test sign-ins, the policy of the tenant |
| platform admins, through the gateway | HTTP gateway (or gRPC), `SSOPlatformAdminService` | all connections, to read and set domains, to delete any connection |
| the browsers of users, sent by hydra-sso and by identity providers | `/login`, `/callback/{connection_id}`, `/consent`, `/error` | the company sign-in itself |

The service talks to these systems:

- its PostgreSQL database
- the tenant service (gRPC, with its own client-credentials token)
- the admin API of Kratos (read accounts and links, remove a link)
- the admin API of hydra-sso (login and consent requests)
- the identity providers of the customers (discovery, token, keys)

## Goals / Non-Goals

**Goals:**
- A new, changed or removed connection never changes the configuration of Kratos or of Hydra, and never affects another tenant.
- No change to the code of Kratos or of Hydra.
- Reuse what Kratos already does (linking, registration, verification). Build only what Kratos cannot do.
- An outage of this service, of hydra-sso or of one identity provider breaks only company sign-ins. An outage of a provider breaks only the company sign-ins of that provider.
- Any number of replicas, with no state shared outside PostgreSQL and the keys.

**Non-Goals:**
- SAML, a connection shared between tenants, a circuit breaker per connection, rate limiting, re-encryption of secrets under a new key, proof of domain ownership.
- The sign-in screen, tenant choice, sessions, MFA and memberships. The login UI, Kratos and the tenant service keep them.

## Decisions

### Decision 1: A second Hydra behind one Kratos provider
- **Decision**: Kratos gets one static OIDC provider, `byo-sso`. The issuer of that provider is a second Hydra, `hydra-sso`. This service is the login and consent provider of hydra-sso. This service does the OIDC exchange with the real identity provider of the tenant. It accepts the hydra-sso login with the subject `<connection id>:<subject>`. Thus the credential that Kratos stores, `byo-sso:<connection id>:<subject>`, is unique per connection.
- **Rationale**: Kratos reads its providers from configuration. Connections are rows in this service's database instead. Kratos and hydra-sso are configured once.
- **Alternatives considered**: One alternative was a Kratos provider per customer. We turned it down. With it, every new or changed connection is a change and a rollout of the Kratos configuration, which touches all tenants. Also, customers' client secrets are then in Kratos's configuration. Another alternative was a change to Kratos so that providers can be added at run time. We turned it down, because the platform runs Kratos unmodified.

  The cost of the choice is one more Hydra to run.

### Decision 2: Sealed tickets and a binding cookie instead of stored attempts
- **Decision**: A sign-in attempt is a ticket. A ticket holds the tenant, the connection, the address, the re-authentication flag and an expiry, sealed with the service's key. The per-browser part (`state`, nonce, PKCE verifier, login challenge) is a sealed cookie. The service stores nothing about a sign-in. The service also does not store the fact that a sign-in was completed. The browser holds a receipt for that fact (Decision 10).
- **Rationale**: The sign-in path writes nothing to the database, and there is nothing to clean up. Any replica finishes what another replica started. The database holds no user data.
- **Alternative considered**: The alternative was a table of attempts with a cleanup job, and we turned it down. Its one advantage is single use, and the pieces that matter are single use already. The identity provider's code works once. The nonce binds the id_token to this sign-in. The service clears the cookie after the callback. A replayed ticket only starts another sign-in that still needs the user at the provider.

### Decision 3: Kratos writes every link
- **Decision**: The service never writes a credential or an account. For a subject with no link, the service checks these conditions:
  - The address is the address that the user entered.
  - The tenant has an active binding to the connection that applies to the address.
  - The address is the address of a member, is invited, or is admitted by auto-join.

  Then the service lets the sign-in through. Kratos sees a subject that it does not know, and treats it as a registration. If an account has the address, the registration stops at the conflict and becomes the account linking of Kratos. In account linking, the user signs in to the account another way before Kratos adds the link. If no account has the address, Kratos registers the account.
- **Rationale**: Proof that a user owns an account is the job of Kratos, and Kratos already does that job. A hostile identity provider that asserts another person's address gains nothing. Kratos adds the link only after that account's owner signs in.
- **Alternative considered**: The alternative was to write the credential from here through the admin API of Kratos. In that alternative the service first proves ownership with a confirmation code of our own. We turned it down. That alternative rebuilds account linking outside Kratos, with codes, limits and cleanup of its own.

### Decision 4: A redirect URI per connection
- **Decision**: Each connection has its own redirect URI, `<PUBLIC_URL>/callback/<connection id>`. The service returns it as `redirect_uri`, for the admin to register at the provider. The service takes an answer only when the connection in the path is the connection through which the sign-in, or the test, started.
- **Rationale**: With one shared callback, an identity provider can answer a sign-in that started at another identity provider (a mix-up attack). Tenant admins choose the providers of all connections. Thus we must assume that one of the providers is hostile.
- **Alternative considered**: The alternative was one `/callback`, with the connection found from the `state`. We turned it down for that reason.

### Decision 5: The tenant service owns the policy; this service checks only what needs connection data
- **Decision**: The tenant service stores bindings, enforcement, auto-join and domains. Tenant admins write them through this service. This service takes locks on the connection rows. Under these locks it checks that each bound connection belongs to the tenant and that each active one is tested. Then it passes the write on. The tenant service checks the policy as a whole under its own row lock.

  The delete of a connection removes its binding first, under the same lock.
- **Rationale**: Every sign-in reads the policy at the tenant service, also the sign-ins that never use this service. Thus the policy must live at the tenant service. Only this service knows the owner and the status of a connection.
- **Alternatives considered**: One alternative was to store the policy here. We turned it down, because an outage here breaks sign-ins that have nothing to do with this service. A second alternative was that admins write straight to the tenant service, which calls back here to check connections. We turned it down, because it makes a cycle between 2 services and a lock across both. A third alternative was versions or read-modify-write between the 2 services. We turned it down, because each write names one part and the service that stores the policy checks that part against what is stored.

### Decision 6: `CompleteAttempt` only verifies
- **Decision**: `CompleteAttempt` answers if the ticket's sign-in ended at the given account. It reads the ticket, the account's address and links, and the receipt that the browser holds (Decision 10). It writes nothing.
- **Rationale**: The login UI needs one fact to record which connection a session came from. A caller can retry a call with no side effects, and the call returns the same answer.
- **Alternative considered**: The alternative was that `CompleteAttempt` also creates the membership of an invited or auto-joined user, and removes the account when the membership is refused. We turned it down. The login UI is the one that knows when a sign-in passed all the tenant's checks. The login UI asks the tenant service for the membership then.
- **Consequence**: A new user can pass the callback, and then the invitation expires or auto-join is switched off. In that case, the account that Kratos registered ends up with no tenant. Nothing in this service removes that account.

### Decision 7: No stored test result
- **Decision**: The service shows the outcome of a test sign-in on the page that the test sign-in ends on. The service keeps only `tested_at`. The service sets `tested_at` at the first success and never clears it. The test's context travels sealed in its `state`.
- **Rationale**: After the test, one fact matters: if the connection can be made active.
- **Alternative considered**: The alternative was a table of test runs and an RPC to read a result. We turned it down, because it needs rows and cleanup for something that is read once.
- **Consequence**: A reload of the result page sends a spent code and shows a failure. In that case the connection's status tells the result of the test. A test is not bound to a browser, so anyone who holds the URL can finish the test. The only effect of that is to mark the connection tested.

### Decision 8: Authentication here, authorization at the gateway
- **Decision**: The service verifies the caller's JWT and nothing more. The gateway asks the Authorization Service if the caller is allowed to act on the tenant in the path, or is a platform admin. `k8s/istio.yaml` routes `/api/v0/sso/tenants` and `/api/v0/sso/connections` through that check. The service itself only makes sure that a resource belongs to the tenant in the path. The RPCs that no user is allowed to call (`SSOSignInService`) have no HTTP route and take no token. The login UI calls them from the internal network.
- **Rationale**: One place decides who is allowed to do what, for every service of the platform.
- **Alternative considered**: The alternative was roles or a list of platform admins in this service. We turned it down, because it is a second copy of a decision, and that copy will drift.
- **Alternative considered**: The alternative was a token for the login UI's sign-in calls. We turned it down. It gives the login UI a client, a token endpoint and the certificate authority of that endpoint to manage. These calls never leave the internal network. On that network the login UI already calls the tenant lookup and the identity provider's admin API with nothing.
- **Consequence**: Anything that reaches the gRPC listener can call a sign-in RPC with any account id. Any admitted token can call any management RPC on that listener. The internal network is the control there.

### Decision 9: The outbound-request guard
- **Decision**: Every request to an identity provider goes through one HTTP client. That client has these properties:
  - It allows only `https`.
  - It connects only to public unicast addresses. It checks the address when it dials, after name resolution.
  - It uses no proxy.
  - It follows no redirects.
  - It caps the response at 64 KiB.
  - It has deadlines.
- **Rationale**: Tenant admins choose the issuer, and the issuer's discovery document chooses the other URLs. The check must be where the client makes the connection. If not, a name that resolves differently the second time gets past the check.
- **Alternatives considered**: One alternative was to check the issuer's address once at create. We turned it down, because it does not cover later resolutions or the discovered endpoints. Another alternative was to require token and key endpoints on the issuer's origin. We turned it down, because real providers host them elsewhere.

### Decision 10: A receipt in the browser shows that the sign-in was completed
- **Decision**: When the callback accepts a sign-in, it sets a cookie that is named for the ticket. The value of the cookie is a receipt. A receipt is an AES-256-GCM authentication tag, under the key of the service, over the ticket and the subject accepted at hydra-sso. A receipt has no content. The login UI reads the cookie of the ticket that it holds and sends the cookie's value in `CompleteAttempt`. `CompleteAttempt` requires a receipt that is valid for the ticket and for the subject of one of the account's links to the ticket's connection.
- **Rationale**: A valid ticket, the account's address and a link do not show that the ticket's sign-in ever reached the identity provider. Without a receipt, an account linked to the connections of 2 tenants can start a sign-in at one connection. The account can then sign in through the other connection and get that session confirmed for the first ticket. That is a way in to a tenant that requires its company sign-in. A user that the tenant's identity provider no longer knows can use this way in. Anyone who runs the other tenant's identity provider can also use it.

  A receipt exists only after the identity provider answered for this ticket, and only in the browser that the answer came through.
- **Alternatives considered**: One alternative was a record on a server. The record is in a table of completed attempts here, or in the rows that hydra-sso keeps for its login flows. We turned it down. Such a record says that the identity provider answered for the ticket, but it does not say in which browser. Thus anyone who started an attempt can have another user's browser finish that attempt. This is silent when that user is signed in at the identity provider. That person can then present the ticket with a session made another way. A table also brings stored attempts back (Decision 2).

  Another alternative was a sealed cookie that holds the connection, the subject and a time. We turned it down. A cookie must not hold anything that a person can read about a user or recover about a user. A tag over the same facts proves as much.
- **Consequences**: The service's browser pages and the login UI must be served on the same host. If they are not, the browser does not send the cookie to the login UI. Kratos and the login UI already have the same requirement. A receipt is not single use. The same browser can present the receipt again until the ticket expires. That gives nothing more than the session that the sign-in already produced.

  A receipt proves that this browser completed the sign-in of this ticket as a subject that the account is linked to. A receipt does not prove that the sign-in produced this particular Kratos session.

## Data model

The data model is one table, `sso_connections`:

| column | |
|---|---|
| `id` | UUIDv7, primary key |
| `owner_tenant_id` | the id from the tenant service, with no foreign key across services, indexed |
| `label`, `issuer`, `client_id` | `issuer` and `client_id` never change |
| `client_secret` | the encrypted secret |
| `created_by` | the account id of the user who created the connection, or null for a service client |
| `created_at`, `updated_at` | the database's clock. `updated_at` moves with every change of the row |
| `tested_at` | null while the connection is a draft. The database's clock |

Links are Kratos credentials. Sign-in attempts are tickets and cookies. hydra-sso keeps a row per login flow, like any Hydra.

## Flows

### A company sign-in
1. The user enters their address in the login UI. The login UI asks the tenant service which tenants and bindings apply, and asks this service for the labels (`ListOptions`). When the user makes a pick, the login UI calls `StartAttempt` and gets a ticket.
2. The login UI submits the `byo-sso` sign-in of Kratos with the ticket as `login_hint`. Kratos redirects the browser to hydra-sso, and hydra-sso redirects the browser to `/login?login_challenge=…`.
3. `/login` reads the login request from hydra-sso and opens the ticket. It reads the connection and the provider's discovery document. It sets the binding cookie and redirects the browser to the identity provider.
4. The user signs in there. The provider redirects the browser to `/callback/{connection_id}` with a code.
5. The callback opens the cookie, and reads the login request and the ticket again. It compares the connection in the path with the ticket's connection, exchanges the code, verifies the id_token and applies the address checks. It asks Kratos who holds the link. For a subject with no link, it asks Kratos who has the address. For such a subject it also asks the tenant service if the address is allowed to sign in. It accepts the hydra-sso login, clears the cookie and sets the receipt cookie.
6. hydra-sso sends the browser to `/consent`, and `/consent` accepts with the address as id_token claims. hydra-sso returns the browser to Kratos with a code. Kratos gets an id_token with `sub`, `email` and, when the provider said it, `email_verified`.
7. Kratos finds the credential and signs the user in, or links, or registers (Decision 3).
8. The browser is back in the login UI with a session. The login UI calls `CompleteAttempt` with the ticket, the account and the receipt that it reads from the browser's cookie. The login UI then applies the tenant's rules. For an invited or auto-joined user, it asks the tenant service for the membership.

A refusal in step 3 or 5 rejects the hydra-sso login with a fixed message. The user reads that message in the login UI.

### A test sign-in
1. A tenant admin calls `StartTestLogin`. The service decrypts the secret and reads a fresh copy of the discovery document. It probes the token endpoint with a made-up code. It returns the authorization URL with a sealed `state`.
2. The admin opens the URL and signs in. The provider redirects to `/callback/{connection_id}`.
3. The callback opens the `state`, exchanges the code, verifies the id_token and requires an `email`. On success the callback sets `tested_at` if `tested_at` is not set, and shows the result page.

### A policy write
1. `PutTenantSSOPolicy` opens a transaction and locks the rows of all bound connections (`FOR UPDATE`, in id order, one statement).
2. It checks owner and status. Then it calls the tenant service's policy write inside the transaction.
3. The tenant service locks the tenant's row, checks the policy as a whole, stores the policy and answers. The transaction ends, and the locks end with it.

`SetTenantDomains` involves no connection. The service passes it straight on. `GetTenantDomains` reads the policy at the tenant service and returns the policy's domains.

### Deleting a connection
1. `DeleteConnection` or `DeleteAnyConnection` opens a transaction and locks the connection's row.
2. It asks the tenant service to remove the connection's binding from the owner's policy. The tenant service refuses when the removal will leave a tenant that requires company sign-in with no active binding. On the platform admins' route, an owner that no longer exists has nothing to unbind.
3. It deletes the row.

## Failure handling

- **A dependency is down**: See the table in `specs/resilience/spec.md`. On the API the caller gets `UNAVAILABLE`. In the browser flow the service rejects the login request as `unavailable`. When hydra-sso itself is down, the service shows a page instead.
- **A sign-in is abandoned** at the provider or during account linking: The service stored nothing, so nothing is left here. The ticket and the cookies expire within 30 minutes.
- **The browser does not return the receipt**: This occurs when the cookie was removed or refused, or when the login UI is on another host. `CompleteAttempt` answers `NOT_APPLICABLE`, and the user starts again from the login UI.
- **The callback fails after the code was exchanged** (for example, hydra-sso does not accept): The code is spent, and the user starts again from the login UI.
- **A delete removed the binding but not the row** (the database failed in between): The policy no longer binds the connection, and the connection still exists. A repeat of the delete finishes it, because the removal of a binding that is not there changes nothing.
- **A policy write was stored but its answer was lost**: A repeat of the write stores the same policy.
- **2 admins write at once**: The later write wins. The service answers `ABORTED` to a write that waits for a lock longer than 2 seconds.
- **A replica stops mid sign-in**: Another replica handles the next request. Nothing was in memory except the discovery cache.
- **The envelope key is changed**: The stored client secrets no longer decrypt. Thus the connections cannot be used until the old key is back or each admin sets the secret again. Sign-ins in progress fail and start again.

## Risks / Trade-offs

- **[Risk] The service holds admin access to Kratos and hydra-sso.** A compromised service can accept hydra-sso logins for any subject and remove credentials. Mitigation: Make the admin API of hydra-sso reachable from this service only. Make the admin API of Kratos reachable from the services of the platform only.
- **[Risk] The gRPC listener trusts the internal network** (Decision 8). Mitigation: Use network policy in the deployment. Use `AUTHENTICATION_ALLOWED_SUBJECTS` or `AUTHENTICATION_REQUIRED_SCOPE` to narrow which tokens the service admits to the management RPCs.
- **[Risk] A tenant admin can register an account for an address that has none.** The tenant invites the address. The tenant's identity provider asserts the address. Kratos registers the account, linked to the tenant's connection. We accept this risk, because tenant admins are customers that the gateway authorizes. Also, domains admit whole ranges of addresses, and only platform admins set domains.
- **[Trade-off] Tickets cannot be withdrawn and are not single use** (Decision 2).
- **[Trade-off] A receipt is not single use, and proves the browser and the ticket, not the session** (Decision 10). While a ticket lives, a browser that completed its sign-in can have another session of the same account confirmed for that ticket.
- **[Trade-off] Tested is permanent.** A connection stays tested after a change of its secret. Such a connection can be active with a secret that nobody tried. We accept this. The rotation of a secret must not take the sign-in of a tenant down until someone runs a test. A wrong secret fails the sign-ins of that tenant only.
- **[Trade-off] The limit of 5 connections is not serialised.** Creates of one tenant at the same moment can pass the limit together.
- **[Trade-off] Links to deleted connections stay in Kratos**, unused.
- **[Trade-off] Each replica caches discovery documents for 10 minutes.** Thus a change of a provider's endpoints takes that long to show.
- **[Trade-off] One more Hydra to run** (Decision 1).
- **[Trade-off] hydra-sso keeps a record of every completed sign-in.** Hydra stores a row for each completed authorization, with the claims that it issued. Thus the rows of hydra-sso hold the address that each sign-in was for. Hydra's janitor removes unfinished and rejected flows, not completed flows. Nothing in this change removes the completed rows, and nothing reads them again after the sign-in is over. Thus hydra-sso's database grows with every company sign-in. We accept this for now.

  The removal of old rows is left to the deployment. A row must not be removed in the 30 minutes after its sign-in. In that time hydra-sso still accepts the consent verifier of that sign-in.
- **[Trade-off] Traces carry addresses.** A lookup of an account by address puts the address in the request URL to Kratos. The span of the HTTP client records that URL. Log lines mask addresses, but traces do not. We accept this for now. Thus the trace store must be treated as a store of personal data.

## Migration Plan

Nothing exists before this change, so there is no data to migrate.

1. The tenant service must serve the RPCs that this service calls: `GetSignInContext` of `TenantSignInService`, and `GetTenantSSOPolicy`, `PutTenantSSOPolicy`, `SetTenantSSODomains` and `RemoveTenantSSOBinding` of `TenantSSOPolicyService`.
2. Create the database and run `migrate --dsn <dsn> up`.
3. Create the service's client at the platform's Hydra (client credentials). Generate an envelope key (32 random bytes).
4. Deploy hydra-sso with its login, consent and error URLs set to the `/login`, `/consent` and `/error` of this service. Register Kratos as a client of hydra-sso.
5. Add the one provider to Kratos: `byo-sso`, generic, issuer hydra-sso, scopes `openid email`, PKCE `auto`. The mapper of the provider takes the address from `email` and marks the address verified only when `email_verified` is true.
6. Deploy the service with `DEV` off. Route the 4 browser pages on the public ingress, on the host that serves the login UI (Decision 10). Route `/api/v0/sso/tenants` and `/api/v0/sso/connections` through the gateway with external authorization (`k8s/istio.yaml`). Keep the gRPC port on the internal network.
7. Company sign-in reaches users only when the login UI starts to offer it. Without a ticket, `/login` rejects every request.

Rollback: Do not offer company sign-in in the login UI any more, and remove the routes. Links already written stay in Kratos, unused. `migrate down` drops `sso_connections`, and with it every connection.
