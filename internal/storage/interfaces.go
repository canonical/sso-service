// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package storage

type scanner interface {
	Scan(dest ...any) error
}
