// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

// Package secrets encrypts client secrets and seals the tokens a sign-in
// carries.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Envelope encrypts with AES-256-GCM under one key.
type Envelope struct {
	aead cipher.AEAD
}

// NewEnvelope takes the key: 32 bytes in base64.
func NewEnvelope(encoded string) (*Envelope, error) {
	key, err := decodeBase64(strings.TrimSpace(encoded))
	if err != nil || len(key) != 32 {
		return nil, errors.New("the envelope key must be 32 bytes in base64")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	return &Envelope{aead: aead}, nil
}

// Encrypt returns nonce||ciphertext. associated is bound as additional data:
// a ciphertext does not decrypt with another.
func (e *Envelope) Encrypt(plaintext, associated string) ([]byte, error) {
	nonce := make([]byte, e.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}

	return e.aead.Seal(nonce, nonce, []byte(plaintext), []byte(associated)), nil
}

func (e *Envelope) Decrypt(ciphertext []byte, associated string) (string, error) {
	if len(ciphertext) < e.aead.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce, sealed := ciphertext[:e.aead.NonceSize()], ciphertext[e.aead.NonceSize():]
	plaintext, err := e.aead.Open(nil, nonce, sealed, []byte(associated))
	if err != nil {
		return "", fmt.Errorf("failed to decrypt: %w", err)
	}

	return string(plaintext), nil
}

// Seal encrypts v, as JSON, into base64url(nonce||ciphertext). A token sealed
// for one purpose does not open for another.
func (e *Envelope) Seal(purpose string, v any) (string, error) {
	plaintext, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sealed, err := e.Encrypt(string(plaintext), purpose)
	if err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (e *Envelope) Open(purpose, token string, v any) error {
	sealed, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return ErrUnsealable
	}
	plaintext, err := e.Decrypt(sealed, purpose)
	if err != nil {
		return ErrUnsealable
	}
	if err := json.Unmarshal([]byte(plaintext), v); err != nil {
		return ErrUnsealable
	}

	return nil
}

// receiptPurpose starts the additional data of a receipt.
const receiptPurpose = "receipt"

// Receipt seals nothing, with the ticket and the subject as additional data,
// into base64url(nonce||tag): proof that a sign-in the ticket started ended
// as subject, with nothing in it to read.
func (e *Envelope) Receipt(ticket, subject string) (string, error) {
	sealed, err := e.Encrypt("", receiptAssociated(ticket, subject))
	if err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// ValidReceipt reports whether receipt was made under this key for ticket
// and subject. Anything else is false, whatever is wrong with it.
func (e *Envelope) ValidReceipt(receipt, ticket, subject string) bool {
	sealed, err := base64.RawURLEncoding.DecodeString(receipt)
	if err != nil || len(sealed) != e.aead.NonceSize()+e.aead.Overhead() {
		return false
	}
	_, err = e.Decrypt(sealed, receiptAssociated(ticket, subject))

	return err == nil
}

// A ticket is base64url and holds no zero byte, so two pairs never share
// additional data.
func receiptAssociated(ticket, subject string) string {
	return receiptPurpose + "\x00" + ticket + "\x00" + subject
}

// RandomToken is size random bytes, base64url without padding.
func RandomToken(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("failed to generate random token: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

// Digest is a short, stable, non-reversible form of an id, for logs.
func Digest(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))

	return base64.RawURLEncoding.EncodeToString(sum[:6])
}

func decodeBase64(value string) ([]byte, error) {
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if decoded, err := encoding.DecodeString(value); err == nil {
			return decoded, nil
		}
	}

	return nil, errors.New("not base64")
}
