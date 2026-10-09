// Copyright 2025 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"context"

	sq "github.com/Masterminds/squirrel"
)

type DBClientInterface interface {
	Statement(context.Context) sq.StatementBuilderType
	WithTx(context.Context, func(context.Context) error) error
	Close()
}

type TxInterface interface {
	Commit() error
	Rollback() error
	sq.BaseRunner
}
