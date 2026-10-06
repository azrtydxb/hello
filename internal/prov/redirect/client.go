package redirect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/config"
	"github.com/azrtydxb/hello/internal/prov"
)

// The credential keys of spec S-11: the keys of the hello-prov-redirect
// secret, the keys of Credentials, and the keys the UI stores.
const (
	KeySnomKeyID        = "snomSrapsAccessKeyId"
	KeySnomKeySecret    = "snomSrapsAccessKeySecret" //nolint:gosec // a key name, not a value
	KeyYealinkKey       = "yealinkRpsAccessKey"
	KeyYealinkSecret    = "yealinkRpsAccessSecret"
	KeyYMCSClientID     = "yealinkYmcsClientId"
	KeyYMCSClientSecret = "yealinkYmcsClientSecret" //nolint:gosec // a key name, not a value
	KeyYMCSRegion       = "yealinkYmcsRegion"
	KeyGDMSClientID     = "gdmsClientId"
	KeyGDMSClientSecret = "gdmsClientSecret"
	KeyGDMSUsername     = "gdmsUsername"
	KeyGDMSPassword     = "gdmsPassword"
	KeyGDMSRegion       = "gdmsRegion"
	KeyGDMSSiteID       = "gdmsSiteId"
)

// maxBody caps what is read of a vendor response.
const maxBody = 1 << 20

// New returns the vendor's client. Poly, Fanvil and Generic get one whose
// calls all return ErrUnsupported without a network call. settings is the
// account's settings JSON object (empty or null for the defaults):
//
//   - snom: {"api": "rest" (default, SRAPS REST with Hawk) | "xmlrpc" (the
//     legacy redirect XML-RPC service, with the same key ID and secret as
//     its user name and password)}
//   - yealink: {"api": "rps" | "ymcs" (default: ymcs when its client ID is
//     set, else rps), "serverName": the RPS server entry Hello creates
//     (default "hello-" plus a hash of the access key), "macOnly": true when
//     Yealink enabled MAC-only registration on the YMCS account, so no
//     serial number is needed}
//   - grandstream: {"orgId": the GDMS organisation id, optional}
//
// hc nil means a client with a 30-second timeout. A missing credential is
// an error naming its key.
func New(v prov.Vendor, creds Credentials, settings []byte, hc *http.Client) (Client, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	switch v {
	case prov.Snom:
		return newSnom(creds, settings, hc)
	case prov.Yealink:
		return newYealink(creds, settings, hc)
	case prov.Grandstream:
		return newGDMS(creds, settings, hc)
	case prov.Poly, prov.Fanvil, prov.Generic:
		return unsupported{vendor: v}, nil
	}
	return nil, fmt.Errorf("redirect: unknown vendor %q", v)
}

// drivable reports whether Hello has a client for the vendor's redirect
// service (Snom SRAPS, Yealink RPS/YMCS, Grandstream GDMS).
func drivable(v prov.Vendor) bool {
	return v == prov.Snom || v == prov.Yealink || v == prov.Grandstream
}

// Deployment returns the redirect credentials the deployment sets (the
// hello-prov-redirect secret mounted as env), by vendor. They take
// precedence over credentials stored through the UI.
func Deployment(c config.ProvRedirect) map[prov.Vendor]Credentials {
	out := map[prov.Vendor]Credentials{}
	if c.SnomKeyID != "" {
		out[prov.Snom] = Credentials{KeySnomKeyID: c.SnomKeyID, KeySnomKeySecret: c.SnomKeySecret}
	}
	y := Credentials{}
	if c.YealinkKey != "" {
		y[KeyYealinkKey], y[KeyYealinkSecret] = c.YealinkKey, c.YealinkSecret
	}
	if c.YMCSClientID != "" {
		y[KeyYMCSClientID], y[KeyYMCSClientSecret], y[KeyYMCSRegion] = c.YMCSClientID, c.YMCSClientSecret, c.YMCSRegion
	}
	if len(y) > 0 {
		out[prov.Yealink] = y
	}
	if c.GDMSClientID != "" {
		out[prov.Grandstream] = Credentials{
			KeyGDMSClientID: c.GDMSClientID, KeyGDMSClientSecret: c.GDMSClientSecret,
			KeyGDMSUsername: c.GDMSUsername, KeyGDMSPassword: c.GDMSPassword,
			KeyGDMSRegion: c.GDMSRegion, KeyGDMSSiteID: c.GDMSSiteID,
		}
	}
	return out
}

