// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/canonical/sso-service/internal/db"
	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/monitoring"
	"github.com/canonical/sso-service/internal/testhelpers"
	"github.com/canonical/sso-service/internal/tracing"
	"github.com/canonical/sso-service/internal/types"
)

const (
	tenantA = "11111111-1111-4111-8111-111111111111"
	tenantB = "22222222-2222-4222-8222-222222222222"

	// short is the timeout of the tests that make a statement wait: a
	// refusal comes soon; quick is how soon at most, with room for a busy
	// machine.
	short = 300 * time.Millisecond
	quick = 10 * time.Second

	lockTimeout      = "lock_timeout"
	statementTimeout = "statement_timeout"
)

// shared is the one Postgres container of this package's integration tests;
// every test takes a database of its own in it.
var shared testhelpers.SharedContainers

func TestMain(m *testing.M) {
	code := m.Run()
	shared.Close()
	os.Exit(code)
}

// plane is a store on a fresh database, with the migrations applied.
type plane struct {
	store *Storage
	// raw is the same database without the store: what the store wrote, and
	// rows it would never write.
	raw *sql.DB
}

func newStore(t *testing.T) *plane {
	t.Helper()

	dsn := shared.Postgres.IsolatedDB(t)
	logger := logging.NewNoopLogger()
	tracer := tracing.NewNoopTracer()
	monitor := monitoring.NewNoopMonitor("sso-service-test", logger)
	client, err := db.NewDBClient(context.Background(), db.Config{
		DSN: dsn, MaxConns: 8, MinConns: 1, MaxConnLifetime: time.Hour, MaxConnIdleTime: 30 * time.Minute,
	}, tracer, monitor, logger)
	if err != nil {
		t.Fatalf("failed to create the DB client: %v", err)
	}
	t.Cleanup(client.Close)

	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("failed to parse the DSN: %v", err)
	}
	raw := stdlib.OpenDB(*config)
	t.Cleanup(func() { raw.Close() })

	return &plane{store: NewStorage(client, tracer, monitor, logger), raw: raw}
}

// newConnection is a draft connection of owner, not stored yet.
func newConnection(owner string) *types.Connection {
	return &types.Connection{
		ID: uuid.Must(uuid.NewV7()).String(), OwnerTenantID: owner, Label: "L",
		Issuer: "https://idp.example", ClientID: "c", ClientSecret: []byte{1, 2, 3},
		CreatedBy: uuid.NewString(),
	}
}

// noLimit is a limit no test's tenant reaches.
const noLimit = 1000

// insert stores draft connections of tenantA and returns them as stored.
func (p *plane) insert(t *testing.T, n int) []*types.Connection {
	t.Helper()
	out := make([]*types.Connection, 0, n)
	for range n {
		created, err := p.store.CreateConnection(context.Background(), newConnection(tenantA), noLimit)
		if err != nil {
			t.Fatalf("failed to insert a connection: %v", err)
		}
		out = append(out, created)
	}
	return out
}

func (p *plane) get(t *testing.T, id string) *types.Connection {
	t.Helper()
	c, err := p.store.GetConnection(context.Background(), id)
	if err != nil {
		t.Fatalf("failed to read connection %s: %v", id, err)
	}
	return c
}

// hold runs fn in a transaction of the store and keeps the transaction open,
// with whatever locks fn took, until release is called (or the test ends).
// It returns once fn has returned; fn failing is fatal.
func (p *plane) hold(t *testing.T, fn func(ctx context.Context) error) (release func()) {
	t.Helper()
	ready, done, finished := make(chan error, 1), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		_ = p.store.WithTx(context.Background(), func(ctx context.Context) error {
			if err := fn(ctx); err != nil {
				ready <- err
				return err
			}
			ready <- nil
			<-done
			return nil
		})
	}()

	var once sync.Once
	release = func() {
		once.Do(func() {
			close(done)
			<-finished
		})
	}
	t.Cleanup(release)

	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("the holding transaction failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the holding transaction did not get what it asked for")
	}

	return release
}

