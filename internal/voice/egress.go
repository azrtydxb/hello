package voice

// The egress guard (spec S-8): MCP URLs are checked when a server is saved
// and re-checked against the address actually dialled, so a hostname that
// resolves private at save time cannot rebinding to a public one between
// save and dial.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

// ErrEgress is wrapped by every egress refusal; the API answers it 400
// mcp_endpoint_not_private on save and 503 on discovery.
var ErrEgress = errors.New("mcp endpoint is not private")

// Egress decides where hello-control may dial. AllowPublic is
// HELLO_VOICE_ALLOW_PUBLIC_MCP, AllowLoopback HELLO_VOICE_ALLOW_LOOPBACK
// (the lab).
type Egress struct{ AllowPublic, AllowLoopback bool }

// Check reports whether raw may be dialled under the guard: https or http,
// and every literal or resolved address private — RFC 1918 or ULA — with
// loopback only behind the lab flag.
func (e *Egress) Check(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: cannot parse URL", ErrEgress)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("%w: scheme must be http or https", ErrEgress)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%w: no userinfo, query or fragment", ErrEgress)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: no host", ErrEgress)
	}
	addrs, isLiteral := literalOrResolved(host)
	if !isLiteral {
		// A hostname is resolved here and again at dial time (rebinding).
		var err error
		addrs, err = net.DefaultResolver.LookupNetIP(context.Background(), "ip", host)
		if err != nil || len(addrs) == 0 {
			return fmt.Errorf("%w: cannot resolve %s", ErrEgress, host)
		}
	}
	for _, a := range addrs {
		if !e.allowed(a) {
			return fmt.Errorf("%w: %s is not a private address", ErrEgress, a)
		}
	}
	return nil
}

// literalOrResolved splits a host into address literals and anything else.
func literalOrResolved(host string) ([]netip.Addr, bool) {
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a.Unmap()}, true
	}
	if ip := net.ParseIP(host); ip != nil {
		a, _ := netip.AddrFromSlice(ip)
		return []netip.Addr{a.Unmap()}, true
	}
	return nil, false
}

// allowed classifies one address: private (RFC 1918, ULA) always, loopback
// only behind the lab flag.
func (e *Egress) allowed(a netip.Addr) bool {
	if a.IsLoopback() {
		return e.AllowLoopback
	}
	if a.IsPrivate() {
		return true
	}
	// fc00::/7's IsPrivate covers the ULA range; a mapped v4 form is
	// already unmapped above. Anything else (public, link-local,
	// multicast, unspecified) is refused.
	return false
}

// Client returns an HTTP client whose dialer re-checks every address at
// connect time, refuses redirects, and caps responses at maxResponseBytes
// (spec S-7). Timeout bounds the whole call.
func (e *Egress) Client(timeout time.Duration) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{
		Timeout: 5 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			return e.checkDialAddress(address)
		},
	}).DialContext
	return &http.Client{
		Timeout:   timeout,
		Transport: &cappedTransport{next: tr},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("voice: redirects are not followed")
		},
	}
}

// maxResponseBytes is the discovery and test response cap (spec S-7).
const maxResponseBytes = 1 << 20

// ErrTooLarge is returned when a response exceeds the cap.
var ErrTooLarge = errors.New("voice: response exceeds 1 MiB")

// cappedTransport enforces the response size cap on every response.
type cappedTransport struct{ next http.RoundTripper }

func (t *cappedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = &cappedBody{ReadCloser: resp.Body}
	return resp, nil
}

// cappedBody errors once more than maxResponseBytes has been read; exactly
// the cap is fine and reads EOF.
type cappedBody struct {
	io.ReadCloser
	total int
}

func (b *cappedBody) Read(p []byte) (int, error) {
	if b.total > maxResponseBytes {
		return 0, ErrTooLarge
	}
	if room := maxResponseBytes + 1 - b.total; len(p) > room {
		p = p[:room]
	}
	n, err := b.ReadCloser.Read(p)
	b.total += n
	if b.total > maxResponseBytes {
		return n, ErrTooLarge
	}
	return n, err
}

// clientOf builds the guarded client for a server's own timeout.
func (e *Egress) clientOf(timeoutMS int) *http.Client {
	if timeoutMS <= 0 {
		timeoutMS = 10_000
	}
	return e.Client(time.Duration(timeoutMS) * time.Millisecond)
}

// checkDialAddress is the connect-time re-check against rebinding (S-8).
func (e *Egress) checkDialAddress(address string) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: cannot parse dial address", ErrEgress)
	}
	if !e.allowed(ap.Addr().Unmap()) {
		return fmt.Errorf("%w: dial to %s refused", ErrEgress, ap.Addr())
	}
	return nil
}
