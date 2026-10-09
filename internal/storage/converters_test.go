// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package storage

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

// fakeRow is a row of sso_connections: its values in the order of
// connectionColumns, or the error scanning it fails with.
type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("wrong number of columns")
	}
	for i, d := range dest {
		switch d := d.(type) {
		case *string:
			*d = r.values[i].(string)
		case *[]byte:
			*d = r.values[i].([]byte)
		case *time.Time:
			*d = r.values[i].(time.Time)
		case *sql.NullString:
			if err := d.Scan(r.values[i]); err != nil {
				return err
			}
		case *sql.NullTime:
			if err := d.Scan(r.values[i]); err != nil {
				return err
			}
		default:
			return errors.New("unexpected destination")
		}
	}
	return nil
}

func TestScanConnection(t *testing.T) {
	created := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	tested := created.Add(time.Hour)
	row := func(createdBy, testedAt any) fakeRow {
		return fakeRow{values: []any{"id", "owner", "label", "https://idp.example", "client", []byte{1, 2}, createdBy, created, created, testedAt}}
	}
	scanErr := errors.New("scan failed")

	t.Run("columns match", func(t *testing.T) {
		if len(row(nil, nil).values) != len(connectionColumns) {
			t.Fatalf("expected a row of %d columns, got %d", len(connectionColumns), len(row(nil, nil).values))
		}
	})

	t.Run("tested", func(t *testing.T) {
		c, err := scanConnection(row("account", tested))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c.ID != "id" || c.OwnerTenantID != "owner" || c.Label != "label" || c.Issuer != "https://idp.example" || c.ClientID != "client" ||
			string(c.ClientSecret) != string([]byte{1, 2}) || !c.CreatedAt.Equal(created) || !c.UpdatedAt.Equal(created) {
			t.Errorf("unexpected connection %+v", c)
		}
		if c.CreatedBy != "account" || !c.Tested() || !c.TestedAt.Equal(tested) {
			t.Errorf("expected created_by and tested_at read, got %q %v", c.CreatedBy, c.TestedAt)
		}
	})

	t.Run("nulls", func(t *testing.T) {
		c, err := scanConnection(row(nil, nil))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c.CreatedBy != "" || c.TestedAt != nil {
			t.Errorf("expected no creator and no test time, got %q %v", c.CreatedBy, c.TestedAt)
		}
	})

	t.Run("no rows", func(t *testing.T) {
		if _, err := scanConnection(fakeRow{err: sql.ErrNoRows}); !errors.Is(err, ErrNotFound) {
			t.Errorf("expected %v, got %v", ErrNotFound, err)
		}
	})

	t.Run("scan error", func(t *testing.T) {
		if _, err := scanConnection(fakeRow{err: scanErr}); !errors.Is(err, scanErr) || errors.Is(err, ErrNotFound) {
			t.Errorf("expected %v, got %v", scanErr, err)
		}
	})
}

func TestNullString(t *testing.T) {
	if got := nullString(""); got.Valid {
		t.Errorf("expected NULL for the empty string, got %+v", got)
	}
	if got := nullString("x"); !got.Valid || got.String != "x" {
		t.Errorf("expected the string itself, got %+v", got)
	}
}
