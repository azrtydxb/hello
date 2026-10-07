package redirect

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // GDMS's documented password encoding, not a security primitive
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/prov"
)

// gdmsHost is the GDMS host for a region (GDMS API guide).
var gdmsHost = map[string]string{"us": "https://www.gdms.cloud", "eu": "https://eu.gdms.cloud"}

type gdmsSettings struct {
	OrgID int64 `json:"orgId"`
}

// gdms is Grandstream's GDMS API: an OAuth password-grant token and a
// SHA-256 signature on every call. GDMS adds a device (MAC and serial) to a
// site; no documented call binds a MAC to a URL, so the site's provisioning
// server, set once by the administrator, is the boot path (spec S-11).
type gdms struct {
	conn
	base                       string
	clientID, clientSecret     string
	username, password, siteID string
	orgID                      int64

	mu      sync.Mutex
	token   string
	expires time.Time
}

func newGDMS(creds Credentials, settings []byte, hc *http.Client) (*gdms, error) {
	v, err := need(prov.Grandstream, creds, KeyGDMSClientID, KeyGDMSClientSecret, KeyGDMSUsername, KeyGDMSPassword, KeyGDMSRegion, KeyGDMSSiteID)
	if err != nil {
		return nil, err
	}
	base, ok := gdmsHost[strings.ToLower(v[4])]
	if !ok {
		return nil, errors.New("redirect: grandstream credential " + KeyGDMSRegion + " must be us or eu")
	}
	var s gdmsSettings
	if err := decodeSettings(prov.Grandstream, settings, &s); err != nil {
		return nil, err
	}
	return &gdms{
		conn: conn{vendor: prov.Grandstream, hc: hc, secrets: []string{v[0], v[1], v[2], v[3], gdmsPassword(v[3])}},
		base: base, clientID: v[0], clientSecret: v[1], username: v[2], password: v[3], siteID: v[5], orgID: s.OrgID,
	}, nil
}

func (*gdms) Vendor() prov.Vendor { return prov.Grandstream }

func (*gdms) Capabilities() Caps { return Caps{NeedsSerial: true, Supported: true} }

func (g *gdms) Check(ctx context.Context) error {
	return g.clean("check", g.call(ctx, http.MethodGet, "/oapi/v1.0.0/site/list", nil, nil), g.tok())
}

// Register adds the device to the configured site. url is not sent: GDMS
// has no per-device URL.
func (g *gdms) Register(ctx context.Context, mac, serial, _ string) error {
	_, err := g.add(ctx, mac, serial)
	return g.clean("register", err, g.tok())
}

// add adds the device; existed reports that GDMS already held it in
// Hello's site, which counts as registered. A device GDMS holds elsewhere
// (another site or organisation) is a rejection with GDMS's message.
func (g *gdms) add(ctx context.Context, mac, serial string) (existed bool, err error) {
	m, err := normMAC(mac)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(serial) == "" {
		return false, errors.New("GDMS needs the phone's serial number")
	}
	m = gdmsMAC(m)
	dev := map[string]any{"deviceName": "Hello " + m, "mac": m, "sn": serial, "siteId": gdmsID(g.siteID)}
	if g.orgID != 0 {
		dev["orgId"] = g.orgID
	}
	err = g.call(ctx, http.MethodPost, "/oapi/v1.0.0/device/add", []any{dev}, nil)
	var r *gdmsRefusal
	if errors.As(err, &r) && gdmsExists(r.Msg) {
		in, lerr := g.inSite(ctx, m)
		switch {
		case lerr != nil:
			return false, fmt.Errorf("%w; confirming the site: %w", err, lerr)
		case !in:
			return false, rejection{err}
		}
		return true, nil
	}
	return false, err
}

// inSite reports whether GDMS lists the device (colon MAC) in Hello's
// site. The list call's body and answer follow the API guide's paged
// calls (unconfirmed): POST device/list {"mac", "pageNum", "pageSize"},
// data.result[] with mac and siteId.
func (g *gdms) inSite(ctx context.Context, m string) (bool, error) {
	var page struct {
		Result []struct {
			MAC    string          `json:"mac"`
			SiteID json.RawMessage `json:"siteId"`
		} `json:"result"`
	}
	if err := g.call(ctx, http.MethodPost, "/oapi/v1.0.0/device/list", map[string]any{"mac": m, "pageNum": 1, "pageSize": 10}, &page); err != nil {
		return false, err
	}
	for _, d := range page.Result {
		if strings.EqualFold(d.MAC, m) && rawCode(d.SiteID) == g.siteID {
			return true, nil
		}
	}
	return false, nil
}

// Unregister deletes the device from GDMS. The delete call and its body
// are not in the public API guide (unconfirmed): POST device/delete with
// {"macList": [...]}, as the guide's other batch calls take MACs.
func (g *gdms) Unregister(ctx context.Context, mac string) error {
	m, err := normMAC(mac)
	if err != nil {
		return g.clean("unregister", err)
	}
	err = g.call(ctx, http.MethodPost, "/oapi/v1.0.0/device/delete", map[string]any{"macList": []string{gdmsMAC(m)}}, nil)
	var r *gdmsRefusal
	if errors.As(err, &r) && gdmsMissing(r.Msg) {
		err = nil
	}
	return g.clean("unregister", err, g.tok())
}

