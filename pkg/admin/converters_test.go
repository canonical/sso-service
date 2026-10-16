// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package admin

import (
	"strings"
	"testing"
	"time"

	v0sso "github.com/canonical/identity-platform-api/v0/sso"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/canonical/sso-service/internal/types"
)

func TestConnectionToProto(t *testing.T) {
	created := time.Date(2026, 9, 1, 10, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
	updated := created.Add(time.Hour)
	draft := &types.Connection{
		ID: connA, OwnerTenantID: tenantA, Label: "Acme", Issuer: "https://idp.example", ClientID: "client",
		ClientSecret: []byte("sealed-secret"), CreatedBy: adminID, CreatedAt: created, UpdatedAt: updated,
	}
	tested := *draft
	tested.TestedAt = &testedAt

	tests := []struct {
		name         string
		connection   *types.Connection
		wantStatus   v0sso.ConnectionStatus
		wantTestTime string
	}{
		{
			name:       "draft",
			connection: draft,
			wantStatus: v0sso.ConnectionStatus_CONNECTION_STATUS_DRAFT,
		},
		{
			name:         "tested",
			connection:   &tested,
			wantStatus:   v0sso.ConnectionStatus_CONNECTION_STATUS_TESTED,
			wantTestTime: "2026-09-01T12:00:00Z",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pb := connectionToProto(tt.connection, publicURL+"/")

			if pb.Id != connA || pb.OwnerTenantId != tenantA || pb.Label != "Acme" || pb.Issuer != "https://idp.example" ||
				pb.ClientId != "client" || pb.CreatedBy != adminID {
				t.Errorf("unexpected connection: %v", pb)
			}
			if pb.CreateTime != "2026-09-01T08:00:00Z" || pb.UpdateTime != "2026-09-01T09:00:00Z" {
				t.Errorf("expected UTC times, got %s and %s", pb.CreateTime, pb.UpdateTime)
			}
			if pb.Status != tt.wantStatus || pb.TestTime != tt.wantTestTime {
				t.Errorf("expected status %v and test time %q, got %v and %q", tt.wantStatus, tt.wantTestTime, pb.Status, pb.TestTime)
			}
			if pb.RedirectUri != redirectA {
				t.Errorf("expected redirect URI %s, got %s", redirectA, pb.RedirectUri)
			}
			if encoded := protojson.Format(pb); strings.Contains(encoded, "sealed-secret") {
				t.Errorf("the client secret must not be returned: %s", encoded)
			}
		})
	}
}

func TestConnectionsToProto(t *testing.T) {
	pb := connectionsToProto([]*types.Connection{{ID: connA}, {ID: connB}}, publicURL)
	if len(pb) != 2 || pb[0].Id != connA || pb[1].Id != connB {
		t.Errorf("unexpected connections: %v", pb)
	}
	if empty := connectionsToProto(nil, publicURL); empty == nil || len(empty) != 0 {
		t.Errorf("expected an empty list, got %v", empty)
	}
}
