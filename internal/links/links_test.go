// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package links

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/canonical/sso-service/internal/kratos"
	"github.com/canonical/sso-service/internal/types"
)

//go:generate mockgen -build_flags=--mod=mod -package links -destination ./mock_links.go -source=./interfaces.go

const (
	connA = "0190a0b0-0000-7000-8000-00000000000a"
	connB = "0190a0b0-0000-7000-8000-00000000000b"
)

func identityWith(credentials map[string]string) *kratos.Identity {
	i := &kratos.Identity{ID: "i", Credentials: map[string]kratos.Credential{}}
	for t, config := range credentials {
		i.Credentials[t] = kratos.Credential{Config: json.RawMessage(config)}
	}
	return i
}

func linkAt(connection string) string {
	return `{"provider":"byo-sso","subject":"` + connection + `:s"}`
}

func TestLink_Identifier(t *testing.T) {
	link := Link{ConnectionID: connA, Subject: "sub:with:colons"}
	if want := types.ProviderID + ":" + connA + ":sub:with:colons"; link.Identifier() != want {
		t.Errorf("expected identifier %q, got %q", want, link.Identifier())
	}
}

func TestOf(t *testing.T) {
	i := identityWith(map[string]string{"oidc": `{"providers":[
		{"provider":"byo-sso","subject":"` + connA + `:sub:with:colons"},
		{"provider":"byo-sso","subject":"not-a-connection:subject"},
		{"provider":"google","subject":"g"}]}`})

	got := Of(i)
	if len(got) != 1 || got[0].ConnectionID != connA || got[0].Subject != "sub:with:colons" {
		t.Errorf("expected the one link at %s, got %+v", connA, got)
	}
	if got := Of(identityWith(nil)); len(got) != 0 {
		t.Errorf("expected no links without an oidc credential, got %+v", got)
	}
}

func TestAt(t *testing.T) {
	i := identityWith(map[string]string{"oidc": `{"providers":[
		{"provider":"byo-sso","subject":"` + connA + `:first"},
		{"provider":"byo-sso","subject":"` + connB + `:other"},
		{"provider":"byo-sso","subject":"` + connA + `:second"},
		{"provider":"google","subject":"` + connA + `:not-a-link"}]}`})

	got := At(i, connA)
	if len(got) != 2 || got[0].Subject != "first" || got[1].Subject != "second" {
		t.Errorf("expected both links at %s, got %+v", connA, got)
	}
	if got := At(i, "0190a0b0-0000-7000-8000-00000000000c"); len(got) != 0 {
		t.Errorf("expected no link at another connection, got %+v", got)
	}
	if got := At(identityWith(nil), connA); len(got) != 0 {
		t.Errorf("expected no link without an oidc credential, got %+v", got)
	}
}

func TestOwnWayIn(t *testing.T) {
	storageErr := errors.New("storage timeout")

	testCases := []struct {
		name        string
		credentials map[string]string
		setupMocks  func(*MockConnectionsInterface)
		expected    bool
		expectedErr error
	}{
		{
			name:        "nothing",
			credentials: map[string]string{"password": `{}`},
		},
		{
			name:        "password",
			credentials: map[string]string{"password": `{"hashed_password":"h"}`},
			expected:    true,
		},
		{
			name:        "public sign-in",
			credentials: map[string]string{"oidc": `{"providers":[{"provider":"github","subject":"x"}]}`},
			expected:    true,
		},
		{
			name:        "passkey",
			credentials: map[string]string{"passkey": `{"credentials":[{"id":"k"}]}`},
			expected:    true,
		},
		{
			name:        "only the excepted link",
			credentials: map[string]string{"oidc": `{"providers":[` + linkAt(connA) + `]}`},
		},
		{
			name:        "link to an existing connection",
			credentials: map[string]string{"oidc": `{"providers":[` + linkAt(connA) + `,` + linkAt(connB) + `]}`},
			setupMocks: func(connections *MockConnectionsInterface) {
				connections.EXPECT().GetConnections(gomock.Any(), []string{connB}).Return([]*types.Connection{{ID: connB}}, nil)
			},
			expected: true,
		},
		{
			name:        "link to a deleted connection",
			credentials: map[string]string{"oidc": `{"providers":[` + linkAt(connB) + `]}`},
			setupMocks: func(connections *MockConnectionsInterface) {
				connections.EXPECT().GetConnections(gomock.Any(), []string{connB}).Return([]*types.Connection{}, nil)
			},
		},
		{
			name:        "storage error",
			credentials: map[string]string{"oidc": `{"providers":[` + linkAt(connB) + `]}`},
			setupMocks: func(connections *MockConnectionsInterface) {
				connections.EXPECT().GetConnections(gomock.Any(), []string{connB}).Return(nil, storageErr)
			},
			expectedErr: storageErr,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockConnections := NewMockConnectionsInterface(ctrl)
			if tc.setupMocks != nil {
				tc.setupMocks(mockConnections)
			}

			got, err := OwnWayIn(context.Background(), mockConnections, identityWith(tc.credentials), connA)

			if !errors.Is(err, tc.expectedErr) {
				t.Errorf("expected error %v, got %v", tc.expectedErr, err)
			}
			if got != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, got)
			}
		})
	}
}
