// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

// Package links reads an account's links: the OIDC credentials Kratos holds
// for it at a connection.
package links

import (
	"context"

	"github.com/canonical/sso-service/internal/kratos"
	"github.com/canonical/sso-service/internal/types"
)

type Link struct {
	ConnectionID string
	Subject      string
}

func (l Link) Identifier() string {
	return types.LinkIdentifier(l.ConnectionID, l.Subject)
}

// Of needs an identity read with its oidc credential.
func Of(identity *kratos.Identity) []Link {
	out := make([]Link, 0)
	for _, provider := range identity.OIDCProviders() {
		if provider.Provider != types.ProviderID {
			continue
		}
		if connectionID, subject, ok := types.SplitHydraSubject(provider.Subject); ok {
			out = append(out, Link{ConnectionID: connectionID, Subject: subject})
		}
	}

	return out
}

func At(identity *kratos.Identity, connectionID string) []Link {
	out := make([]Link, 0)
	for _, l := range Of(identity) {
		if l.ConnectionID == connectionID {
			out = append(out, l)
		}
	}

	return out
}

// OwnWayIn reports whether the account can sign in without its links at
// except: with a password, a passkey, a public sign-in, or a link to another
// connection that still exists. A "code" credential does not count: Kratos
// gives one to every account with an email address, so counting it would
// make every account look as if it had a way in. The identity must have been
// read with kratos.FirstFactorCredentials.
func OwnWayIn(ctx context.Context, connections ConnectionsInterface, identity *kratos.Identity, except string) (bool, error) {
	if identity.HasPassword() || identity.HasPasskey() {
		return true, nil
	}

	others := make([]string, 0)
	for _, provider := range identity.OIDCProviders() {
		if provider.Provider != types.ProviderID {
			return true, nil
		}
		if connectionID, _, ok := types.SplitHydraSubject(provider.Subject); ok && connectionID != except {
			others = append(others, connectionID)
		}
	}
	if len(others) == 0 {
		return false, nil
	}

	existing, err := connections.GetConnections(ctx, others)
	if err != nil {
		return false, err
	}

	return len(existing) > 0, nil
}
