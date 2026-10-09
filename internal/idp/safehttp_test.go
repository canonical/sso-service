// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package idp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/canonical/sso-service/internal/limits"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPublicUnicast(t *testing.T) {
	testCases := []struct {
		name     string
		addr     string
		expected bool
	}{
		{name: "IPv4", addr: "8.8.8.8", expected: true},
		{name: "IPv6", addr: "2606:4700::1111", expected: true},
		{name: "10/8", addr: "10.0.0.1"},
		{name: "172.16/12", addr: "172.16.0.1"},
		{name: "192.168/16", addr: "192.168.1.1"},
		{name: "loopback", addr: "127.0.0.1"},
		{name: "link-local", addr: "169.254.169.254"},
		{name: "unspecified", addr: "0.0.0.0"},
		{name: "IPv6 unspecified", addr: "::"},
		{name: "IPv6 loopback", addr: "::1"},
		{name: "unique local", addr: "fd00::1"},
		{name: "IPv6 link-local", addr: "fe80::1"},
		{name: "multicast", addr: "224.0.0.1"},
		{name: "IPv6 multicast", addr: "ff02::1"},
		{name: "broadcast", addr: "255.255.255.255"},
		// An IPv4 address written as IPv6 is judged as the IPv4 address it is.
		{name: "mapped private", addr: "::ffff:10.0.0.1"},
		{name: "mapped loopback", addr: "::ffff:127.0.0.1"},
		{name: "mapped carrier-grade NAT", addr: "::ffff:100.64.0.1"},
		{name: "mapped public", addr: "::ffff:8.8.8.8", expected: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PublicUnicast(netip.MustParseAddr(tc.addr)); got != tc.expected {
				t.Errorf("expected %v for %s, got %v", tc.expected, tc.addr, got)
			}
		})
	}

	t.Run("no address", func(t *testing.T) {
		if PublicUnicast(netip.Addr{}) {
			t.Error("expected no address not to be a public one")
		}
	})
}

// The ranges added to netip's predicates: each reaches inside a network
// although it reads as a global unicast address. Both ends of every range
// are refused; the addresses next to it are not.
func TestPublicUnicast_NonPublicRanges(t *testing.T) {
	testCases := []struct {
		name    string
		inside  []string
		outside []string
	}{
		{
			name:    "100.64.0.0/10",
			inside:  []string{"100.64.0.0", "100.64.0.1", "100.100.100.200", "100.127.255.255"},
			outside: []string{"100.63.255.255", "100.128.0.0"},
		},
		{
			name:    "198.18.0.0/15",
			inside:  []string{"198.18.0.0", "198.18.0.1", "198.19.255.255"},
			outside: []string{"198.17.255.255", "198.20.0.0"},
		},
		{
			name:    "240.0.0.0/4",
			inside:  []string{"240.0.0.0", "240.0.0.1", "250.1.2.3", "255.255.255.254"},
			outside: []string{"223.255.255.255"},
		},
		{
			name:    "64:ff9b::/96",
			inside:  []string{"64:ff9b::", "64:ff9b::a00:1", "64:ff9b::7f00:1", "64:ff9b::ffff:ffff"},
			outside: []string{"64:ff9a:ffff:ffff:ffff:ffff:ffff:ffff", "64:ff9b:0:0:0:1::"},
		},
		{
			name:    "64:ff9b:1::/48",
			inside:  []string{"64:ff9b:1::", "64:ff9b:1::a00:1", "64:ff9b:1:ffff:ffff:ffff:ffff:ffff"},
			outside: []string{"64:ff9b:2::", "64:ff9b:0:ffff:ffff:ffff:ffff:ffff"},
		},
		{
			name:    "2002::/16",
			inside:  []string{"2002::", "2002:a00:1::1", "2002:7f00:1::", "2002:ffff:ffff:ffff:ffff:ffff:ffff:ffff"},
			outside: []string{"2001:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "2003::"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			for _, addr := range tc.inside {
				if PublicUnicast(netip.MustParseAddr(addr)) {
					t.Errorf("expected %s, inside the range, not to be public", addr)
				}
			}
			for _, addr := range tc.outside {
				if !PublicUnicast(netip.MustParseAddr(addr)) {
					t.Errorf("expected %s, outside the range, to be public", addr)
				}
			}
		})
	}
}

func TestHostPolicy_CheckURL(t *testing.T) {
	testCases := []struct {
		name      string
		dev       bool
		url       string
		expectErr bool
	}{
		{name: "https", url: "https://idp.example/token"},
		{name: "http", url: "http://idp.example/token", expectErr: true},
		{name: "userinfo", url: "https://user:pw@idp.example/token", expectErr: true},
		{name: "relative", url: "/token", expectErr: true},
		{name: "no host", url: "https:///token", expectErr: true},
		{name: "dev http", dev: true, url: "http://dex:5556/token"},
		{name: "dev userinfo", dev: true, url: "http://user:pw@dex:5556/token", expectErr: true},
		{name: "dev ftp", dev: true, url: "ftp://dex/token", expectErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := hostPolicy{dev: tc.dev}.checkURL(tc.url)
			if tc.expectErr {
				if !errors.Is(err, ErrInsecureURL) {
					t.Errorf("expected %v, got %v", ErrInsecureURL, err)
				}
				return
			}
			if err != nil || u.String() != tc.url {
				t.Errorf("expected the URL parsed, got %v %v", u, err)
			}
		})
	}
}

