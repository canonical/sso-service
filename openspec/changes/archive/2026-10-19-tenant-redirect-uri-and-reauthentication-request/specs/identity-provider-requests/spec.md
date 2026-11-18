## MODIFIED Requirements

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
