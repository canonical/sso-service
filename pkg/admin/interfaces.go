// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package admin

import (
	"context"

	v0tenant "github.com/canonical/identity-platform-api/v0/tenant"

	"github.com/canonical/sso-service/internal/idp"
	"github.com/canonical/sso-service/internal/storage"
	"github.com/canonical/sso-service/internal/types"
)

type ServiceInterface interface {
	ListConnections(ctx context.Context, ownerTenantID, pageToken string, pageSize int) ([]*types.Connection, string, error)
	CreateConnection(ctx context.Context, tenantID, label, issuer, clientID, clientSecret string) (*types.Connection, error)
	GetConnection(ctx context.Context, tenantID, connectionID string) (*types.Connection, error)
	UpdateConnection(ctx context.Context, tenantID, connectionID string, label, clientSecret *string) (*types.Connection, error)
	DeleteConnection(ctx context.Context, tenantID, connectionID string) error
	DeleteAnyConnection(ctx context.Context, connectionID string) error
	StartTestLogin(ctx context.Context, tenantID, connectionID string) (string, error)
	GetTenantSSOPolicy(ctx context.Context, tenantID string) (*v0tenant.TenantSSOPolicy, error)
	PutTenantSSOPolicy(ctx context.Context, tenantID string, enforcement v0tenant.Enforcement, autoJoin bool, bindings []*v0tenant.SSOBinding) (*v0tenant.TenantSSOPolicy, error)
	GetTenantDomains(ctx context.Context, tenantID string) ([]string, error)
	SetTenantDomains(ctx context.Context, tenantID string, domains []string) (*v0tenant.TenantSSOPolicy, error)
}

type StorageInterface interface {
	WithTx(ctx context.Context, fn func(context.Context) error) error

	CreateConnection(ctx context.Context, c *types.Connection, limit int) (*types.Connection, error)
	GetConnection(ctx context.Context, id string) (*types.Connection, error)
	ListConnections(ctx context.Context, ownerTenantID, afterID string, limit int) ([]*types.Connection, error)
	LockConnections(ctx context.Context, ids []string) ([]*types.Connection, error)
	UpdateConnection(ctx context.Context, id string, update storage.ConnectionUpdate) (*types.Connection, error)
	DeleteConnection(ctx context.Context, id string) error
}

type TenantsClientInterface interface {
	GetTenantSSOPolicy(ctx context.Context, tenantID string) (*v0tenant.TenantSSOPolicy, error)
	PutTenantSSOPolicy(ctx context.Context, req *v0tenant.PutTenantSSOPolicyRequest) (*v0tenant.TenantSSOPolicy, error)
	SetTenantSSODomains(ctx context.Context, tenantID string, domains []string) (*v0tenant.TenantSSOPolicy, error)
	RemoveTenantSSOBinding(ctx context.Context, tenantID, connectionID string) error
}

type IdPClientInterface interface {
	ValidateIssuer(raw string) error
	AuthCodeURL(ctx context.Context, connection *types.Connection, request *idp.AuthRequest) (string, error)
	Check(ctx context.Context, connection *types.Connection, clientSecret, redirectURI string) error
}

type EnvelopeInterface interface {
	Encrypt(plaintext, associated string) ([]byte, error)
	Decrypt(ciphertext []byte, associated string) (string, error)
	Seal(purpose string, v any) (string, error)
}
