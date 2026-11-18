// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package bridge

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel/trace"

	"github.com/canonical/sso-service/internal/idp"
	"github.com/canonical/sso-service/internal/storage"
	"github.com/canonical/sso-service/internal/types"
)

// testCallback finishes a test sign-in and shows its result. A success makes
// the connection tested; nothing else is kept of a test.
func (s *Service) testCallback(ctx context.Context, test *types.TestState, callback *Callback) (*Result, error) {
	if test.Expired(s.now()) {
		return nil, ErrUnknownState
	}
	connection, err := s.storage.GetConnection(ctx, test.ConnectionID)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, ErrUnknownState
	}
	if err != nil {
		s.recordError(trace.SpanFromContext(ctx), "failed to get connection", err, "connection_id", test.ConnectionID)

		return nil, fmt.Errorf("failed to get connection: %w", err)
	}
	if !strings.EqualFold(connection.OwnerTenantID, callback.TenantID) {
		return nil, ErrUnknownState
	}

	if failure := s.testFailure(ctx, test, connection, callback); failure != "" {
		s.logger.Infow("test sign-in", "connection_id", connection.ID, "outcome", "failed", "error", failure)

		return &Result{Page: messagePage("Test sign-in failed", failure)}, nil
	}

	if err := s.storage.SetTested(ctx, connection.ID); err != nil {
		s.recordError(trace.SpanFromContext(ctx), "failed to set connection tested", err, "connection_id", connection.ID)

		return nil, err
	}
	s.logger.Infow("test sign-in", "connection_id", connection.ID, "outcome", "succeeded")

	return &Result{Page: testSucceededPage(connection.Label)}, nil
}

// testFailure returns what to tell the admin when the answer is not one a
// sign-in would accept, in this service's own words, or nothing.
func (s *Service) testFailure(ctx context.Context, test *types.TestState, connection *types.Connection, callback *Callback) string {
	if callback.Error != "" {
		s.logger.Warnw("test sign-in refused at the identity provider", "connection_id", connection.ID, "error", bounded(callback.Error))

		return "The identity provider refused the sign-in."
	}

	secret, err := s.envelope.Decrypt(connection.ClientSecret, connection.ID)
	if err != nil {
		return "The client secret could not be read."
	}
	claims, err := s.idp.Exchange(ctx, connection, secret, &idp.AuthRequest{
		RedirectURI:  types.RedirectURI(s.publicURL, connection.OwnerTenantID),
		State:        callback.State,
		Nonce:        test.Nonce,
		PKCEVerifier: test.PKCEVerifier,
	}, callback.Code)
	if err != nil {
		s.logger.Warnw("test sign-in exchange failed", "connection_id", connection.ID, "error", err)

		return idp.TestError(err)
	}
	if claims.Email == "" {
		return "The identity provider asserted no email address, so members cannot be matched to their accounts."
	}

	return ""
}
