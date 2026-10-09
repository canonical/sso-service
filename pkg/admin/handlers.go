// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package admin

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"buf.build/go/protovalidate"
	v0sso "github.com/canonical/identity-platform-api/v0/sso"
	v0tenant "github.com/canonical/identity-platform-api/v0/tenant"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/canonical/sso-service/internal/apierrors"
	"github.com/canonical/sso-service/internal/idp"
	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/storage"
	"github.com/canonical/sso-service/internal/tenants"
	"github.com/canonical/sso-service/internal/tracing"
)

// Handler implements the gRPC and HTTP endpoints of the tenant admin API,
// whose routes name their tenant, and of the platform admin API.
type Handler struct {
	v0sso.UnimplementedSSOTenantAdminServiceServer
	v0sso.UnimplementedSSOPlatformAdminServiceServer
	service   ServiceInterface
	publicURL string
	tracer    tracing.TracingInterface
	logger    logging.LoggerInterface
	validator protovalidate.Validator
}

func NewHandler(
	service ServiceInterface,
	validator protovalidate.Validator,
	publicURL string,
	tracer tracing.TracingInterface,
	logger logging.LoggerInterface,
) *Handler {
	return &Handler{
		service:   service,
		publicURL: publicURL,
		tracer:    tracer,
		logger:    logger,
		validator: validator,
	}
}

func (h *Handler) ListConnections(ctx context.Context, req *v0sso.ListConnectionsRequest) (*v0sso.ListConnectionsResponse, error) {
	ctx, span := h.tracer.Start(ctx, "admin.Handler.ListConnections")
	defer span.End()

	if err := h.validator.Validate(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid request: %v", err)
	}

	connections, nextPageToken, err := h.service.ListConnections(ctx, strings.ToLower(req.GetTenantId()), req.GetPageToken(), int(req.GetPageSize()))
	if err != nil {
		return nil, h.mapErrorToStatus(err, "list connections")
	}

	return &v0sso.ListConnectionsResponse{
		Connections:   connectionsToProto(connections, h.publicURL),
		NextPageToken: nextPageToken,
	}, nil
}

func (h *Handler) CreateConnection(ctx context.Context, req *v0sso.CreateConnectionRequest) (*v0sso.CreateConnectionResponse, error) {
	ctx, span := h.tracer.Start(ctx, "admin.Handler.CreateConnection")
	defer span.End()

	if err := h.validator.Validate(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid request: %v", err)
	}

	connection, err := h.service.CreateConnection(
		ctx,
		strings.ToLower(req.GetTenantId()),
		req.GetLabel(),
		req.GetIssuer(),
		req.GetClientId(),
		req.GetClientSecret(),
	)
	if err != nil {
		return nil, h.mapErrorToStatus(err, "create connection")
	}

	return &v0sso.CreateConnectionResponse{Connection: connectionToProto(connection, h.publicURL)}, nil
}

func (h *Handler) GetConnection(ctx context.Context, req *v0sso.GetConnectionRequest) (*v0sso.GetConnectionResponse, error) {
	ctx, span := h.tracer.Start(ctx, "admin.Handler.GetConnection")
	defer span.End()

	if err := h.validator.Validate(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid request: %v", err)
	}

	connection, err := h.service.GetConnection(ctx, strings.ToLower(req.GetTenantId()), strings.ToLower(req.GetConnectionId()))
	if err != nil {
		return nil, h.mapErrorToStatus(err, "get connection")
	}

	return &v0sso.GetConnectionResponse{Connection: connectionToProto(connection, h.publicURL)}, nil
}

func (h *Handler) UpdateConnection(ctx context.Context, req *v0sso.UpdateConnectionRequest) (*v0sso.UpdateConnectionResponse, error) {
	ctx, span := h.tracer.Start(ctx, "admin.Handler.UpdateConnection")
	defer span.End()

	if err := h.validator.Validate(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid request: %v", err)
	}

	// An HTTP body with no field gives a mask with one empty path.
	paths := slices.DeleteFunc(slices.Clone(req.GetUpdateMask().GetPaths()), func(path string) bool { return path == "" })
	if len(paths) == 0 {
		return nil, status.Error(codes.InvalidArgument, "update_mask names nothing")
	}
	var label, clientSecret *string
	for _, path := range paths {
		switch path {
		case "label":
			value := req.GetConnection().GetLabel()
			if value == "" || utf8.RuneCountInString(value) > 100 {
				return nil, status.Error(codes.InvalidArgument, "label must be 1 to 100 characters")
			}
			label = &value
		case "client_secret":
			value := req.GetConnection().GetClientSecret()
			if value == "" || utf8.RuneCountInString(value) > 4096 {
				return nil, status.Error(codes.InvalidArgument, "client_secret must be 1 to 4096 characters")
			}
			clientSecret = &value
		default:
			return nil, status.Errorf(codes.InvalidArgument, "invalid update_mask path: %s", path)
		}
	}

	connection, err := h.service.UpdateConnection(ctx, strings.ToLower(req.GetTenantId()), strings.ToLower(req.GetConnectionId()), label, clientSecret)
	if err != nil {
		return nil, h.mapErrorToStatus(err, "update connection")
	}

	return &v0sso.UpdateConnectionResponse{Connection: connectionToProto(connection, h.publicURL)}, nil
}

