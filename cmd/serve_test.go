// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"testing"
	"time"
)

// setEnvironment sets the least the service starts with.
func setEnvironment(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"PUBLIC_URL":                  "https://sso.example",
		"DSN":                         "postgres://sso:sso@db:5432/sso",
		"HYDRA_SSO_ADMIN_URL":         "http://hydra-sso:4445",
		"KRATOS_ADMIN_URL":            "http://kratos:4434",
		"TENANT_SERVICE_GRPC_ADDRESS": "tenant-service:50051",
		"AUTHENTICATION_ISSUER":       "https://hydra.example",
		"SERVICE_TOKEN_URL":           "https://hydra.example/oauth2/token",
		"SERVICE_CLIENT_ID":           "sso-service",
		"SERVICE_CLIENT_SECRET":       "s",
		"ENVELOPE_KEY":                "a2V5",
	} {
		t.Setenv(k, v)
	}
}

func TestLoadSpecs(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		setEnvironment(t)

		specs, err := loadSpecs()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if specs.LogLevel != "error" || specs.Port != 8080 || specs.GRPCPort != 50051 || specs.Dev || !specs.TracingEnabled {
			t.Errorf("unexpected defaults: %+v", specs)
		}
		if !specs.AuthenticationEnabled || specs.AuthenticationIssuer != "https://hydra.example" || specs.AuthenticationJwksURL != "" ||
			specs.AuthenticationAllowedSubjects != "" || specs.AuthenticationRequiredScope != "" {
			t.Errorf("unexpected authentication defaults: %+v", specs)
		}
		if specs.TenantServiceGRPCTimeout != 5*time.Second || specs.TenantServiceTLSEnabled {
			t.Errorf("unexpected tenant-service defaults: %+v", specs)
		}
	})

	t.Run("from the environment", func(t *testing.T) {
		setEnvironment(t)
		for k, v := range map[string]string{
			"DEV": "true", "PUBLIC_URL": "http://localhost:4460", "LOG_LEVEL": "debug", "PORT": "4460", "GRPC_PORT": "4461",
			"TENANT_SERVICE_GRPC_TIMEOUT": "2s", "TENANT_SERVICE_TLS_ENABLED": "true",
			"AUTHENTICATION_JWKS_URL":         "https://hydra.example/.well-known/jwks.json",
			"AUTHENTICATION_ALLOWED_SUBJECTS": "login-ui, admin-ui",
			"AUTHENTICATION_REQUIRED_SCOPE":   "sso-service",
		} {
			t.Setenv(k, v)
		}

		specs, err := loadSpecs()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !specs.Dev || specs.LogLevel != "debug" || specs.Port != 4460 || specs.GRPCPort != 4461 ||
			specs.TenantServiceGRPCTimeout != 2*time.Second || !specs.TenantServiceTLSEnabled ||
			specs.AuthenticationJwksURL != "https://hydra.example/.well-known/jwks.json" ||
			specs.AuthenticationAllowedSubjects != "login-ui, admin-ui" || specs.AuthenticationRequiredScope != "sso-service" {
			t.Errorf("unexpected specs: %+v", specs)
		}
	})

	t.Run("authentication disabled needs no issuer", func(t *testing.T) {
		setEnvironment(t)
		t.Setenv("AUTHENTICATION_ENABLED", "false")
		t.Setenv("AUTHENTICATION_ISSUER", "")

		specs, err := loadSpecs()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if specs.AuthenticationEnabled {
			t.Error("expected authentication disabled")
		}
	})

	testCases := []struct {
		name  string
		key   string
		value string
	}{
		{name: "no issuer", key: "AUTHENTICATION_ISSUER", value: ""},
		{name: "invalid JWKS URL", key: "AUTHENTICATION_JWKS_URL", value: "keys"},
		{name: "one port for both", key: "GRPC_PORT", value: "8080"},
		{name: "invalid boolean", key: "DEV", value: "maybe"},
		{name: "invalid duration", key: "TENANT_SERVICE_GRPC_TIMEOUT", value: "soon"},
		{name: "no public URL", key: "PUBLIC_URL", value: ""},
		{name: "public URL not https", key: "PUBLIC_URL", value: "http://sso.example"},
		{name: "public URL with a query", key: "PUBLIC_URL", value: "https://sso.example/?a=b"},
		{name: "public URL with a fragment", key: "PUBLIC_URL", value: "https://sso.example/#a"},
		{name: "no envelope key", key: "ENVELOPE_KEY", value: ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			setEnvironment(t)
			t.Setenv(tc.key, tc.value)

			if _, err := loadSpecs(); err == nil {
				t.Error("expected error but got none")
			}
		})
	}
}