// The address rule is applied at dial time, to the address the name
// resolved to.
func TestHostPolicy_DialContext(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, _ := net.SplitHostPort(listener.Addr().String())

	t.Run("loopback", func(t *testing.T) {
		_, err := hostPolicy{}.dialContext(time.Second)(context.Background(), "tcp", listener.Addr().String())
		if !errors.Is(err, ErrForbiddenAddress) {
			t.Errorf("expected %v, got %v", ErrForbiddenAddress, err)
		}
	})

	t.Run("name resolving to loopback", func(t *testing.T) {
		_, err := hostPolicy{}.dialContext(time.Second)(context.Background(), "tcp", net.JoinHostPort("localhost", port))
		if !errors.Is(err, ErrForbiddenAddress) {
			t.Errorf("expected %v, got %v", ErrForbiddenAddress, err)
		}
	})

	t.Run("dev", func(t *testing.T) {
		conn, err := hostPolicy{dev: true}.dialContext(time.Second)(context.Background(), "tcp", listener.Addr().String())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		conn.Close()
	})
}

// The scheme rule is applied to every request the client sends, whatever
// built its URL.
func TestSchemeGuard_RoundTrip(t *testing.T) {
	testCases := []struct {
		name            string
		dev             bool
		url             string
		status          int
		body            string
		expectedErr     error
		expectedReached bool
	}{
		{name: "https", url: "https://idp.example/token", status: http.StatusOK, body: "ok", expectedReached: true},
		{name: "http", url: "http://idp.example/token", status: http.StatusOK, expectedErr: ErrInsecureURL},
		{name: "userinfo", url: "https://user:pw@idp.example/token", status: http.StatusOK, expectedErr: ErrInsecureURL},
		{name: "dev http", dev: true, url: "http://dex:5556/token", status: http.StatusOK, body: "ok", expectedReached: true},
		{name: "4xx", url: "https://idp.example/token", status: http.StatusBadRequest, body: "no", expectedReached: true},
		{name: "5xx", url: "https://idp.example/token", status: http.StatusBadGateway, expectedErr: ErrUnavailable, expectedReached: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			guard := schemeGuard{policy: hostPolicy{dev: tc.dev}, next: roundTrip(func(r *http.Request) (*http.Response, error) {
				reached = true
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			request, _ := http.NewRequest(http.MethodGet, tc.url, nil)

			response, err := guard.RoundTrip(request)

			if !errors.Is(err, tc.expectedErr) {
				t.Errorf("expected %v, got %v", tc.expectedErr, err)
			}
			if reached != tc.expectedReached {
				t.Errorf("expected the request sent: %v, got %v", tc.expectedReached, reached)
			}
			if tc.expectedErr == nil {
				body, _ := io.ReadAll(response.Body)
				if response.StatusCode != tc.status || string(body) != tc.body {
					t.Errorf("expected the answer passed on, got %d %q", response.StatusCode, body)
				}
			}
		})
	}

	t.Run("transport error", func(t *testing.T) {
		refused := errors.New("connection refused")
		guard := schemeGuard{next: roundTrip(func(*http.Request) (*http.Response, error) { return nil, refused })}
		request, _ := http.NewRequest(http.MethodGet, "https://idp.example/token", nil)

		if _, err := guard.RoundTrip(request); !errors.Is(err, refused) {
			t.Errorf("expected %v, got %v", refused, err)
		}
	})
}

func TestCappedBody_Read(t *testing.T) {
	testCases := []struct {
		name        string
		size        int
		expectedErr error
	}{
		{name: "at the limit", size: limits.MaxResponseBytes},
		{name: "over the limit", size: limits.MaxResponseBytes + 1, expectedErr: ErrResponseTooLarge},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			body := &cappedBody{ReadCloser: io.NopCloser(strings.NewReader(strings.Repeat("x", tc.size)))}

			_, err := io.ReadAll(body)
			if !errors.Is(err, tc.expectedErr) {
				t.Errorf("expected %v, got %v", tc.expectedErr, err)
			}
		})
	}
}

func TestNewHTTPClient(t *testing.T) {
	client := newHTTPClient(hostPolicy{}, 3*time.Second)

	if client.Timeout != 3*time.Second {
		t.Errorf("expected a timeout of 3s for one request, got %s", client.Timeout)
	}
	if _, ok := client.Transport.(schemeGuard); !ok {
		t.Errorf("expected every request to go through the scheme guard, got %T", client.Transport)
	}
	if err := client.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Errorf("expected no redirect followed, got %v", err)
	}
}
