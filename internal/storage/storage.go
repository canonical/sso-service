// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	sq "github.com/Masterminds/squirrel"

	"github.com/canonical/sso-service/internal/db"
	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/monitoring"
	"github.com/canonical/sso-service/internal/tracing"
	"github.com/canonical/sso-service/internal/types"
)

var connectionColumns = []string{
	"id", "owner_tenant_id", "label", "issuer", "client_id", "client_secret",
	"created_by", "created_at", "updated_at", "tested_at",
}

// ConnectionUpdate carries what UpdateConnection may change; nil leaves a
// field as it is.
type ConnectionUpdate struct {
	Label        *string
	ClientSecret []byte
}

type Storage struct {
	db db.DBClientInterface

	logger  logging.LoggerInterface
	tracer  tracing.TracingInterface
	monitor monitoring.MonitorInterface
}

func NewStorage(c db.DBClientInterface, tracer tracing.TracingInterface, monitor monitoring.MonitorInterface, logger logging.LoggerInterface) *Storage {
	s := new(Storage)

	s.db = c

	s.logger = logger
	s.tracer = tracer
	s.monitor = monitor

	return s
}

// WithTx runs fn in one transaction with a deadline of its own: a lock taken
// in it is released when fn returns or the deadline passes, whichever is
// first.
func (s *Storage) WithTx(ctx context.Context, fn func(context.Context) error) error {
	return wrapTimeout(s.db.WithTx(ctx, fn))
}

// done records the operation's latency and names its timeouts.
func (s *Storage) done(operation string, start time.Time, err error) error {
	err = wrapTimeout(err)
	status := "success"
	if err != nil && !errors.Is(err, ErrNotFound) {
		status = "error"
	}
	s.monitor.SetStorageResponseTimeMetric(map[string]string{
		"operation": operation,
		"status":    status,
	}, time.Since(start).Seconds())

	return err
}

// CreateConnection stores c as a draft, with the database's time, unless its
// owner already has limit connections, which is ErrConnectionLimit. Creates
// that run at the same time do not see each other's rows, so they can pass
// the limit together.
func (s *Storage) CreateConnection(ctx context.Context, c *types.Connection, limit int) (connection *types.Connection, err error) {
	defer func(start time.Time) { err = s.done("CreateConnection", start, err) }(time.Now())
	ctx, span := s.tracer.Start(ctx, "storage.CreateConnection")
	defer span.End()

	columns := []string{"id", "owner_tenant_id", "label", "issuer", "client_id", "client_secret", "created_by"}
	values := sq.Select().
		Column(sq.Expr(sq.Placeholders(len(columns)),
			c.ID, c.OwnerTenantID, c.Label, c.Issuer, c.ClientID, c.ClientSecret, nullString(c.CreatedBy))).
		Where(sq.Expr("(SELECT count(*) FROM sso_connections WHERE owner_tenant_id = ?) < ?", c.OwnerTenantID, limit))

	connection, err = scanConnection(s.db.Statement(ctx).
		Insert("sso_connections").
		Columns(columns...).
		Select(values).
		Suffix("RETURNING " + strings.Join(connectionColumns, ", ")).
		QueryRowContext(ctx))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrConnectionLimit
	}

	return connection, err
}

func (s *Storage) GetConnection(ctx context.Context, id string) (connection *types.Connection, err error) {
	defer func(start time.Time) { err = s.done("GetConnection", start, err) }(time.Now())
	ctx, span := s.tracer.Start(ctx, "storage.GetConnection")
	defer span.End()

	return scanConnection(s.db.Statement(ctx).
		Select(connectionColumns...).
		From("sso_connections").
		Where(sq.Eq{"id": id}).
		QueryRowContext(ctx))
}

// GetConnections returns those of ids that exist, in no particular order.
func (s *Storage) GetConnections(ctx context.Context, ids []string) (connections []*types.Connection, err error) {
	defer func(start time.Time) { err = s.done("GetConnections", start, err) }(time.Now())
	ctx, span := s.tracer.Start(ctx, "storage.GetConnections")
	defer span.End()

	if len(ids) == 0 {
		return []*types.Connection{}, nil
	}

	rows, err := s.db.Statement(ctx).
		Select(connectionColumns...).
		From("sso_connections").
		Where(sq.Eq{"id": ids}).
		QueryContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get connections: %w", err)
	}

	return scanConnections(rows)
}

