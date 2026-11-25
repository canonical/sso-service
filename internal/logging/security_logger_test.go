// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package logging

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWithRequest_Host(t *testing.T) {
	testCases := []struct {
		name     string
		host     string
		hostname string
		port     string
	}{
		{name: "IPv6", host: "[::1]:8080", hostname: "::1", port: "8080"},
		{name: "no port", host: "sso.example", hostname: "sso.example"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Host = tc.host

			fields := map[string]string{}
			for _, field := range WithRequest(request) {
				fields[field.Key] = field.String
			}

			if fields["hostname"] != tc.hostname || fields["port"] != tc.port {
				t.Errorf("expected hostname %q and port %q, got %q and %q", tc.hostname, tc.port, fields["hostname"], fields["port"])
			}
		})
	}
}

func TestWithContext_SkipsAValueThatIsNotAString(t *testing.T) {
	ctx := context.WithValue(context.WithValue(context.Background(), HostnameKey, 8080), PortKey, "8080")

	fields := WithContext(ctx)

	if len(fields) != 1 || fields[0].Key != "port" {
		t.Errorf("expected only the port, got %v", fields)
	}
}
