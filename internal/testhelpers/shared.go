// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package testhelpers

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	pgmodule "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// SharedContainers holds lazily-initialized containers for the lifetime of a
// test binary. Declare one package-level variable per package and close it
// from TestMain:
//
//	var shared testhelpers.SharedContainers
//
//	func TestMain(m *testing.M) {
//		code := m.Run()
//		shared.Close()
//		os.Exit(code)
//	}
//
// Each container starts exactly once, on the first test that needs it. Tests
// that never touch a fixture never pay its startup cost (a `go test -short`
// run starts none), and Close is a no-op for fixtures that were never
// started.
type SharedContainers struct {
	Postgres LazyPostgres
	Hydra    LazyHydra
}

// Close terminates any containers that were started. Safe to call even if no
// container was initialized.
func (s *SharedContainers) Close() {
	s.Postgres.close()
	s.Hydra.close()
}

// lazyShared tracks the lifecycle of one lazily-started container. The first
// call to ensure runs start exactly once; the outcome (success or error) is
// recorded and replayed to every caller, so a failed start fails every
// dependent test with the same error instead of a confusing nil pointer.
type lazyShared struct {
	once     sync.Once
	startErr error
	started  bool
}

// ensure runs start once and fails the calling test if startup failed. A
// panic inside start is recovered and recorded as the startup error; any
// failure — panic or incomplete start — is replayed to every caller
// (sync.Once marks done even on panic or runtime.Goexit from t.Fatal).
func (l *lazyShared) ensure(t *testing.T, start func()) {
	t.Helper()
	l.once.Do(func() {
		defer func() {
			if r := recover(); r != nil {
				l.startErr = fmt.Errorf("shared container start panicked: %v", r)
			}
		}()
		start()
	})
	if l.startErr != nil {
		t.Fatalf("shared container failed to start: %v", l.startErr)
	}
	if !l.started {
		t.Fatalf("shared container start did not complete (fatal called inside start?)")
	}
}

// dbNameSeq generates unique database names (test_db_1, test_db_2, ...) for
// the lifetime of the test binary. The shared container lives for one
// binary, so a process-wide counter trivially guarantees uniqueness; names
// stay valid unquoted identifiers.
var dbNameSeq atomic.Uint64

// LazyPostgres lazily starts a shared Postgres container. The zero value is
// ready to use.
type LazyPostgres struct {
	shared    lazyShared
	connStr   string
	container *pgmodule.PostgresContainer
}

// connString returns the shared container's connection string, starting the
// container on first call. start runs under sync.Once; it cannot call t.Fatal
// safely (the calling test may have already returned), so failures are
// recorded and replayed by ensure.
func (p *LazyPostgres) connString(t *testing.T) string {
	t.Helper()
	p.shared.ensure(t, func() {
		connStr, pgContainer, err := startPostgres()
		if err != nil {
			p.shared.startErr = err
			return
		}

		p.connStr = connStr
		p.container = pgContainer
		p.shared.started = true
	})
	return p.connStr
}

// IsolatedDB returns a connection string to a fresh database in the shared
// Postgres container, with all migrations applied. The database is dropped
// in t.Cleanup. Tests within a package share one container but get fully
// separate databases, so t.Parallel is safe, and so is a test that leaves
// rows, locks or session settings behind. Tests should size MaxConns
// against the shared container's max_connections (Postgres default 100): a
// package with ~10 parallel tests at MaxConns 5 is fine, but should be
// revisited if a future package goes higher.
//
// The connection string is in keyword/value form
// ("host=... port=... dbname=..."), which pgx and the service's own
// db.NewDBClient both take.
func (p *LazyPostgres) IsolatedDB(t *testing.T) string {
	t.Helper()

	base := p.connString(t)

	cfg, err := pgx.ParseConfig(base)
	if err != nil {
		t.Fatalf("failed to parse connection string: %v", err)
	}

	dbName := fmt.Sprintf("test_db_%d", dbNameSeq.Add(1))
	t.Logf("creating isolated database %s for %s", dbName, t.Name())

	adminDB := stdlib.OpenDB(*cfg)
	defer adminDB.Close()
	if _, err := adminDB.ExecContext(context.Background(),
		fmt.Sprintf("CREATE DATABASE %s", pgx.Identifier{dbName}.Sanitize())); err != nil {
		t.Fatalf("failed to create database %s: %v", dbName, err)
	}

	t.Cleanup(func() {
		dropDB := stdlib.OpenDB(*cfg)
		defer dropDB.Close()
		// Terminate connections to the database before dropping it so
		// parallel tests, or a test that left a transaction open, cannot
		// hold the drop hostage.
		if _, err := dropDB.ExecContext(context.Background(),
			"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()", dbName); err != nil {
			t.Logf("failed to terminate connections to %s: %v", dbName, err)
		}
		if _, err := dropDB.ExecContext(context.Background(),
			fmt.Sprintf("DROP DATABASE %s", pgx.Identifier{dbName}.Sanitize())); err != nil {
			t.Logf("failed to drop database %s: %v", dbName, err)
		}
	})

	isolated := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, dbName)

	RunMigrations(t, isolated)

	return isolated
}

// close terminates the shared container if it was started; no-op otherwise.
func (p *LazyPostgres) close() {
	if p.shared.started && p.container != nil {
		p.container.Terminate(context.Background()) //nolint:errcheck
	}
}

// LazyHydra lazily starts a shared Hydra container. The zero value is ready
// to use.
type LazyHydra struct {
	shared    lazyShared
	env       *HydraEnv
	container testcontainers.Container
}

// Env starts the Hydra container on first call and returns its endpoints.
func (h *LazyHydra) Env(t *testing.T) *HydraEnv {
	t.Helper()
	h.shared.ensure(t, func() {
		env, container, err := startHydra()
		if err != nil {
			h.shared.startErr = err
			return
		}
		h.env = env
		h.container = container
		h.shared.started = true
	})
	return h.env
}

// close terminates the shared container if it was started; no-op otherwise.
func (h *LazyHydra) close() {
	if h.shared.started && h.container != nil {
		h.container.Terminate(context.Background()) //nolint:errcheck
	}
}
