## MODIFIED Requirements

### Requirement: The outbound-request guard
The system SHALL apply all of these rules to every request to an identity provider (discovery, token and key endpoints):

- The URL is `https` only, and it has no userinfo.
- The service opens a connection only to a public unicast address. It checks the address when it dials, after it resolved the name.
- The service refuses private, loopback, link-local, multicast and unspecified addresses. It also refuses every address that has an IPv6 zone.
- The service also refuses these IPv4 ranges: `0.0.0.0/8`, `100.64.0.0/10`, `198.18.0.0/15` and `240.0.0.0/4`.
- The service also refuses these IPv6 ranges: IPv4-compatible `::/96`, IPv4-translated `::ffff:0:0:0/96`, NAT64 `64:ff9b::/96` and `64:ff9b:1::/48`, Teredo `2001::/32` and 6to4 `2002::/16`.
- The service judges an IPv4-mapped IPv6 address as the IPv4 address that it carries.
- The service follows no redirect.
- The service sends a request through the proxy that `HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY` name for its URL. Before that, it resolves the host of the request. It refuses the request when one of the addresses is not a public unicast address.
- The service connects to the address of a proxy without the address check only for a request that it sends through that proxy. The operator names the proxy. A request that the service sends straight to the address of a proxy gets the address check.
- The service reads a response body up to 64 KiB. A longer body fails the request.
- A 5xx answer counts as a provider that is unavailable.
- The request carries no trace context. The identity provider is outside the platform.
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

#### Scenario: An address with an IPv6 zone
- **WHEN** the discovery document of a provider names a token endpoint at `::%1`, an IPv6 address with a zone
- **THEN** the service refuses the connection when it dials
- **AND** the sign-in or the test fails as a provider that the service cannot use

#### Scenario: A request straight to the address of the proxy
- **WHEN** `HTTPS_PROXY` names a proxy at `proxy.internal:3128`, and `NO_PROXY` names `proxy.internal`
- **AND** the discovery document of a provider names a token endpoint at `https://proxy.internal:3128`
- **THEN** the service sends the request without the proxy
- **AND** the service refuses the connection when it dials, because the proxy's address is not public
- **AND** the sign-in or the test fails as a provider that the service cannot use

### Requirement: How a provider's failures are told apart
The system SHALL sort every failure into one of 5 kinds. The kind decides what the service tells a user or an admin (see `company-sign-in`, `test-sign-in`):

| kind | what it covers |
|---|---|
| unavailable | no answer, a timeout or a 5xx at any endpoint, and keys that the service cannot fetch for another cause |
| credentials refused | the token endpoint answers 401, `invalid_client` or `unauthorized_client` |
| misconfigured | a refused discovery document. A URL that is not `https`, a forbidden address or a response over the size limit, at any endpoint |
| id_token refused | any of the id_token checks fails |
| code refused | any other refusal of the code exchange, or a token response with no id_token |

The system SHALL sort a failure by its cause, also when the failure occurs while the system reads a response body. Thus a body over the size limit is misconfigured. A body that does not arrive before the timeout is unavailable. Keys that the rules of the guard refuse are misconfigured.

The system SHALL NOT show a provider's own text to a user, and the API SHALL NOT return it. In the log the service quotes the text and cuts it short.

#### Scenario: A wrong client secret during a sign-in
- **WHEN** the token endpoint answers `invalid_client` during a company sign-in
- **THEN** the failure is "credentials refused"
- **AND** the service tells the user that company sign-in is unavailable
