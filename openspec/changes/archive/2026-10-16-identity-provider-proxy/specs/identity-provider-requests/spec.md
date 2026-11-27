## MODIFIED Requirements

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
