// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

// Package testhelpers provides shared testcontainers-based fixtures for
// integration tests. Helpers start containers, register teardown via
// t.Cleanup, and fail hard via t.Fatalf on any error. A container runtime
// (Docker or Podman socket) is a documented prerequisite for running
// integration tests; use `go test -short` for the unit-only suite.
//
// Integration tests live next to the code they exercise, in
// *_integration_test.go files with no build tag, and each starts with
//
//	if testing.Short() {
//		t.Skip("Skipping integration test in short mode")
//	}
//
// This package imports only the migrations: internal/db, internal/storage
// and cmd use these helpers from in-package tests, which an import of any
// of them here would turn into an import cycle; this leaf positioning is
// intentional. The mock identity provider the sign-in flow test drives is
// in the mockidp subpackage.
package testhelpers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Container image versions used across all integration tests. Bump here once.
const (
	postgresImage = "postgres:16-alpine"
	hydraImage    = "oryd/hydra:v25.4.0"
)

const (
	// hydraIssuer is the issuer URL the test Hydra container is configured
	// with (URLS_SELF_ISSUER). Clients reach Hydra through a mapped host
	// port, but the issuer claim in issued tokens stays this loopback URL.
	hydraIssuer = "http://127.0.0.1:4444/"

	pingTimeout   = 30 * time.Second
	pingRetryWait = time.Second
)

// HydraEnv carries the mapped endpoints of a started Ory Hydra container.
// Issuer is the URL Hydra is configured with (URLS_SELF_ISSUER); it is the
// issuer claim tokens will carry, not the mapped public address.
type HydraEnv struct {
	PublicURL string
	AdminURL  string
	Issuer    string
}

// terminate registers container termination on t.Cleanup. Termination
// failures are logged, not fatal, so they never mask real test failures.
func terminate(t *testing.T, c testcontainers.Container) {
	t.Helper()
	t.Cleanup(func() {
		if err := c.Terminate(context.Background()); err != nil {
			t.Logf("failed to terminate container: %v", err)
		}
	})
}

// pingPostgres pings the database until it accepts connections or the
// pingTimeout deadline is reached. Returns an error instead of failing the
// test so the caller decides how to surface the failure.
func pingPostgres(connStr string) error {
	cfg, err := pgx.ParseConfig(connStr)
	if err != nil {
		return fmt.Errorf("failed to parse connection string: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()

	for {
		sqlDB := stdlib.OpenDB(*cfg)
		err := sqlDB.PingContext(ctx)
		sqlDB.Close()
		if err == nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("postgres at %s did not become ready within %s", cfg.Host, pingTimeout)
		case <-time.After(pingRetryWait):
		}
	}
}

// startPostgres starts the Postgres container and waits until it accepts
// connections, without registering cleanup: teardown is the caller's. The
// container is terminated here if it cannot be used.
func startPostgres() (string, *postgres.PostgresContainer, error) {
	ctx := context.Background()

	pgContainer, err := postgres.Run(ctx,
		postgresImage,
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
	)
	if err != nil {
		return "", nil, fmt.Errorf("failed to start postgres container (is a container runtime available?): %v", err)
	}

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		pgContainer.Terminate(ctx) //nolint:errcheck
		return "", nil, fmt.Errorf("failed to get postgres connection string: %v", err)
	}

	if err := pingPostgres(connStr); err != nil {
		pgContainer.Terminate(ctx) //nolint:errcheck
		return "", nil, err
	}

	return connStr, pgContainer, nil
}

// SetupPostgres starts a Postgres container, waits until it accepts
// connections, applies all migrations, and returns the connection string.
// Teardown is registered on t; callers never terminate the container
// themselves. Any failure is fatal to the calling test.
//
// A container per test is slow: packages with more than one integration
// test share one through SharedContainers and take an IsolatedDB each.
func SetupPostgres(t *testing.T) string {
	t.Helper()

	connStr, pgContainer, err := startPostgres()
	if err != nil {
		t.Fatalf("%v", err)
	}
	terminate(t, pgContainer)

	RunMigrations(t, connStr)

	return connStr
}

// SetupHydra starts an Ory Hydra container in dev mode with an in-memory
// DSN and JWT access tokens. Teardown is registered on t; callers never
// terminate the container themselves. Any failure is fatal to the calling
// test.
func SetupHydra(t *testing.T) *HydraEnv {
	t.Helper()

	env, container, err := startHydra()
	if err != nil {
		t.Fatalf("%v", err)
	}
	terminate(t, container)

	return env
}

// startHydra starts the Hydra container without registering cleanup, leaving
// teardown to the caller.
func startHydra() (*HydraEnv, testcontainers.Container, error) {
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        hydraImage,
		User:         "1000:1000",
		ExposedPorts: []string{"4444/tcp", "4445/tcp"},
		Env: map[string]string{
			"DSN":                     "memory",
			"URLS_SELF_ISSUER":        hydraIssuer,
			"URLS_LOGIN":              "http://127.0.0.1:8000/login",
			"URLS_CONSENT":            "http://127.0.0.1:8000/consent",
			"SECRETS_SYSTEM":          "test-secret-that-needs-to-be-long-enough",
			"STRATEGIES_ACCESS_TOKEN": "jwt",
			"LOG_LEVEL":               "info",
		},
		Cmd:        []string{"serve", "all", "--dev"},
		WaitingFor: wait.ForHTTP("/health/ready").WithPort("4445/tcp"),
	}

	hydraContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to start hydra container (is a container runtime available?): %v", err)
	}

	host, err := hydraContainer.Host(ctx)
	if err != nil {
		hydraContainer.Terminate(ctx) //nolint:errcheck
		return nil, nil, fmt.Errorf("failed to get hydra container host: %v", err)
	}

	publicPort, err := hydraContainer.MappedPort(ctx, "4444")
	if err != nil {
		hydraContainer.Terminate(ctx) //nolint:errcheck
		return nil, nil, fmt.Errorf("failed to get hydra public port: %v", err)
	}

	adminPort, err := hydraContainer.MappedPort(ctx, "4445")
	if err != nil {
		hydraContainer.Terminate(ctx) //nolint:errcheck
		return nil, nil, fmt.Errorf("failed to get hydra admin port: %v", err)
	}

	return &HydraEnv{
		PublicURL: fmt.Sprintf("http://%s:%s", host, publicPort.Port()),
		AdminURL:  fmt.Sprintf("http://%s:%s", host, adminPort.Port()),
		Issuer:    hydraIssuer,
	}, hydraContainer, nil
}
