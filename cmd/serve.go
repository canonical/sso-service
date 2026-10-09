// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"buf.build/go/protovalidate"
	v0sso "github.com/canonical/identity-platform-api/v0/sso"
	v0tenant "github.com/canonical/identity-platform-api/v0/tenant"
	"github.com/go-playground/validator/v10"
	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/kelseyhightower/envconfig"
	"github.com/pressly/goose/v3"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"github.com/canonical/sso-service/internal/config"
	"github.com/canonical/sso-service/internal/db"
	"github.com/canonical/sso-service/internal/grpcutil"
	"github.com/canonical/sso-service/internal/hydra"
	"github.com/canonical/sso-service/internal/idp"
	"github.com/canonical/sso-service/internal/kratos"
	"github.com/canonical/sso-service/internal/limits"
	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/monitoring/prometheus"
	"github.com/canonical/sso-service/internal/secrets"
	"github.com/canonical/sso-service/internal/storage"
	"github.com/canonical/sso-service/internal/tenants"
	"github.com/canonical/sso-service/internal/tracing"
	"github.com/canonical/sso-service/migrations"
	"github.com/canonical/sso-service/pkg/admin"
	"github.com/canonical/sso-service/pkg/authentication"
	"github.com/canonical/sso-service/pkg/bridge"
	"github.com/canonical/sso-service/pkg/sso"
	"github.com/canonical/sso-service/pkg/web"
)

const (
	// maxRequestBytes caps a request body or gRPC message: the largest
	// legitimate one, a connection with its client secret, is a few kilobytes.
	maxRequestBytes = 1 << 20

	// startupTimeout bounds reaching the database at start-up.
	startupTimeout = 30 * time.Second

	shutdownTimeout = 20 * time.Second
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "serve starts the web server",
	Long:  `Launch the web application, list of environment variables is available in the readme`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return serve()
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
}

func loadSpecs() (*config.EnvSpec, error) {
	specs := new(config.EnvSpec)
	if err := envconfig.Process("", specs); err != nil {
		return nil, fmt.Errorf("issues with environment sourcing: %w", err)
	}
	if err := validator.New(validator.WithRequiredStructEnabled()).Struct(specs); err != nil {
		return nil, fmt.Errorf("issues with environment variables validation: %w", err)
	}
	if err := validatePublicURL(specs.PublicURL, specs.Dev); err != nil {
		return nil, fmt.Errorf("PUBLIC_URL: %w", err)
	}

	return specs, nil
}

// validatePublicURL refuses a URL the browser pages cannot work at: browsers
// return the binding cookie over https only, and the redirect URIs are built
// by appending to the URL's path.
func validatePublicURL(raw string, dev bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "https" && !dev {
		return errors.New("must be an https URL unless DEV is set")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("must have no query and no fragment")
	}

	return nil
}

