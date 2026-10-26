// Package safehttp is the one outbound HTTP client for anything an
// administrator, a tenant or a remote system can point Hello at: webhook
// URLs, CRM and directory APIs, OIDC discovery, client ID metadata
// documents (spec integrations I-41, T-3).
//
// It defends against server-side request forgery in the places a URL check
// cannot: the address is validated when the connection is made, after name
// resolution, so DNS rebinding and redirects cannot reach an internal
// service. Loopback, link-local (which includes the 169.254.169.254 cloud
// metadata address), unspecified and multicast addresses are never allowed.
// Private ranges (RFC 1918, unique-local IPv6, carrier-grade NAT) are
// refused unless the caller names them in an allow-list.
package safehttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// Defaults.
const (
	DefaultTimeout = 10 * time.Second
	// DefaultMaxBody bounds a response read through ReadBody.
	DefaultMaxBody = 8 << 20
)

// ErrBlockedAddress is a connection refused because its address is not
// allowed. The message names the class, never the address.
var ErrBlockedAddress = errors.New("address is private, loopback or link-local")

// ErrTooLarge is a response body over the limit.
var ErrTooLarge = errors.New("response body too large")

// Options configure a client.
type Options struct {
	// Timeout bounds a whole request including reading headers; zero is
	// DefaultTimeout.
	Timeout time.Duration
	// Allow lists private ranges the client may reach (for example a
	// directory server or an in-cluster webhook receiver). Loopback,
	// link-local, unspecified and multicast addresses stay refused even
	// when a range contains them; ParseAllow rejects such ranges.
	Allow []netip.Prefix
	// AllowAllPrivate lets the client reach any private range (not
	// loopback, link-local, unspecified or multicast). It exists for the
	// client ID metadata fetcher's lab switch; prefer Allow.
	AllowAllPrivate bool
	// FollowSameHostRedirects permits up to this many redirects, each to
	// the same host and an http(s) URL. Zero follows none.
	FollowSameHostRedirects int
	// DialContext replaces the guarded dialer. Tests only: it removes the
	// protection.
	DialContext func(ctx context.Context, network, address string) (net.Conn, error)
}

// neverAllowed are the ranges no allow-list can open.
var neverAllowed = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

// privateRanges are blocked by default and opened by Allow.
var privateRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("fc00::/7"),
}

// translators embed an IPv4 address in an IPv6 one; the embedded address is
// the one that is reached.
var (
	nat64 = netip.MustParsePrefix("64:ff9b::/96")
	sixTo = netip.MustParsePrefix("2002::/16")
)

// embedded returns the IPv4 address a NAT64 or 6to4 address stands for.
func embedded(a netip.Addr) (netip.Addr, bool) {
	b := a.As16()
	switch {
	case nat64.Contains(a):
		return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), true
	case sixTo.Contains(a):
		return netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}), true
	}
	return netip.Addr{}, false
}

func within(ps []netip.Prefix, a netip.Addr) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Allowed reports whether a connection to a may be made under opts: public
// unicast always, a private address only when allowed.
func (o Options) allowed(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsUnspecified() || a.IsMulticast() || a.IsLoopback() || a.IsLinkLocalUnicast() ||
		within(neverAllowed, a) {
		return false
	}
	if e, ok := embedded(a); ok {
		return o.allowed(e)
	}
	if a.IsPrivate() || within(privateRanges, a) {
		return o.AllowAllPrivate || within(o.Allow, a)
	}
	return a.IsGlobalUnicast()
}

// Public reports whether a is a public unicast address, the class a client
// without an allow-list may reach.
func Public(a netip.Addr) bool { return Options{}.allowed(a) }

// ParseAllow parses a comma- or space-separated list of CIDR prefixes (or
// single addresses) for Options.Allow. A prefix that overlaps a range that
// can never be allowed (loopback, link-local, unspecified, multicast) or
// that is not wholly inside a private range (a public range needs no allowance) is an error, so a
// configuration mistake cannot open the metadata address.
func ParseAllow(list string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		p, err := netip.ParsePrefix(f)
		if err != nil {
			a, aerr := netip.ParseAddr(f)
			if aerr != nil {
				return nil, fmt.Errorf("%q is not a CIDR prefix or address", f)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		p = p.Masked()
		for _, bad := range neverAllowed {
			if p.Overlaps(bad) {
				return nil, fmt.Errorf("%q overlaps a range that is never allowed (loopback, link-local, unspecified or multicast)", f)
			}
		}
		private := false
		for _, r := range privateRanges {
			if r.Bits() <= p.Bits() && r.Contains(p.Addr()) {
				private = true
			}
		}
		if !private {
			return nil, fmt.Errorf("%q is not wholly inside a private range; public addresses need no allowance", f)
		}
		out = append(out, p)
	}
	return out, nil
}

// NewTransport returns a transport whose dialer refuses disallowed
// addresses after resolution. It ignores proxy environment variables.
func NewTransport(o Options) *http.Transport {
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	dial := o.DialContext
	if dial == nil {
		d := &net.Dialer{Timeout: timeout, Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return err
			}
			if !o.allowed(ap.Addr()) {
				return ErrBlockedAddress
			}
			return nil
		}}
		dial = d.DialContext
	}
	return &http.Transport{
		DialContext:           dial,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       time.Minute,
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
	}
}

// NewClient returns a client over NewTransport that follows no redirects
// unless Options.FollowSameHostRedirects allows same-host ones.
func NewClient(o Options) *http.Client {
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	limit := o.FollowSameHostRedirects
	return &http.Client{
		Transport: NewTransport(o),
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if limit <= 0 || len(via) > limit || req.URL.Host != via[0].URL.Host ||
				(req.URL.Scheme != "https" && req.URL.Scheme != "http") {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

// ValidateURL checks a user-supplied URL before it is stored or used: it
// must be absolute http or https (https only when httpsOnly), have a host,
// carry no userinfo or fragment, and a host that is an IP literal must be
// allowed under o. Names are not resolved here; the dialer checks the
// resolved address on every connection.
func ValidateURL(raw string, httpsOnly bool, o Options) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return nil, errors.New("the URL must be absolute with a host")
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && !httpsOnly:
	default:
		if httpsOnly {
			return nil, errors.New("the URL must use https")
		}
		return nil, errors.New("the URL must use http or https")
	}
	if u.User != nil {
		return nil, errors.New("the URL must not contain credentials")
	}
	if u.Fragment != "" {
		return nil, errors.New("the URL must not contain a fragment")
	}
	if len(raw) > 2048 {
		return nil, errors.New("the URL is too long")
	}
	if a, err := netip.ParseAddr(strings.Trim(u.Hostname(), "[]")); err == nil && !o.allowed(a) {
		return nil, ErrBlockedAddress
	}
	if strings.EqualFold(u.Hostname(), "localhost") || strings.HasSuffix(strings.ToLower(u.Hostname()), ".localhost") {
		return nil, ErrBlockedAddress
	}
	return u, nil
}

// ReadBody reads at most max bytes of body (DefaultMaxBody when max <= 0).
// A longer body is ErrTooLarge; the rest is not read.
func ReadBody(body io.Reader, max int64) ([]byte, error) {
	if max <= 0 {
		max = DefaultMaxBody
	}
	b, err := io.ReadAll(io.LimitReader(body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, ErrTooLarge
	}
	return b, nil
}
