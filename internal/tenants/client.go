// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package tenants

import (
	"context"
	"errors"
	"time"

	v0tenant "github.com/canonical/identity-platform-api/v0/tenant"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/monitoring"
	"github.com/canonical/sso-service/internal/tracing"
)

type Client struct {
	signInClient v0tenant.TenantSignInServiceClient
	policyClient v0tenant.TenantSSOPolicyServiceClient
	timeout      time.Duration

	tracer  tracing.TracingInterface
	monitor monitoring.MonitorInterface
	logger  logging.LoggerInterface
}

// NewClient creates a tenant-service client backed by the generated gRPC clients.
// timeout caps the total time allowed for each call, its retries included.
func NewClient(
	signInClient v0tenant.TenantSignInServiceClient,
	policyClient v0tenant.TenantSSOPolicyServiceClient,
	timeout time.Duration,
	tracer tracing.TracingInterface,
	monitor monitoring.MonitorInterface,
	logger logging.LoggerInterface,
) *Client {
	return &Client{
		signInClient: signInClient,
		policyClient: policyClient,
		timeout:      timeout,
		tracer:       tracer,
		monitor:      monitor,
		logger:       logger,
	}
}

func (c *Client) GetSignInContext(ctx context.Context, tenantID, email, identityID string) (*v0tenant.SignInContext, error) {
	ctx, span := c.tracer.Start(ctx, "tenants.Client.GetSignInContext")
	defer span.End()

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.signInClient.GetSignInContext(ctx, &v0tenant.GetSignInContextRequest{
		TenantId:   tenantID,
		Email:      email,
		IdentityId: identityID,
	})
	if err != nil {
		return nil, recordError(span, err)
	}
	if resp.GetContext() == nil {
		return nil, recordError(span, errors.New("tenant-service returned no sign-in context"))
	}

	return resp.GetContext(), nil
}

func (c *Client) GetTenantSSOPolicy(ctx context.Context, tenantID string) (*v0tenant.TenantSSOPolicy, error) {
	ctx, span := c.tracer.Start(ctx, "tenants.Client.GetTenantSSOPolicy")
	defer span.End()

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.policyClient.GetTenantSSOPolicy(ctx, &v0tenant.GetTenantSSOPolicyRequest{TenantId: tenantID})
	if err != nil {
		return nil, recordError(span, err)
	}

	return resp.GetPolicy(), nil
}

func (c *Client) PutTenantSSOPolicy(ctx context.Context, req *v0tenant.PutTenantSSOPolicyRequest) (*v0tenant.TenantSSOPolicy, error) {
	ctx, span := c.tracer.Start(ctx, "tenants.Client.PutTenantSSOPolicy")
	defer span.End()

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.policyClient.PutTenantSSOPolicy(ctx, req)
	if err != nil {
		return nil, recordError(span, err)
	}

	return resp.GetPolicy(), nil
}

func (c *Client) SetTenantSSODomains(ctx context.Context, tenantID string, domains []string) (*v0tenant.TenantSSOPolicy, error) {
	ctx, span := c.tracer.Start(ctx, "tenants.Client.SetTenantSSODomains")
	defer span.End()

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.policyClient.SetTenantSSODomains(ctx, &v0tenant.SetTenantSSODomainsRequest{TenantId: tenantID, Domains: domains})
	if err != nil {
		return nil, recordError(span, err)
	}

	return resp.GetPolicy(), nil
}

// RemoveTenantSSOBinding changes nothing when the policy does not bind the
// connection.
func (c *Client) RemoveTenantSSOBinding(ctx context.Context, tenantID, connectionID string) error {
	ctx, span := c.tracer.Start(ctx, "tenants.Client.RemoveTenantSSOBinding")
	defer span.End()

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	_, err := c.policyClient.RemoveTenantSSOBinding(ctx, &v0tenant.RemoveTenantSSOBindingRequest{TenantId: tenantID, ConnectionId: connectionID})
	if err != nil {
		return recordError(span, err)
	}

	return nil
}

// recordError records the answer on the span and returns it by its name.
func recordError(span trace.Span, err error) error {
	err = fromStatus(err)
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())

	return err
}
