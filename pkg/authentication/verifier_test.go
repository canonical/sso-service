// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package authentication

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/mock/gomock"
)

// createTestJWKSServer sets up an httptest server serving JWKS with an RSA key
func createTestJWKSServer(t *testing.T, privKey *rsa.PrivateKey, keyID string) *httptest.Server {
	t.Helper()
	nStr := base64.RawURLEncoding.EncodeToString(privKey.N.Bytes())
	eBytes := big.NewInt(int64(privKey.E)).Bytes()
	eStr := base64.RawURLEncoding.EncodeToString(eBytes)

	jwks := map[string]interface{}{
		"keys": []map[string]interface{}{
			{
				"kty": "RSA",
				"alg": "RS256",
				"use": "sig",
				"kid": keyID,
				"n":   nStr,
				"e":   eStr,
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	}))

	return server
}

// signTestJWT signs a test JWT using the given RSA private key
func signTestJWT(privKey *rsa.PrivateKey, keyID string, claims map[string]interface{}) (string, error) {
	header := map[string]interface{}{
		"alg": "RS256",
		"typ": "JWT",
		"kid": keyID,
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	encodedHeader := base64.RawURLEncoding.EncodeToString(headerJSON)
	encodedClaims := base64.RawURLEncoding.EncodeToString(claimsJSON)

	signingInput := encodedHeader + "." + encodedClaims

	h := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, privKey, crypto.SHA256, h[:])
	if err != nil {
		return "", err
	}

	encodedSig := base64.RawURLEncoding.EncodeToString(sig)
	return signingInput + "." + encodedSig, nil
}

func TestNewProviderWithJWKS(t *testing.T) {
	ctx := context.Background()
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	server := createTestJWKSServer(t, privKey, "test-key-id")
	defer server.Close()

	verifier, err := NewProviderWithJWKS(ctx, "https://issuer.example.com", server.URL)
	require.NoError(t, err)
	assert.NotNil(t, verifier)
}

