# identity-provider-requests Specification

## Purpose

The customers' identity providers are the only systems outside the platform that this service calls, and others choose the URLs: a tenant admin names the issuer, and the issuer's discovery document names the token and key endpoints. Without a guard, a tenant admin could make the service send requests into the platform's own network (server-side request forgery). This capability is that guard and the OIDC relying party built on it, used by company sign-ins and test sign-ins alike.

Key decisions:

- **The guard is in the HTTP client, at the moment it connects.** The address is checked after the name was resolved, and every request is covered, whichever library made it. Checking the issuer's host once, at create, was turned down: the name can resolve differently later, and it says nothing about the endpoints the discovery document names.
- **Token and key endpoints may be on other hosts than the issuer.** Requiring the issuer's origin was turned down: real providers serve them elsewhere.
- **No redirects, no proxy.** A redirect would point the service at an address the check did not see; a proxy would hide the real destination from it.
- **No retries, no circuit breaker.** A code works once; every call has a deadline; a provider that is down fails only its own sign-ins.

Non-goals: the userinfo endpoint; refresh tokens; client authentication other than a client secret; choosing another claim than `sub` as the subject.

## Requirements
### Requirement: The outbound-request guard
The system SHALL apply all of these to every request to an identity provider (discovery, token and key endpoints):

- only `https` URLs, with no userinfo;
- a connection is opened only to a public unicast address, checked when dialling, after name resolution. Refused are private, loopback, link-local, multicast and unspecified addresses, `100.64.0.0/10`, `198.18.0.0/15`, `240.0.0.0/4`, the NAT64 ranges `64:ff9b::/96` and `64:ff9b:1::/48`, and 6to4 `2002::/16`; an IPv4-mapped IPv6 address is judged as the IPv4 address it carries;
- no proxy is used and no redirect is followed;
- a response body is read up to 64 KiB, and a longer one fails the request;
- a 5xx answer counts as the provider being unavailable;
- one call takes at most 10 seconds, and everything one operation asks of a provider (discovery, token, keys) at most 15 seconds.

With the setting `DEV` on, plain `http` and non-public addresses are allowed and the other rules stay. `DEV` is for development and test deployments only.

#### Scenario: A discovery document that points inside the network
- **WHEN** a provider's discovery document names a token endpoint that resolves to `10.0.0.5`
- **THEN** the connection is refused when it is dialled, and the sign-in or test fails as a provider that cannot be used

### Requirement: Discovery
The system SHALL read a provider's configuration from `<issuer>/.well-known/openid-configuration` and refuse the document when its `issuer` is not exactly the connection's issuer, when its authorization endpoint is missing or not `https`, or when it has no token endpoint. An issuer shared by many organisations, whose document names another issuer, therefore cannot be used.

Each replica keeps the document per issuer for 10 minutes. When a refresh fails because the provider is unreachable, the document in hand is used for another 10 minutes; a refresh that returns a document the service refuses fails the request instead. The check that starts a test sign-in always reads the document afresh. The provider's keys are fetched when an id_token is verified, and again when an id_token names a key the service does not have.

#### Scenario: Another issuer in the document
- **WHEN** the document at the connection's issuer names a different `issuer`, even one that differs by a trailing slash
- **THEN** the provider is treated as misconfigured

### Requirement: The authorization request and the code exchange
The system SHALL ask every provider for the scopes `openid email`, with the authorization code flow, PKCE (S256), a `state` and a nonce. It SHALL exchange the code once, with the PKCE verifier and the connection's client credentials sent as `client_secret_basic`, or as `client_secret_post` when the discovery document lists the token endpoint's authentication methods and they include `client_secret_post` but not `client_secret_basic`.

#### Scenario: A provider that only takes the secret in the body
- **WHEN** a provider's discovery document lists `client_secret_post` and not `client_secret_basic`
- **THEN** the client id and secret are sent in the body of the token request

### Requirement: The id_token checks
The system SHALL accept an id_token only when all of these hold:

- its signature verifies against the issuer's published keys, with RS256 or ES256;
- `iss` is the connection's issuer and `aud` contains the connection's client id;
- it has not expired, allowing 60 seconds of clock difference;
- `azp`, when present, is the connection's client id, and is present when `aud` names several audiences;
- `nonce` is the nonce the service sent;
- `sub` is not empty, is at most 210 characters and is printable ASCII, so that `byo-sso:<connection id>:<subject>` fits the 255 characters Kratos stores a credential identifier in.

`iat` is not checked. From an accepted id_token the system reads `sub`, `email`, `email_verified` (a boolean, or the strings `true` and `false`) and `auth_time`.

#### Scenario: A token signed with HS256
- **WHEN** a provider returns an id_token signed with HS256
- **THEN** the id_token is refused

#### Scenario: A subject that is too long
- **WHEN** an id_token's `sub` has 211 characters
- **THEN** the id_token is refused, at a test sign-in as at a company sign-in

### Requirement: How a provider's failures are told apart
The system SHALL sort every failure into one of five kinds, which decide what a user or an admin is told (see `company-sign-in`, `test-sign-in`):

| kind | what it covers |
|---|---|
| unavailable | no answer, a timeout, a 5xx, keys that cannot be fetched |
| credentials refused | the token endpoint answers 401, `invalid_client` or `unauthorized_client` |
| misconfigured | a refused discovery document, a URL that is not `https`, a forbidden address, a response over the size limit |
| id_token refused | any of the id_token checks fails |
| code refused | any other refusal of the code exchange, or a token response with no id_token |

A provider's own text SHALL NOT be shown to a user or returned by the API. In the log it is quoted and cut short.

#### Scenario: A wrong client secret during a sign-in
- **WHEN** the token endpoint answers `invalid_client` during a company sign-in
- **THEN** the failure is "credentials refused", and the user is told company sign-in is unavailable

