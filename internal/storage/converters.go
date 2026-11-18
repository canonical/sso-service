// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package storage

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/canonical/sso-service/internal/types"
)

func scanConnection(row scanner) (*types.Connection, error) {
	c := new(types.Connection)
	var testedAt sql.NullTime
	var createdBy sql.NullString

	err := row.Scan(&c.ID, &c.OwnerTenantID, &c.Label, &c.Issuer, &c.ClientID, &c.ClientSecret,
		&createdBy, &c.CreatedAt, &c.UpdatedAt, &testedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	c.CreatedBy = createdBy.String
	if testedAt.Valid {
		c.TestedAt = &testedAt.Time
	}

	return c, nil
}

func scanConnections(rows *sql.Rows) ([]*types.Connection, error) {
	defer rows.Close()

	connections := make([]*types.Connection, 0)
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan connection: %w", err)
		}
		connections = append(connections, c)
	}

	return connections, rows.Err()
}

func nullString(v string) sql.NullString {
	return sql.NullString{String: v, Valid: v != ""}
}