// with runs fn in a transaction of the store whose statements run under
// settings instead of the timeouts every connection has: shorter, so that a
// refusal comes soon, or longer, to wait one out. Zero sets none.
func (p *plane) with(settings map[string]time.Duration, fn func(ctx context.Context) error) error {
	return p.store.WithTx(context.Background(), func(ctx context.Context) error {
		for name, timeout := range settings {
			var applied string
			err := p.store.db.Statement(ctx).
				Select().
				Column(sq.Expr("set_config(?, ?, true)", name, strconv.FormatInt(timeout.Milliseconds(), 10))).
				QueryRowContext(ctx).
				Scan(&applied)
			if err != nil {
				return fmt.Errorf("failed to set %s: %w", name, err)
			}
		}
		return fn(ctx)
	})
}

// lock asks for the rows of ids in a transaction of its own, which ends at
// once.
func (p *plane) lock(ids ...string) ([]*types.Connection, error) {
	return p.lockWithin(0, ids...)
}

// lockWithin is lock, waiting for the rows for timeout instead of the
// connection's lock_timeout.
func (p *plane) lockWithin(timeout time.Duration, ids ...string) ([]*types.Connection, error) {
	settings := map[string]time.Duration{}
	if timeout > 0 {
		settings[lockTimeout] = timeout
	}
	var locked []*types.Connection
	err := p.with(settings, func(ctx context.Context) (err error) {
		locked, err = p.store.LockConnections(ctx, ids)
		return err
	})
	return locked, err
}

// refusedAfter checks that what just returned err was refused as want, and
// after waiting about timeout: not at once, and not left hanging.
func refusedAfter(t *testing.T, what string, err, want error, started time.Time, timeout time.Duration) {
	t.Helper()
	took := time.Since(started)
	if !errors.Is(err, want) {
		t.Fatalf("%s: expected %v, got %v (after %s)", what, want, err, took)
	}
	if took < timeout-timeout/5 || took > quick {
		t.Fatalf("%s: refused after %s, expected about %s", what, took, timeout)
	}
}

