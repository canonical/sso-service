// Copyright 2025 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package authentication

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/monitoring"
	"github.com/canonical/sso-service/internal/tracing"
)

type JWTVerifier struct {
	verifier        *oidc.IDTokenVerifier
	allowedSubjects []string
	requiredScope   string

	tracer  tracing.TracingInterface
	monitor monitoring.MonitorInterface
	logger  logging.LoggerInterface
}

func (v *JWTVerifier) VerifyToken(ctx context.Context, rawToken string) (string, error) {
	ctx, span := v.tracer.Start(ctx, "authentication.JWTVerifier.VerifyToken")
	defer span.End()

	token, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return "", err
	}

	var claims struct {
		Subject string   `json:"sub"`
		Scope   string   `json:"scope"`
		Scopes  []string `json:"scp"`
	}

	if err := token.Claims(&claims); err != nil {
		v.logger.Debugf("Failed to extract claims: %v", err)
		return "", err
	}

	if claims.Subject == "" {
		v.logger.Debugf("Token missing subject claim")
		v.logger.Security().AuthzFailure("", "jwt_api_access")
		return "", fmt.Errorf("unauthorized: token missing subject claim")
	}

	if len(v.allowedSubjects) > 0 && slices.Contains(v.allowedSubjects, claims.Subject) {
		return claims.Subject, nil
	}

	if v.requiredScope != "" {
		if claims.Scope != "" {
			scopes := strings.Fields(claims.Scope)
			if slices.Contains(scopes, v.requiredScope) {
				return claims.Subject, nil
			}
		}
		if slices.Contains(claims.Scopes, v.requiredScope) {
			return claims.Subject, nil
		}
	}

	if len(v.allowedSubjects) == 0 && v.requiredScope == "" {
		// When no subject/scope policy is configured, accept any validly signed token with a subject
		return claims.Subject, nil
	}

	v.logger.Security().AuthzFailure(claims.Subject, "jwt_api_access")
	return "", fmt.Errorf("unauthorized: missing required scope or subject not allowed")
}

func NewJWTVerifier(
	provider ProviderInterface,
	issuer string,
	allowedSubjects []string,
	requiredScope string,
	tracer tracing.TracingInterface,
	monitor monitoring.MonitorInterface,
	logger logging.LoggerInterface,
) *JWTVerifier {
	v := &JWTVerifier{
		allowedSubjects: allowedSubjects,
		requiredScope:   requiredScope,
		tracer:          tracer,
		monitor:         monitor,
		logger:          logger,
	}

	config := &oidc.Config{
		SkipClientIDCheck:    true,
		SkipIssuerCheck:      false,
		SupportedSigningAlgs: []string{oidc.RS256, oidc.ES256},
	}

	v.verifier = provider.Verifier(config)

	return v
}

func NewJWTVerifierDirect(
	verifier *oidc.IDTokenVerifier,
	allowedSubjects []string,
	requiredScope string,
	tracer tracing.TracingInterface,
	monitor monitoring.MonitorInterface,
	logger logging.LoggerInterface,
) *JWTVerifier {
	return &JWTVerifier{
		verifier:        verifier,
		allowedSubjects: allowedSubjects,
		requiredScope:   requiredScope,
		tracer:          tracer,
		monitor:         monitor,
		logger:          logger,
	}
}
