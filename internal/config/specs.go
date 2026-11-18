// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"time"
)

// EnvSpec is the basic environment configuration setup needed for the app to start
type EnvSpec struct {
	OtelGRPCEndpoint string `envconfig:"otel_grpc_endpoint"`
	OtelHTTPEndpoint string `envconfig:"otel_http_endpoint"`
	TracingEnabled   bool   `envconfig:"tracing_enabled" default:"true"`

	LogLevel string `envconfig:"log_level" default:"error"`
	// Dev allows identity providers over plain http and at private addresses.
	Dev bool `envconfig:"dev" default:"false"`

	PublicURL string `envconfig:"public_url" required:"true" validate:"required,url"`

	Port     int `envconfig:"port" default:"8080" validate:"required"`
	GRPCPort int `envconfig:"grpc_port" default:"50051" validate:"required,nefield=Port"`

	DSN string `envconfig:"dsn" required:"true" validate:"required"`

	DBMaxConns        int32         `envconfig:"db_max_conns" default:"10"`
	DBMinConns        int32         `envconfig:"db_min_conns" default:"1"`
	DBMaxConnLifetime time.Duration `envconfig:"db_max_conn_lifetime" default:"1h"`
	DBMaxConnIdleTime time.Duration `envconfig:"db_max_conn_idle_time" default:"30m"`

	HydraSSOAdminURL string `envconfig:"hydra_sso_admin_url" required:"true" validate:"required,url"`
	KratosAdminURL   string `envconfig:"kratos_admin_url" required:"true" validate:"required,url"`

	TenantServiceGRPCAddress string        `envconfig:"tenant_service_grpc_address" required:"true" validate:"required"`
	TenantServiceGRPCTimeout time.Duration `envconfig:"tenant_service_grpc_timeout" default:"5s"`
	TenantServiceTLSEnabled  bool          `envconfig:"tenant_service_tls_enabled" default:"false"`

	AuthenticationEnabled         bool   `envconfig:"authentication_enabled" default:"true"`
	AuthenticationIssuer          string `envconfig:"authentication_issuer" validate:"required_if=AuthenticationEnabled true"`
	AuthenticationJwksURL         string `envconfig:"authentication_jwks_url" validate:"omitempty,url"`
	AuthenticationAllowedSubjects string `envconfig:"authentication_allowed_subjects"`
	AuthenticationRequiredScope   string `envconfig:"authentication_required_scope"`

	ServiceTokenURL     string `envconfig:"service_token_url" required:"true" validate:"required,url"`
	ServiceClientID     string `envconfig:"service_client_id" required:"true" validate:"required"`
	ServiceClientSecret string `envconfig:"service_client_secret" required:"true" validate:"required"`
	ServiceTokenScopes  string `envconfig:"service_token_scopes"`

	// EnvelopeKey is 32 bytes in base64. It encrypts the client secrets and
	// seals what a sign-in carries.
	EnvelopeKey string `envconfig:"envelope_key" required:"true" validate:"required"`
}
