// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package sso

import (
	v0sso "github.com/canonical/identity-platform-api/v0/sso"

	"github.com/canonical/sso-service/internal/types"
)

func optionsToProto(connections []*types.Connection) []*v0sso.Option {
	pb := make([]*v0sso.Option, len(connections))
	for i, c := range connections {
		pb[i] = &v0sso.Option{ConnectionId: c.ID, Label: c.Label}
	}

	return pb
}

func linksToProto(connections []*types.Connection) []*v0sso.Link {
	pb := make([]*v0sso.Link, len(connections))
	for i, c := range connections {
		pb[i] = &v0sso.Link{ConnectionId: c.ID, Label: c.Label, TenantId: c.OwnerTenantID}
	}

	return pb
}
