// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package admin

import (
	"time"

	v0sso "github.com/canonical/identity-platform-api/v0/sso"

	"github.com/canonical/sso-service/internal/types"
)

func connectionToProto(c *types.Connection, publicURL string) *v0sso.Connection {
	pb := &v0sso.Connection{
		Id:            c.ID,
		OwnerTenantId: c.OwnerTenantID,
		Label:         c.Label,
		Issuer:        c.Issuer,
		ClientId:      c.ClientID,
		Status:        v0sso.ConnectionStatus_CONNECTION_STATUS_DRAFT,
		CreatedBy:     c.CreatedBy,
		CreateTime:    c.CreatedAt.UTC().Format(time.RFC3339),
		UpdateTime:    c.UpdatedAt.UTC().Format(time.RFC3339),
		RedirectUri:   types.RedirectURI(publicURL, c.ID),
	}
	if c.Tested() {
		pb.Status = v0sso.ConnectionStatus_CONNECTION_STATUS_TESTED
		pb.TestTime = c.TestedAt.UTC().Format(time.RFC3339)
	}

	return pb
}

func connectionsToProto(connections []*types.Connection, publicURL string) []*v0sso.Connection {
	pb := make([]*v0sso.Connection, len(connections))
	for i, c := range connections {
		pb[i] = connectionToProto(c, publicURL)
	}

	return pb
}
