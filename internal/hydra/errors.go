// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package hydra

import "errors"

var ErrNotFound = errors.New("unknown or expired challenge")

// The SDK gives no value for an empty or null 2xx body.
var errNoRedirect = errors.New("hydra-sso sent no redirect")
