// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package logging

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/mock/gomock"
)

func TestLogFormatter_NoQueryNoHeaders(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockLogger := NewMockLoggerInterface(ctrl)

	var line string
	mockLogger.EXPECT().Debug(gomock.Any()).Do(func(args ...interface{}) { line = fmt.Sprint(args...) })

	r := httptest.NewRequest(http.MethodGet, "/callback/c?code=one-time-code&state=st", nil)
	entry := NewLogFormatter(mockLogger).NewLogEntry(r)
	entry.Write(http.StatusSeeOther, 0, http.Header{"Set-Cookie": {"binding=sealed-value"}}, time.Millisecond, nil)

	if !strings.Contains(line, "/callback/c ") || !strings.Contains(line, "303") {
		t.Errorf("expected the path and the status, got %q", line)
	}
	if strings.Contains(line, "one-time-code") || strings.Contains(line, "sealed-value") {
		t.Errorf("expected no query and no headers, got %q", line)
	}
}
