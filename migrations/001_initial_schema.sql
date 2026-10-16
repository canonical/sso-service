--  Copyright 2026 Canonical Ltd.
--  SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- +goose StatementBegin

CREATE TABLE sso_connections (
    id UUID PRIMARY KEY,
    owner_tenant_id UUID NOT NULL,
    label TEXT NOT NULL,
    issuer TEXT NOT NULL,
    client_id TEXT NOT NULL,
    client_secret BYTEA NOT NULL,
    created_by UUID,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    tested_at TIMESTAMP WITH TIME ZONE
);

CREATE INDEX idx_sso_connections_owner_tenant_id ON sso_connections (owner_tenant_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS sso_connections;

-- +goose StatementEnd
