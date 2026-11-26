// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package idp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/propagation"
	"golang.org/x/net/http/httpproxy"
	"golang.org/x/net/idna"

	"github.com/canonical/sso-service/internal/limits"
)

// nonPublic are ranges netip's own predicates let through although they can
// lead inside a network: address space used as private without being so by
// RFC 1918, and the IPv6 forms that carry an arbitrary IPv4 address.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking, used as internal space
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, used as internal space
	netip.MustParsePrefix("::/96"),           // IPv4-compatible
	netip.MustParsePrefix("::ffff:0:0:0/96"), // SIIT, IPv4-translated
	netip.MustParsePrefix("64:ff9b::/96"),    // NAT64
	netip.MustParsePrefix("64:ff9b:1::/48"),  // NAT64, local use
	netip.MustParsePrefix("2001::/32"),       // Teredo
	netip.MustParsePrefix("2002::/16"),       // 6to4
}

func PublicUnicast(addr netip.Addr) bool {
	// A public address needs no zone, and netip's predicates ignore one.
	if addr.Zone() != "" {
		return false
	}
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
	if err != nil || !u.IsAbs() || u.Hostname() == "" || u.User != nil {
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

// proxiedKey holds, in a request's context, an *atomic.Bool that the
// transport's Proxy sets when it sends the request through a proxy.
type proxiedKey struct{}

// dialContext refuses a non-public address when it dials, after the name was
// resolved, so DNS rebinding gains nothing. A proxy's address is dialled
// unchecked only for a request sent through a proxy: the operator names it.
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
		// net/http dials with a context that keeps the request's values.
		if proxied, _ := ctx.Value(proxiedKey{}).(*atomic.Bool); proxied != nil && proxied.Load() && proxies[strings.ToLower(address)] {
			return proxyDialer.DialContext(ctx, network, address)
		}

		return dialer.DialContext(ctx, network, address)
	}
}

// proxyAddresses are the host:port pairs the transport dials for the
// configuration's proxies, read as httpproxy reads them, in lower case.
func proxyAddresses(config *httpproxy.Config) map[string]bool {
	ports := map[string]string{"http": "80", "https": "443", "socks5": "1080", "socks5h": "1080"}
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
		// The transport dials a non-ASCII name in punycode.
		host := u.Hostname()
		if ascii, err := idna.Lookup.ToASCII(host); err == nil {
			host = ascii
		}
		addresses[strings.ToLower(net.JoinHostPort(host, port))] = true
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
	r = r.WithContext(context.WithValue(r.Context(), proxiedKey{}, new(atomic.Bool)))
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
	// The body is read here, so a failed or oversized read is the request's
	// error: the libraries' own reads keep only its text.
	body, err := io.ReadAll(io.LimitReader(response.Body, limits.MaxResponseBytes+1))
	_ = response.Body.Close()
	if err != nil {
		return nil, err
	}
	if len(body) > limits.MaxResponseBytes {
		return nil, ErrResponseTooLarge
	}
	response.Body = io.NopCloser(bytes.NewReader(body))

	return response, nil
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
			if proxied, _ := r.Context().Value(proxiedKey{}).(*atomic.Bool); proxied != nil {
				proxied.Store(true)
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
		Timeout: timeout,
		// The guard is inside the span, so its refusals are the span's errors.
		// No trace context goes to an identity provider: it is outside the
		// platform.
		Transport: otelhttp.NewTransport(schemeGuard{policy: policy, next: transport},
			otelhttp.WithPropagators(propagation.NewCompositeTextMapPropagator())),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
