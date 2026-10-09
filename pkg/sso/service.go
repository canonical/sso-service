// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package sso

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/canonical/sso-service/internal/kratos"
	"github.com/canonical/sso-service/internal/limits"
	"github.com/canonical/sso-service/internal/links"
	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/monitoring"
	"github.com/canonical/sso-service/internal/secrets"
	"github.com/canonical/sso-service/internal/storage"
	"github.com/canonical/sso-service/internal/tracing"
	"github.com/canonical/sso-service/internal/types"
)

// Service provides the sign-in logic the login UI calls.
type Service struct {
	storage  StorageInterface
	kratos   KratosClientInterface
	envelope EnvelopeInterface
	now      func() time.Time

	tracer  tracing.TracingInterface
	monitor monitoring.MonitorInterface
	logger  logging.LoggerInterface
}

func NewService(
	storage StorageInterface,
	kratosClient KratosClientInterface,
	envelope EnvelopeInterface,
	tracer tracing.TracingInterface,
	monitor monitoring.MonitorInterface,
	logger logging.LoggerInterface,
) *Service {
	return &Service{
		storage:  storage,
		kratos:   kratosClient,
		envelope: envelope,
		now:      time.Now,
		tracer:   tracer,
		monitor:  monitor,
		logger:   logger,
	}
}

// recordError records an error on the span and emits a structured error log.
// The "error" key is always appended to keysAndValues automatically.
func (s *Service) recordError(span trace.Span, msg string, err error, keysAndValues ...interface{}) {
	span.RecordError(err)
	span.SetStatus(otelcodes.Error, err.Error())
	s.logger.Errorw(msg, append(keysAndValues, "error", err)...)
}

// ListOptions returns the tested connections among those asked for, in the
// order asked.
func (s *Service) ListOptions(ctx context.Context, connectionIDs []string) ([]*types.Connection, error) {
	ctx, span := s.tracer.Start(ctx, "sso.Service.ListOptions")
	defer span.End()

	connections, err := s.storage.GetConnections(ctx, connectionIDs)
	if err != nil {
		s.recordError(span, "failed to get connections", err)

		return nil, fmt.Errorf("failed to get connections: %w", err)
	}
	byID := make(map[string]*types.Connection, len(connections))
	for _, c := range connections {
		byID[c.ID] = c
	}

	options := make([]*types.Connection, 0, len(connections))
	for _, id := range connectionIDs {
		if c, ok := byID[id]; ok && c.Tested() {
			options = append(options, c)
			delete(byID, id)
		}
	}

	return options, nil
}

// StartAttempt seals a sign-in attempt into its ticket; nothing is stored.
func (s *Service) StartAttempt(ctx context.Context, tenantID, email, connectionID string, reauthenticate bool) (string, error) {
	ctx, span := s.tracer.Start(ctx, "sso.Service.StartAttempt")
	defer span.End()

	connection, err := s.storage.GetConnection(ctx, connectionID)
	if errors.Is(err, storage.ErrNotFound) || (err == nil && !connection.Tested()) {
		return "", ErrConnectionNotUsable
	}
	if err != nil {
		s.recordError(span, "failed to get connection", err, "connection_id", connectionID)

		return "", fmt.Errorf("failed to get connection: %w", err)
	}
	now := s.now().UTC()
	ticket, err := s.envelope.Seal(types.PurposeTicket, &types.Ticket{
		TenantID:       tenantID,
		ConnectionID:   connection.ID,
		Email:          email,
		Reauthenticate: reauthenticate,
		IssuedAt:       now.Unix(),
		ExpiresAt:      now.Add(limits.AttemptTTL).Unix(),
	})
	if err != nil {
		s.recordError(span, "failed to seal ticket", err)

		return "", fmt.Errorf("failed to seal ticket: %w", err)
	}
	s.logger.Infow("attempt started",
		"attempt", secrets.Digest(ticket), "connection_id", connection.ID, "tenant_id", tenantID,
		"reauthenticate", reauthenticate)

	return ticket, nil
}