func (h *Handler) DeleteConnection(ctx context.Context, req *v0sso.DeleteConnectionRequest) (*v0sso.DeleteConnectionResponse, error) {
	ctx, span := h.tracer.Start(ctx, "admin.Handler.DeleteConnection")
	defer span.End()

	if err := h.validator.Validate(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid request: %v", err)
	}

	err := h.service.DeleteConnection(ctx, strings.ToLower(req.GetTenantId()), strings.ToLower(req.GetConnectionId()))
	if err != nil {
		return nil, h.mapErrorToStatus(err, "delete connection")
	}

	return &v0sso.DeleteConnectionResponse{}, nil
}

func (h *Handler) StartTestLogin(ctx context.Context, req *v0sso.StartTestLoginRequest) (*v0sso.StartTestLoginResponse, error) {
	ctx, span := h.tracer.Start(ctx, "admin.Handler.StartTestLogin")
	defer span.End()

	if err := h.validator.Validate(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid request: %v", err)
	}

	url, err := h.service.StartTestLogin(ctx, strings.ToLower(req.GetTenantId()), strings.ToLower(req.GetConnectionId()))
	if err != nil {
		return nil, h.mapErrorToStatus(err, "start test login")
	}

	return &v0sso.StartTestLoginResponse{Url: url}, nil
}

func (h *Handler) GetTenantSSOPolicy(ctx context.Context, req *v0sso.GetTenantSSOPolicyRequest) (*v0sso.GetTenantSSOPolicyResponse, error) {
	ctx, span := h.tracer.Start(ctx, "admin.Handler.GetTenantSSOPolicy")
	defer span.End()

	if err := h.validator.Validate(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid request: %v", err)
	}

	policy, err := h.service.GetTenantSSOPolicy(ctx, strings.ToLower(req.GetTenantId()))
	if err != nil {
		return nil, h.mapErrorToStatus(err, "get tenant sso policy")
	}

	return &v0sso.GetTenantSSOPolicyResponse{Policy: policy}, nil
}

func (h *Handler) PutTenantSSOPolicy(ctx context.Context, req *v0sso.PutTenantSSOPolicyRequest) (*v0sso.PutTenantSSOPolicyResponse, error) {
	ctx, span := h.tracer.Start(ctx, "admin.Handler.PutTenantSSOPolicy")
	defer span.End()

	if err := h.validator.Validate(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid request: %v", err)
	}

	bindings := make([]*v0tenant.SSOBinding, len(req.GetBindings()))
	for i, b := range req.GetBindings() {
		bindings[i] = &v0tenant.SSOBinding{ConnectionId: strings.ToLower(b.GetConnectionId()), Active: b.GetActive()}
	}

	policy, err := h.service.PutTenantSSOPolicy(ctx, strings.ToLower(req.GetTenantId()), req.GetEnforcement(), req.GetAutoJoin(), bindings)
	if err != nil {
		return nil, h.mapErrorToStatus(err, "put tenant sso policy")
	}

	return &v0sso.PutTenantSSOPolicyResponse{Policy: policy}, nil
}

// ListAllConnections lists every connection, or one owner's.
func (h *Handler) ListAllConnections(ctx context.Context, req *v0sso.ListAllConnectionsRequest) (*v0sso.ListAllConnectionsResponse, error) {
	ctx, span := h.tracer.Start(ctx, "admin.Handler.ListAllConnections")
	defer span.End()

	if err := h.validator.Validate(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid request: %v", err)
	}

	connections, nextPageToken, err := h.service.ListConnections(ctx, strings.ToLower(req.GetOwnerTenantId()), req.GetPageToken(), int(req.GetPageSize()))
	if err != nil {
		return nil, h.mapErrorToStatus(err, "list all connections")
	}

	return &v0sso.ListAllConnectionsResponse{
		Connections:   connectionsToProto(connections, h.publicURL),
		NextPageToken: nextPageToken,
	}, nil
}

