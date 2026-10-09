// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package bridge

import "testing"

func TestMessage(t *testing.T) {
	seen := map[string]string{}
	for _, reason := range []string{
		ReasonIdPRefused, ReasonInvalidToken, ReasonReauthentication, ReasonAddressMismatch, ReasonAddressUnconfirmed,
		ReasonNotAMember, ReasonAlreadyLinked, ReasonUnavailable, ReasonExpired,
	} {
		message := Message(reason)
		if other, ok := seen[message]; ok || message == "" {
			t.Errorf("%s: expected a sentence of its own, got %q (as %s)", reason, message, other)
		}
		seen[message] = reason
	}
	if Message("never heard of it") != Message(ReasonUnavailable) {
		t.Error("expected an unknown reason to read as unavailable")
	}
}