func serve() error {
	specs, err := loadSpecs()
	if err != nil {
		return err
	}

	startup, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()

	if err := checkMigrations(startup, specs.DSN); err != nil {
		return err
	}

	logger := logging.NewLogger(specs.LogLevel)
	defer logger.Sync()

	monitor := prometheus.NewMonitor("sso-service", logger)
	tracer := tracing.NewTracer(tracing.NewConfig(specs.TracingEnabled, specs.OtelGRPCEndpoint, specs.OtelHTTPEndpoint, logger))

	envelope, err := secrets.NewEnvelope(specs.EnvelopeKey)
	if err != nil {
		return fmt.Errorf("ENVELOPE_KEY: %w", err)
	}

	dbClient, err := db.NewDBClient(startup, db.Config{
		DSN:             specs.DSN,
		MaxConns:        specs.DBMaxConns,
		MinConns:        specs.DBMinConns,
		MaxConnLifetime: specs.DBMaxConnLifetime,
		MaxConnIdleTime: specs.DBMaxConnIdleTime,
		TracingEnabled:  specs.TracingEnabled,
	}, tracer, monitor, logger)
	if err != nil {
		return fmt.Errorf("failed to create database client: %w", err)
	}
	defer dbClient.Close()
	s := storage.NewStorage(dbClient, tracer, monitor, logger)

	if specs.Dev {
		logger.Warn("DEV is set: identity providers are reached over plain http and at private addresses. Never in production.")
	}
	idpClient := idp.NewClient(idp.Config{Dev: specs.Dev}, tracer, monitor, logger)

	tokenSource := tenants.TokenSource(tenants.Credentials{
		TokenURL:     specs.ServiceTokenURL,
		ClientID:     specs.ServiceClientID,
		ClientSecret: specs.ServiceClientSecret,
		Scopes:       strings.Fields(specs.ServiceTokenScopes),
	})
	tenantServiceConn, err := tenants.NewGRPCConn(specs.TenantServiceGRPCAddress, specs.TenantServiceTLSEnabled, tenants.DialOptions(tokenSource)...)
	if err != nil {
		return err
	}
	defer tenantServiceConn.Close()
	tenantsClient := tenants.NewClient(
		v0tenant.NewTenantSignInServiceClient(tenantServiceConn),
		v0tenant.NewTenantSSOPolicyServiceClient(tenantServiceConn),
		specs.TenantServiceGRPCTimeout,
		tracer,
		monitor,
		logger,
	)

	kratosClient := kratos.NewClient(specs.KratosAdminURL, tracer, monitor, logger)
	hydraClient := hydra.NewClient(specs.HydraSSOAdminURL, tracer, monitor, logger)

	// Callers are authenticated, never authorized: the gateway authorizes
	// the admin routes, and the gRPC listener is for the internal network.
	var jwtVerifier authentication.TokenVerifierInterface
	if specs.AuthenticationEnabled {
		// Parse allowed subjects from comma-separated string
		var allowedSubjects []string
		for _, subject := range strings.Split(specs.AuthenticationAllowedSubjects, ",") {
			if trimmed := strings.TrimSpace(subject); trimmed != "" {
				allowedSubjects = append(allowedSubjects, trimmed)
			}
		}

		jwtVerifier, err = authentication.NewJWTAuthenticator(
			context.Background(),
			specs.AuthenticationIssuer,
			specs.AuthenticationJwksURL,
			allowedSubjects,
			specs.AuthenticationRequiredScope,
			tracer,
			monitor,
			logger,
		)
		if err != nil {
			return fmt.Errorf("failed to setup JWT authenticator: %v", err)
		}
	} else {
		logger.Warn("WARNING: JWT authentication is DISABLED — this should only be used in development/debug mode")
		jwtVerifier = authentication.NewNoopVerifier()
	}
	authMiddleware := authentication.NewMiddleware(jwtVerifier, tracer, monitor, logger)

	validator, err := protovalidate.New()
	if err != nil {
		return fmt.Errorf("failed to create request validator: %w", err)
	}

	adminService := admin.NewService(s, tenantsClient, idpClient, envelope, specs.PublicURL, tracer, monitor, logger)
	adminHandler := admin.NewHandler(adminService, validator, specs.PublicURL, tracer, logger)

	ssoService := sso.NewService(s, kratosClient, envelope, tracer, monitor, logger)
	ssoHandler := sso.NewHandler(ssoService, validator, tracer, logger)

	bridgeService := bridge.NewService(
		s,
		idpClient,
		hydraClient,
		kratosClient,
		tenantsClient,
		envelope,
		specs.PublicURL,
		tracer,
		monitor,
		logger,
	)

	grpc_prometheus.EnableHandlingTimeHistogram()

	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxRequestBytes),
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		// Clients ping an idle connection every 30 seconds; the default policy
		// would close it for that.
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 20 * time.Second, PermitWithoutStream: true}),
		grpc.ChainUnaryInterceptor(
			logging.LoggingUnaryInterceptor(logger),
			grpc_prometheus.UnaryServerInterceptor,
			grpcutil.DeadlineUnaryInterceptor(limits.RequestTimeout),
			authMiddleware.GRPCInterceptor,
		),
	)
	v0sso.RegisterSSOSignInServiceServer(grpcServer, ssoHandler)
	v0sso.RegisterSSOTenantAdminServiceServer(grpcServer, adminHandler)
	v0sso.RegisterSSOPlatformAdminServiceServer(grpcServer, adminHandler)
	grpc_prometheus.Register(grpcServer)

	lis, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%v", specs.GRPCPort))
	if err != nil {
		return fmt.Errorf("failed to listen on gRPC port %v: %v", specs.GRPCPort, err)
	}

	router := web.NewRouter(
		bridge.NewAPI(bridgeService, tracer, logger),
		adminHandler,
		authMiddleware,
		limits.RequestTimeout,
		tracer,
		monitor,
		logger,
	)

	srv := &http.Server{
		Addr:              fmt.Sprintf("0.0.0.0:%v", specs.Port),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		Handler:           http.MaxBytesHandler(router, maxRequestBytes),
	}

	logger.Infof("Starting HTTP server on port %v", specs.Port)
	logger.Infof("Starting gRPC server on port %v", specs.GRPCPort)

	return run(logger, srv, grpcServer, lis)
}

func run(logger *logging.Logger, srv *http.Server, grpcServer *grpc.Server, lis net.Listener) error {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	failures := make(chan error, 2)

	logger.Security().SystemStartup()
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failures <- fmt.Errorf("server error: %w", err)
		}
	}()
	go func() {
		if err := grpcServer.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			failures <- fmt.Errorf("failed to serve gRPC: %w", err)
		}
	}()

	var serverError error
	select {
	case <-c:
	case serverError = <-failures:
	}

	// Both servers stop taking requests and let those in flight finish; what
	// is still running at the deadline is cut, so the process always exits.
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	logger.Security().SystemShutdown()

	drained := make(chan struct{})
	go func() {
		grpcServer.GracefulStop()
		close(drained)
	}()
	shutdownError := srv.Shutdown(ctx)
	select {
	case <-drained:
	case <-ctx.Done():
		grpcServer.Stop()
	}

	return errors.Join(serverError, shutdownError)
}

func migrationProvider(ctx context.Context, dsn string) (*goose.Provider, func(), error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("DSN validation failed: %w", err)
	}

	db := stdlib.OpenDB(*config)
	if err := db.PingContext(ctx); err != nil {
		db.Close()

		return nil, nil, fmt.Errorf("DB connection failed: %w", err)
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.EmbedMigrations, goose.WithLogger(goose.NopLogger()))
	if err != nil {
		db.Close()

		return nil, nil, fmt.Errorf("failed to create goose provider: %w", err)
	}

	return provider, func() { db.Close() }, nil
}

// checkMigrations refuses to start with a migration pending.
func checkMigrations(ctx context.Context, dsn string) error {
	provider, closeDB, err := migrationProvider(ctx, dsn)
	if err != nil {
		return err
	}
	defer closeDB()

	pending, err := provider.HasPending(ctx)
	if err != nil {
		return fmt.Errorf("failed to check pending migrations: %w", err)
	}
	if pending {
		return errors.New("a database migration is pending: run `sso-service migrate --dsn ...` first")
	}

	return nil
}
