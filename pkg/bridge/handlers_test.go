// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package bridge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/mock/gomock"

	"github.com/canonical/sso-service/internal/limits"
)

// serve answers one request with the browser pages over a mocked service.
// spanName is the span the handler starts; "" when no handler is reached, or
// it starts none.
func serve(t *testing.T, r *http.Request, spanName string, setupMocks func(*MockServiceInterface)) *httptest.ResponseRecorder {
	t.Helper()
	ctrl := gomock.NewController(t)
	mockService := NewMockServiceInterface(ctrl)
	mockTracer := NewMockTracingInterface(ctrl)
	mockLogger := NewMockLoggerInterface(ctrl)
	setupLoggerMock(mockLogger)

	if spanName != "" {
		mockTracer.EXPECT().Start(gomock.Any(), spanName).Return(context.Background(), trace.SpanFromContext(context.Background()))
	}
	if setupMocks != nil {
		setupMocks(mockService)
	}

	mux := chi.NewMux()
	NewAPI(mockService, mockTracer, mockLogger).RegisterEndpoints(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	return w
}

// Every connection has a callback of its own: nothing answers without a
// connection in the path.
func TestAPI_RegisterEndpoints(t *testing.T) {
	testCases := []struct {
		name           string
		method         string
		path           string
		expectedStatus int
	}{
		{name: "callback without a connection", method: http.MethodGet, path: "/callback?state=st&code=c", expectedStatus: http.StatusNotFound},
		{name: "callback with a trailing slash", method: http.MethodGet, path: "/callback/?state=st&code=c", expectedStatus: http.StatusNotFound},
		{name: "callback with a longer path", method: http.MethodGet, path: "/callback/" + connA + "/extra?state=st&code=c", expectedStatus: http.StatusNotFound},
		{name: "callback by post", method: http.MethodPost, path: "/callback/" + connA + "?state=st&code=c", expectedStatus: http.StatusMethodNotAllowed},
		{name: "login by post", method: http.MethodPost, path: "/login?login_challenge=lc", expectedStatus: http.StatusMethodNotAllowed},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			w := serve(t, httptest.NewRequest(tc.method, tc.path, nil), "", nil)

			if w.Code != tc.expectedStatus {
				t.Errorf("expected status %d, got %d", tc.expectedStatus, w.Code)
			}
		})
	}
}

func TestAPI_login(t *testing.T) {
	testCases := []struct {
		name             string
		path             string
		setupMocks       func(*MockServiceInterface)
		expectedStatus   int
		expectedLocation string
		expectedCookie   bool
	}{
		{
			name: "success",
			path: "/login?login_challenge=lc",
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().StartLogin(gomock.Any(), "lc").Return("https://idp.example/authorize", &Binding{State: "st", Value: "bind"}, nil)
			},
			expectedStatus:   http.StatusFound,
			expectedLocation: "https://idp.example/authorize",
			expectedCookie:   true,
		},
		{
			name: "rejected at hydra",
			path: "/login?login_challenge=lc",
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().StartLogin(gomock.Any(), "lc").Return(rejected, nil, nil)
			},
			expectedStatus:   http.StatusFound,
			expectedLocation: rejected,
		},
		{
			name:           "no challenge",
			path:           "/login",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "unknown challenge",
			path: "/login?login_challenge=lc",
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().StartLogin(gomock.Any(), "lc").Return("", nil, ErrUnknownLogin)
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "service error",
			path: "/login?login_challenge=lc",
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().StartLogin(gomock.Any(), "lc").Return("", nil, errors.New("dial tcp 10.1.2.3:4445: connection refused"))
			},
			expectedStatus: http.StatusInternalServerError,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			w := serve(t, httptest.NewRequest(http.MethodGet, tc.path, nil), "bridge.API.login", tc.setupMocks)

			if w.Code != tc.expectedStatus || w.Header().Get("Location") != tc.expectedLocation {
				t.Fatalf("expected status %d to %q, got %d to %q", tc.expectedStatus, tc.expectedLocation, w.Code, w.Header().Get("Location"))
			}
			if strings.Contains(w.Body.String(), "10.1.2.3") {
				t.Errorf("the page shows the error: %s", w.Body.String())
			}
			cookies := w.Result().Cookies()
			if !tc.expectedCookie {
				if len(cookies) != 0 {
					t.Errorf("expected no cookie, got %+v", cookies)
				}
				return
			}
			if len(cookies) != 1 {
				t.Fatalf("expected the binding cookie, got %+v", cookies)
			}
			cookie := cookies[0]
			if cookie.Name != bindingCookieName("st") || cookie.Value != "bind" || cookie.Path != "/" ||
				!cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode ||
				cookie.MaxAge != int(limits.AttemptTTL.Seconds()) {
				t.Errorf("unexpected binding cookie %+v", cookie)
			}
		})
	}
}

