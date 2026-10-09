// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package limits

import "time"

const (
	// AttemptTTL is how long a sign-in, or a test sign-in, may take.
	AttemptTTL = 30 * time.Minute

	// IdPTimeout bounds one call to an identity provider, IdPExchangeTimeout
	// everything one request asks of it.
	IdPTimeout         = 10 * time.Second
	IdPExchangeTimeout = 15 * time.Second

	RequestTimeout  = 25 * time.Second
	PlatformTimeout = 5 * time.Second
	TokenTimeout    = 5 * time.Second
	DiscoveryTTL    = 10 * time.Minute
	ClockSkew       = 60 * time.Second

	// MaxSubjectLength keeps byo-sso:<connection_id>:<subject> within the 255
	// characters Kratos stores a credential identifier in.
	MaxSubjectLength = 210

	MaxConnectionsPerTenant = 5
	MaxResponseBytes        = 64 << 10
)
