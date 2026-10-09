// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package bridge

import (
	"context"
	"errors"
	"fmt"

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

	claims, failure := s.testClaims(ctx, test, connection, callback)
	if failure != "" {
		s.logger.Infow("test sign-in", "connection_id", connection.ID, "outcome", "failed", "error", failure)

		return &Result{Page: messagePage("Test sign-in failed", failure)}, nil
	}

	if err := s.storage.SetTested(ctx, connection.ID); err != nil {
		s.recordError(trace.SpanFromContext(ctx), "failed to set connection tested", err, "connection_id", connection.ID)

		return nil, err
	}
	s.logger.Infow("test sign-in", "connection_id", connection.ID, "outcome", "succeeded")

	return &Result{Page: testSucceededPage(connection.Label, claims.AuthTime != nil)}, nil
}

// testClaims returns claims a sign-in would accept, or what to tell the
// admin, in this service's own words.
func (s *Service) testClaims(ctx context.Context, test *types.TestState, connection *types.Connection, callback *Callback) (*idp.Claims, string) {
	if callback.Error != "" {
		s.logger.Warnw("test sign-in refused at the identity provider", "connection_id", connection.ID, "error", bounded(callback.Error))

		return nil, "The identity provider refused the sign-in."
	}

	secret, err := s.envelope.Decrypt(connection.ClientSecret, connection.ID)
	if err != nil {
		return nil, "The client secret could not be read."
	}
	claims, err := s.idp.Exchange(ctx, connection, secret, &idp.AuthRequest{
		RedirectURI:  types.RedirectURI(s.publicURL, connection.ID),
		State:        callback.State,
		Nonce:        test.Nonce,
		PKCEVerifier: test.PKCEVerifier,
	}, callback.Code)
	if err != nil {
		s.logger.Warnw("test sign-in exchange failed", "connection_id", connection.ID, "error", err)

		return nil, idp.TestError(err)
	}
	if claims.Email == "" {
		return nil, "The identity provider asserted no email address, so members cannot be matched to their accounts."
	}

	return claims, ""
}
