# secrets Specification

## Purpose

The service holds 2 kinds of secret material:

- The client secrets that tenants register for their identity providers.
- The tokens that carry a sign-in between requests: tickets, browser-binding cookies and the `state` of a test sign-in.

The service protects both kinds with one key. The setting `ENVELOPE_KEY` gives that key. The service also uses the same key to make the receipts. A receipt shows that a sign-in was completed.

Key decisions:

- **The service encrypts client secrets in the database**, so that a dump or a backup does not hold them.
- **The service seals tokens and does not store them.** Sealing encrypts and authenticates a token. Thus a ticket or a cookie can pass through the browser, Kratos and hydra-sso. Nobody can read it or change it there.
- **The service binds each ciphertext to its use.** It binds a client secret to its connection, and a token to its purpose.
- **A receipt is an authentication tag and nothing else.** The service puts the receipt in a cookie. A cookie must not hold anything that a person can read about a user or recover about a user. For that reason we turned down a sealed cookie that holds the connection, the subject and a time. A tag over the same facts proves as much.
- **One key, and no key ids.** For now we turned down a list of keys with ids. Such a list is configuration and a column that nothing needs until key rotation is possible. Rotation belongs with a key store in the database.

Non-goals:

- Rotation of the key without the need to enter the client secrets again. A change of `ENVELOPE_KEY` makes every stored client secret unreadable. It also makes every sign-in in progress fail and start again.
- A key management service.

## Requirements
### Requirement: The envelope key
The system SHALL read its key from `ENVELOPE_KEY`. The value is 32 bytes in base64. The service SHALL refuse to start when the setting is missing, is not base64, or does not decode to 32 bytes.

#### Scenario: A key of the wrong length
- **WHEN** `serve` starts with an `ENVELOPE_KEY` that decodes to fewer than 32 bytes
- **THEN** `serve` exits with an error that names the setting

#### Scenario: The key is changed while secrets are stored
- **WHEN** the service restarts with another key
- **THEN** the service refuses company sign-ins through the existing connections as unavailable
- **AND** the service answers the start of a test sign-in with `IDP_CHECK_FAILED`
- **AND** the answer says that the service cannot read the connection's client secret
- **AND** the answer says that the client secret must be set again
- **AND** this continues until a tenant admin sets each client secret again

### Requirement: Client secrets are encrypted and never returned
The system SHALL encrypt a client secret with AES-256-GCM under the key and store the ciphertext. Each encryption SHALL use a fresh random nonce, and the connection's id as additional authenticated data. The system SHALL decrypt a secret only to call the connection's identity provider. An RPC or a route SHALL NOT return a client secret in any form.

#### Scenario: A ciphertext moved to another connection
- **WHEN** one connection's stored ciphertext is copied into another connection's row
- **THEN** the ciphertext does not decrypt there

### Requirement: Sealed tokens
The system SHALL seal tickets, binding-cookie values and test states as base64url text. The text is the JSON of the content, encrypted with AES-256-GCM under the key. The additional authenticated data is the token's purpose: `sso-ticket`, `sso-sign-in` or `sso-test-state`. A token SHALL open only when all of these conditions are true:

- The token is unaltered.
- The token was sealed under the same key.
- The system opens the token for the purpose that the token was sealed for.

The random values of a sign-in (`state`, nonce, PKCE verifier) are 32 bytes each. They come from the system's cryptographic random source.

#### Scenario: A token used for another purpose
- **WHEN** a ticket arrives as the `state` of a callback, or the `state` of a test arrives as a ticket
- **THEN** the token does not open

### Requirement: Receipts
The system SHALL make the receipt of a ticket and a subject as base64url text without padding. The system SHALL make the receipt with AES-256-GCM under the key, over an empty plaintext, with a fresh random nonce. Thus a receipt is 28 bytes: the 12-byte nonce, then the 16-byte tag. The additional authenticated data SHALL be these parts, in this order:

1. The bytes of `receipt`.
2. A zero byte.
3. The ticket, as the text that it was issued as.
4. A zero byte.
5. The subject, `<connection id>:<the identity provider's subject>`.

A ticket is base64url text and holds no zero byte. Thus 2 different pairs of ticket and subject never have the same additional data. A receipt has no content. There is nothing in it to decrypt, and only this service can make or check a receipt.

The system SHALL take a receipt as valid for a ticket and a subject only when both of these conditions are true:

- The receipt decodes to 28 bytes.
- The receipt's tag verifies under the key with that additional data.

Anything else is not valid, and the check does not say why. This applies to an empty value, text that is not base64url, another length, another ticket, another subject and another key.

#### Scenario: A receipt for another ticket or subject
- **WHEN** the system made a receipt for one ticket and subject
- **AND** the system checks that receipt for another ticket, or for another subject
- **THEN** the receipt is not valid

#### Scenario: Nothing to read
- **WHEN** a person decodes a receipt
- **THEN** the result is a nonce and a tag
- **AND** the receipt does not hold the ticket, the connection, the subject or an address

