// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package sso

import (
	"context"
	"errors"
	"strings"

	"buf.build/go/protovalidate"
	v0sso "github.com/canonical/identity-platform-api/v0/sso"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/canonical/sso-service/internal/apierrors"
	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/storage"
	"github.com/canonical/sso-service/internal/tracing"
)

// Handler implements the sign-in API the login UI calls. It is gRPC only.
type Handler struct {
	v0sso.UnimplementedSSOSignInServiceServer
	service   ServiceInterface
	tracer    tracing.TracingInterface
	logger    logging.LoggerInterface
	validator protovalidate.Validator
}

func NewHandler(
	service ServiceInterface,
	validator protovalidate.Validator,
	tracer tracing.TracingInterface,
	logger logging.LoggerInterface,
) *Handler {
	return &Handler{
		service:   service,
		tracer:    tracer,
		logger:    logger,
		validator: validator,
	}
}

func (h *Handler) ListOptions(ctx context.Context, req *v0sso.ListOptionsRequest) (*v0sso.ListOptionsResponse, error) {
	ctx, span := h.tracer.Start(ctx, "sso.Handler.ListOptions")
	defer span.End()

	if err := h.validator.Validate(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid request: %v", err)
	}

	ids := make([]string, len(req.GetConnectionIds()))
	for i, id := range req.GetConnectionIds() {
		ids[i] = strings.ToLower(id)
	}

	connections, err := h.service.ListOptions(ctx, ids)
	if err != nil {
		return nil, h.mapErrorToStatus(err, "list options")
	}

	return &v0sso.ListOptionsResponse{Options: optionsToProto(connections)}, nil
}

func (h *Handler) StartAttempt(ctx context.Context, req *v0sso.StartAttemptRequest) (*v0sso.StartAttemptResponse, error) {
	ctx, span := h.tracer.Start(ctx, "sso.Handler.StartAttempt")
	defer span.End()

	if err := h.validator.Validate(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid request: %v", err)
	}

	ticket, err := h.service.StartAttempt(
		ctx,
		strings.ToLower(req.GetTenantId()),
		strings.ToLower(req.GetEmail()),
		strings.ToLower(req.GetConnectionId()),
		req.GetReauthenticate(),
	)
	if err != nil {
		return nil, h.mapErrorToStatus(err, "start attempt")
	}

	return &v0sso.StartAttemptResponse{Ticket: ticket}, nil
}

func (h *Handler) CompleteAttempt(ctx context.Context, req *v0sso.CompleteAttemptRequest) (*v0sso.CompleteAttemptResponse, error) {
	ctx, span := h.tracer.Start(ctx, "sso.Handler.CompleteAttempt")
	defer span.End()

	if err := h.validator.Validate(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid request: %v", err)
	}

	ticket, err := h.service.CompleteAttempt(ctx, req.GetTicket(), strings.ToLower(req.GetIdentityId()), req.GetReceipt())
	if err != nil {
		return nil, h.mapErrorToStatus(err, "complete attempt")
	}

	return &v0sso.CompleteAttemptResponse{ConnectionId: ticket.ConnectionID, TenantId: ticket.TenantID}, nil
}

func (h *Handler) ListLinks(ctx context.Context, req *v0sso.ListLinksRequest) (*v0sso.ListLinksResponse, error) {
	ctx, span := h.tracer.Start(ctx, "sso.Handler.ListLinks")
	defer span.End()

	if err := h.validator.Validate(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid request: %v", err)
	}

	connections, err := h.service.ListLinks(ctx, strings.ToLower(req.GetIdentityId()))
	if err != nil {
		return nil, h.mapErrorToStatus(err, "list links")
	}

	return &v0sso.ListLinksResponse{Links: linksToProto(connections)}, nil
}

func (h *Handler) DeleteLink(ctx context.Context, req *v0sso.DeleteLinkRequest) (*v0sso.DeleteLinkResponse, error) {
	ctx, span := h.tracer.Start(ctx, "sso.Handler.DeleteLink")
	defer span.End()

	if err := h.validator.Validate(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid request: %v", err)
	}

	err := h.service.DeleteLink(ctx, strings.ToLower(req.GetIdentityId()), strings.ToLower(req.GetConnectionId()))
	if err != nil {
		return nil, h.mapErrorToStatus(err, "delete link")
	}

	return &v0sso.DeleteLinkResponse{}, nil
}

func (h *Handler) mapErrorToStatus(err error, action string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrConnectionNotUsable):
		return apierrors.New(codes.FailedPrecondition, apierrors.NotApplicable, ErrConnectionNotUsable.Error())
	case errors.Is(err, ErrAttemptNotCompleted):
		return apierrors.New(codes.FailedPrecondition, apierrors.NotApplicable, ErrAttemptNotCompleted.Error())
	case errors.Is(err, ErrLastCredential):
		return apierrors.New(codes.FailedPrecondition, apierrors.LastCredential, ErrLastCredential.Error())
	case errors.Is(err, ErrAccountNotFound):
		return status.Error(codes.NotFound, ErrAccountNotFound.Error())
	case errors.Is(err, ErrLinkNotFound):
		return status.Error(codes.NotFound, ErrLinkNotFound.Error())
	case errors.Is(err, ErrKratosUnavailable):
		return status.Error(codes.Unavailable, ErrKratosUnavailable.Error())
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
