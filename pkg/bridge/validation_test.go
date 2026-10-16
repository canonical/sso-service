// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package bridge

import (
	"strings"
	"testing"

	v0tenant "github.com/canonical/identity-platform-api/v0/tenant"
)

func TestAdmits(t *testing.T) {
	testCases := []struct {
		name     string
		signIn   *v0tenant.SignInContext
		expected bool
	}{
		{name: "bound", signIn: signInContext(true, false, connB, connA), expected: true},
		{name: "upper case", signIn: signInContext(true, false, strings.ToUpper(connA)), expected: true},
		{name: "another connection", signIn: signInContext(true, false, connB), expected: false},
		{name: "no binding", signIn: signInContext(true, false), expected: false},
		{name: "no context", signIn: nil, expected: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := admits(tc.signIn, connA); got != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, got)
			}
		})
	}
}

func TestBounded(t *testing.T) {
	if got := bounded(strings.Repeat("a", 500) + "\n"); len(got) != 120 {
		t.Errorf("expected 120 characters, got %d", len(got))
	}
	if got := bounded("line one\r\nforged line\x7f"); got != "line oneforged line" {
		t.Errorf("expected the control characters gone, got %q", got)
	}
}
