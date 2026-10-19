# identity-provider-requests Specification

## Purpose

The identity providers of the customers are the only systems outside the platform that this service calls. Other parties choose the URLs. A tenant admin names the issuer, and the discovery document of the issuer names the token and key endpoints. Without a guard, a tenant admin can make the service send requests into the platform's own network (server-side request forgery). This capability is that guard. It is also the OIDC relying party that is built on the guard. Company sign-ins and test sign-ins both use it.

Key decisions:

- **The guard is in the HTTP client, at the moment it connects.** The client checks the address after it resolved the name. The guard covers every request, whichever library made it. We turned down a check of the issuer's host once, at create, because the name can resolve in a different way later. That check also says nothing about the endpoints that the discovery document names.
- **Other hosts than the issuer can serve the token and key endpoints.** We turned down a rule that they must have the issuer's origin. Real providers serve them elsewhere.
- **No redirects, and a check before the proxy.** A redirect can point the service at an address that the check did not see. Some networks have no way out but an HTTP proxy. Through a proxy, the dial check sees only the proxy. Thus the service resolves the host of the request and checks its addresses before the request goes to the proxy. The proxy resolves the name again, so the proxy must restrict what it can reach.
- **No retries and no circuit breaker.** A code works once. Every call has a deadline. A provider that is down fails only its own sign-ins.

Non-goals:

- The userinfo endpoint.
- Refresh tokens.
- Client authentication other than a client secret.
- A claim other than `sub` as the subject.

## Requirements
### Requirement: The outbound-request guard
The system SHALL apply all of these rules to every request to an identity provider (discovery, token and key endpoints):

- The URL is `https` only, and it has no userinfo.
- The service opens a connection only to a public unicast address. It checks the address when it dials, after it resolved the name.
- The service refuses these addresses: private, loopback, link-local, multicast and unspecified addresses, `100.64.0.0/10`, `198.18.0.0/15`, `240.0.0.0/4`, the NAT64 ranges `64:ff9b::/96` and `64:ff9b:1::/48`, and 6to4 `2002::/16`.
- The service judges an IPv4-mapped IPv6 address as the IPv4 address that it carries.
- The service follows no redirect.
- The service sends a request through the proxy that `HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY` name for its URL. Before that, it resolves the host of the request. It refuses the request when one of the addresses is not a public unicast address.
- The service connects to the address of the proxy without the address check. The operator names the proxy.
- The service reads a response body up to 64 KiB. A longer body fails the request.
- A 5xx answer counts as a provider that is unavailable.
- One call takes at most 10 seconds.
- All that one operation asks of a provider (discovery, token, keys) takes at most 15 seconds.

With the setting `DEV` on, the service allows plain `http` and non-public addresses. The other rules stay. Use `DEV` for development and test deployments only.

#### Scenario: A discovery document that points inside the network
- **WHEN** the discovery document of a provider names a token endpoint that resolves to `10.0.0.5`
- **THEN** the service refuses the connection when it dials
- **AND** the sign-in or the test fails as a provider that the service cannot use

#### Scenario: A provider inside the network, through the proxy
- **WHEN** `HTTPS_PROXY` names a proxy
- **AND** the token endpoint of a provider resolves to `10.0.0.5`
- **THEN** the service refuses the request before it sends the request to the proxy
- **AND** the sign-in or the test fails as a provider that the service cannot use

### Requirement: Discovery
The system SHALL read the configuration of a provider from `<issuer>/.well-known/openid-configuration`. The system SHALL refuse the document in any of these cases:

- Its `issuer` is not exactly the issuer of the connection.
- Its authorization endpoint is missing or is not `https`.
- It has no token endpoint.

For this reason the service cannot use an issuer that many organisations share and whose document names another issuer.

Each replica keeps the document for each issuer for 10 minutes. When a refresh fails because the provider is unreachable, the replica uses the document that it has for another 10 minutes. When a refresh returns a document that the service refuses, the request fails instead. The check that starts a test sign-in always reads the document again. The service fetches the keys of the provider when it verifies an id_token. It fetches them again when an id_token names a key that the service does not have.

#### Scenario: Another issuer in the document
- **WHEN** the document at the connection's issuer names a different `issuer`, even one that differs by a trailing slash
- **THEN** the service treats the provider as misconfigured

### Requirement: The authorization request and the code exchange
The system SHALL ask every provider for the scopes `openid email`. It SHALL use the authorization code flow, PKCE (S256), a `state` and a nonce. The system SHALL exchange the code once. The exchange SHALL send the PKCE verifier and the connection's client credentials. The credentials SHALL go as `client_secret_basic`, with one exception. They SHALL go as `client_secret_post` when the discovery document lists the token endpoint's authentication methods and the list includes `client_secret_post` but not `client_secret_basic`.

#### Scenario: A provider that only takes the secret in the body
- **WHEN** the discovery document of a provider lists `client_secret_post` and not `client_secret_basic`
- **THEN** the service sends the client id and the secret in the body of the token request

### Requirement: The id_token checks
The system SHALL accept an id_token only when all of these are true:

- Its signature verifies against the published keys of the issuer, with RS256 or ES256.
- `iss` is the issuer of the connection.
- `aud` contains the client id of the connection.
- It has not expired. The check allows 60 seconds of clock difference.
- `azp`, when present, is the client id of the connection.
- `azp` is present when `aud` names several audiences.
- `nonce` is the nonce that the service sent.
- `sub` is not empty, has at most 210 characters and is printable ASCII.

The limits on `sub` make `byo-sso:<connection id>:<subject>` fit in the 255 characters that Kratos uses to store a credential identifier.

The service does not check `iat`. From an accepted id_token the system reads `sub`, `email` and `email_verified`. `email_verified` is a boolean, or the string `true` or `false`.

#### Scenario: A token signed with HS256
- **WHEN** a provider returns an id_token signed with HS256
- **THEN** the service refuses the id_token

#### Scenario: A subject that is too long
- **WHEN** the `sub` of an id_token has 211 characters
- **THEN** the service refuses the id_token
- **AND** this is true at a test sign-in and at a company sign-in

### Requirement: How a provider's failures are told apart
The system SHALL sort every failure into one of 5 kinds. The kind decides what the service tells a user or an admin (see `company-sign-in`, `test-sign-in`):

| kind | what it covers |
|---|---|
| unavailable | no answer, a timeout, a 5xx, keys that cannot be fetched |
| credentials refused | the token endpoint answers 401, `invalid_client` or `unauthorized_client` |
| misconfigured | a refused discovery document, a URL that is not `https`, a forbidden address, a response over the size limit |
| id_token refused | any of the id_token checks fails |
| code refused | any other refusal of the code exchange, or a token response with no id_token |

The system SHALL NOT show a provider's own text to a user, and the API SHALL NOT return it. In the log the service quotes the text and cuts it short.

#### Scenario: A wrong client secret during a sign-in
- **WHEN** the token endpoint answers `invalid_client` during a company sign-in
- **THEN** the failure is "credentials refused"
- **AND** the service tells the user that company sign-in is unavailable

