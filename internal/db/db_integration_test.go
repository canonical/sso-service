// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/monitoring"
	"github.com/canonical/sso-service/internal/testhelpers"
	"github.com/canonical/sso-service/internal/tracing"
)

// shared is the one Postgres container of this package's integration tests;
// every test takes a database of its own in it.
var shared testhelpers.SharedContainers

func TestMain(m *testing.M) {
	code := m.Run()
	shared.Close()
	os.Exit(code)
}

// newClient is a DBClient on a fresh database, with a table to write to.
func newClient(t *testing.T, cfg Config) *DBClient {
	t.Helper()

	cfg.DSN = shared.Postgres.IsolatedDB(t)
	if cfg.MaxConns == 0 {
		cfg.MaxConns = 5
	}
	cfg.MinConns, cfg.MaxConnLifetime, cfg.MaxConnIdleTime = 1, time.Hour, 30*time.Minute

	logger := logging.NewNoopLogger()
	client, err := NewDBClient(context.Background(), cfg, tracing.NewNoopTracer(), monitoring.NewNoopMonitor("sso-service-test", logger), logger)
	if err != nil {
		t.Fatalf("failed to create the DB client: %v", err)
	}
	t.Cleanup(client.Close)

	if _, err := client.db.Exec("create table notes (id int primary key, body text not null)"); err != nil {
		t.Fatalf("failed to create the test table: %v", err)
	}

	return client
}

// insert writes one row through the client, in the context's transaction if
// it carries one.
func insert(ctx context.Context, client *DBClient, id int) error {
	_, err := client.Statement(ctx).Insert("notes").Columns("id", "body").Values(id, "note").ExecContext(ctx)
	return err
}

// ids reads the committed rows, on the pool.
func ids(t *testing.T, client *DBClient) []int {
	t.Helper()
	rows, err := client.Statement(context.Background()).Select("id").From("notes").OrderBy("id").QueryContext(context.Background())
	if err != nil {
		t.Fatalf("failed to read the rows: %v", err)
	}
	defer rows.Close()
	out := []int{}
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// show reads a session setting in the context's transaction, or on a pooled
// connection.
func show(ctx context.Context, client *DBClient, setting string) (string, error) {
	var value string
	tx := txFromContext(ctx)
	if tx == nil {
		return value, client.db.QueryRowContext(ctx, "show "+setting).Scan(&value)
	}

	rows, err := tx.Query("show " + setting)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", errors.New("show returned no row")
	}

	return value, rows.Scan(&value)
}

// expectSettings checks session settings where ctx runs. It only reports, so
// it may be called from any goroutine.
func expectSettings(t *testing.T, ctx context.Context, client *DBClient, want map[string]string) {
	t.Helper()
	for setting, value := range want {
		if got, err := show(ctx, client, setting); err != nil || got != value {
			t.Errorf("%s: got %q (%v), want %q", setting, got, err, value)
		}
	}
}

// Every connection of the pool has the timeouts as session settings: no
// statement waits on a lock or runs without a bound, and Postgres ends a
// transaction that sits idle.
func TestIntegration_NewDBClient_SessionTimeouts(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	t.Parallel()

	want := map[string]string{
		"lock_timeout":                        "2s",
		"statement_timeout":                   "5s",
		"idle_in_transaction_session_timeout": "15s",
	}
	client := newClient(t, Config{MaxConns: 3})

	t.Run("pooled connection", func(t *testing.T) {
		expectSettings(t, context.Background(), client, want)
	})

	// Three transactions at once hold the pool's three connections.
	t.Run("every connection", func(t *testing.T) {
		var (
			mu       sync.Mutex
			backends = map[int]bool{}
			held     sync.WaitGroup
			done     sync.WaitGroup
		)
		release := make(chan struct{})
		held.Add(3)
		for range 3 {
			done.Add(1)
			go func() {
				defer done.Done()
				err := client.WithTx(context.Background(), func(ctx context.Context) error {
					var pid int
					if err := client.Statement(ctx).Select("pg_backend_pid()").QueryRowContext(ctx).Scan(&pid); err != nil {
						held.Done()
						return err
					}
					expectSettings(t, ctx, client, want)
					mu.Lock()
					backends[pid] = true
					mu.Unlock()
					held.Done()
					<-release

					return nil
				})
				if err != nil {
					t.Errorf("transaction failed: %v", err)
				}
			}()
		}
		held.Wait()
		close(release)
		done.Wait()
		if len(backends) != 3 {
			t.Fatalf("expected three connections, saw %d", len(backends))
		}
	})
}

