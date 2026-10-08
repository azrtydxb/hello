package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Client ID metadata document limits (spec S-8).
const (
	cimdTimeout  = 5 * time.Second
	cimdMaxBytes = 64 << 10
	cimdMaxCache = 24 * time.Hour
)

// errPrivateAddress is a dial refused because the address is private,
// loopback or link-local.
var errPrivateAddress = errors.New("address is private, loopback or link-local")

// cimdFetcher fetches client ID metadata documents with the SSRF guards of
// spec S-8.
type cimdFetcher struct {
	client  *http.Client
	metrics *Metrics
}

func newCIMDFetcher(c *http.Client, allowPrivate bool, m *Metrics) *cimdFetcher {
	var hc http.Client
	if c != nil {
		hc = *c
	} else {
		hc.Transport = guardedTransport(allowPrivate)
	}
	hc.Timeout = cimdTimeout
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &cimdFetcher{client: &hc, metrics: m}
}

// guardedTransport refuses, at dial time (after resolution, so DNS
// rebinding cannot slip through), any address that is not public unicast.
func guardedTransport(allowPrivate bool) *http.Transport {
	d := &net.Dialer{Timeout: cimdTimeout, Control: func(_, address string, _ syscall.RawConn) error {
		if allowPrivate {
			return nil
		}
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return err
		}
		if !publicAddr(ap.Addr()) {
			return errPrivateAddress
		}
		return nil
	}}
	return &http.Transport{
		DialContext:           d.DialContext,
		TLSHandshakeTimeout:   cimdTimeout,
		ResponseHeaderTimeout: cimdTimeout,
		MaxIdleConns:          4,
		IdleConnTimeout:       time.Minute,
		Proxy:                 nil,
	}
}

func publicAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsValid() && a.IsGlobalUnicast() && !a.IsPrivate() && !a.IsLoopback() &&
		!a.IsLinkLocalUnicast() && !a.IsUnspecified() &&
		!netip.MustParsePrefix("100.64.0.0/10").Contains(a) // carrier-grade NAT
}

// isCIMDClientID reports whether id is a client ID metadata document URL:
// https with a host and a path other than "/".
func isCIMDClientID(id string) bool {
	u, err := url.Parse(id)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.Path != "" && u.Path != "/" &&
		u.Fragment == "" && u.User == nil
}

// cimdDoc is the part of a client ID metadata document Hello reads.
type cimdDoc struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	ClientURI               string   `json:"client_uri"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

// fetch retrieves and validates the document at id. Its errors are safe to
// show on the error page.
func (f *cimdFetcher) fetch(ctx context.Context, id string, now time.Time) (Client, error) {
	c, err := f.get(ctx, id, now)
	if err != nil {
		f.metrics.fetched("error")
		return Client{}, err
	}
	f.metrics.fetched("ok")
	return c, nil
}

func (f *cimdFetcher) get(ctx context.Context, id string, now time.Time) (Client, error) {
	if !isCIMDClientID(id) {
		return Client{}, errors.New("client_id is not an https URL with a path")
	}
	ctx, cancel := context.WithTimeout(ctx, cimdTimeout)
	defer cancel()
	// The URL is the client's by design (a client ID metadata document);
	// isCIMDClientID, the dial-time address guard, no redirects, the timeout
	// and the size cap are the SSRF defences (spec S-8).
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, id, nil) //nolint:gosec // G704: see above
	if err != nil {
		return Client{}, errors.New("client metadata document URL is invalid")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := f.client.Do(req) //nolint:gosec // G704: guarded as above
	if err != nil {
		if errors.Is(err, errPrivateAddress) {
			return Client{}, errors.New("client metadata document host resolves to a private address")
		}
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "Timeout") {
			return Client{}, errors.New("client metadata document fetch timed out")
		}
		return Client{}, errors.New("client metadata document could not be fetched")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 == 3 {
		return Client{}, errors.New("client metadata document redirects; redirects are not followed")
	}
	if resp.StatusCode != http.StatusOK {
		return Client{}, fmt.Errorf("client metadata document answered %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, cimdMaxBytes+1))
	if err != nil {
		return Client{}, errors.New("client metadata document could not be read")
	}
	if len(body) > cimdMaxBytes {
		return Client{}, errors.New("client metadata document exceeds 64 KiB")
	}
	var doc cimdDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return Client{}, errors.New("client metadata document is not JSON")
	}
	if doc.ClientID != id {
		return Client{}, errors.New("client metadata document names another client_id")
	}
	if m := doc.TokenEndpointAuthMethod; m != "" && m != "none" {
		return Client{}, errors.New("client metadata document must use token_endpoint_auth_method none")
	}
	if len(doc.RedirectURIs) == 0 {
		return Client{}, errors.New("client metadata document has no redirect_uris")
	}
	for _, r := range doc.RedirectURIs {
		if !validRedirect(r) {
			return Client{}, errors.New("client metadata document has an invalid redirect URI")
		}
	}
	name := doc.ClientName
	if name == "" {
		name = req.URL.Host
	}
	return Client{
		ID: id, Kind: ClientCIMD, Name: name, ClientURI: doc.ClientURI, RedirectURIs: doc.RedirectURIs,
		Metadata: body, CacheUntil: now.Add(cacheFor(resp.Header, now)),
	}, nil
}

// cacheFor is how long a response may be cached per its Cache-Control or
// Expires header, at most 24 h; no-store and no-cache mean not at all.
func cacheFor(h http.Header, now time.Time) time.Duration {
	var d time.Duration
	if cc := h.Get("Cache-Control"); cc != "" {
		for _, part := range strings.Split(cc, ",") {
			k, v, _ := strings.Cut(strings.TrimSpace(strings.ToLower(part)), "=")
			switch k {
			case "no-store", "no-cache":
				return 0
			case "max-age":
				if n, err := strconv.Atoi(strings.Trim(v, `"`)); err == nil && n > 0 {
					d = time.Duration(n) * time.Second
				}
			}
		}
	} else if e := h.Get("Expires"); e != "" {
		if t, err := http.ParseTime(e); err == nil {
			d = t.Sub(now)
		}
	}
	return min(max(d, 0), cimdMaxCache)
}

// validRedirect reports whether r may be registered as a redirect URI:
// https, or http on a loopback host (RFC 8252), never with a fragment.
func validRedirect(r string) bool {
	u, err := url.Parse(r)
	if err != nil || u.Fragment != "" || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		return isLoopback(u.Hostname())
	}
	return false
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.IsLoopback()
}

// redirectMatches reports whether requested equals a registered redirect
// URI exactly, except that a loopback http redirect may use any port.
func redirectMatches(registered []string, requested string) bool {
	for _, r := range registered {
		if r == requested {
			return true
		}
	}
	q, err := url.Parse(requested)
	if err != nil || q.Scheme != "http" || !isLoopback(q.Hostname()) {
		return false
	}
	for _, r := range registered {
		u, err := url.Parse(r)
		if err != nil || u.Scheme != "http" || !isLoopback(u.Hostname()) {
			continue
		}
		if u.Hostname() == q.Hostname() && u.EscapedPath() == q.EscapedPath() && u.RawQuery == q.RawQuery &&
			q.User == nil && q.Fragment == "" {
			return true
		}
	}
	return false
}
