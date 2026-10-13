## Purpose

The service holds two kinds of secret material: the client secrets tenants register for their identity providers, and the tokens that carry a sign-in between requests (tickets, browser-binding cookies, the `state` of a test sign-in). Both are protected with one key, given in the setting `ENVELOPE_KEY`. The same key makes the receipts that show a sign-in was completed.

Key decisions:

- **Client secrets are encrypted in the database**, so that a dump or a backup does not hold them.
- **Tokens are sealed, not stored.** Sealing encrypts and authenticates, so a ticket or a cookie can pass through the browser, Kratos and hydra-sso without being readable or changeable there.
- **Each ciphertext is bound to its use**: a client secret to its connection, a token to its purpose.
- **A receipt is an authentication tag and nothing else.** It goes into a cookie, and nothing about a user is to be readable in, or recoverable from, a cookie. A sealed cookie holding the connection, the subject and a time was turned down for that reason: a tag over the same facts proves as much.
- **One key, and no key ids.** A list of keys with ids was turned down for now: it is configuration and a column that nothing needs until keys can be rotated, and rotation belongs with a key store in the database.

Non-goals: rotating the key without re-entering the client secrets (changing `ENVELOPE_KEY` makes every stored client secret unreadable, and every sign-in in progress fail and start again); a key management service.

## ADDED Requirements

### Requirement: The envelope key
The system SHALL read its key from `ENVELOPE_KEY`: 32 bytes in base64. The service SHALL refuse to start when the setting is missing, is not base64, or does not decode to 32 bytes.

#### Scenario: A key of the wrong length
- **WHEN** `serve` is started with an `ENVELOPE_KEY` that decodes to fewer than 32 bytes
- **THEN** it exits with an error that names the setting

#### Scenario: The key is changed while secrets are stored
- **WHEN** the service restarts with another key
- **THEN** company sign-ins through the existing connections are refused as unavailable, and starting a test sign-in is answered `IDP_CHECK_FAILED`, saying that the connection's client secret cannot be read and has to be set again, until a tenant admin sets each client secret again

### Requirement: Client secrets are encrypted and never returned
The system SHALL encrypt a client secret with AES-256-GCM under the key, with a fresh random nonce and the connection's id as additional authenticated data, and store the ciphertext. It SHALL decrypt a secret only to call the connection's identity provider. No RPC or route SHALL return a client secret, in any form.

#### Scenario: A ciphertext moved to another connection
- **WHEN** one connection's stored ciphertext is copied into another connection's row
- **THEN** it does not decrypt there

### Requirement: Sealed tokens
The system SHALL seal tickets, binding-cookie values and test states as base64url text: the JSON of the content encrypted with AES-256-GCM under the key, with the token's purpose as additional authenticated data (`sso-ticket`, `sso-sign-in`, `sso-test-state`). A token SHALL open only when it is unaltered, was sealed under the same key and is opened for the purpose it was sealed for. The random values of a sign-in (`state`, nonce, PKCE verifier) are 32 bytes each from the system's cryptographic random source.

#### Scenario: A token used for another purpose
- **WHEN** a ticket is sent as the `state` of a callback, or a test's `state` as a ticket
- **THEN** it does not open

### Requirement: Receipts
The system SHALL make the receipt of a ticket and a subject as base64url text without padding: AES-256-GCM under the key over an empty plaintext, with a fresh random nonce, so 28 bytes, the 12-byte nonce followed by the 16-byte tag. The additional authenticated data SHALL be the bytes of `receipt`, a zero byte, the ticket as the text it was issued as, a zero byte, and the subject, `<connection id>:<the identity provider's subject>`. A ticket is base64url text and holds no zero byte, so two different pairs of ticket and subject never have the same additional data. A receipt has no content: there is nothing in it to decrypt, and only this service can make or check one.

The system SHALL take a receipt as valid for a ticket and a subject only when it decodes to 28 bytes and its tag verifies under the key with that additional data. Anything else is not valid, and the check does not say why: an empty value, text that is not base64url, another length, another ticket, another subject, another key.

#### Scenario: A receipt for another ticket or subject
- **WHEN** a receipt made for one ticket and subject is checked for another ticket, or for another subject
- **THEN** it is not valid

#### Scenario: Nothing to read
- **WHEN** a receipt is decoded
- **THEN** it is a nonce and a tag: neither the ticket, the connection, the subject nor an address is in it