func TestJWTVerifier_STS_TokenVerification(t *testing.T) {
	ctx := context.Background()

	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	server := createTestJWKSServer(t, privKey, "sts-key-1")
	defer server.Close()

	// Create verifier with JWKS URL
	idTokenVerifier, err := NewProviderWithJWKS(ctx, "https://sts.example.com", server.URL)
	require.NoError(t, err)

	setupMocks := func(t *testing.T) (*gomock.Controller, *MockTracingInterface, *MockMonitorInterface, *MockLoggerInterface) {
		ctrl := gomock.NewController(t)
		mockTracer := NewMockTracingInterface(ctrl)
		mockMonitor := NewMockMonitorInterface(ctrl)
		mockLogger := NewMockLoggerInterface(ctrl)

		mockTracer.EXPECT().Start(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(
			func(ctx context.Context, spanName string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
				return ctx, trace.SpanFromContext(ctx)
			},
		)
		mockLogger.EXPECT().Debugf(gomock.Any(), gomock.Any()).AnyTimes()

		return ctrl, mockTracer, mockMonitor, mockLogger
	}

	t.Run("STS token accepted when no allowed subjects or required scopes are configured", func(t *testing.T) {
		ctrl, mockTracer, mockMonitor, mockLogger := setupMocks(t)
		defer ctrl.Finish()

		verifier := NewJWTVerifierDirect(idTokenVerifier, nil, "", mockTracer, mockMonitor, mockLogger)

		token, err := signTestJWT(privKey, "sts-key-1", map[string]interface{}{
			"iss": "https://sts.example.com",
			"sub": "user-sts-456",
			"exp": time.Now().Add(1 * time.Hour).Unix(),
			"aud": "tenant-service",
		})
		require.NoError(t, err)

		sub, err := verifier.VerifyToken(ctx, token)
		require.NoError(t, err)
		assert.Equal(t, "user-sts-456", sub)
	})

	t.Run("STS token rejected when issuer does not match", func(t *testing.T) {
		ctrl, mockTracer, mockMonitor, mockLogger := setupMocks(t)
		defer ctrl.Finish()

		mockSecurity := NewMockSecurityLoggerInterface(ctrl)
		mockLogger.EXPECT().Security().Return(mockSecurity).AnyTimes()
		mockSecurity.EXPECT().AuthzFailure(gomock.Any(), "jwt_api_access").AnyTimes()

		verifier := NewJWTVerifierDirect(idTokenVerifier, nil, "", mockTracer, mockMonitor, mockLogger)

		token, err := signTestJWT(privKey, "sts-key-1", map[string]interface{}{
			"iss": "https://different-issuer.example.com",
			"sub": "user-sts-456",
			"exp": time.Now().Add(1 * time.Hour).Unix(),
			"aud": "tenant-service",
		})
		require.NoError(t, err)

		_, err = verifier.VerifyToken(ctx, token)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "oidc: id token issued by a different provider")
	})

	t.Run("STS token rejected when empty subject", func(t *testing.T) {
		ctrl, mockTracer, mockMonitor, mockLogger := setupMocks(t)
		defer ctrl.Finish()

		mockSecurity := NewMockSecurityLoggerInterface(ctrl)
		mockLogger.EXPECT().Security().Return(mockSecurity).AnyTimes()
		mockSecurity.EXPECT().AuthzFailure(gomock.Any(), "jwt_api_access").AnyTimes()

		verifier := NewJWTVerifierDirect(idTokenVerifier, nil, "", mockTracer, mockMonitor, mockLogger)

		token, err := signTestJWT(privKey, "sts-key-1", map[string]interface{}{
			"iss": "https://sts.example.com",
			"sub": "",
			"exp": time.Now().Add(1 * time.Hour).Unix(),
			"aud": "tenant-service",
		})
		require.NoError(t, err)

		_, err = verifier.VerifyToken(ctx, token)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "token missing subject claim")
	})

	t.Run("Token accepted when allowedSubjects matches", func(t *testing.T) {
		ctrl, mockTracer, mockMonitor, mockLogger := setupMocks(t)
		defer ctrl.Finish()

		verifier := NewJWTVerifierDirect(idTokenVerifier, []string{"user-sts-456"}, "", mockTracer, mockMonitor, mockLogger)

		token, err := signTestJWT(privKey, "sts-key-1", map[string]interface{}{
			"iss": "https://sts.example.com",
			"sub": "user-sts-456",
			"exp": time.Now().Add(1 * time.Hour).Unix(),
		})
		require.NoError(t, err)

		sub, err := verifier.VerifyToken(ctx, token)
		require.NoError(t, err)
		assert.Equal(t, "user-sts-456", sub)
	})

	t.Run("Token rejected when allowedSubjects does not match", func(t *testing.T) {
		ctrl, mockTracer, mockMonitor, mockLogger := setupMocks(t)
		defer ctrl.Finish()

		mockSecurity := NewMockSecurityLoggerInterface(ctrl)
		mockLogger.EXPECT().Security().Return(mockSecurity).AnyTimes()
		mockSecurity.EXPECT().AuthzFailure("other-user", "jwt_api_access").Times(1)

		verifier := NewJWTVerifierDirect(idTokenVerifier, []string{"user-sts-456"}, "", mockTracer, mockMonitor, mockLogger)

		token, err := signTestJWT(privKey, "sts-key-1", map[string]interface{}{
			"iss": "https://sts.example.com",
			"sub": "other-user",
			"exp": time.Now().Add(1 * time.Hour).Unix(),
		})
		require.NoError(t, err)

		_, err = verifier.VerifyToken(ctx, token)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing required scope or subject not allowed")
	})

	t.Run("Token accepted when requiredScope matches", func(t *testing.T) {
		ctrl, mockTracer, mockMonitor, mockLogger := setupMocks(t)
		defer ctrl.Finish()

		verifier := NewJWTVerifierDirect(idTokenVerifier, nil, "read:tenants", mockTracer, mockMonitor, mockLogger)

		token, err := signTestJWT(privKey, "sts-key-1", map[string]interface{}{
			"iss":   "https://sts.example.com",
			"sub":   "user-scope",
			"exp":   time.Now().Add(1 * time.Hour).Unix(),
			"scope": "read:tenants write:tenants",
		})
		require.NoError(t, err)

		sub, err := verifier.VerifyToken(ctx, token)
		require.NoError(t, err)
		assert.Equal(t, "user-scope", sub)
	})
}