// ListConnections pages by id, after afterID; every owner's connections when
// ownerTenantID is "".
func (s *Storage) ListConnections(ctx context.Context, ownerTenantID, afterID string, limit int) (connections []*types.Connection, err error) {
	defer func(start time.Time) { err = s.done("ListConnections", start, err) }(time.Now())
	ctx, span := s.tracer.Start(ctx, "storage.ListConnections")
	defer span.End()

	query := s.db.Statement(ctx).
		Select(connectionColumns...).
		From("sso_connections").
		OrderBy("id").
		Limit(uint64(limit))

	if ownerTenantID != "" {
		query = query.Where(sq.Eq{"owner_tenant_id": ownerTenantID})
	}
	if afterID != "" {
		query = query.Where(sq.Gt{"id": afterID})
	}

	rows, err := query.QueryContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list connections: %w", err)
	}

	return scanConnections(rows)
}

// LockConnections locks the rows of ids for the rest of the transaction, in
// id order and in one statement, so two transactions locking overlapping
// sets cannot deadlock. Missing ids are absent from the result; a lock not
// granted within lock_timeout is ErrBusy.
func (s *Storage) LockConnections(ctx context.Context, ids []string) (connections []*types.Connection, err error) {
	defer func(start time.Time) { err = s.done("LockConnections", start, err) }(time.Now())
	ctx, span := s.tracer.Start(ctx, "storage.LockConnections")
	defer span.End()

	if len(ids) == 0 {
		return []*types.Connection{}, nil
	}

	rows, err := s.db.Statement(ctx).
		Select(connectionColumns...).
		From("sso_connections").
		Where(sq.Eq{"id": ids}).
		OrderBy("id").
		Suffix("FOR UPDATE").
		QueryContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to lock connections: %w", err)
	}

	return scanConnections(rows)
}

func (s *Storage) UpdateConnection(ctx context.Context, id string, update ConnectionUpdate) (connection *types.Connection, err error) {
	defer func(start time.Time) { err = s.done("UpdateConnection", start, err) }(time.Now())
	ctx, span := s.tracer.Start(ctx, "storage.UpdateConnection")
	defer span.End()

	query := s.db.Statement(ctx).
		Update("sso_connections").
		Set("updated_at", sq.Expr("NOW()")).
		Where(sq.Eq{"id": id})

	if update.Label != nil {
		query = query.Set("label", *update.Label)
	}
	if update.ClientSecret != nil {
		query = query.Set("client_secret", update.ClientSecret)
	}

	return scanConnection(query.
		Suffix("RETURNING " + strings.Join(connectionColumns, ", ")).
		QueryRowContext(ctx))
}

func (s *Storage) DeleteConnection(ctx context.Context, id string) (err error) {
	defer func(start time.Time) { err = s.done("DeleteConnection", start, err) }(time.Now())
	ctx, span := s.tracer.Start(ctx, "storage.DeleteConnection")
	defer span.End()

	result, err := s.db.Statement(ctx).
		Delete("sso_connections").
		Where(sq.Eq{"id": id}).
		ExecContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to delete connection: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}

	return nil
}

// SetTested marks a connection tested, once: one that is tested already, or
// gone, is left as it is.
func (s *Storage) SetTested(ctx context.Context, id string) (err error) {
	defer func(start time.Time) { err = s.done("SetTested", start, err) }(time.Now())
	ctx, span := s.tracer.Start(ctx, "storage.SetTested")
	defer span.End()

	_, err = s.db.Statement(ctx).
		Update("sso_connections").
		Set("tested_at", sq.Expr("NOW()")).
		Set("updated_at", sq.Expr("NOW()")).
		Where(sq.Eq{"id": id, "tested_at": nil}).
		ExecContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to set connection tested: %w", err)
	}

	return nil
}
