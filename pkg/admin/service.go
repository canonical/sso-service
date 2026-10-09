// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package admin

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	v0tenant "github.com/canonical/identity-platform-api/v0/tenant"
	"github.com/google/uuid"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/canonical/sso-service/internal/idp"
	"github.com/canonical/sso-service/internal/limits"
	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/monitoring"
	"github.com/canonical/sso-service/internal/secrets"
	"github.com/canonical/sso-service/internal/storage"
	"github.com/canonical/sso-service/internal/tenants"
	"github.com/canonical/sso-service/internal/tracing"
	"github.com/canonical/sso-service/internal/types"
	"github.com/canonical/sso-service/pkg/authentication"
)

const defaultPageSize = 50

// Service provides the logic of the tenant admin and the platform admin APIs.
// It authorizes no caller: the gateway in front does. It only checks that a
// resource belongs to the tenant it is asked for.
type Service struct {
	storage  StorageInterface
	tenants  TenantsClientInterface
	idp      IdPClientInterface
	envelope EnvelopeInterface

	publicURL string
	now       func() time.Time

	tracer  tracing.TracingInterface
	monitor monitoring.MonitorInterface
	logger  logging.LoggerInterface
}

func NewService(
	storage StorageInterface,
	tenantsClient TenantsClientInterface,
	idpClient IdPClientInterface,
	envelope EnvelopeInterface,
	publicURL string,
	tracer tracing.TracingInterface,
	monitor monitoring.MonitorInterface,
	logger logging.LoggerInterface,
) *Service {
	return &Service{
		storage:   storage,
		tenants:   tenantsClient,
		idp:       idpClient,
		envelope:  envelope,
		publicURL: publicURL,
		now:       time.Now,
		tracer:    tracer,
		monitor:   monitor,
		logger:    logger,
	}
}

// recordError records an error on the span and emits a structured error log.
// The "error" key is always appended to keysAndValues automatically.
func (s *Service) recordError(span trace.Span, msg string, err error, keysAndValues ...interface{}) {
	span.RecordError(err)
	span.SetStatus(otelcodes.Error, err.Error())
	s.logger.Errorw(msg, append(keysAndValues, "error", err)...)
}

// ListConnections pages the connections of ownerTenantID, or every
// connection when it is "". The owner need not exist any more.
func (s *Service) ListConnections(ctx context.Context, ownerTenantID, pageToken string, pageSize int) ([]*types.Connection, string, error) {
	ctx, span := s.tracer.Start(ctx, "admin.Service.ListConnections")
	defer span.End()

	if pageSize == 0 {
		pageSize = defaultPageSize
	}
	after := ""
	if pageToken != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(pageToken)
		id, perr := uuid.Parse(string(decoded))
		if err != nil || perr != nil {
			return nil, "", ErrInvalidPageToken
		}
		after = id.String()
	}

	connections, err := s.storage.ListConnections(ctx, ownerTenantID, after, pageSize+1)
	if err != nil {
		s.recordError(span, "failed to list connections", err)

		return nil, "", fmt.Errorf("failed to list connections: %w", err)
	}
	nextPageToken := ""
	if len(connections) > pageSize {
		connections = connections[:pageSize]
		nextPageToken = base64.RawURLEncoding.EncodeToString([]byte(connections[pageSize-1].ID))
	}

	return connections, nextPageToken, nil
}

func (s *Service) CreateConnection(ctx context.Context, tenantID, label, issuer, clientID, clientSecret string) (*types.Connection, error) {
	ctx, span := s.tracer.Start(ctx, "admin.Service.CreateConnection")
	defer span.End()

	actor, _ := authentication.GetUserID(ctx)

	// The tenant exists and is not a personal tenant.
	if _, err := s.tenants.GetTenantSSOPolicy(ctx, tenantID); err != nil {
		return nil, s.tenantServiceError(span, err)
	}
	if err := s.idp.ValidateIssuer(issuer); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidIssuer, err)
	}

	id, err := uuid.NewV7()
	if err != nil {
		s.recordError(span, "failed to generate connection ID", err)

		return nil, fmt.Errorf("failed to generate connection ID: %w", err)
	}
	sealed, err := s.envelope.Encrypt(clientSecret, id.String())
	if err != nil {
		s.recordError(span, "failed to encrypt the client secret", err)

		return nil, fmt.Errorf("failed to encrypt the client secret: %w", err)
	}

	created, err := s.storage.CreateConnection(ctx, &types.Connection{
		ID:            id.String(),
		OwnerTenantID: tenantID,
		Label:         label,
		Issuer:        issuer,
		ClientID:      clientID,
		ClientSecret:  sealed,
		CreatedBy:     accountID(actor),
	}, limits.MaxConnectionsPerTenant)
	if errors.Is(err, storage.ErrConnectionLimit) {
		return nil, ErrConnectionLimit
	}
	if err != nil {
		s.recordError(span, "failed to create connection", err, "tenant_id", tenantID)

		return nil, fmt.Errorf("failed to create connection: %w", err)
	}

	s.logger.Security().AdminAction(actor, "create_connection", "admin.Service.CreateConnection", created.ID)

	return created, nil
}

