// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package bridge

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/canonical/sso-service/internal/idp"
	"github.com/canonical/sso-service/internal/storage"
	"github.com/canonical/sso-service/internal/types"
)

// newTestState is a test sign-in through connA, started a moment ago.
func newTestState() *types.TestState {
	return &types.TestState{ConnectionID: connA, Nonce: "n", PKCEVerifier: "v", ExpiresAt: testNow.Add(time.Minute).Unix()}
}

// A test sign-in carries no binding cookie: its state is the test, sealed.
func TestService_testCallback(t *testing.T) {
	authTime := testNow.Add(-time.Minute)
	draft := func() *types.Connection {
		c := newConnection()
		c.TestedAt = nil
		return c
	}
	// expectExchange redeems the code as a sign-in does: with the test's own
	// values, at the connection's own redirect URI.
	expectExchange := func(m *mocks, connection *types.Connection, state string, claims *idp.Claims, err error) {
		m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(connection, nil)
		m.idp.EXPECT().Exchange(gomock.Any(), connection, "secret",
			&idp.AuthRequest{RedirectURI: redirectA, State: state, Nonce: "n", PKCEVerifier: "v"}, "code").Return(claims, err)
	}

	testCases := []struct {
		name string
		// state is the callback's state; nil is a valid test's.
		state func(t *testing.T) string
		// callback is the identity provider's answer; nil is a code at
		// connA's redirect URI.
		callback     func(state string) *Callback
		setupMocks   func(m *mocks, state string)
		expectedPage *Page
		expectedErr  error
	}{
		{
			name: "success",
			setupMocks: func(m *mocks, state string) {
				expectExchange(m, draft(), state, &idp.Claims{Subject: "s", Email: email, EmailVerified: verified(true), AuthTime: &authTime}, nil)
				m.storage.EXPECT().SetTested(gomock.Any(), connA).Return(nil).Times(1)
			},
			expectedPage: testSucceededPage("Acme Okta", true),
		},
		{
			name: "success without auth_time",
			setupMocks: func(m *mocks, state string) {
				expectExchange(m, draft(), state, &idp.Claims{Subject: "s", Email: email}, nil)
				m.storage.EXPECT().SetTested(gomock.Any(), connA).Return(nil).Times(1)
			},
			expectedPage: testSucceededPage("Acme Okta", false),
		},
		{
			name: "already tested",
			setupMocks: func(m *mocks, state string) {
				expectExchange(m, newConnection(), state, &idp.Claims{Subject: "s", Email: email, AuthTime: &authTime}, nil)
				m.storage.EXPECT().SetTested(gomock.Any(), connA).Return(nil).Times(1)
			},
			expectedPage: testSucceededPage("Acme Okta", true),
		},
		{
			name: "no email",
			setupMocks: func(m *mocks, state string) {
				expectExchange(m, draft(), state, &idp.Claims{Subject: "s"}, nil)
			},
			expectedPage: messagePage("Test sign-in failed",
				"The identity provider asserted no email address, so members cannot be matched to their accounts."),
		},
		{
			// The code is not redeemed, and the page says nothing of the
			// identity provider's own words.
			name: "idp refused",
			callback: func(state string) *Callback {
				return &Callback{ConnectionID: connA, State: state, Error: "<script>access_denied"}
			},
			setupMocks: func(m *mocks, state string) {
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(draft(), nil)
			},
			expectedPage: messagePage("Test sign-in failed", "The identity provider refused the sign-in."),
		},
		{
			name: "exchange fails",
			setupMocks: func(m *mocks, state string) {
				expectExchange(m, draft(), state, nil, fmt.Errorf("%w: token endpoint answered 401 \"invalid_client\"", idp.ErrCredentials))
			},
			expectedPage: messagePage("Test sign-in failed", idp.TestError(idp.ErrCredentials)),
		},
		{
			name: "secret that does not decrypt",
			setupMocks: func(m *mocks, state string) {
				unreadable := draft()
				unreadable.ClientSecret = []byte("encrypted under another key")
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(unreadable, nil)
			},
			expectedPage: messagePage("Test sign-in failed", "The client secret could not be read."),
		},
		{
			name: "set tested fails",
			setupMocks: func(m *mocks, state string) {
				expectExchange(m, draft(), state, &idp.Claims{Subject: "s", Email: email}, nil)
				m.storage.EXPECT().SetTested(gomock.Any(), connA).Return(storage.ErrTimeout)
			},
			expectedErr: storage.ErrTimeout,
		},
		{
			name: "connection deleted",
			setupMocks: func(m *mocks, state string) {
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(nil, storage.ErrNotFound)
			},
			expectedErr: ErrUnknownState,
		},
		{
			name: "database down",
			setupMocks: func(m *mocks, state string) {
				m.storage.EXPECT().GetConnection(gomock.Any(), connA).Return(nil, storage.ErrTimeout)
			},
			expectedErr: storage.ErrTimeout,
		},
		{
			// Nothing is read or redeemed.
			name: "another connection's redirect URI",
			callback: func(state string) *Callback {
				return &Callback{ConnectionID: connB, State: state, Code: "code"}
			},
			expectedErr: ErrUnknownState,
		},
		{
			name: "no connection",
			callback: func(state string) *Callback {
				return &Callback{State: state, Code: "code"}
			},
			expectedErr: ErrUnknownState,
		},
		{
			name: "expired",
			state: func(t *testing.T) string {
				expired := newTestState()
				expired.ExpiresAt = testNow.Unix()
				return seal(t, types.PurposeTestState, expired)
			},
			expectedErr: ErrUnknownState,
		},
		{
			// A state that does not open as a test's is taken for a sign-in's,
			// and no browser holds a binding for it.
			name:        "forged",
			state:       func(t *testing.T) string { return "Zm9yZ2Vk" },
			expectedErr: ErrBindingMissing,
		},
		{
			name:        "sealed for another purpose",
			state:       func(t *testing.T) string { return seal(t, types.PurposeTicket, newTicket()) },
			expectedErr: ErrBindingMissing,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s, m := newService(t)
			m.expectSpan("bridge.Service.Callback")

			state := seal(t, types.PurposeTestState, newTestState())
			if tc.state != nil {
				state = tc.state(t)
			}
			if tc.setupMocks != nil {
				tc.setupMocks(m, state)
			}
			callback := &Callback{ConnectionID: connA, State: state, Code: "code"}
			if tc.callback != nil {
				callback = tc.callback(state)
			}

			result, err := s.Callback(context.Background(), callback)

			if tc.expectedErr != nil {
				if !errors.Is(err, tc.expectedErr) || result != nil {
					t.Fatalf("expected error %v and no result, got %+v %v", tc.expectedErr, result, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Page == nil || *result.Page != *tc.expectedPage || result.Redirect != "" || result.Receipt != nil {
				t.Errorf("expected page %+v and nothing else, got %+v", tc.expectedPage, result)
			}
		})
	}
}
