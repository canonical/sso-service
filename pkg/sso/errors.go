// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package sso

import "errors"

var (
	ErrConnectionNotUsable = errors.New("the connection does not exist or is not tested")
	ErrAttemptNotCompleted = errors.New("the attempt did not end at this account, or has expired")
	ErrAccountNotFound     = errors.New("no such account")
	ErrLinkNotFound        = errors.New("the account has no link at this connection")
	ErrLastCredential      = errors.New("the account would have no way to sign in of its own; set a password first")
	ErrKratosUnavailable   = errors.New("Kratos is unavailable")
)