func (s *Service) GetConnection(ctx context.Context, tenantID, connectionID string) (*types.Connection, error) {
	ctx, span := s.tracer.Start(ctx, "admin.Service.GetConnection")
	defer span.End()

	return s.owned(ctx, tenantID, connectionID)
}

// UpdateConnection changes the label, the client secret or both; nil leaves
// one as it is.
func (s *Service) UpdateConnection(ctx context.Context, tenantID, connectionID string, label, clientSecret *string) (*types.Connection, error) {
	ctx, span := s.tracer.Start(ctx, "admin.Service.UpdateConnection")
	defer span.End()

	actor, _ := authentication.GetUserID(ctx)

	connection, err := s.owned(ctx, tenantID, connectionID)
	if err != nil {
		return nil, err
	}

	update := storage.ConnectionUpdate{Label: label}
	if clientSecret != nil {
		sealed, err := s.envelope.Encrypt(*clientSecret, connection.ID)
		if err != nil {
			s.recordError(span, "failed to encrypt the client secret", err)

			return nil, fmt.Errorf("failed to encrypt the client secret: %w", err)
		}
		update.ClientSecret = sealed
	}

	updated, err := s.storage.UpdateConnection(ctx, connection.ID, update)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, ErrConnectionNotFound
	}
	if err != nil {
		s.recordError(span, "failed to update connection", err, "connection_id", connection.ID)

		return nil, fmt.Errorf("failed to update connection: %w", err)
	}

	s.logger.Security().AdminAction(actor, "update_connection", "admin.Service.UpdateConnection", connection.ID)

	return updated, nil
}

// DeleteConnection deletes a connection of the tenant, and its binding in the
// tenant's policy with it.
func (s *Service) DeleteConnection(ctx context.Context, tenantID, connectionID string) error {
	ctx, span := s.tracer.Start(ctx, "admin.Service.DeleteConnection")
	defer span.End()

	actor, _ := authentication.GetUserID(ctx)

	if err := s.deleteConnection(ctx, tenantID, connectionID); err != nil {
		return err
	}

	s.logger.Security().AdminAction(actor, "delete_connection", "admin.Service.DeleteConnection", connectionID)

	return nil
}

// DeleteAnyConnection deletes any connection, whether its owner still
// exists or not.
func (s *Service) DeleteAnyConnection(ctx context.Context, connectionID string) error {
	ctx, span := s.tracer.Start(ctx, "admin.Service.DeleteAnyConnection")
	defer span.End()

	actor, _ := authentication.GetUserID(ctx)

	if err := s.deleteConnection(ctx, "", connectionID); err != nil {
		return err
	}

	s.logger.Security().AdminAction(actor, "delete_any_connection", "admin.Service.DeleteAnyConnection", connectionID)

	return nil
}

