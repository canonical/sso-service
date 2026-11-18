// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package bridge

import (
	"context"

	v0tenant "github.com/canonical/identity-platform-api/v0/tenant"

	"github.com/canonical/sso-service/internal/hydra"
	"github.com/canonical/sso-service/internal/idp"
	"github.com/canonical/sso-service/internal/kratos"
	"github.com/canonical/sso-service/internal/types"
)

type ServiceInterface interface {
	StartLogin(ctx context.Context, challenge string) (string, *Binding, error)
	Callback(ctx context.Context, callback *Callback) (*Result, error)
	Consent(ctx context.Context, challenge string) (string, error)
}

type StorageInterface interface {
	GetConnection(ctx context.Context, id string) (*types.Connection, error)
	SetTested(ctx context.Context, id string) error
}

type IdPClientInterface interface {
	AuthCodeURL(ctx context.Context, connection *types.Connection, request *idp.AuthRequest) (string, error)
	Exchange(ctx context.Context, connection *types.Connection, clientSecret string, request *idp.AuthRequest, code string) (*idp.Claims, error)
}

type HydraClientInterface interface {
	GetLoginRequest(ctx context.Context, challenge string) (*hydra.LoginRequest, error)
	AcceptLogin(ctx context.Context, challenge, subject string, loginContext map[string]any) (string, error)
	RejectLogin(ctx context.Context, challenge, description string) (string, error)
	GetConsentRequest(ctx context.Context, challenge string) (*hydra.ConsentRequest, error)
	AcceptConsent(ctx context.Context, request *hydra.ConsentRequest, idToken map[string]any) (string, error)
}

type KratosClientInterface interface {
	ListByIdentifier(ctx context.Context, identifier string) ([]kratos.Identity, error)
}

type TenantsClientInterface interface {
	GetSignInContext(ctx context.Context, tenantID, email, identityID string) (*v0tenant.SignInContext, error)
}

type EnvelopeInterface interface {
	Decrypt(ciphertext []byte, associated string) (string, error)
	Seal(purpose string, v any) (string, error)
	Open(purpose, token string, v any) error
	Receipt(ticket, subject string) (string, error)
}
