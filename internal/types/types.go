// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package types

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// ProviderID is the one Kratos OIDC provider every connection goes through.
const ProviderID = "byo-sso"

const CallbackPath = "/callback"

// RedirectURI is one per connection, so an identity provider's answer can
// only be taken for the connection it was asked through.
func RedirectURI(publicURL, connectionID string) string {
	return strings.TrimRight(publicURL, "/") + CallbackPath + "/" + connectionID
}

// Connection is a tenant's registration of its OIDC identity provider.
// ClientSecret is encrypted.
type Connection struct {
	ID            string
	OwnerTenantID string
	Label         string
	Issuer        string
	ClientID      string
	ClientSecret  []byte
	CreatedBy     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	TestedAt      *time.Time
}

func (c *Connection) Tested() bool {
	return c != nil && c.TestedAt != nil
}

// A token is sealed for one purpose, so one cannot stand in for another.
const (
	PurposeTicket    = "sso-ticket"
	PurposeSignIn    = "sso-sign-in"
	PurposeTestState = "sso-test-state"
)

// Ticket is a sign-in attempt, sealed: nothing is stored for it. It reaches
// /login as hydra-sso's login_hint, and CompleteAttempt from the login UI.
type Ticket struct {
	TenantID       string `json:"t"`
	ConnectionID   string `json:"c"`
	Email          string `json:"e"`
	Reauthenticate bool   `json:"r,omitempty"`
	IssuedAt       int64  `json:"i"`
	ExpiresAt      int64  `json:"x"`
}

func (t *Ticket) Issued() time.Time {
	return time.Unix(t.IssuedAt, 0)
}

func (t *Ticket) Expired(now time.Time) bool {
	return !now.Before(time.Unix(t.ExpiresAt, 0))
}

// SignIn is what /callback needs of a sign-in and must not take from the
// browser's URL, sealed into the cookie /login sets.
type SignIn struct {
	State          string `json:"s"`
	Nonce          string `json:"n"`
	PKCEVerifier   string `json:"v"`
	LoginChallenge string `json:"l"`
	ExpiresAt      int64  `json:"x"`
}

func (s *SignIn) Expired(now time.Time) bool {
	return !now.Before(time.Unix(s.ExpiresAt, 0))
}

// TestState is a test sign-in, sealed into its OAuth state.
type TestState struct {
	ConnectionID string `json:"c"`
	Nonce        string `json:"n"`
	PKCEVerifier string `json:"v"`
	ExpiresAt    int64  `json:"x"`
}

func (s *TestState) Expired(now time.Time) bool {
	return !now.Before(time.Unix(s.ExpiresAt, 0))
}

// LinkIdentifier is the Kratos credential identifier of a link,
// "<provider>:<subject>".
func LinkIdentifier(connectionID, subject string) string {
	return ProviderID + ":" + HydraSubject(connectionID, subject)
}

// HydraSubject is the subject a login at hydra-sso is accepted with.
func HydraSubject(connectionID, subject string) string {
	return connectionID + ":" + subject
}

// SplitHydraSubject splits at the first colon: a connection id is a UUID,
// which holds none, while an identity provider's subject may.
func SplitHydraSubject(value string) (connectionID, subject string, ok bool) {
	connectionID, subject, ok = strings.Cut(value, ":")
	if !ok || subject == "" {
		return "", "", false
	}
	if _, err := uuid.Parse(connectionID); err != nil || len(connectionID) != 36 {
		return "", "", false
	}

	return connectionID, subject, true
}

// MaskEmail keeps the first character of the local part and the domain:
// "a***@example.com".
func MaskEmail(email string) string {
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" {
		return "***"
	}

	_, size := utf8.DecodeRuneInString(local)

	return local[:size] + "***@" + domain
}
