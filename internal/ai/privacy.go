package ai

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// errNotPrivate is the dialer's refusal of a host that resolves to a public
// address while public endpoints are not allowed (spec S-3).
var errNotPrivate = errors.New("the endpoint host resolves to a public address")

// sharedSpace is RFC 6598's carrier-grade NAT range, which netip does not
// count as private.
var sharedSpace = netip.MustParsePrefix("100.64.0.0/10")

// privateAddr reports whether a is loopback, RFC 1918, RFC 6598, an IPv6
// unique local or a link-local address: the only places PBX data may go
// without HELLO_AI_ALLOW_PUBLIC_ENDPOINT.
func privateAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || sharedSpace.Contains(a)
}

// resolver resolves a host to addresses; *net.Resolver's LookupNetIP fits,
// and tests replace it to simulate DNS rebinding.
type resolver func(ctx context.Context, host string) ([]netip.Addr, error)

func systemResolver(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// resolve returns host's addresses: an IP literal is itself and localhost
// is loopback without asking DNS.
func (r resolver) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if a, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return []netip.Addr{a}, nil
	}
	if strings.EqualFold(host, "localhost") {
		return []netip.Addr{netip.IPv6Loopback(), netip.AddrFrom4([4]byte{127, 0, 0, 1})}, nil
	}
	addrs, err := r(ctx, host)
	if err == nil && len(addrs) == 0 {
		err = fmt.Errorf("%s has no addresses", host)
	}
	return addrs, err
}

// allPrivate reports whether every address is private; one public answer
// among private ones fails, so a mixed DNS answer cannot leak.
func allPrivate(addrs []netip.Addr) bool {
	for _, a := range addrs {
		if !privateAddr(a) {
			return false
		}
	}
	return len(addrs) > 0
}

// endpointHost is the base URL's host:port (never its path or key).
func endpointHost(baseURL string) (hostport, host string, err error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "", fmt.Errorf("HELLO_AI_BASE_URL is not an http(s) URL")
	}
	return u.Host, u.Hostname(), nil
}

// checkAtStart is the start-time privacy check (spec S-3): private reports
// whether the host resolved only to private addresses, resolved whether it
// resolved at all. A host that does not resolve keeps AI on; its calls fail
// with provider_error until it does.
func (r resolver) checkAtStart(ctx context.Context, host string) (private, resolved bool) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := r.resolve(ctx, host)
	if err != nil {
		return false, false
	}
	return allPrivate(addrs), true
}

// dialFunc is net.Dialer.DialContext's shape.
type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// privacyDialer resolves the host itself, checks every address and dials
// only a checked one, so DNS rebinding between the start check and a call
// (or between the check and the dial) cannot reach a public address. With
// allowPublic it still dials the resolved address, unchecked.
func privacyDialer(r resolver, dial dialFunc, allowPublic bool) dialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		addrs, err := r.resolve(ctx, host)
		if err != nil {
			return nil, err
		}
		if !allowPublic && !allPrivate(addrs) {
			return nil, errNotPrivate
		}
		var last error
		for _, a := range addrs {
			c, err := dial(ctx, network, net.JoinHostPort(a.Unmap().String(), port))
			if err == nil {
				return c, nil
			}
			last = err
		}
		return nil, last
	}
}