func (h *Handler) GetTenantDomains(ctx context.Context, req *v0sso.GetTenantDomainsRequest) (*v0sso.GetTenantDomainsResponse, error) {
	ctx, span := h.tracer.Start(ctx, "admin.Handler.GetTenantDomains")
	defer span.End()

	if err := h.validator.Validate(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid request: %v", err)
	}

	domains, err := h.service.GetTenantDomains(ctx, strings.ToLower(req.GetTenantId()))
	if err != nil {
		return nil, h.mapErrorToStatus(err, "get tenant domains")
	}

	return &v0sso.GetTenantDomainsResponse{Domains: domains}, nil
}

func (h *Handler) SetTenantDomains(ctx context.Context, req *v0sso.SetTenantDomainsRequest) (*v0sso.SetTenantDomainsResponse, error) {
	ctx, span := h.tracer.Start(ctx, "admin.Handler.SetTenantDomains")
	defer span.End()

	if err := h.validator.Validate(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid request: %v", err)
	}

	policy, err := h.service.SetTenantDomains(ctx, strings.ToLower(req.GetTenantId()), req.GetDomains())
	if err != nil {
		return nil, h.mapErrorToStatus(err, "set tenant domains")
	}

	return &v0sso.SetTenantDomainsResponse{Policy: policy}, nil
}

func (h *Handler) DeleteAnyConnection(ctx context.Context, req *v0sso.DeleteAnyConnectionRequest) (*v0sso.DeleteAnyConnectionResponse, error) {
	ctx, span := h.tracer.Start(ctx, "admin.Handler.DeleteAnyConnection")
	defer span.End()

	if err := h.validator.Validate(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid request: %v", err)
	}

	if err := h.service.DeleteAnyConnection(ctx, strings.ToLower(req.GetConnectionId())); err != nil {
		return nil, h.mapErrorToStatus(err, "delete any connection")
	}

	return &v0sso.DeleteAnyConnectionResponse{}, nil
}

func (h *Handler) mapErrorToStatus(err error, action string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrConnectionNotFound):
		return apierrors.New(codes.NotFound, apierrors.ConnectionNotFound, ErrConnectionNotFound.Error())
	case errors.Is(err, ErrConnectionNotTested):
		return apierrors.New(codes.FailedPrecondition, apierrors.ConnectionNotTested, ErrConnectionNotTested.Error())
	case errors.Is(err, ErrConnectionLimit):
		return apierrors.New(codes.FailedPrecondition, apierrors.ConnectionLimit, ErrConnectionLimit.Error())
	case errors.Is(err, ErrIdPCheckFailed):
		return apierrors.New(codes.FailedPrecondition, apierrors.IdPCheckFailed, idp.TestError(err))
	case errors.Is(err, ErrClientSecretUnreadable):
		return apierrors.New(codes.FailedPrecondition, apierrors.IdPCheckFailed, "The connection's client secret cannot be read: set it again.")
	case errors.Is(err, ErrInvalidIssuer), errors.Is(err, ErrInvalidPageToken):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, tenants.ErrPersonalTenant):
		return apierrors.New(codes.FailedPrecondition, apierrors.PersonalTenant, tenants.ErrPersonalTenant.Error())
	case errors.Is(err, tenants.ErrRequiredNeedsActiveBinding):
		return apierrors.New(codes.FailedPrecondition, apierrors.RequiredNeedsActiveBinding, tenants.ErrRequiredNeedsActiveBinding.Error())
	case errors.Is(err, tenants.ErrAutoJoinNeedsRequiredAndDomains):
		return apierrors.New(codes.FailedPrecondition, apierrors.AutoJoinNeedsRequiredAndDomains, tenants.ErrAutoJoinNeedsRequiredAndDomains.Error())
	case errors.Is(err, tenants.ErrTenantNotFound):
		return status.Error(codes.NotFound, tenants.ErrTenantNotFound.Error())
	case errors.Is(err, tenants.ErrBusy):
		return status.Error(codes.Aborted, tenants.ErrBusy.Error())
	case errors.Is(err, tenants.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, tenants.ErrRefused):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, tenants.ErrUnavailable):
		return status.Error(codes.Unavailable, tenants.ErrUnavailable.Error())
	case errors.Is(err, storage.ErrBusy):
		return status.Error(codes.Aborted, "another request is changing this resource; try again")
	case errors.Is(err, storage.ErrTimeout):
		return status.Error(codes.Unavailable, "the database did not answer in time; try again later")
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "the request was cancelled")
	default:
		h.logger.Errorf("Unhandled error in %s: %v", action, err)
		return status.Error(codes.Internal, "internal error")
	}
}