// Lookup is unsupported: GDMS keeps no per-device URL to read back.
func (*gdms) Lookup(context.Context, string) (string, bool, error) {
	return "", false, ErrUnsupported
}

func gdmsExists(msg string) bool {
	return existsText(msg) && !gdmsMissing(msg)
}

func gdmsMissing(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "not exist") || strings.Contains(m, "not found")
}

// gdmsMAC is a normalised MAC as GDMS wants it: XX:XX:XX:XX:XX:XX.
func gdmsMAC(mac string) string {
	m := strings.ToUpper(strings.NewReplacer(":", "", "-", "", ".", "").Replace(mac))
	var parts []string
	for i := 0; i+2 <= len(m); i += 2 {
		parts = append(parts, m[i:i+2])
	}
	return strings.Join(parts, ":")
}

// gdmsID sends a numeric id as a number, as the guide's examples do.
func gdmsID(s string) any {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	return s
}

// gdmsPassword is the token request's password: SHA-256 hex of the MD5 hex
// of the login password.
func gdmsPassword(pw string) string {
	m := md5.Sum([]byte(pw)) //nolint:gosec // GDMS's documented encoding
	return sha256Hex([]byte(hex.EncodeToString(m[:])))
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// gdmsSign is the GDMS signature: SHA-256 hex of "&" + the parameters
// sorted by name as "k=v" joined with "&" + "&", followed by the SHA-256
// hex of the body and "&" when there is a body.
func gdmsSign(params map[string]string, body []byte) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = k + "=" + params[k]
	}
	s := "&" + strings.Join(pairs, "&") + "&"
	if len(body) > 0 {
		s += sha256Hex(body) + "&"
	}
	return sha256Hex([]byte(s))
}

func (g *gdms) tok() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.token
}

// accessToken logs in with the password grant, caching the token until a
// minute before it expires.
func (g *gdms) accessToken(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.token != "" && time.Now().Before(g.expires) {
		return g.token, nil
	}
	p := map[string]string{
		"grant_type": "password", "username": g.username, "password": gdmsPassword(g.password),
		"client_id": g.clientID, "client_secret": g.clientSecret,
		"timestamp": strconv.FormatInt(time.Now().UnixMilli(), 10),
	}
	form := url.Values{}
	for k, v := range p {
		form.Set(k, v)
	}
	form.Set("signature", gdmsSign(p, nil))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.base+"/oapi/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	status, body, err := g.do(req)
	if err != nil {
		return "", err
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &t)
	if status != http.StatusOK || t.AccessToken == "" {
		return "", &apiError{Status: status, Msg: firstNonEmpty(t.Description, t.Error, "no access token")}
	}
	if t.ExpiresIn <= 0 {
		t.ExpiresIn = 3600
	}
	g.token = t.AccessToken
	g.expires = time.Now().Add(time.Duration(max(t.ExpiresIn-60, 0)) * time.Second)
	return g.token, nil
}

// gdmsRefusal is an answer with a non-zero retCode.
type gdmsRefusal struct {
	Code int
	Msg  string
}

func (e *gdmsRefusal) Error() string {
	return fmt.Sprintf("GDMS refused (retCode %d): %s", e.Code, e.Msg)
}

// call sends a signed request; a refusal of the access token (HTTP 401,
// or a refusal naming the token) drops it and retries once with a new one.
func (g *gdms) call(ctx context.Context, method, path string, body any, dst any) error {
	err := g.callOnce(ctx, method, path, body, dst)
	if gdmsTokenRefused(err) {
		g.mu.Lock()
		g.token = ""
		g.mu.Unlock()
		err = g.callOnce(ctx, method, path, body, dst)
	}
	return err
}

func gdmsTokenRefused(err error) bool {
	var a *apiError
	if errors.As(err, &a) && a.Status == http.StatusUnauthorized {
		return true
	}
	var r *gdmsRefusal
	return errors.As(err, &r) && strings.Contains(strings.ToLower(r.Msg), "token")
}

// callOnce sends one signed request: access_token, timestamp and
// signature in the query, the JSON body (if any) covered by the signature.
func (g *gdms) callOnce(ctx context.Context, method, path string, body any, dst any) error {
	tok, err := g.accessToken(ctx)
	if err != nil {
		return err
	}
	var raw []byte
	if body != nil {
		if raw, err = json.Marshal(body); err != nil {
			return err
		}
	}
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	q := url.Values{
		"access_token": {tok},
		"timestamp":    {ts},
		"signature":    {gdmsSign(map[string]string{"access_token": tok, "client_id": g.clientID, "client_secret": g.clientSecret, "timestamp": ts}, raw)},
	}
	req, err := http.NewRequestWithContext(ctx, method, g.base+path+"?"+q.Encode(), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if raw != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	status, rb, err := g.do(req)
	if err != nil {
		return err
	}
	var env struct {
		RetCode *int            `json:"retCode"`
		Msg     string          `json:"msg"`
		Data    json.RawMessage `json:"data"`
	}
	if jerr := json.Unmarshal(rb, &env); jerr != nil || env.RetCode == nil {
		if status >= 300 {
			return &apiError{Status: status}
		}
		return errors.New("GDMS answered without a retCode")
	}
	if *env.RetCode != 0 {
		return &gdmsRefusal{Code: *env.RetCode, Msg: env.Msg}
	}
	if dst != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, dst); err != nil {
			return fmt.Errorf("decoding the GDMS data: %w", err)
		}
	}
	return nil
}
