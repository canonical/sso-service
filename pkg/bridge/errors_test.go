// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package bridge

import (
	"errors"
	"fmt"
	"testing"

	"github.com/canonical/sso-service/internal/idp"
)

func TestExchangeReason(t *testing.T) {
	testCases := []struct {
		name     string
		err      error
		expected string
	}{
		{name: "unavailable", err: idp.ErrUnavailable, expected: ReasonUnavailable},
		{name: "invalid token", err: idp.ErrInvalidToken, expected: ReasonInvalidToken},
		{name: "misconfigured", err: idp.ErrMisconfigured, expected: ReasonUnavailable},
		{name: "credentials", err: idp.ErrCredentials, expected: ReasonUnavailable},
		{name: "rejected", err: idp.ErrRejected, expected: ReasonIdPRefused},
		{name: "wrapped", err: fmt.Errorf("%w: token endpoint answered 401", idp.ErrCredentials), expected: ReasonUnavailable},
		{name: "anything else", err: errors.New("anything else"), expected: ReasonIdPRefused},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exchangeReason(tc.err); got != tc.expected {
				t.Errorf("expected %s, got %s", tc.expected, got)
			}
		})
	}
}