// need returns the named credentials, or an error naming the first missing
// key (never a value).
func need(v prov.Vendor, creds Credentials, keys ...string) ([]string, error) {
	vals := make([]string, len(keys))
	for i, k := range keys {
		vals[i] = strings.TrimSpace(creds[k])
		if vals[i] == "" {
			return nil, fmt.Errorf("redirect: %s credential %s is missing", v, k)
		}
	}
	return vals, nil
}

// decodeSettings reads a settings object, refusing unknown keys so a typo
// does not silently select the default.
func decodeSettings(v prov.Vendor, settings []byte, dst any) error {
	settings = bytes.TrimSpace(settings)
	if len(settings) == 0 || string(settings) == "null" {
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(settings))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return fmt.Errorf("redirect: %s settings: %w", v, err)
	}
	return nil
}

// unsupported is the client of a vendor Hello cannot drive.
type unsupported struct{ vendor prov.Vendor }

func (u unsupported) Vendor() prov.Vendor                                  { return u.vendor }
func (unsupported) Capabilities() Caps                                     { return Caps{} }
func (unsupported) Check(context.Context) error                            { return ErrUnsupported }
func (unsupported) Register(context.Context, string, string, string) error { return ErrUnsupported }
func (unsupported) Unregister(context.Context, string) error               { return ErrUnsupported }
func (unsupported) Lookup(context.Context, string) (string, bool, error) {
	return "", false, ErrUnsupported
}

// conn is the HTTP side every vendor client shares: it reads capped
// responses and makes sure no error it returns carries a secret.
type conn struct {
	vendor  prov.Vendor
	hc      *http.Client
	secrets []string // credential values and tokens, never to appear in an error
}

// apiError is a vendor's refusal: HTTP status and the vendor's own message.
type apiError struct {
	Status int
	Msg    string
}

func (e *apiError) Error() string {
	if e.Msg == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Msg)
}

// do sends req and returns the status and the body. Transport errors come
// back without the request's query (GDMS carries its access token there).
func (c *conn) do(req *http.Request) (int, []byte, error) {
	req.Header.Set("User-Agent", "Kuvryn-Hello")
	resp, err := c.hc.Do(req) //nolint:gosec // requests go only to the fixed vendor hosts of this package
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			u := *req.URL
			u.RawQuery, u.User = "", nil
			return 0, nil, fmt.Errorf("%s %s: %w", req.Method, u.String(), ue.Err)
		}
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("%s %s: reading the response: %w", req.Method, req.URL.Path, err)
	}
	return resp.StatusCode, body, nil
}

// clean prefixes err with the vendor and operation and replaces every
// secret the client holds (plus extra ones, such as an access token) with a
// placeholder. Context errors stay matchable with errors.Is.
func (c *conn) clean(op string, err error, extra ...string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrUnsupported) {
		return err
	}
	msg := err.Error()
	for _, s := range slices.Concat(c.secrets, extra) {
		if len(s) >= 4 {
			msg = strings.ReplaceAll(msg, s, "[redacted]")
		}
	}
	msg = fmt.Sprintf("redirect %s %s: %s", c.vendor, op, msg)
	for _, ce := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, ce) {
			return fmt.Errorf("%s (%w)", msg, ce)
		}
	}
	return errors.New(msg)
}

// sameOrigin reports whether link points at the same scheme and host as
// base, so a vendor response cannot send a signed request elsewhere.
func sameOrigin(base, link string) bool {
	b, err1 := url.Parse(base)
	l, err2 := url.Parse(link)
	return err1 == nil && err2 == nil && b.Scheme == l.Scheme && b.Host == l.Host
}
