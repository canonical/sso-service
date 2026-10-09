// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package sso

import (
	"context"

	"github.com/canonical/sso-service/internal/kratos"
	"github.com/canonical/sso-service/internal/types"
)

type ServiceInterface interface {
	ListOptions(ctx context.Context, connectionIDs []string) ([]*types.Connection, error)
	StartAttempt(ctx context.Context, tenantID, email, connectionID string, reauthenticate bool) (string, error)
	CompleteAttempt(ctx context.Context, ticket, identityID, receipt string) (*types.Ticket, error)
	ListLinks(ctx context.Context, identityID string) ([]*types.Connection, error)
	DeleteLink(ctx context.Context, identityID, connectionID string) error
}

type StorageInterface interface {
	GetConnection(ctx context.Context, id string) (*types.Connection, error)
	GetConnections(ctx context.Context, ids []string) ([]*types.Connection, error)
}

type KratosClientInterface interface {
	GetIdentity(ctx context.Context, id string, credentials ...string) (*kratos.Identity, error)
	DeleteOIDCIdentifier(ctx context.Context, identityID, identifier string) error
}

type EnvelopeInterface interface {
	Seal(purpose string, v any) (string, error)
	Open(purpose, token string, v any) error
	ValidReceipt(receipt, ticket, subject string) bool
}