// CompleteAttempt confirms that the sign-in the ticket started ended at this
// account: the ticket is unexpired, the account has the ticket's address and
// holds a link to the ticket's connection, and the receipt shows that the
// browser completed the sign-in as the subject of such a link. Nothing is
// stored or written, so a repeat gets the same answer.
func (s *Service) CompleteAttempt(ctx context.Context, token, identityID, receipt string) (*types.Ticket, error) {
	ctx, span := s.tracer.Start(ctx, "sso.Service.CompleteAttempt")
	defer span.End()

	refused := func(detail string) error {
		s.incrementCompleteAttemptRefused()
		s.logger.Infow("complete attempt refused", "detail", detail,
			"attempt", secrets.Digest(token), "identity", secrets.Digest(identityID))

		return ErrAttemptNotCompleted
	}

	ticket := new(types.Ticket)
	if err := s.envelope.Open(types.PurposeTicket, token, ticket); err != nil {
		return nil, refused("unreadable ticket")
	}
	if ticket.Expired(s.now()) {
		return nil, refused("expired")
	}

	identity, err := s.kratos.GetIdentity(ctx, identityID, kratos.OIDCCredential)
	switch {
	case errors.Is(err, kratos.ErrNotFound):
		return nil, refused("no such account")
	case err != nil:
		s.recordError(span, "failed to get identity", err)

		return nil, fmt.Errorf("%w: %v", ErrKratosUnavailable, err)
	case !strings.EqualFold(identity.Email(), ticket.Email):
		return nil, refused("the account's address differs")
	}
	held := links.At(identity, ticket.ConnectionID)
	if len(held) == 0 {
		return nil, refused("the account holds no link to the ticket's connection")
	}
	// A receipt is made for one subject, and the account may hold several.
	if !slices.ContainsFunc(held, func(l links.Link) bool {
		return s.envelope.ValidReceipt(receipt, token, types.HydraSubject(l.ConnectionID, l.Subject))
	}) {
		return nil, refused("no receipt of the ticket's sign-in as a subject the account holds")
	}

	s.incrementAttemptsCompleted()
	s.logger.Infow("attempt completed", "attempt", secrets.Digest(token),
		"connection_id", ticket.ConnectionID, "tenant_id", ticket.TenantID, "identity", secrets.Digest(identityID))

	return ticket, nil
}

// ListLinks returns the connections the account holds a link to. Links to
// connections that no longer exist are ignored.
func (s *Service) ListLinks(ctx context.Context, identityID string) ([]*types.Connection, error) {
	ctx, span := s.tracer.Start(ctx, "sso.Service.ListLinks")
	defer span.End()

	identity, err := s.kratos.GetIdentity(ctx, identityID, kratos.OIDCCredential)
	if errors.Is(err, kratos.ErrNotFound) {
		return nil, ErrAccountNotFound
	}
	if err != nil {
		s.recordError(span, "failed to get identity", err)

		return nil, fmt.Errorf("%w: %v", ErrKratosUnavailable, err)
	}

	accountLinks := links.Of(identity)
	ids := make([]string, 0, len(accountLinks))
	for _, l := range accountLinks {
		ids = append(ids, l.ConnectionID)
	}
	connections, err := s.storage.GetConnections(ctx, ids)
	if err != nil {
		s.recordError(span, "failed to get connections", err)

		return nil, fmt.Errorf("failed to get connections: %w", err)
	}
	byID := make(map[string]*types.Connection, len(connections))
	for _, c := range connections {
		byID[c.ID] = c
	}

	linked := make([]*types.Connection, 0, len(accountLinks))
	for _, id := range ids {
		if c, ok := byID[id]; ok {
			linked = append(linked, c)
			delete(byID, id)
		}
	}

	return linked, nil
}

// DeleteLink removes the account's link at one connection, unless that would
// leave it no way to sign in of its own.
func (s *Service) DeleteLink(ctx context.Context, identityID, connectionID string) error {
	ctx, span := s.tracer.Start(ctx, "sso.Service.DeleteLink")
	defer span.End()

	identity, err := s.kratos.GetIdentity(ctx, identityID, kratos.FirstFactorCredentials...)
	if errors.Is(err, kratos.ErrNotFound) {
		return ErrAccountNotFound
	}
	if err != nil {
		s.recordError(span, "failed to get identity", err)

		return fmt.Errorf("%w: %v", ErrKratosUnavailable, err)
	}

	remove := links.At(identity, connectionID)
	if len(remove) == 0 {
		return ErrLinkNotFound
	}

	wayIn, err := links.OwnWayIn(ctx, s.storage, identity, connectionID)
	if err != nil {
		s.recordError(span, "failed to get connections", err)

		return fmt.Errorf("failed to get connections: %w", err)
	}
	if !wayIn {
		return ErrLastCredential
	}

	for _, l := range remove {
		if err := s.kratos.DeleteOIDCIdentifier(ctx, identity.ID, l.Identifier()); err != nil && !errors.Is(err, kratos.ErrNotFound) {
			s.recordError(span, "failed to delete link", err)

			return fmt.Errorf("%w: %v", ErrKratosUnavailable, err)
		}
	}
	s.logger.Security().AdminAction(identity.ID, "delete_link", "sso.Service.DeleteLink", connectionID)

	return nil
}

func (s *Service) incrementAttemptsCompleted() {
	if err := s.monitor.IncrementAttemptsCompleted(); err != nil {
		s.logger.Warnf("failed to increment attempts completed counter: %v", err)
	}
}

func (s *Service) incrementCompleteAttemptRefused() {
	if err := s.monitor.IncrementCompleteAttemptRefused(); err != nil {
		s.logger.Warnf("failed to increment complete attempt refused counter: %v", err)
	}
}
