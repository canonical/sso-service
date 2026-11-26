// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package bridge

import (
	"strings"
	"unicode"
	"unicode/utf8"

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
	var out strings.Builder
	for _, r := range raw {
		if r == utf8.RuneError || !unicode.IsPrint(r) {
			continue
		}
		if out.Len()+utf8.RuneLen(r) > 120 {
			break
		}
		out.WriteRune(r)
	}

	return out.String()
}