func TestIntegration_DBClient_WithTx(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	t.Parallel()

	// One connection: a transaction that is not ended would leave the next
	// test case waiting for it, and fail on its deadline.
	client := newClient(t, Config{MaxConns: 1})
	ctx := context.Background()
	failure := errors.New("the function's own error")

	t.Run("commit", func(t *testing.T) {
		err := client.WithTx(ctx, func(ctx context.Context) error {
			if err := insert(ctx, client, 1); err != nil {
				return err
			}
			return insert(ctx, client, 2)
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := ids(t, client); !equal(got, []int{1, 2}) {
			t.Fatalf("expected both rows committed, got %v", got)
		}
	})

	t.Run("rollback on error", func(t *testing.T) {
		err := client.WithTx(ctx, func(ctx context.Context) error {
			if err := insert(ctx, client, 3); err != nil {
				return err
			}
			return failure
		})
		if err != failure {
			t.Fatalf("expected the function's error, got %v", err)
		}
		if got := ids(t, client); !equal(got, []int{1, 2}) {
			t.Fatalf("expected the row rolled back, got %v", got)
		}
	})

	t.Run("rollback before any statement", func(t *testing.T) {
		if err := client.WithTx(ctx, func(context.Context) error { return failure }); err != failure {
			t.Fatalf("expected the function's error, got %v", err)
		}
		// The transaction is over and its connection back in the pool: the
		// next one starts.
		if err := client.WithTx(ctx, func(ctx context.Context) error { return insert(ctx, client, 4) }); err != nil {
			t.Fatalf("the connection was not released: %v", err)
		}
		if got := ids(t, client); !equal(got, []int{1, 2, 4}) {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("rollback on a failed statement", func(t *testing.T) {
		err := client.WithTx(ctx, func(ctx context.Context) error {
			if err := insert(ctx, client, 5); err != nil {
				return err
			}
			return insert(ctx, client, 1) // a duplicate key
		})
		if err == nil {
			t.Fatal("expected the duplicate key to fail")
		}
		if got := ids(t, client); !equal(got, []int{1, 2, 4}) {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("rollback on panic", func(t *testing.T) {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("expected the panic to reach the caller")
				}
			}()
			_ = client.WithTx(ctx, func(ctx context.Context) error {
				if err := insert(ctx, client, 6); err != nil {
					t.Error(err)
				}
				panic("boom")
			})
		}()
		if got := ids(t, client); !equal(got, []int{1, 2, 4}) {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("one transaction under one deadline", func(t *testing.T) {
		started := time.Now()
		err := client.WithTx(ctx, func(ctx context.Context) error {
			expectSettings(t, ctx, client, map[string]string{"transaction_isolation": "read committed"})
			deadline, ok := ctx.Deadline()
			if !ok || deadline.After(started.Add(txTimeout+time.Second)) || deadline.Before(started.Add(txTimeout-time.Second)) {
				t.Errorf("expected the transaction's deadline (%s) on the function's context, got %v %v", txTimeout, deadline.Sub(started), ok)
			}
			var first, second int64
			for _, id := range []*int64{&first, &second} {
				if err := client.Statement(ctx).Select("txid_current()").QueryRowContext(ctx).Scan(id); err != nil {
					return err
				}
			}
			if first != second {
				t.Errorf("two statements ran in two transactions: %d, %d", first, second)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("begin fails", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		ran := false
		err := client.WithTx(cancelled, func(context.Context) error {
			ran = true
			return nil
		})
		if err == nil || !errors.Is(err, context.Canceled) || ran {
			t.Fatalf("expected the begin to fail, got %v (ran: %v)", err, ran)
		}
	})
}

// What a transaction wrote is seen by nobody else until it commits: a
// statement without the function's context runs on the pool, outside it.
func TestIntegration_DBClient_WithTx_Isolation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	t.Parallel()

	client := newClient(t, Config{MaxConns: 2})
	err := client.WithTx(context.Background(), func(ctx context.Context) error {
		if err := insert(ctx, client, 1); err != nil {
			return err
		}
		if got := ids(t, client); len(got) != 0 {
			t.Errorf("an uncommitted row is visible outside its transaction: %v", got)
		}
		var inside int
		if err := client.Statement(ctx).Select("count(*)").From("notes").QueryRowContext(ctx).Scan(&inside); err != nil || inside != 1 {
			t.Errorf("the transaction sees its own row: %d %v", inside, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := ids(t, client); !equal(got, []int{1}) {
		t.Fatalf("got %v", got)
	}
}

// A WithTx inside a WithTx is the same transaction: it neither commits nor
// rolls back on its own, and has no deadline of its own.
func TestIntegration_DBClient_WithTx_Nested(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	t.Parallel()

	// One connection: an inner transaction of its own could not even start.
	client := newClient(t, Config{MaxConns: 1})
	ctx := context.Background()
	failure := errors.New("the outer function's error")
	txid := func(ctx context.Context) (id int64) {
		if err := client.Statement(ctx).Select("txid_current()").QueryRowContext(ctx).Scan(&id); err != nil {
			t.Errorf("txid_current: %v", err)
		}
		return id
	}

	t.Run("one transaction", func(t *testing.T) {
		err := client.WithTx(ctx, func(outer context.Context) error {
			outerID := txid(outer)
			outerDeadline, _ := outer.Deadline()
			return client.WithTx(outer, func(inner context.Context) error {
				if innerID := txid(inner); innerID != outerID {
					t.Errorf("the inner WithTx ran in transaction %d, the outer in %d", innerID, outerID)
				}
				if innerDeadline, _ := inner.Deadline(); !innerDeadline.Equal(outerDeadline) {
					t.Errorf("the inner WithTx has a deadline of its own: %v, outer %v", innerDeadline, outerDeadline)
				}
				return insert(inner, client, 1)
			})
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := ids(t, client); !equal(got, []int{1}) {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("inner does not commit", func(t *testing.T) {
		err := client.WithTx(ctx, func(outer context.Context) error {
			if err := client.WithTx(outer, func(inner context.Context) error { return insert(inner, client, 2) }); err != nil {
				return err
			}
			// Still the same, open transaction after the inner returned.
			if err := insert(outer, client, 3); err != nil {
				return err
			}
			return failure
		})
		if err != failure {
			t.Fatalf("expected the outer function's error, got %v", err)
		}
		if got := ids(t, client); !equal(got, []int{1}) {
			t.Fatalf("expected the inner WithTx's row rolled back with the outer, got %v", got)
		}
	})

	t.Run("inner does not roll back", func(t *testing.T) {
		err := client.WithTx(ctx, func(outer context.Context) error {
			if err := insert(outer, client, 4); err != nil {
				return err
			}
			if err := client.WithTx(outer, func(context.Context) error { return failure }); err != failure {
				t.Errorf("expected the inner function's error, got %v", err)
			}
			// The outer goes on and commits.
			return insert(outer, client, 5)
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := ids(t, client); !equal(got, []int{1, 4, 5}) {
			t.Fatalf("got %v", got)
		}
	})
}

// A transaction whose function outlives the transaction timeout does not
// commit, whatever the function returns, and gives its connection back.
func TestIntegration_DBClient_WithTx_Deadline(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	t.Parallel()

	const timeout = 300 * time.Millisecond
	client := newClient(t, Config{MaxConns: 1})
	client.txTimeout = timeout
	ctx := context.Background()

	// outlived runs a transaction that writes a row and is still going when
	// its deadline passes, ending as end says; it returns WithTx's error once
	// the row is seen rolled back and the connection released.
	outlived := func(t *testing.T, id int, end func(ctx context.Context) error) error {
		t.Helper()
		started := time.Now()
		err := client.WithTx(ctx, func(ctx context.Context) error {
			if err := insert(ctx, client, id); err != nil {
				return err
			}
			<-ctx.Done() // a remote call that takes too long
			return end(ctx)
		})
		if took := time.Since(started); took < timeout || took > 10*time.Second {
			t.Fatalf("returned after %s with a transaction timeout of %s", took, timeout)
		}
		if err == nil {
			t.Fatal("a transaction past its deadline must not commit")
		}
		// The pool has one connection: the rows are read only once the
		// timed-out transaction has let go of it.
		if got := ids(t, client); len(got) != 0 {
			t.Fatalf("expected the row rolled back, got %v", got)
		}
		return err
	}

	t.Run("returns the context error", func(t *testing.T) {
		err := outlived(t, 1, func(ctx context.Context) error { return ctx.Err() })
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected the deadline, got %v", err)
		}
	})
	t.Run("goes on to a statement", func(t *testing.T) {
		err := outlived(t, 2, func(ctx context.Context) error { return insert(ctx, client, 3) })
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected the deadline, got %v", err)
		}
	})
	// A function that returns nil after the deadline is not committed either:
	// that is the deadline too, not database/sql's "transaction has already
	// been committed or rolled back".
	t.Run("returns nil", func(t *testing.T) {
		err := outlived(t, 4, func(context.Context) error { return nil })
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected the deadline, got %v", err)
		}
	})
}

// With tracing on, the pool is traced and its statistics recorded; it works
// as without.
func TestIntegration_NewDBClient_TracingEnabled(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	t.Parallel()

	client := newClient(t, Config{TracingEnabled: true})
	if err := client.WithTx(context.Background(), func(ctx context.Context) error { return insert(ctx, client, 1) }); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := ids(t, client); !equal(got, []int{1}) {
		t.Fatalf("got %v", got)
	}
}
