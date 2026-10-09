// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package kratos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	client "github.com/ory/kratos-client-go/v25"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/canonical/sso-service/internal/limits"
	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/monitoring"
	"github.com/canonical/sso-service/internal/tracing"
)

const OIDCCredential = "oidc"

// FirstFactorCredentials are the credential types an account can sign in
// with. "code" is not among them: Kratos gives it to every account.
var FirstFactorCredentials = []string{"password", OIDCCredential, "passkey", "webauthn"}

type Identity struct {
	ID          string                `json:"id"`
	Traits      json.RawMessage       `json:"traits"`
	Credentials map[string]Credential `json:"credentials,omitempty"`
}

// Credential carries its config only when the identity was read with that
// credential type included.
type Credential struct {
	Config json.RawMessage `json:"config,omitempty"`
}

type OIDCProvider struct {
	Provider string `json:"provider"`
	Subject  string `json:"subject"`
}

func (i *Identity) Email() string {
	traits := new(struct {
		Email string `json:"email"`
	})
	_ = json.Unmarshal(i.Traits, traits)

	return traits.Email
}

func (i *Identity) OIDCProviders() []OIDCProvider {
	credential, ok := i.Credentials[OIDCCredential]
	if !ok || len(credential.Config) == 0 {
		return nil
	}
	config := new(struct {
		Providers []OIDCProvider `json:"providers"`
	})
	_ = json.Unmarshal(credential.Config, config)

	return config.Providers
}

// HasPassword needs a hash: Kratos gives every identity a password credential
// for its address, so the credential alone says nothing.
func (i *Identity) HasPassword() bool {
	credential, ok := i.Credentials["password"]
	if !ok || len(credential.Config) == 0 {
		return false
	}
	config := new(struct {
		HashedPassword string `json:"hashed_password"`
	})
	_ = json.Unmarshal(credential.Config, config)

	return config.HashedPassword != ""
}

// HasPasskey counts a passkey and a passwordless WebAuthn key; a WebAuthn key
// used as a second factor is not a way to sign in.
func (i *Identity) HasPasskey() bool {
	if credential, ok := i.Credentials["passkey"]; ok && len(credential.Config) > 0 {
		config := new(struct {
			Credentials []json.RawMessage `json:"credentials"`
		})
		_ = json.Unmarshal(credential.Config, config)
		if len(config.Credentials) > 0 {
			return true
		}
	}
	if credential, ok := i.Credentials["webauthn"]; ok && len(credential.Config) > 0 {
		config := new(struct {
			Credentials []struct {
				IsPasswordless bool `json:"is_passwordless"`
			} `json:"credentials"`
		})
		_ = json.Unmarshal(credential.Config, config)
		for _, c := range config.Credentials {
			if c.IsPasswordless {
				return true
			}
		}
	}

	return false
}

type Client struct {
	api client.IdentityAPI

	tracer  tracing.TracingInterface
	monitor monitoring.MonitorInterface
	logger  logging.LoggerInterface
}

func NewClient(kratosAdminURL string, tracer tracing.TracingInterface, monitor monitoring.MonitorInterface, logger logging.LoggerInterface) *Client {
	conf := client.NewConfiguration()
	conf.Servers = client.ServerConfigurations{{URL: strings.TrimRight(kratosAdminURL, "/")}}
	conf.HTTPClient = &http.Client{Timeout: limits.PlatformTimeout, Transport: otelhttp.NewTransport(http.DefaultTransport)}

	return &Client{
		api:     client.NewAPIClient(conf).IdentityAPI,
		tracer:  tracer,
		monitor: monitor,
		logger:  logger,
	}
}

// GetIdentity reads one identity, with the configs of the given credential
// types.
func (c *Client) GetIdentity(ctx context.Context, id string, credentials ...string) (*Identity, error) {
	ctx, span := c.tracer.Start(ctx, "kratos.Client.GetIdentity")
	defer span.End()

	request := c.api.GetIdentity(ctx, id)
	if len(credentials) > 0 {
		request = request.IncludeCredential(credentials)
	}
	identity, response, err := request.Execute()
	if err != nil {
		return nil, recordError(span, fmt.Errorf("failed to get identity: %w", responseError(response, err)))
	}

	out, err := toIdentity(identity)
	if err != nil {
		return nil, recordError(span, err)
	}

	return out, nil
}

// ListByIdentifier finds identities by an exact credentials identifier: an
// email address, or a link's identifier.
func (c *Client) ListByIdentifier(ctx context.Context, identifier string) ([]Identity, error) {
	ctx, span := c.tracer.Start(ctx, "kratos.Client.ListByIdentifier")
	defer span.End()

	identities, response, err := c.api.ListIdentities(ctx).CredentialsIdentifier(identifier).Execute()
	if err != nil {
		return nil, recordError(span, fmt.Errorf("failed to list identities: %w", responseError(response, err)))
	}
	out := make([]Identity, 0, len(identities))
	for i := range identities {
		identity, err := toIdentity(&identities[i])
		if err != nil {
			return nil, recordError(span, err)
		}
		out = append(out, *identity)
	}

	return out, nil
}

func (c *Client) DeleteOIDCIdentifier(ctx context.Context, identityID, identifier string) error {
	ctx, span := c.tracer.Start(ctx, "kratos.Client.DeleteOIDCIdentifier")
	defer span.End()

	response, err := c.api.DeleteIdentityCredentials(ctx, identityID, OIDCCredential).Identifier(identifier).Execute()
	if err != nil {
		return recordError(span, fmt.Errorf("failed to delete identity credentials: %w", responseError(response, err)))
	}

	return nil
}

func recordError(span trace.Span, err error) error {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())

	return err
}

func toIdentity(identity *client.Identity) (*Identity, error) {
	encoded, err := json.Marshal(identity)
	if err != nil {
		return nil, fmt.Errorf("failed to encode identity: %w", err)
	}
	out := new(Identity)
	if err := json.Unmarshal(encoded, out); err != nil {
		return nil, fmt.Errorf("failed to decode identity: %w", err)
	}

	return out, nil
}

func responseError(response *http.Response, err error) error {
	if response == nil {
		// A transport error names the URL, and with it the address or the
		// identifier that was looked up: keep the cause only.
		var transport *url.Error
		if errors.As(err, &transport) {
			return transport.Err
		}

		return err
	}
	if response.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}

	var apiError *client.GenericOpenAPIError
	body := err.Error()
	if errors.As(err, &apiError) {
		body = string(apiError.Body())
	}
	if len(body) > 300 {
		body = body[:300]
	}

	return fmt.Errorf("%d %s", response.StatusCode, body)
}
