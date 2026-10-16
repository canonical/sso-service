// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package types

import (
	"testing"
	"time"
)

const conn = "0190a0b0-0000-7000-8000-00000000000a"

var now = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

func TestConnection_Tested(t *testing.T) {
	at := time.Now()
	var none *Connection

	testCases := []struct {
		name       string
		connection *Connection
		expected   bool
	}{
		{name: "nil", connection: none},
		{name: "draft", connection: &Connection{}},
		{name: "tested", connection: &Connection{TestedAt: &at}, expected: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.connection.Tested(); got != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, got)
			}
		})
	}
}

func TestTicket_Issued(t *testing.T) {
	if issued := (&Ticket{IssuedAt: now.Unix()}).Issued(); !issued.Equal(now) {
		t.Errorf("expected %s, got %s", now, issued)
	}
}

// Each is expired at its expiry time, not a second later.
func TestTicket_Expired(t *testing.T) {
	ticket := &Ticket{ExpiresAt: now.Unix()}
	if ticket.Expired(now.Add(-time.Second)) || !ticket.Expired(now) {
		t.Error("expected the ticket expired from its expiry time on")
	}
}

func TestSignIn_Expired(t *testing.T) {
	signIn := &SignIn{ExpiresAt: now.Unix()}
	if signIn.Expired(now.Add(-time.Second)) || !signIn.Expired(now) {
		t.Error("expected the sign-in expired from its expiry time on")
	}
}

func TestTestState_Expired(t *testing.T) {
	test := &TestState{ExpiresAt: now.Unix()}
	if test.Expired(now.Add(-time.Second)) || !test.Expired(now) {
		t.Error("expected the test expired from its expiry time on")
	}
}

func TestRedirectURI(t *testing.T) {
	redirect := "https://sso.example/callback/" + conn

	testCases := []struct {
		name      string
		publicURL string
		expected  string
	}{
		{name: "bare", publicURL: "https://sso.example", expected: redirect},
		{name: "trailing slash", publicURL: "https://sso.example/", expected: redirect},
		{name: "trailing slashes", publicURL: "https://sso.example//", expected: redirect},
		{name: "with a path", publicURL: "https://portal.example/sso", expected: "https://portal.example/sso/callback/" + conn},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedirectURI(tc.publicURL, conn); got != tc.expected {
				t.Errorf("expected %s, got %s", tc.expected, got)
			}
		})
	}
}

func TestLinkIdentifier(t *testing.T) {
	if got, want := LinkIdentifier(conn, "a:b"), "byo-sso:"+conn+":a:b"; got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestSplitHydraSubject(t *testing.T) {
	testCases := []struct {
		name               string
		value              string
		expectedConnection string
		expectedSubject    string
		expectedOK         bool
	}{
		{name: "subject", value: HydraSubject(conn, "s"), expectedConnection: conn, expectedSubject: "s", expectedOK: true},
		{name: "subject with colons", value: HydraSubject(conn, "a:b"), expectedConnection: conn, expectedSubject: "a:b", expectedOK: true},
		{name: "empty", value: ""},
		{name: "no subject", value: conn},
		{name: "empty subject", value: conn + ":"},
		{name: "not a uuid", value: "not-a-uuid:s"},
		{name: "a provider", value: "google:s"},
		{name: "braced uuid", value: "{" + conn + "}:s"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			connection, subject, ok := SplitHydraSubject(tc.value)
			if connection != tc.expectedConnection || subject != tc.expectedSubject || ok != tc.expectedOK {
				t.Errorf("expected %q %q %v, got %q %q %v",
					tc.expectedConnection, tc.expectedSubject, tc.expectedOK, connection, subject, ok)
			}
		})
	}
}

func TestMaskEmail(t *testing.T) {
	testCases := []struct {
		name     string
		email    string
		expected string
	}{
		{name: "address", email: "alice@test.example", expected: "a***@test.example"},
		{name: "multi-byte first character", email: "élise@test.example", expected: "é***@test.example"},
		{name: "no local part", email: "@test.example", expected: "***"},
		{name: "no at sign", email: "no-at", expected: "***"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MaskEmail(tc.email); got != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, got)
			}
		})
	}
}
