// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package bridge

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRenderPage(t *testing.T) {
	w := httptest.NewRecorder()

	renderPage(w, http.StatusBadRequest, messagePage("<b>Title", "<script>alert(1)</script>"))

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "<script>") || strings.Contains(body, "<b>Title") || !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("expected the title and the message escaped: %s", body)
	}
	for header, expected := range map[string]string{
		"Content-Type":    "text/html; charset=utf-8",
		"Cache-Control":   "no-store",
		"X-Frame-Options": "DENY",
	} {
		if got := w.Header().Get(header); got != expected {
			t.Errorf("%s: expected %q, got %q", header, expected, got)
		}
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("unexpected Content-Security-Policy %q", csp)
	}
}

func TestTestSucceededPage(t *testing.T) {
	testCases := []struct {
		name             string
		authTimeReturned bool
		expectedWarning  bool
	}{
		{name: "auth_time returned", authTimeReturned: true, expectedWarning: false},
		{name: "no auth_time", authTimeReturned: false, expectedWarning: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			page := testSucceededPage("Acme Okta", tc.authTimeReturned)

			if page.Title != "Test sign-in succeeded" || !strings.Contains(page.Message, "Acme Okta") {
				t.Errorf("unexpected page %+v", page)
			}
			if warned := strings.Contains(page.Message, "auth_time"); warned != tc.expectedWarning {
				t.Errorf("expected warning %v, got %q", tc.expectedWarning, page.Message)
			}
		})
	}
}
