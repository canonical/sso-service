// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package hydra

import "errors"

var ErrNotFound = errors.New("unknown or expired challenge")
