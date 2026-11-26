// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package admin

import (
	"errors"
	"fmt"

	"github.com/canonical/sso-service/internal/limits"
)

var (
	// ErrConnectionNotFound is also a connection another tenant owns.
	ErrConnectionNotFound  = errors.New("no such connection")
	ErrConnectionNotTested = errors.New("only a tested connection can be active")
	ErrConnectionLimit     = fmt.Errorf("a tenant owns at most %d connections", limits.MaxConnectionsPerTenant)
	ErrInvalidIssuer       = errors.New("invalid issuer")
	ErrInvalidPageToken    = errors.New("invalid page_token")
	// ErrIdPCheckFailed wraps what the identity provider's client said.
	ErrIdPCheckFailed = errors.New("the identity provider cannot be used for a test sign-in")
	// ErrClientSecretUnreadable is a stored client secret that does not
	// decrypt: it was encrypted under another envelope key.
	ErrClientSecretUnreadable = errors.New("the connection's client secret cannot be read")
)
