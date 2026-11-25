// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package bridge

import (
	"strings"

	v0tenant "github.com/canonical/identity-platform-api/v0/tenant"
)

// admits reports whether an active binding to the connection applies to the
// address.
func admits(signIn *v0tenant.SignInContext, connectionID string) bool {
	for _, id := range signIn.GetConnectionIds() {
		if strings.EqualFold(id, connectionID) {
			return true
		}
	}

	return false
}

// bounded keeps text the identity provider controls short and printable, for
// the log.
func bounded(raw string) string {
	if len(raw) > 120 {
		raw = raw[:120]
	}

	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}

		return r
	}, raw)
}
