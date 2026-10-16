// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package hydra

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	client "github.com/ory/hydra-client-go/v26"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/canonical/sso-service/internal/limits"
	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/monitoring"
	"github.com/canonical/sso-service/internal/tracing"
)

type LoginRequest struct {
	OIDCContext struct {
		LoginHint string `json:"login_hint"`
	} `json:"oidc_context"`
}

type ConsentRequest struct {
	Challenge                    string         `json:"challenge"`
	RequestedScope               []string       `json:"requested_scope"`
	RequestedAccessTokenAudience []string       `json:"requested_access_token_audience"`
	Context                      map[string]any `json:"context"`
}

type Client struct {
	api client.OAuth2API

	tracer  tracing.TracingInterface
	monitor monitoring.MonitorInterface
	logger  logging.LoggerInterface
}

func NewClient(hydraAdminURL string, tracer tracing.TracingInterface, monitor monitoring.MonitorInterface, logger logging.LoggerInterface) *Client {
	conf := client.NewConfiguration()
	conf.Servers = client.ServerConfigurations{{URL: strings.TrimRight(hydraAdminURL, "/")}}
	conf.HTTPClient = &http.Client{Timeout: limits.PlatformTimeout, Transport: otelhttp.NewTransport(http.DefaultTransport)}

	return &Client{
		api:     client.NewAPIClient(conf).OAuth2API,
		tracer:  tracer,
		monitor: monitor,
		logger:  logger,
	}
}

func (c *Client) GetLoginRequest(ctx context.Context, challenge string) (*LoginRequest, error) {
	ctx, span := c.tracer.Start(ctx, "hydra.Client.GetLoginRequest")
	defer span.End()

	request, response, err := c.api.GetOAuth2LoginRequest(ctx).LoginChallenge(challenge).Execute()
	if err != nil {
		return nil, recordError(span, fmt.Errorf("failed to get login request: %w", responseError(response, err)))
	}
	out := new(LoginRequest)
	if err := convert(request, out); err != nil {
		return nil, recordError(span, err)
	}

	return out, nil
}

// AcceptLogin never remembers the login: every sign-in goes to the identity
// provider.
func (c *Client) AcceptLogin(ctx context.Context, challenge, subject string, loginContext map[string]any) (string, error) {
	ctx, span := c.tracer.Start(ctx, "hydra.Client.AcceptLogin")
	defer span.End()

	remember := false
	to, response, err := c.api.AcceptOAuth2LoginRequest(ctx).LoginChallenge(challenge).
		AcceptOAuth2LoginRequest(client.AcceptOAuth2LoginRequest{Subject: subject, Remember: &remember, Context: loginContext}).Execute()
	if err != nil {
		return "", recordError(span, fmt.Errorf("failed to accept login request: %w", responseError(response, err)))
	}

	return to.RedirectTo, nil
}

func (c *Client) RejectLogin(ctx context.Context, challenge, description string) (string, error) {
	ctx, span := c.tracer.Start(ctx, "hydra.Client.RejectLogin")
	defer span.End()

	code := "access_denied"
	to, response, err := c.api.RejectOAuth2LoginRequest(ctx).LoginChallenge(challenge).
		RejectOAuth2Request(client.RejectOAuth2Request{Error: &code, ErrorDescription: &description}).Execute()
	if err != nil {
		return "", recordError(span, fmt.Errorf("failed to reject login request: %w", responseError(response, err)))
	}

	return to.RedirectTo, nil
}

func (c *Client) GetConsentRequest(ctx context.Context, challenge string) (*ConsentRequest, error) {
	ctx, span := c.tracer.Start(ctx, "hydra.Client.GetConsentRequest")
	defer span.End()

	request, response, err := c.api.GetOAuth2ConsentRequest(ctx).ConsentChallenge(challenge).Execute()
	if err != nil {
		return nil, recordError(span, fmt.Errorf("failed to get consent request: %w", responseError(response, err)))
	}
	out := new(ConsentRequest)
	if err := convert(request, out); err != nil {
		return nil, recordError(span, err)
	}

	return out, nil
}

// AcceptConsent grants what was asked, never remembers it, and puts idToken
// into the id_token the client gets.
func (c *Client) AcceptConsent(ctx context.Context, request *ConsentRequest, idToken map[string]any) (string, error) {
	ctx, span := c.tracer.Start(ctx, "hydra.Client.AcceptConsent")
	defer span.End()

	remember := false
	to, response, err := c.api.AcceptOAuth2ConsentRequest(ctx).ConsentChallenge(request.Challenge).
		AcceptOAuth2ConsentRequest(client.AcceptOAuth2ConsentRequest{
			GrantScope:               request.RequestedScope,
			GrantAccessTokenAudience: request.RequestedAccessTokenAudience,
			Remember:                 &remember,
			Session:                  &client.AcceptOAuth2ConsentRequestSession{IdToken: idToken},
		}).Execute()
	if err != nil {
		return "", recordError(span, fmt.Errorf("failed to accept consent request: %w", responseError(response, err)))
	}

	return to.RedirectTo, nil
}

func recordError(span trace.Span, err error) error {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())

	return err
}

func convert(from, to any) error {
	encoded, err := json.Marshal(from)
	if err != nil {
		return fmt.Errorf("failed to encode request: %w", err)
	}
	if err := json.Unmarshal(encoded, to); err != nil {
		return fmt.Errorf("failed to decode request: %w", err)
	}

	return nil
}

// responseError reads a challenge that is unknown, used or expired as
// ErrNotFound.
func responseError(response *http.Response, err error) error {
	if response == nil {
		return err
	}
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone {
		return ErrNotFound
	}

	return fmt.Errorf("%d: %w", response.StatusCode, err)
}
