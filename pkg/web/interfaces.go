// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package web

import v0sso "github.com/canonical/identity-platform-api/v0/sso"

type AdminInterface interface {
	v0sso.SSOTenantAdminServiceServer
	v0sso.SSOPlatformAdminServiceServer
}
