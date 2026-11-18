// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// Sentinel errors for storage operations.
var (
	ErrNotFound        = errors.New("resource not found")
	ErrConnectionLimit = errors.New("connection limit reached")
	// ErrBusy is a lock another transaction held for longer than
	// lock_timeout: the caller may try again.
	ErrBusy = errors.New("resource is locked by another request")
	// ErrTimeout is a statement or transaction that ran out of time. A
	// transaction was rolled back; a single statement cut by its context may
	// still have been applied.
	ErrTimeout = errors.New("storage timeout")
)

// PostgreSQL error codes
const (
	pgErrCodeLockNotAvailable = "55P03"
	pgErrCodeQueryCanceled    = "57014"
)

func wrapTimeout(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case pgErrCodeLockNotAvailable:
			return fmt.Errorf("%w: %s", ErrBusy, pgErr.Message)
		case pgErrCodeQueryCanceled:
			return fmt.Errorf("%w: %s", ErrTimeout, pgErr.Message)
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", ErrTimeout, err)
	}

	return err
}
