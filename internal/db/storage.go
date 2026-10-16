// Copyright 2025 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/monitoring"
	"github.com/canonical/sso-service/internal/tracing"
)

// A lock is given up before the statement that waits for it, a statement
// before its transaction, and a transaction before Postgres ends it as idle.
const (
	lockTimeout      = 2 * time.Second
	statementTimeout = 5 * time.Second
	txTimeout        = 10 * time.Second
	idleInTxTimeout  = 15 * time.Second
)

type TxContextKey struct{}

var txContextKey TxContextKey

type Config struct {
	DSN             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	TracingEnabled  bool
}

type DBClient struct {
	// pool is the native PGX pool we hold to allow closing
	pool *pgxpool.Pool
	// db original instance to handle transactions
	db *sql.DB

	txTimeout time.Duration

	tracer  tracing.TracingInterface
	monitor monitoring.MonitorInterface
	logger  logging.LoggerInterface
}

// Statement provides a StatementBuilderType configured to use the DBClient's database connection.
// If a transaction exists in the context, it will be used.
func (d *DBClient) Statement(ctx context.Context) sq.StatementBuilderType {
	if tx := txFromContext(ctx); tx != nil {
		return sq.StatementBuilder.PlaceholderFormat(sq.Dollar).RunWith(tx)
	}

	return sq.StatementBuilder.PlaceholderFormat(sq.Dollar).RunWith(d.db)
}

func txFromContext(ctx context.Context) TxInterface {
	if tx, ok := ctx.Value(txContextKey).(TxInterface); ok {
		return tx
	}
	return nil
}

// WithTx executes a function within a transaction context.
// If the function returns an error, the transaction is rolled back.
// Otherwise, the transaction is committed.
// The transaction and everything fn does with its context share one deadline:
// when it passes the transaction is rolled back and its locks are released.
func (d *DBClient) WithTx(ctx context.Context, fn func(context.Context) error) error {
	if txFromContext(ctx) != nil {
		return fn(ctx)
	}

	ctx, cancel := context.WithTimeout(ctx, d.txTimeout)
	defer cancel()

	tx, err := d.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			d.logger.Errorf("failed to rollback transaction: %v", err)
		}
	}()

	if err := fn(context.WithValue(ctx, txContextKey, TxInterface(tx))); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		// fn finished just as the deadline passed: the transaction was rolled
		// back then. Report the timeout, not that the transaction is done.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("transaction not committed: %w", ctxErr)
		}
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

func (d *DBClient) Close() {
	if d.db != nil {
		_ = d.db.Close()
	}

	if d.pool != nil {
		d.pool.Close()
	}
}

// NewDBClient creates a new DBClient instance with the provided DSN and configuration options.
// ctx bounds the first connection to the database.
func NewDBClient(ctx context.Context, cfg Config, tracer tracing.TracingInterface, monitor monitoring.MonitorInterface, logger logging.LoggerInterface) (*DBClient, error) {
	config, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		logger.Fatalf("DSN validation failed, shutting down, err: %v", err)
	}

	if cfg.TracingEnabled {
		// otelpgx.NewTracer will use default global TracerProvider, just like our tracer struct
		config.ConnConfig.Tracer = otelpgx.NewTracer()
	}

	config.MaxConns = cfg.MaxConns
	config.MinConns = cfg.MinConns
	config.MaxConnLifetime = cfg.MaxConnLifetime
	config.MaxConnLifetimeJitter = cfg.MaxConnLifetime / 10 // Add 10% jitter to avoid thundering herd
	config.MaxConnIdleTime = cfg.MaxConnIdleTime

	for name, timeout := range map[string]time.Duration{
		"lock_timeout":                        lockTimeout,
		"statement_timeout":                   statementTimeout,
		"idle_in_transaction_session_timeout": idleInTxTimeout,
	} {
		config.ConnConfig.RuntimeParams[name] = strconv.FormatInt(timeout.Milliseconds(), 10)
	}

	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		return nil, fmt.Errorf("failed to create db pool: %v", err)
	}

	if cfg.TracingEnabled {
		// when tracing is enabled, also collect metrics
		if err := otelpgx.RecordStats(pool); err != nil {
			return nil, fmt.Errorf("failed to start metrics collection for database: %v", err)
		}
	}

	db := stdlib.OpenDBFromPool(pool)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		pool.Close()
		return nil, fmt.Errorf("failed to connect to the database: %v", err)
	}

	d := new(DBClient)
	d.pool = pool
	d.db = db
	d.txTimeout = txTimeout

	d.tracer = tracer
	d.monitor = monitor
	d.logger = logger

	return d, nil
}