// settled waits, for a bounded time, until no statement of this database is
// waiting for a lock.
func (p *plane) settled(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := p.raw.QueryRow(
			"select count(*) from pg_stat_activity where datname = current_database() and wait_event_type = 'Lock'").Scan(&waiting); err != nil {
			t.Fatalf("failed to read pg_stat_activity: %v", err)
		}
		if waiting == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d statements still wait for a lock", waiting)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// same compares two connections field by field, times as instants: one read
// from the database carries its session's time zone, one built here UTC.
func same(a, b *types.Connection) bool {
	instants := func(x, y *time.Time) bool {
		if x == nil || y == nil {
			return x == y
		}
		return x.Equal(*y)
	}
	return a.ID == b.ID && a.OwnerTenantID == b.OwnerTenantID && a.Label == b.Label && a.Issuer == b.Issuer &&
		a.ClientID == b.ClientID && string(a.ClientSecret) == string(b.ClientSecret) &&
		a.CreatedBy == b.CreatedBy && a.CreatedAt.Equal(b.CreatedAt) && a.UpdatedAt.Equal(b.UpdatedAt) &&
		instants(a.TestedAt, b.TestedAt)
}

func first(connections []*types.Connection) *types.Connection {
	if len(connections) == 0 {
		return nil
	}
	return connections[0]
}

func TestIntegration_Storage_Connections(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	t.Parallel()

	p := newStore(t)
	store, ctx := p.store, context.Background()

	c := newConnection(tenantA)
	created, err := store.CreateConnection(ctx, c, noLimit)
	if err != nil {
		t.Fatal(err)
	}
	// The times are the database's.
	if created.CreatedAt.IsZero() || !created.UpdatedAt.Equal(created.CreatedAt) || time.Since(created.CreatedAt).Abs() > time.Minute {
		t.Fatalf("expected created_at and updated_at set to now, got %v and %v", created.CreatedAt, created.UpdatedAt)
	}
	c.CreatedAt, c.UpdatedAt = created.CreatedAt, created.UpdatedAt
	if !same(created, c) {
		t.Fatalf("created\n%+v\nasked for\n%+v", created, c)
	}
	var pgErr *pgconn.PgError
	if _, err := store.CreateConnection(ctx, c, noLimit); !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("an id is unique: %v", err)
	}
	got, err := store.GetConnection(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !same(got, c) {
		t.Fatalf("read back\n%+v\nstored\n%+v", got, c)
	}
	if _, err := store.GetConnection(ctx, uuid.NewString()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}

	label := "New"
	if _, err := store.UpdateConnection(ctx, uuid.NewString(), ConnectionUpdate{Label: &label}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	updated, err := store.UpdateConnection(ctx, c.ID, ConnectionUpdate{Label: &label})
	if err != nil || updated.Label != "New" || updated.Tested() || !updated.UpdatedAt.After(c.UpdatedAt) ||
		!updated.CreatedAt.Equal(c.CreatedAt) || string(updated.ClientSecret) != string(c.ClientSecret) {
		t.Fatalf("%+v %v", updated, err)
	}
	// What an update does not name stays.
	secret, err := store.UpdateConnection(ctx, c.ID, ConnectionUpdate{ClientSecret: []byte{9, 9}})
	if err != nil || string(secret.ClientSecret) != string([]byte{9, 9}) || secret.Label != "New" {
		t.Fatalf("%+v %v", secret, err)
	}

	// Paging by id.
	p.insert(t, 4)
	firstPage, _ := store.ListConnections(ctx, tenantA, "", 3)
	rest, _ := store.ListConnections(ctx, tenantA, firstPage[2].ID, 3)
	if len(firstPage) != 3 || len(rest) != 2 || firstPage[0].ID >= firstPage[1].ID || firstPage[2].ID >= rest[0].ID {
		t.Fatalf("pages %d %d", len(firstPage), len(rest))
	}

	// Every owner's connections, or one's.
	other := newConnection(tenantB)
	if _, err := store.CreateConnection(ctx, other, noLimit); err != nil {
		t.Fatal(err)
	}
	if all, _ := store.ListConnections(ctx, "", "", 100); len(all) != 6 {
		t.Fatalf("all %d", len(all))
	}
	if theirs, _ := store.ListConnections(ctx, tenantB, "", 100); len(theirs) != 1 {
		t.Fatalf("tenant B's %d", len(theirs))
	}

	// Those of a set that exist, each once, whatever was asked for.
	some, err := store.GetConnections(ctx, []string{other.ID, uuid.NewString(), c.ID, c.ID})
	if err != nil || len(some) != 2 {
		t.Fatalf("%v %v", some, err)
	}
	if one, err := store.GetConnections(ctx, []string{strings.ToUpper(c.ID)}); err != nil || len(one) != 1 || one[0].ID != c.ID {
		t.Fatalf("an id in upper case: %v %v", one, err)
	}
	if none, err := store.GetConnections(ctx, nil); err != nil || len(none) != 0 {
		t.Fatalf("%v %v", none, err)
	}

	if err := store.DeleteConnection(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteConnection(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteConnection(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a second delete: %v", err)
	}
}

// created_by is the admin's account, and none for a client acting for
// itself: "" in a Connection, NULL in the row, both ways.
func TestIntegration_Storage_CreatedBy(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	t.Parallel()

	p := newStore(t)
	store, ctx := p.store, context.Background()
	isNull := func(id string) (null bool) {
		if err := p.raw.QueryRow("select created_by is null from sso_connections where id = $1", id).Scan(&null); err != nil {
			t.Fatalf("failed to read created_by: %v", err)
		}
		return null
	}

	byClient := newConnection(tenantA)
	byClient.CreatedBy = ""
	byAdmin := newConnection(tenantA)
	for _, c := range []*types.Connection{byClient, byAdmin} {
		created, err := store.CreateConnection(ctx, c, noLimit)
		if err != nil {
			t.Fatal(err)
		}
		if created.CreatedBy != c.CreatedBy {
			t.Fatalf("created by %q, got %q", c.CreatedBy, created.CreatedBy)
		}
	}
	if !isNull(byClient.ID) || isNull(byAdmin.ID) {
		t.Fatal(`"" is stored as NULL, an account as itself`)
	}

	// NULL reads as "" by every way a connection is read.
	label := "renamed"
	read := map[string]func() (*types.Connection, error){
		"GetConnection": func() (*types.Connection, error) { return store.GetConnection(ctx, byClient.ID) },
		"UpdateConnection": func() (*types.Connection, error) {
			return store.UpdateConnection(ctx, byClient.ID, ConnectionUpdate{Label: &label})
		},
		"GetConnections": func() (*types.Connection, error) {
			found, err := store.GetConnections(ctx, []string{byClient.ID})
			return first(found), err
		},
		"ListConnections": func() (*types.Connection, error) {
			found, err := store.ListConnections(ctx, tenantA, "", 1)
			return first(found), err
		},
		"LockConnections": func() (*types.Connection, error) {
			found, err := p.lock(byClient.ID)
			return first(found), err
		},
	}
	for name, how := range read {
		got, err := how()
		if err != nil || got == nil || got.ID != byClient.ID || got.CreatedBy != "" {
			t.Errorf("%s: %+v %v", name, got, err)
		}
	}
	if got := p.get(t, byAdmin.ID); got.CreatedBy != byAdmin.CreatedBy {
		t.Fatalf("an account reads back as itself: %q", got.CreatedBy)
	}
	// An update leaves it as it is.
	if !isNull(byClient.ID) {
		t.Fatal("an update must not write created_by")
	}
}

// A tenant's connections stop at the limit CreateConnection is given, in the
// one statement that inserts.
func TestIntegration_Storage_CreateConnection_Limit(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	t.Parallel()

	p := newStore(t)
	store, ctx := p.store, context.Background()
	const limit = 3

	for i := range limit {
		if _, err := store.CreateConnection(ctx, newConnection(tenantA), limit); err != nil {
			t.Fatalf("connection %d of %d: %v", i+1, limit, err)
		}
	}
	refused := newConnection(tenantA)
	if created, err := store.CreateConnection(ctx, refused, limit); !errors.Is(err, ErrConnectionLimit) || created != nil {
		t.Fatalf("expected ErrConnectionLimit, got %+v %v", created, err)
	}
	if _, err := store.GetConnection(ctx, refused.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a refused connection is not stored: %v", err)
	}
	stored, _ := store.ListConnections(ctx, tenantA, "", 100)
	if len(stored) != limit {
		t.Fatalf("%d rows stored with a limit of %d", len(stored), limit)
	}

	// The limit is each tenant's own.
	if _, err := store.CreateConnection(ctx, newConnection(tenantB), limit); err != nil {
		t.Fatalf("another tenant: %v", err)
	}
	// One deleted makes room for one.
	if err := store.DeleteConnection(ctx, stored[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateConnection(ctx, newConnection(tenantA), limit); err != nil {
		t.Fatalf("after a delete: %v", err)
	}
	if _, err := store.CreateConnection(ctx, newConnection(tenantA), limit); !errors.Is(err, ErrConnectionLimit) {
		t.Fatalf("expected ErrConnectionLimit, got %v", err)
	}
}

// A policy write and a delete each take the connection's row for themselves,
// and nobody waits for it longer than lock_timeout.
func TestIntegration_Storage_LockConnections(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	t.Parallel()

	p := newStore(t)
	store := p.store
	connections := p.insert(t, 2)
	a, b := connections[0], connections[1]
	impatient := map[string]time.Duration{lockTimeout: short}

	t.Run("id order", func(t *testing.T) {
		locked, err := p.lock(b.ID, uuid.NewString(), a.ID, a.ID)
		if err != nil || len(locked) != 2 || locked[0].ID != a.ID || locked[1].ID != b.ID {
			t.Fatalf("expected both rows once, in id order, and the missing id absent: %v %v", locked, err)
		}
		if none, err := p.lock(); err != nil || len(none) != 0 {
			t.Fatalf("no ids: %v %v", none, err)
		}
	})

	t.Run("held", func(t *testing.T) {
		release := p.hold(t, func(ctx context.Context) error {
			_, err := store.LockConnections(ctx, []string{a.ID})
			return err
		})

		started := time.Now()
		_, err := p.lockWithin(short, a.ID)
		refusedAfter(t, "LockConnections on a held row", err, ErrBusy, started, short)

		started = time.Now()
		_, err = p.lockWithin(short, b.ID, a.ID)
		refusedAfter(t, "LockConnections on a set with a held row", err, ErrBusy, started, short)

		label := "changed while held"
		started = time.Now()
		err = p.with(impatient, func(ctx context.Context) error {
			_, err := store.UpdateConnection(ctx, a.ID, ConnectionUpdate{Label: &label})
			return err
		})
		refusedAfter(t, "UpdateConnection on a held row", err, ErrBusy, started, short)

		started = time.Now()
		err = p.with(impatient, func(ctx context.Context) error { return store.SetTested(ctx, a.ID) })
		refusedAfter(t, "SetTested on a held row", err, ErrBusy, started, short)

		started = time.Now()
		err = p.with(impatient, func(ctx context.Context) error { return store.DeleteConnection(ctx, a.ID) })
		refusedAfter(t, "DeleteConnection on a held row", err, ErrBusy, started, short)

		// Reading is never stopped by a lock.
		if got, err := store.GetConnection(context.Background(), a.ID); err != nil || got.Label != "L" || got.Tested() {
			t.Fatalf("a read of a held row: %+v %v", got, err)
		}
		// The set that was refused left nothing locked behind.
		if locked, err := p.lock(b.ID); err != nil || len(locked) != 1 {
			t.Fatalf("the other row of a refused set: %v %v", locked, err)
		}

		release()
		if locked, err := p.lock(a.ID); err != nil || len(locked) != 1 {
			t.Fatalf("free once it let go: %v %v", locked, err)
		}
	})

	// As deployed: the connection's own lock_timeout ends the wait.
	t.Run("connection lock_timeout", func(t *testing.T) {
		release := p.hold(t, func(ctx context.Context) error {
			_, err := store.LockConnections(ctx, []string{b.ID})
			return err
		})
		defer release()

		started := time.Now()
		_, err := p.lock(b.ID)
		if took := time.Since(started); !errors.Is(err, ErrBusy) || took < time.Second || took > 2*time.Second+quick {
			t.Fatalf("expected ErrBusy after about 2s, got %v after %s", err, took)
		}
	})
}

// A statement Postgres cancels for running longer than statement_timeout is
// ErrTimeout, as is one cut by its context's deadline.
func TestIntegration_Storage_StatementTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	t.Parallel()

	p := newStore(t)
	store, ctx := p.store, context.Background()
	a := p.insert(t, 1)[0]

	t.Run("statement runs too long", func(t *testing.T) {
		started := time.Now()
		var statementErr error
		err := p.with(map[string]time.Duration{statementTimeout: short}, func(ctx context.Context) error {
			_, statementErr = store.db.Statement(ctx).Select("pg_sleep(30)").ExecContext(ctx)
			return statementErr
		})
		var pgErr *pgconn.PgError
		if !errors.As(statementErr, &pgErr) || pgErr.Code != pgErrCodeQueryCanceled {
			t.Fatalf("expected Postgres to cancel the statement (57014), got %v", statementErr)
		}
		refusedAfter(t, "pg_sleep under statement_timeout", err, ErrTimeout, started, short)
		if errors.Is(err, ErrBusy) {
			t.Fatal("a statement timeout is not a busy lock")
		}
	})

	// With no lock_timeout, a statement that waits for a lock runs into
	// statement_timeout instead.
	t.Run("store method waits too long", func(t *testing.T) {
		release := p.hold(t, func(ctx context.Context) error {
			_, err := store.LockConnections(ctx, []string{a.ID})
			return err
		})
		defer release()
		settings := map[string]time.Duration{lockTimeout: 0, statementTimeout: short}

		label := "late"
		started := time.Now()
		err := p.with(settings, func(ctx context.Context) error {
			_, err := store.UpdateConnection(ctx, a.ID, ConnectionUpdate{Label: &label})
			return err
		})
		refusedAfter(t, "UpdateConnection under statement_timeout", err, ErrTimeout, started, short)

		started = time.Now()
		err = p.with(settings, func(ctx context.Context) error { return store.DeleteConnection(ctx, a.ID) })
		refusedAfter(t, "DeleteConnection under statement_timeout", err, ErrTimeout, started, short)
	})

	t.Run("context deadline", func(t *testing.T) {
		p := newStore(t)
		a := p.insert(t, 1)[0]
		release := p.hold(t, func(ctx context.Context) error {
			_, err := p.store.LockConnections(ctx, []string{a.ID})
			return err
		})
		defer release()

		// The caller's deadline is sooner than any timeout of Postgres's.
		impatient, cancel := context.WithTimeout(ctx, short)
		defer cancel()
		label := "late"
		started := time.Now()
		_, err := p.store.UpdateConnection(impatient, a.ID, ConnectionUpdate{Label: &label})
		refusedAfter(t, "UpdateConnection past its context's deadline", err, ErrTimeout, started, short)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("the deadline stays readable in the error: %v", err)
		}

		// A caller that goes away is not a timeout.
		gone, cancel := context.WithCancel(ctx)
		time.AfterFunc(100*time.Millisecond, cancel)
		_, err = p.store.UpdateConnection(gone, a.ID, ConnectionUpdate{Label: &label})
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrTimeout) || errors.Is(err, ErrBusy) {
			t.Fatalf("a cancelled caller: %v", err)
		}

		// The statements the callers gave up on are cancelled in Postgres
		// too, a moment after the caller has its error: once none waits for
		// the lock any more, letting go of the lock applies neither.
		p.settled(t)
		release()
		if got := p.get(t, a.ID); got.Label != "L" {
			t.Fatalf("a statement that was cut was applied later: %q", got.Label)
		}
	})
}

// SetTested marks a connection tested once: the first time is the one kept.
func TestIntegration_Storage_SetTested(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	t.Parallel()

	p := newStore(t)
	store, ctx := p.store, context.Background()

	t.Run("draft", func(t *testing.T) {
		connections := p.insert(t, 2)
		c, untouched := connections[0], connections[1]

		if err := store.SetTested(ctx, c.ID); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got := p.get(t, c.ID)
		if !got.Tested() || !got.TestedAt.Equal(got.UpdatedAt) || !got.TestedAt.After(c.UpdatedAt) {
			t.Fatalf("expected tested_at and updated_at set together to now, got %v and %v", got.TestedAt, got.UpdatedAt)
		}
		// Nothing else of the row changed, and no other row.
		c.TestedAt, c.UpdatedAt = got.TestedAt, got.UpdatedAt
		if !same(got, c) {
			t.Fatalf("read back\n%+v\nexpected\n%+v", got, c)
		}
		if other := p.get(t, untouched.ID); !same(other, untouched) {
			t.Fatalf("another connection changed: %+v", other)
		}
	})

	t.Run("already tested", func(t *testing.T) {
		c := p.insert(t, 1)[0]
		if err := store.SetTested(ctx, c.ID); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		first := p.get(t, c.ID)
		label := "renamed since"
		renamed, err := store.UpdateConnection(ctx, c.ID, ConnectionUpdate{Label: &label})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if err := store.SetTested(ctx, c.ID); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got := p.get(t, c.ID)
		if !got.TestedAt.Equal(*first.TestedAt) || !got.UpdatedAt.Equal(renamed.UpdatedAt) {
			t.Fatalf("expected tested_at kept at %v and updated_at as the update left it, got %v and %v", first.TestedAt, got.TestedAt, got.UpdatedAt)
		}
	})

	t.Run("no such connection", func(t *testing.T) {
		if err := store.SetTested(ctx, uuid.NewString()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("concurrent", func(t *testing.T) {
		c := p.insert(t, 1)[0]
		var wg sync.WaitGroup
		results := make(chan error, 6)
		for range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results <- store.SetTested(ctx, c.ID)
			}()
		}
		wg.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		}
		// One of them set both times; the others changed nothing.
		got := p.get(t, c.ID)
		if !got.Tested() || !got.TestedAt.Equal(got.UpdatedAt) {
			t.Fatalf("expected tested_at and updated_at set together by one call, got %v and %v", got.TestedAt, got.UpdatedAt)
		}
	})

	t.Run("in a transaction rolled back", func(t *testing.T) {
		c := p.insert(t, 1)[0]
		failure := errors.New("failure")
		err := store.WithTx(ctx, func(ctx context.Context) error {
			if err := store.SetTested(ctx, c.ID); err != nil {
				return err
			}
			return failure
		})
		if err != failure {
			t.Fatalf("expected the function's error, got %v", err)
		}
		if got := p.get(t, c.ID); got.Tested() {
			t.Fatalf("expected a draft still, tested at %v", got.TestedAt)
		}
	})
}

// The table refuses rows the store never writes.
func TestIntegration_Storage_Constraints(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	t.Parallel()

	p := newStore(t)
	// insert writes a row as the store writes it, with changes to columns.
	insert := func(changes map[string]any) error {
		now := time.Now().UTC()
		row := map[string]any{
			"id": uuid.NewString(), "owner_tenant_id": tenantA, "label": "L", "issuer": "https://idp.example",
			"client_id": "c", "client_secret": []byte{1}, "created_by": nil,
			"created_at": now, "updated_at": now, "tested_at": nil,
		}
		for column, value := range changes {
			row[column] = value
		}
		placeholders, values := make([]string, len(connectionColumns)), make([]any, len(connectionColumns))
		for i, column := range connectionColumns {
			placeholders[i], values[i] = fmt.Sprintf("$%d", i+1), row[column]
		}
		_, err := p.raw.Exec("insert into sso_connections ("+strings.Join(connectionColumns, ", ")+") values ("+strings.Join(placeholders, ", ")+")", values...)
		return err
	}
	id := uuid.NewString()

	accepted := map[string]map[string]any{
		"draft":                 {"id": id},
		"tested":                {"tested_at": time.Now().UTC()},
		"created by an account": {"created_by": uuid.NewString()},
	}
	for name, changes := range accepted {
		t.Run(name, func(t *testing.T) {
			if err := insert(changes); err != nil {
				t.Fatalf("a row the store writes was refused: %v", err)
			}
		})
	}

	const uniqueViolation, notNullViolation, invalidText = "23505", "23502", "22P02"
	refused := map[string]struct {
		changes map[string]any
		code    string
	}{
		"duplicate id":         {map[string]any{"id": id}, uniqueViolation},
		"no owner":             {map[string]any{"owner_tenant_id": nil}, notNullViolation},
		"no label":             {map[string]any{"label": nil}, notNullViolation},
		"no issuer":            {map[string]any{"issuer": nil}, notNullViolation},
		"no client id":         {map[string]any{"client_id": nil}, notNullViolation},
		"no secret":            {map[string]any{"client_secret": nil}, notNullViolation},
		"no created_at":        {map[string]any{"created_at": nil}, notNullViolation},
		"no updated_at":        {map[string]any{"updated_at": nil}, notNullViolation},
		"owner not a uuid":     {map[string]any{"owner_tenant_id": "acme"}, invalidText},
		"created_by not a uid": {map[string]any{"created_by": "sso-service"}, invalidText},
	}
	for name, tc := range refused {
		t.Run(name, func(t *testing.T) {
			err := insert(tc.changes)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != tc.code {
				t.Fatalf("expected SQLSTATE %s, got %v", tc.code, err)
			}
		})
	}
}