// deleteConnection removes the connection's binding from its owner's policy,
// then the connection, with the connection's row locked: no policy write
// binds it in the meantime. With a tenantID the connection must be that
// tenant's; without one, an owner that no longer exists has nothing to
// unbind.
func (s *Service) deleteConnection(ctx context.Context, tenantID, connectionID string) error {
	span := trace.SpanFromContext(ctx)

	return s.storage.WithTx(ctx, func(ctx context.Context) error {
		locked, err := s.storage.LockConnections(ctx, []string{connectionID})
		if err != nil {
			s.recordError(span, "failed to lock connection", err, "connection_id", connectionID)

			return err
		}
		if len(locked) == 0 || (tenantID != "" && locked[0].OwnerTenantID != tenantID) {
			return ErrConnectionNotFound
		}

		err = s.tenants.RemoveTenantSSOBinding(ctx, locked[0].OwnerTenantID, connectionID)
		if err != nil && !(tenantID == "" && errors.Is(err, tenants.ErrTenantNotFound)) {
			return s.tenantServiceWriteError(span, "RemoveTenantSSOBinding", err)
		}

		err = s.storage.DeleteConnection(ctx, connectionID)
		if errors.Is(err, storage.ErrNotFound) {
			return ErrConnectionNotFound
		}
		if err != nil {
			s.recordError(span, "failed to delete connection", err, "connection_id", connectionID)
		}

		return err
	})
}

// StartTestLogin returns where to send a browser for a test sign-in through
// the connection. The test is sealed into its OAuth state; nothing is stored.
func (s *Service) StartTestLogin(ctx context.Context, tenantID, connectionID string) (string, error) {
	ctx, span := s.tracer.Start(ctx, "admin.Service.StartTestLogin")
	defer span.End()

	actor, _ := authentication.GetUserID(ctx)

	connection, err := s.owned(ctx, tenantID, connectionID)
	if err != nil {
		return "", err
	}

	secret, err := s.envelope.Decrypt(connection.ClientSecret, connection.ID)
	if err != nil {
		s.recordError(span, "failed to decrypt the client secret", err, "connection_id", connection.ID)

		return "", ErrClientSecretUnreadable
	}
	redirectURI := types.RedirectURI(s.publicURL, connection.ID)
	if err := s.idp.Check(ctx, connection, secret, redirectURI); err != nil {
		s.logger.Warnw("test sign-in pre-check failed", "connection_id", connection.ID, "error", err)

		return "", fmt.Errorf("%w: %w", ErrIdPCheckFailed, err)
	}

	values := make([]string, 2)
	for i := range values {
		if values[i], err = secrets.RandomToken(32); err != nil {
			s.recordError(span, "failed to generate random token", err)

			return "", err
		}
	}
	test := &types.TestState{
		ConnectionID: connection.ID,
		Nonce:        values[0],
		PKCEVerifier: values[1],
		ExpiresAt:    s.now().Add(limits.AttemptTTL).Unix(),
	}
	state, err := s.envelope.Seal(types.PurposeTestState, test)
	if err != nil {
		s.recordError(span, "failed to seal test state", err)

		return "", fmt.Errorf("failed to seal test state: %w", err)
	}

	target, err := s.idp.AuthCodeURL(ctx, connection, &idp.AuthRequest{
		RedirectURI:    redirectURI,
		State:          state,
		Nonce:          test.Nonce,
		PKCEVerifier:   test.PKCEVerifier,
		Reauthenticate: true,
	})
	if err != nil {
		s.logger.Warnw("failed to build the authorization request", "connection_id", connection.ID, "error", err)

		return "", fmt.Errorf("%w: %w", ErrIdPCheckFailed, err)
	}

	s.logger.Security().AdminAction(actor, "start_test_login", "admin.Service.StartTestLogin", connection.ID)

	return target, nil
}

func (s *Service) GetTenantSSOPolicy(ctx context.Context, tenantID string) (*v0tenant.TenantSSOPolicy, error) {
	ctx, span := s.tracer.Start(ctx, "admin.Service.GetTenantSSOPolicy")
	defer span.End()

	policy, err := s.tenants.GetTenantSSOPolicy(ctx, tenantID)
	if err != nil {
		return nil, s.tenantServiceError(span, err)
	}

	return policy, nil
}

