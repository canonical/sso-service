// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package bridge

import (
	"errors"

	"github.com/canonical/sso-service/internal/idp"
)

// Refusals with no login challenge to reject through: the browser is
// answered directly.
var (
	ErrUnknownState    = errors.New("this sign-in is unknown, expired or already used")
	ErrBindingMissing  = errors.New("this sign-in was already completed, or was not started in this browser")
	ErrBindingMismatch = errors.New("this sign-in was started in another browser")
	ErrUnknownLogin    = errors.New("this sign-in request is unknown or expired")
)

func exchangeReason(err error) string {
	switch {
	case errors.Is(err, idp.ErrUnavailable):
		return ReasonUnavailable
	case errors.Is(err, idp.ErrInvalidToken):
		return ReasonInvalidToken
	case errors.Is(err, idp.ErrMisconfigured), errors.Is(err, idp.ErrCredentials):
		return ReasonUnavailable
	default:
		return ReasonIdPRefused
	}
}
