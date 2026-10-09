// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package secrets

import "errors"

// ErrUnsealable is a token that is altered, malformed, sealed for another
// purpose, or sealed under another key.
var ErrUnsealable = errors.New("token does not open")
