// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package links

import (
	"context"

	"github.com/canonical/sso-service/internal/types"
)

type ConnectionsInterface interface {
	GetConnections(ctx context.Context, ids []string) ([]*types.Connection, error)
}