func TestAPI_callback(t *testing.T) {
	testCases := []struct {
		name       string
		path       string
		cookies    map[string]string
		setupMocks func(*MockServiceInterface)

		expectedStatus   int
		expectedLocation string
		expectedBody     string
		// expectedCleared is the state whose binding cookie is cleared, and
		// no other cookie touched; "" when none is.
		expectedCleared string
		// expectedReceipt is the one other cookie set: the receipt's.
		expectedReceipt bool
	}{
		{
			// The connection in the path reaches the service in lower case.
			name:    "success",
			path:    "/callback/" + strings.ToUpper(connA) + "?state=st&code=c&error=e&error_description=d",
			cookies: map[string]string{"st": "bind", "other": "other-bind"},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().Callback(gomock.Any(), &Callback{
					ConnectionID: connA, State: "st", Code: "c", Error: "e", ErrorDescription: "d", Binding: "bind",
				}).Return(&Result{Redirect: accepted, Receipt: &Receipt{Ticket: "ticket", Value: "receipt", MaxAge: 1740}}, nil)
			},
			expectedStatus:   http.StatusSeeOther,
			expectedLocation: accepted,
			expectedCleared:  "st",
			expectedReceipt:  true,
		},
		{
			name:    "sign-in refused",
			path:    "/callback/" + connA + "?state=st&code=c",
			cookies: map[string]string{"st": "bind"},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().Callback(gomock.Any(), &Callback{ConnectionID: connA, State: "st", Code: "c", Binding: "bind"}).
					Return(&Result{Redirect: rejected}, nil)
			},
			expectedStatus:   http.StatusSeeOther,
			expectedLocation: rejected,
			expectedCleared:  "st",
		},
		{
			name:    "another state's cookie",
			path:    "/callback/" + connA + "?state=st&code=c",
			cookies: map[string]string{"other": "other-bind"},
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().Callback(gomock.Any(), &Callback{ConnectionID: connA, State: "st", Code: "c"}).Return(nil, ErrBindingMissing)
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "page",
			path: "/callback/" + connA + "?state=sealed-state&code=c",
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().Callback(gomock.Any(), &Callback{ConnectionID: connA, State: "sealed-state", Code: "c"}).
					Return(&Result{Page: messagePage("Test sign-in succeeded", "ok")}, nil)
			},
			expectedStatus:  http.StatusOK,
			expectedBody:    "Test sign-in succeeded",
			expectedCleared: "sealed-state",
		},
		{
			name: "binding mismatch",
			path: "/callback/" + connA + "?state=st&code=c",
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().Callback(gomock.Any(), gomock.Any()).Return(nil, ErrBindingMismatch)
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "unknown state",
			path: "/callback/" + connA + "?state=st&code=c",
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().Callback(gomock.Any(), gomock.Any()).Return(nil, ErrUnknownState)
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "service error",
			path: "/callback/" + connA + "?state=st&code=c",
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().Callback(gomock.Any(), gomock.Any()).Return(nil, errors.New("failed to reject login request: dial tcp 10.1.2.3:4445"))
			},
			expectedStatus: http.StatusInternalServerError,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			for state, value := range tc.cookies {
				r.AddCookie(&http.Cookie{Name: bindingCookieName(state), Value: value})
			}

			w := serve(t, r, "bridge.API.callback", tc.setupMocks)

			if w.Code != tc.expectedStatus || w.Header().Get("Location") != tc.expectedLocation {
				t.Fatalf("expected status %d to %q, got %d to %q", tc.expectedStatus, tc.expectedLocation, w.Code, w.Header().Get("Location"))
			}
			if body := w.Body.String(); !strings.Contains(body, tc.expectedBody) || strings.Contains(body, "10.1.2.3") {
				t.Errorf("unexpected page: %s", body)
			}
			cookies := w.Result().Cookies()
			if tc.expectedCleared == "" {
				// A refusal consumes nothing: the cookie stays for the real callback.
				if len(cookies) != 0 {
					t.Errorf("expected no cookie set, got %+v", cookies)
				}
				return
			}
			if len(cookies) == 0 || cookies[0].Name != bindingCookieName(tc.expectedCleared) || cookies[0].MaxAge >= 0 {
				t.Fatalf("expected the binding cookie of the state cleared, got %+v", cookies)
			}
			if !tc.expectedReceipt {
				if len(cookies) != 1 {
					t.Errorf("expected no other cookie, got %+v", cookies)
				}
				return
			}
			if len(cookies) != 2 {
				t.Fatalf("expected one receipt cookie, got %+v", cookies)
			}
			// The name ends in the first 16 hex digits of the SHA-256 of "ticket".
			receipt := cookies[1]
			if receipt.Name != "__Host-sso_receipt_14069429150abcbf" || receipt.Value != "receipt" || receipt.Path != "/" ||
				receipt.Domain != "" || !receipt.Secure || !receipt.HttpOnly || receipt.SameSite != http.SameSiteLaxMode ||
				receipt.MaxAge != 1740 {
				t.Errorf("unexpected receipt cookie %+v", receipt)
			}
		})
	}
}

