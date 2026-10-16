// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package idp

import (
	"errors"
	"fmt"
)

var (
	// ErrUnavailable is an identity provider that could not be reached, timed
	// out or answered 5xx.
	ErrUnavailable = errors.New("identity provider unavailable")
	// ErrRejected is a code exchange the identity provider refused.
	ErrRejected     = errors.New("identity provider refused")
	ErrInvalidToken = errors.New("id_token rejected")
	// ErrMisconfigured is a discovery document or keys that cannot be used.
	ErrMisconfigured = errors.New("identity provider misconfigured")
	ErrCredentials   = errors.New("client credentials refused")

	ErrForbiddenAddress = errors.New("the IdP address is not a public unicast address")
	ErrInsecureURL      = errors.New("the IdP URL is not https")
	ErrResponseTooLarge = errors.New("response too large")
)

// said carries what a library or the identity provider said about an error.
// That text may be the provider's own and ends up in a log line, so it is
// printed quoted and cut short.
type said struct {
	kind  error
	cause error
}

func (e *said) Error() string   { return fmt.Sprintf("%v: %.200q", e.kind, e.cause.Error()) }
func (e *said) Unwrap() []error { return []error{e.kind, e.cause} }

// TestError is what a test sign-in says about a failure at the identity
// provider: never the provider's own text.
func TestError(err error) string {
	switch {
	case errors.Is(err, ErrUnavailable):
		return "The identity provider could not be reached, or answered with an error."
	case errors.Is(err, ErrCredentials):
		return "The identity provider refused the client credentials."
	case errors.Is(err, ErrMisconfigured):
		return "The identity provider's discovery document or keys are not usable (issuer, endpoints, JWKS)."
	case errors.Is(err, ErrInvalidToken):
		return "The id_token did not pass the checks (signature RS256/ES256, issuer, audience, expiry, nonce, or a subject of at most 210 ASCII characters)."
	default:
		return "The identity provider refused the code exchange. A code works once, so reloading the page of a finished test shows this too: the connection's status says whether the test passed."
	}
}
