// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package idp

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// A failure is told in one sentence per kind, never in the identity
// provider's words.
func TestTestError(t *testing.T) {
	seen := map[string]bool{}
	for _, err := range []error{ErrUnavailable, ErrCredentials, ErrMisconfigured, ErrInvalidToken, ErrRejected} {
		text := TestError(fmt.Errorf("%w: <b>the provider's words</b>", err))
		if text == "" || strings.Contains(text, "provider's words") || seen[text] {
			t.Errorf("%v: unexpected text %q", err, text)
		}
		seen[text] = true
	}
	if TestError(errors.New("anything else")) != TestError(ErrRejected) {
		t.Error("expected an unknown failure to read as a refused exchange")
	}
}
