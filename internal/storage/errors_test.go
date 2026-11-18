// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package storage

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestWrapTimeout(t *testing.T) {
	other := errors.New("something else")
	uniqueViolation := &pgconn.PgError{Code: "23505"}

	testCases := []struct {
		name        string
		err         error
		expectedErr error
	}{
		{
			name:        "lock timeout",
			err:         &pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout"},
			expectedErr: ErrBusy,
		},
		{
			name:        "lock timeout, wrapped",
			err:         fmt.Errorf("failed to scan connection: %w", &pgconn.PgError{Code: "55P03"}),
			expectedErr: ErrBusy,
		},
		{
			name:        "statement timeout",
			err:         &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"},
			expectedErr: ErrTimeout,
		},
		{name: "context deadline", err: context.DeadlineExceeded, expectedErr: ErrTimeout},
		{
			name:        "context deadline, wrapped",
			err:         fmt.Errorf("failed to begin transaction: %w", context.DeadlineExceeded),
			expectedErr: ErrTimeout,
		},
		{name: "cancelled context", err: context.Canceled, expectedErr: context.Canceled},
		{name: "unique violation", err: uniqueViolation, expectedErr: uniqueViolation},
		{name: "not found", err: ErrNotFound, expectedErr: ErrNotFound},
		{name: "another error", err: other, expectedErr: other},
		{name: "nil"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapTimeout(tc.err)

			if !errors.Is(got, tc.expectedErr) {
				t.Errorf("expected %v, got %v", tc.expectedErr, got)
			}
			if tc.expectedErr != ErrBusy && errors.Is(got, ErrBusy) || tc.expectedErr != ErrTimeout && errors.Is(got, ErrTimeout) {
				t.Errorf("expected neither busy nor a timeout, got %v", got)
			}
			// What is no timeout is returned as it was.
			if tc.expectedErr != ErrBusy && tc.expectedErr != ErrTimeout && got != tc.err {
				t.Errorf("expected the error as it was, got %v", got)
			}
		})
	}

	t.Run("deadline stays readable", func(t *testing.T) {
		if got := wrapTimeout(context.DeadlineExceeded); !errors.Is(got, context.DeadlineExceeded) {
			t.Errorf("expected %v wrapped, got %v", context.DeadlineExceeded, got)
		}
	})
}
