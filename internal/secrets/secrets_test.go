// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package secrets

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func key(b byte) string {
	return base64.StdEncoding.EncodeToString([]byte(strings.Repeat(string(b), 32)))
}

func newEnvelope(t *testing.T, encoded string) *Envelope {
	t.Helper()
	envelope, err := NewEnvelope(encoded)
	if err != nil {
		t.Fatalf("failed to read the envelope key: %v", err)
	}
	return envelope
}

func TestNewEnvelope(t *testing.T) {
	testCases := []struct {
		name      string
		encoded   string
		expectErr bool
	}{
		{name: "32 bytes in base64", encoded: key('a')},
		{name: "surrounding space", encoded: " " + key('a') + "\n"},
		{name: "empty", encoded: "", expectErr: true},
		{name: "not base64", encoded: "!!", expectErr: true},
		{name: "short key", encoded: base64.StdEncoding.EncodeToString([]byte("short")), expectErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewEnvelope(tc.encoded)
			if tc.expectErr != (err != nil) {
				t.Errorf("expected error %v, got %v", tc.expectErr, err)
			}
		})
	}
}

func TestEnvelope_Decrypt(t *testing.T) {
	envelope := newEnvelope(t, key('a'))
	sealed, err := envelope.Encrypt("s3cret", "conn-1")
	if err != nil {
		t.Fatalf("failed to encrypt: %v", err)
	}

	t.Run("same key", func(t *testing.T) {
		plain, err := envelope.Decrypt(sealed, "conn-1")
		if err != nil || plain != "s3cret" {
			t.Errorf("expected the secret, got %q %v", plain, err)
		}
	})
	t.Run("another associated value", func(t *testing.T) {
		if _, err := envelope.Decrypt(sealed, "conn-2"); err == nil {
			t.Error("expected error but got none")
		}
	})
	t.Run("another key", func(t *testing.T) {
		if _, err := newEnvelope(t, key('b')).Decrypt(sealed, "conn-1"); err == nil {
			t.Error("expected error but got none")
		}
	})
	t.Run("short ciphertext", func(t *testing.T) {
		if _, err := envelope.Decrypt([]byte{1}, "conn-1"); err == nil {
			t.Error("expected error but got none")
		}
	})
}

func TestEnvelope_Seal(t *testing.T) {
	type ticket struct {
		Email string `json:"e"`
	}

	token, err := newEnvelope(t, key('a')).Seal("ticket", &ticket{Email: "alice@test.example"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token == "" || strings.Contains(token, "alice") {
		t.Errorf("expected an opaque token, got %q", token)
	}
}

func TestEnvelope_Open(t *testing.T) {
	type ticket struct {
		Email string `json:"e"`
	}

	envelope := newEnvelope(t, key('a'))
	token, err := envelope.Seal("ticket", &ticket{Email: "alice@test.example"})
	if err != nil {
		t.Fatalf("failed to seal: %v", err)
	}

	t.Run("same key and purpose", func(t *testing.T) {
		var got ticket
		if err := envelope.Open("ticket", token, &got); err != nil || got.Email != "alice@test.example" {
			t.Errorf("expected the ticket, got %+v %v", got, err)
		}
	})

	flipped := []byte(token)
	flipped[len(flipped)/2] ^= 'A' ^ 'B'

	testCases := []struct {
		name     string
		envelope *Envelope
		purpose  string
		token    string
	}{
		{name: "another purpose", envelope: envelope, purpose: "cookie", token: token},
		{name: "altered", envelope: envelope, purpose: "ticket", token: string(flipped)},
		{name: "not base64url", envelope: envelope, purpose: "ticket", token: "!!"},
		{name: "another key", envelope: newEnvelope(t, key('b')), purpose: "ticket", token: token},
		{name: "empty", envelope: envelope, purpose: "ticket", token: ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var got ticket
			if err := tc.envelope.Open(tc.purpose, tc.token, &got); !errors.Is(err, ErrUnsealable) {
				t.Errorf("expected %v, got %v", ErrUnsealable, err)
			}
		})
	}
}

func TestEnvelope_ValidReceipt(t *testing.T) {
	envelope := newEnvelope(t, key('a'))
	receipt, err := envelope.Receipt("ticket", "conn-1:alice")
	if err != nil {
		t.Fatalf("failed to make the receipt: %v", err)
	}
	// A nonce and a tag over the ticket and the subject: it holds neither.
	associated := "receipt\x00ticket\x00conn-1:alice"
	sealed, _ := base64.RawURLEncoding.DecodeString(receipt)
	if _, err := envelope.Decrypt(sealed, associated); err != nil || len(sealed) != 28 {
		t.Fatalf("expected 28 bytes sealed with the ticket and the subject, got %d: %v", len(sealed), err)
	}
	payload, err := envelope.Encrypt("payload", associated)
	if err != nil {
		t.Fatalf("failed to encrypt: %v", err)
	}

	testCases := []struct {
		name     string
		envelope *Envelope
		receipt  string
		ticket   string
		subject  string
		expected bool
	}{
		{name: "same key, ticket and subject", envelope: envelope, receipt: receipt, ticket: "ticket", subject: "conn-1:alice", expected: true},
		{name: "another ticket", envelope: envelope, receipt: receipt, ticket: "other", subject: "conn-1:alice"},
		{name: "another subject", envelope: envelope, receipt: receipt, ticket: "ticket", subject: "conn-1:bob"},
		{name: "another key", envelope: newEnvelope(t, key('b')), receipt: receipt, ticket: "ticket", subject: "conn-1:alice"},
		{name: "not base64url", envelope: envelope, receipt: "!!", ticket: "ticket", subject: "conn-1:alice"},
		{name: "another length", envelope: envelope, receipt: base64.RawURLEncoding.EncodeToString(payload), ticket: "ticket", subject: "conn-1:alice"},
		{name: "empty", envelope: envelope, receipt: "", ticket: "ticket", subject: "conn-1:alice"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.envelope.ValidReceipt(tc.receipt, tc.ticket, tc.subject); got != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, got)
			}
		})
	}
}

func TestRandomToken(t *testing.T) {
	a, err := RandomToken(32)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := RandomToken(32)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a == b || len(a) != 43 {
		t.Errorf("expected two distinct tokens of 43 characters, got %q and %q", a, b)
	}
}

func TestDigest(t *testing.T) {
	if Digest("x") != Digest("x") || Digest("x") == Digest("y") {
		t.Error("expected a stable digest, distinct per value")
	}
	if Digest("") != "" {
		t.Errorf("expected no digest of nothing, got %q", Digest(""))
	}
}