func TestAPI_consent(t *testing.T) {
	testCases := []struct {
		name             string
		path             string
		setupMocks       func(*MockServiceInterface)
		expectedStatus   int
		expectedLocation string
	}{
		{
			name: "success",
			path: "/consent?consent_challenge=cc",
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().Consent(gomock.Any(), "cc").Return("https://kratos/cb", nil)
			},
			expectedStatus:   http.StatusFound,
			expectedLocation: "https://kratos/cb",
		},
		{
			name:           "no challenge",
			path:           "/consent",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "unknown challenge",
			path: "/consent?consent_challenge=cc",
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().Consent(gomock.Any(), "cc").Return("", ErrUnknownLogin)
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "service error",
			path: "/consent?consent_challenge=cc",
			setupMocks: func(mockSvc *MockServiceInterface) {
				mockSvc.EXPECT().Consent(gomock.Any(), "cc").Return("", errors.New("dial tcp 10.1.2.3:4445: connection refused"))
			},
			expectedStatus: http.StatusInternalServerError,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			w := serve(t, httptest.NewRequest(http.MethodGet, tc.path, nil), "bridge.API.consent", tc.setupMocks)

			if w.Code != tc.expectedStatus || w.Header().Get("Location") != tc.expectedLocation {
				t.Errorf("expected status %d to %q, got %d to %q", tc.expectedStatus, tc.expectedLocation, w.Code, w.Header().Get("Location"))
			}
			if strings.Contains(w.Body.String(), "10.1.2.3") {
				t.Errorf("the page shows the error: %s", w.Body.String())
			}
		})
	}
}

// The page hydra-sso sends the browser to shows nothing of what it was sent.
func TestAPI_error(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/error?error=request_forbidden&error_description=%3Cscript%3Eleak&error_hint=hint", nil)

	w := serve(t, r, "", nil)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
	for _, sent := range []string{"request_forbidden", "leak", "hint"} {
		if strings.Contains(w.Body.String(), sent) {
			t.Errorf("the page shows %q: %s", sent, w.Body.String())
		}
	}
}

func TestBindingCookieName(t *testing.T) {
	if bindingCookieName("a") == bindingCookieName("b") || bindingCookieName("a") != bindingCookieName("a") {
		t.Error("expected one name per state")
	}
	name := bindingCookieName(strings.Repeat("s", 4096))
	if !strings.HasPrefix(name, "__Host-sso_bind_") || len(name) != len("__Host-sso_bind_")+16 {
		t.Errorf("expected a bounded __Host- name whatever the state, got %q", name)
	}
}
