// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package idp

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"golang.org/x/net/http/httpproxy"

	"github.com/canonical/sso-service/internal/limits"
)

// nonPublic are ranges netip's own predicates let through although they can
// lead inside a network: address space used as private without being so by
// RFC 1918, and the IPv6 forms that carry an arbitrary IPv4 address.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),  // carrier-grade NAT
	netip.MustParsePrefix("198.18.0.0/15"),  // benchmarking, used as internal space
	netip.MustParsePrefix("240.0.0.0/4"),    // reserved, used as internal space
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64
	netip.MustParsePrefix("64:ff9b:1::/48"), // NAT64, local use
	netip.MustParsePrefix("2002::/16"),      // 6to4
}

func PublicUnicast(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() ||
		addr.IsLinkLocalUnicast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	for _, prefix := range nonPublic {
		if prefix.Contains(addr) {
			return false
		}
	}

	return true
}

// hostPolicy allows https URLs and public unicast addresses only; dev lifts
// both rules.
type hostPolicy struct {
	dev bool
}

func (p hostPolicy) checkURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("not an absolute URL without userinfo: %w", ErrInsecureURL)
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && p.dev:
	default:
		return nil, ErrInsecureURL
	}

	return u, nil
}

// checkHost refuses a host that is, or resolves to, a non-public address. It
// is for a request sent through a proxy, where the dial check sees only the
// proxy. The proxy resolves the name again: what it can reach is its own to
// restrict.
func (p hostPolicy) checkHost(ctx context.Context, host string) error {
	if p.dev {
		return nil
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return err
	}
	for _, addr := range addrs {
		if !PublicUnicast(addr) {
			return ErrForbiddenAddress
		}
	}

	return nil
}

// dialContext refuses a non-public address when it dials, after the name was
// resolved, so DNS rebinding gains nothing. The proxies' addresses are dialled
// unchecked: the operator names them.
func (p hostPolicy) dialContext(timeout time.Duration, proxies map[string]bool) func(ctx context.Context, network, address string) (net.Conn, error) {
	proxyDialer := &net.Dialer{Timeout: timeout}
	dialer := &net.Dialer{Timeout: timeout}
	if !p.dev {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return ErrForbiddenAddress
			}
			addr, err := netip.ParseAddr(host)
			if err != nil || !PublicUnicast(addr) {
				return ErrForbiddenAddress
			}

			return nil
		}
	}

	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if proxies[address] {
			return proxyDialer.DialContext(ctx, network, address)
		}

		return dialer.DialContext(ctx, network, address)
	}
}

// proxyAddresses are the host:port pairs the transport dials for the
// configuration's proxies, read as httpproxy reads them.
func proxyAddresses(config *httpproxy.Config) map[string]bool {
	ports := map[string]string{"http": "80", "https": "443", "socks5": "1080"}
	addresses := make(map[string]bool)
	for _, raw := range []string{config.HTTPProxy, config.HTTPSProxy} {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			if u, err = url.Parse("http://" + raw); err != nil {
				continue
			}
		}
		port := u.Port()
		if port == "" {
			port = ports[u.Scheme]
		}
		addresses[net.JoinHostPort(u.Hostname(), port)] = true
	}

	return addresses
}

// schemeGuard refuses a request that is not https, whichever library built
// it.
type schemeGuard struct {
	policy hostPolicy
	next   http.RoundTripper
}

func (g schemeGuard) RoundTrip(r *http.Request) (*http.Response, error) {
	if _, err := g.policy.checkURL(r.URL.String()); err != nil {
		return nil, err
	}
	response, err := g.next.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	// A 5xx is the identity provider being unavailable, whichever of its
	// endpoints gave it and whichever library asked.
	if response.StatusCode >= http.StatusInternalServerError {
		_ = response.Body.Close()

		return nil, fmt.Errorf("%w: it answered %d", ErrUnavailable, response.StatusCode)
	}
	response.Body = &cappedBody{ReadCloser: response.Body}

	return response, nil
}

// cappedBody fails a read past limits.MaxResponseBytes.
type cappedBody struct {
	io.ReadCloser
	read int
}

func (b *cappedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.read += n
	if b.read > limits.MaxResponseBytes {
		return n, ErrResponseTooLarge
	}

	return n, err
}

// newHTTPClient sends a request through the proxy the configuration names for
// its URL, after checking the request's host, and follows no redirect.
func newHTTPClient(policy hostPolicy, proxy *httpproxy.Config, timeout time.Duration) *http.Client {
	proxyFor := proxy.ProxyFunc()
	transport := &http.Transport{
		Proxy: func(r *http.Request) (*url.URL, error) {
			proxyURL, err := proxyFor(r.URL)
			if err != nil || proxyURL == nil {
				return proxyURL, err
			}
			if err := policy.checkHost(r.Context(), r.URL.Hostname()); err != nil {
				return nil, err
			}

			return proxyURL, nil
		},
		DialContext:           policy.dialContext(timeout, proxyAddresses(proxy)),
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: schemeGuard{policy: policy, next: otelhttp.NewTransport(transport)},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
