// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package logging

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"go.uber.org/mock/gomock"
)

func TestRedactedURI(t *testing.T) {
	tests := []struct {
		uri, want string
	}{
		{"/callback/c", "/callback/c"},
		{"/callback/c?code=one-time-code&state=st", "/callback/c?code=REDACTED&state=REDACTED"},
		{"/login?debug&login_challenge=", "/login?debug&login_challenge=REDACTED"},
	}

	for _, tt := range tests {
		u, err := url.ParseRequestURI(tt.uri)
		if err != nil {
			t.Fatal(err)
		}
		if got := redactedURI(u); got != tt.want {
			t.Errorf("redactedURI(%q) = %q, want %q", tt.uri, got, tt.want)
		}
	}
}

func TestLogFormatter_RedactedQueryNoHeaders(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockLogger := NewMockLoggerInterface(ctrl)

	var line string
	mockLogger.EXPECT().Debug(gomock.Any()).Do(func(args ...interface{}) { line = fmt.Sprint(args...) })

	r := httptest.NewRequest(http.MethodGet, "/callback/c?code=one-time-code&state=st", nil)
	entry := NewLogFormatter(mockLogger).NewLogEntry(r)
	entry.Write(http.StatusSeeOther, 0, http.Header{"Set-Cookie": {"binding=sealed-value"}}, time.Millisecond, nil)

	if !strings.Contains(line, "/callback/c?code=REDACTED&state=REDACTED ") || !strings.Contains(line, "303") {
		t.Errorf("expected the redacted URI and the status, got %q", line)
	}
	if strings.Contains(line, "one-time-code") || strings.Contains(line, "sealed-value") {
		t.Errorf("expected no query value and no headers, got %q", line)
	}
}