// PutTenantSSOPolicy writes the tenant's bindings, enforcement and auto-join. A
// binding may name only a connection the tenant owns, and an active one only
// a tested connection; tenant-service checks everything else.
//
// The connections it binds are locked until the write to tenant-service is
// done, so the policy never names a connection that is gone.
func (s *Service) PutTenantSSOPolicy(ctx context.Context, tenantID string, enforcement v0tenant.Enforcement, autoJoin bool, bindings []*v0tenant.SSOBinding) (*v0tenant.TenantSSOPolicy, error) {
	ctx, span := s.tracer.Start(ctx, "admin.Service.PutTenantSSOPolicy")
	defer span.End()

	actor, _ := authentication.GetUserID(ctx)

	ids := make([]string, len(bindings))
	for i, b := range bindings {
		ids[i] = b.GetConnectionId()
	}

	var written *v0tenant.TenantSSOPolicy
	err := s.storage.WithTx(ctx, func(ctx context.Context) error {
		locked, err := s.storage.LockConnections(ctx, ids)
		if err != nil {
			s.recordError(span, "failed to lock connections", err, "tenant_id", tenantID)

			return err
		}
		byID := make(map[string]*types.Connection, len(locked))
		for _, c := range locked {
			byID[c.ID] = c
		}
		for _, b := range bindings {
			connection, ok := byID[b.GetConnectionId()]
			if !ok || connection.OwnerTenantID != tenantID {
				return ErrConnectionNotFound
			}
			if b.GetActive() && !connection.Tested() {
				return ErrConnectionNotTested
			}
		}

		written, err = s.tenants.PutTenantSSOPolicy(ctx, &v0tenant.PutTenantSSOPolicyRequest{
			TenantId:    tenantID,
			Enforcement: enforcement,
			AutoJoin:    autoJoin,
			Bindings:    bindings,
		})
		if err != nil {
			return s.tenantServiceWriteError(span, "PutTenantSSOPolicy", err)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	s.logger.Security().AdminAction(actor, "put_tenant_sso_policy", "admin.Service.PutTenantSSOPolicy", tenantID)

	return written, nil
}

func (s *Service) GetTenantDomains(ctx context.Context, tenantID string) ([]string, error) {
	ctx, span := s.tracer.Start(ctx, "admin.Service.GetTenantDomains")
	defer span.End()

	policy, err := s.tenants.GetTenantSSOPolicy(ctx, tenantID)
	if err != nil {
		return nil, s.tenantServiceError(span, err)
	}

	return policy.GetDomains(), nil
}

func (s *Service) SetTenantDomains(ctx context.Context, tenantID string, domains []string) (*v0tenant.TenantSSOPolicy, error) {
	ctx, span := s.tracer.Start(ctx, "admin.Service.SetTenantDomains")
	defer span.End()

	actor, _ := authentication.GetUserID(ctx)

	written, err := s.tenants.SetTenantSSODomains(ctx, tenantID, domains)
	if err != nil {
		return nil, s.tenantServiceWriteError(span, "SetTenantSSODomains", err)
	}

	s.logger.Security().AdminAction(actor, "set_tenant_domains", "admin.Service.SetTenantDomains", tenantID)

	return written, nil
}

// owned reads a connection of the tenant. One that does not exist and one
// another tenant owns get the same answer.
func (s *Service) owned(ctx context.Context, tenantID, connectionID string) (*types.Connection, error) {
	connection, err := s.storage.GetConnection(ctx, connectionID)
	if errors.Is(err, storage.ErrNotFound) || (err == nil && connection.OwnerTenantID != tenantID) {
		return nil, ErrConnectionNotFound
	}
	if err != nil {
		s.recordError(trace.SpanFromContext(ctx), "failed to get connection", err, "connection_id", connectionID)

		return nil, fmt.Errorf("failed to get connection: %w", err)
	}

	return connection, nil
}

// tenantServiceError records a call tenant-service did not answer; a refusal
// is the caller's to hear, not a failure.
func (s *Service) tenantServiceError(span trace.Span, err error) error {
	if errors.Is(err, tenants.ErrUnavailable) {
		s.recordError(span, "tenant-service call failed", err)
	}

	return err
}

// tenantServiceWriteError also counts a write tenant-service did not answer.
func (s *Service) tenantServiceWriteError(span trace.Span, rpc string, err error) error {
	if errors.Is(err, tenants.ErrUnavailable) {
		if merr := s.monitor.IncrementTenantServiceWriteFailures(map[string]string{"rpc": rpc}); merr != nil {
			s.logger.Warnf("failed to increment tenant-service write failures counter: %v", merr)
		}
		s.recordError(span, "tenant-service write failed", err, "rpc", rpc)
	}

	return err
}

// accountID is the caller's account: a client acting for itself, whose token
// subject is its client id, has none.
func accountID(subject string) string {
	if id, err := uuid.Parse(subject); err == nil {
		return id.String()
	}

	return ""
}
