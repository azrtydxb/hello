package redirect

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // Content-MD5 is the RPS API's body digest, not a security primitive
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/prov"
)

// Yealink's redirect services: the RPS JSON API v3.6 and YMCS v2.
var (
	rpsBase = "https://api-dm.yealink.com:8443"
	// ymcsHost is the YMCS API host for a region ("eu", "us", ...).
	ymcsHost = func(region string) string { return "https://" + region + "-api.ymcs.yealink.com" }
)

var ymcsRegion = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

type yealinkSettings struct {
	API        string `json:"api"`        // "rps" or "ymcs"
	ServerName string `json:"serverName"` // RPS server entry
	MACOnly    bool   `json:"macOnly"`    // YMCS: MAC-only registration enabled
}

func newYealink(creds Credentials, settings []byte, hc *http.Client) (Client, error) {
	var s yealinkSettings
	if err := decodeSettings(prov.Yealink, settings, &s); err != nil {
		return nil, err
	}
	// RPS unless the settings choose YMCS (spec S-11): YMCS credentials
	// alone do not switch the API.
	api := s.API
	if api == "" {
		api = "rps"
	}
	switch api {
	case "rps":
		v, err := need(prov.Yealink, creds, KeyYealinkKey, KeyYealinkSecret)
		if err != nil {
			return nil, err
		}
		name := s.ServerName
		if name == "" {
			sum := sha256.Sum256([]byte(v[0]))
			name = "hello-" + hex.EncodeToString(sum[:6])
		}
		if len(name) > 20 {
			return nil, errors.New("redirect: yealink settings: serverName is at most 20 characters")
		}
		return &rps{conn: conn{vendor: prov.Yealink, hc: hc, secrets: v}, key: v[0], secret: v[1], serverName: name}, nil
	case "ymcs":
		v, err := need(prov.Yealink, creds, KeyYMCSClientID, KeyYMCSClientSecret, KeyYMCSRegion)
		if err != nil {
			return nil, err
		}
		if !ymcsRegion.MatchString(v[2]) {
			return nil, errors.New("redirect: yealink credential " + KeyYMCSRegion + " must be a region code such as eu or us")
		}
		return &ymcs{conn: conn{vendor: prov.Yealink, hc: hc, secrets: v[:2]}, id: v[0], secret: v[1],
			base: ymcsHost(v[2]), macOnly: s.MACOnly}, nil
	}
	return nil, errors.New(`redirect: yealink settings: api must be "rps" or "ymcs"`)
}

// yealinkMAC normalises a MAC the vendor returned, for comparison.
func yealinkMAC(mac string) string {
	return strings.ToLower(strings.NewReplacer(":", "", "-", "", ".", "").Replace(mac))
}

// rps is the Yealink RPS JSON API: every call signed with the access key
// secret (HMAC-SHA256 over the method, the X-Ca-* headers and the path).
type rps struct {
	conn
	key, secret string
	serverName  string

	mu       sync.Mutex
	serverID string
}

func (*rps) Vendor() prov.Vendor { return prov.Yealink }

func (*rps) Capabilities() Caps { return Caps{RegistersURL: true, Supported: true} }

func (r *rps) Check(ctx context.Context) error {
	return r.clean("check", r.call(ctx, http.MethodGet, "/api/open/v1/device/serverList", nil, nil, nil))
}

// Register binds the MAC to the phone's URL (uniqueServerUrl) under Hello's
// server entry, editing the device when RPS already holds it.
func (r *rps) Register(ctx context.Context, mac, _, u string) error {
	m, err := normMAC(mac)
	if err != nil {
		return r.clean("register", err)
	}
	id, err := r.server(ctx, u)
	if err != nil {
		return r.clean("register", err)
	}
	err = r.call(ctx, http.MethodPost, "/api/open/v1/device/add", nil,
		map[string]any{"macs": []string{m}, "serverId": id, "uniqueServerUrl": u}, nil)
	if rpsCode(err) == "device.mac.existed" {
		var dev rpsDevice
		var found bool
		if dev, found, err = r.device(ctx, m); err == nil {
			if !found {
				err = errors.New("RPS reports the MAC as added but does not list it")
			} else {
				err = r.call(ctx, http.MethodPost, "/api/open/v1/device/edit", nil,
					map[string]any{"id": dev.ID, "serverId": id, "uniqueServerUrl": u}, nil)
			}
		}
	}
	return r.clean("register", err)
}

func (r *rps) Unregister(ctx context.Context, mac string) error {
	m, err := normMAC(mac)
	if err != nil {
		return r.clean("unregister", err)
	}
	err = r.call(ctx, http.MethodPost, "/api/open/v1/device/delete", nil, map[string]any{"macs": []string{m}}, nil)
	if rpsCode(err) == "device.not.found" {
		err = nil
	}
	return r.clean("unregister", err)
}

func (r *rps) Lookup(ctx context.Context, mac string) (string, bool, error) {
	m, err := normMAC(mac)
	if err != nil {
		return "", false, r.clean("lookup", err)
	}
	dev, found, err := r.device(ctx, m)
	if err != nil || !found {
		return "", false, r.clean("lookup", err)
	}
	u := firstNonEmpty(dev.UniqueServerURL, dev.ServerURL)
	return u, u != "", nil
}

type rpsDevice struct {
	ID              string `json:"id"`
	MAC             string `json:"mac"`
	UniqueServerURL string `json:"uniqueServerUrl"`
	ServerURL       string `json:"serverUrl"`
}

// device finds the account's device with this MAC.
func (r *rps) device(ctx context.Context, m string) (rpsDevice, bool, error) {
	var page struct {
		Data []rpsDevice `json:"data"`
	}
	if err := r.call(ctx, http.MethodPost, "/api/open/v1/device/list", nil,
		map[string]any{"key": m, "skip": 0, "limit": 50}, &page); err != nil {
		return rpsDevice{}, false, err
	}
	for _, d := range page.Data {
		if yealinkMAC(d.MAC) == m {
			return d, true, nil
		}
	}
	return rpsDevice{}, false, nil
}

// server returns the id of Hello's server entry, creating it with the
// origin of the phone's URL the first time. RPS server names are unique
// across all enterprises, so the name is looked up first.
func (r *rps) server(ctx context.Context, phoneURL string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.serverID != "" {
		return r.serverID, nil
	}
	var page struct {
		Data []struct {
			ID         string `json:"id"`
			ServerName string `json:"serverName"`
		} `json:"data"`
	}
	if err := r.call(ctx, http.MethodPost, "/api/open/v1/server/list", nil,
		map[string]any{"key": r.serverName, "skip": 0, "limit": 50}, &page); err != nil {
		return "", err
	}
	for _, s := range page.Data {
		if s.ServerName == r.serverName {
			r.serverID = s.ID
			return s.ID, nil
		}
	}
	pu, err := url.Parse(phoneURL)
	if err != nil || pu.Host == "" {
		return "", errors.New("the phone URL has no host")
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := r.call(ctx, http.MethodPost, "/api/open/v1/server/add", nil,
		map[string]any{"serverName": r.serverName, "url": pu.Scheme + "://" + pu.Host + "/"}, &created); err != nil {
		return "", err
	}
	if created.ID == "" {
		return "", errors.New("RPS created the server entry without an id")
	}
	r.serverID = created.ID
	return created.ID, nil
}

// rpsRefusal is an RPS answer with ret < 0; Code is the message key such
// as device.mac.existed.
type rpsRefusal struct{ Code string }

func (e *rpsRefusal) Error() string { return "RPS refused: " + e.Code }

func rpsCode(err error) string {
	var r *rpsRefusal
	if errors.As(err, &r) {
		return r.Code
	}
	return ""
}

// call sends a signed RPS request: a JSON body, or query parameters, never
// both. dst receives the answer's data.
func (r *rps) call(ctx context.Context, method, path string, query url.Values, body any, dst any) error {
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			return err
		}
	}
	u := rpsBase + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	h := map[string]string{
		"X-Ca-Key":       r.key,
		"X-Ca-Nonce":     hex.EncodeToString(nonce),
		"X-Ca-Timestamp": strconv.FormatInt(time.Now().UnixMilli(), 10),
	}
	if raw != nil {
		sum := md5.Sum(raw) //nolint:gosec // the API's Content-MD5
		h["Content-MD5"] = base64.StdEncoding.EncodeToString(sum[:])
		req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	}
	for k, v := range h {
		req.Header.Set(k, v)
	}
	req.Header.Set("X-Ca-Signature", rpsSign(r.secret, rpsStringToSign(method, h, path, query)))
	status, rb, err := r.do(req)
	if err != nil {
		return err
	}
	var env struct {
		Ret    int             `json:"ret"`
		Data   json.RawMessage `json:"data"`
		Error  *rpsErrBody     `json:"error"`
		Errors *rpsErrBody     `json:"errors"`
	}
	if jerr := json.Unmarshal(rb, &env); jerr != nil {
		if status >= 300 {
			return &apiError{Status: status}
		}
		return fmt.Errorf("decoding the RPS answer: %w", jerr)
	}
	if env.Ret < 0 || status >= 300 {
		e := env.Error
		if e == nil {
			e = env.Errors
		}
		code := ""
		if e != nil {
			code = e.Msg
			for _, f := range e.FieldErrors {
				code = firstNonEmpty(code, f.Msg)
			}
		}
		if code == "" {
			return &apiError{Status: status}
		}
		return &rpsRefusal{Code: code}
	}
	if dst != nil && len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, dst); err != nil {
			return fmt.Errorf("decoding the RPS data: %w", err)
		}
	}
	return nil
}

type rpsErrBody struct {
	Msg         string `json:"msg"`
	FieldErrors []struct {
		Msg string `json:"msg"`
	} `json:"fieldErrors"`
}

// rpsStringToSign is the RPS v3.6 signing string: the method, the system
// headers sorted by name as "Key:value\n", the path without its leading
// slash, and for query parameters "\n" plus the sorted "k=v" pairs joined
// with "&" (a pair with an empty value is just its key).
func rpsStringToSign(method string, headers map[string]string, path string, query url.Values) string {
	var b strings.Builder
	b.WriteString(strings.ToUpper(method) + "\n")
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		b.WriteString(k + ":" + headers[k] + "\n")
	}
	b.WriteString(strings.TrimPrefix(path, "/"))
	if len(query) > 0 {
		qk := make([]string, 0, len(query))
		for k := range query {
			qk = append(qk, k)
		}
		slices.Sort(qk)
		pairs := make([]string, 0, len(qk))
		for _, k := range qk {
			if v := strings.TrimSpace(query.Get(k)); v != "" {
				pairs = append(pairs, k+"="+query.Get(k))
			} else {
				pairs = append(pairs, k)
			}
		}
		b.WriteString("\n" + strings.Join(pairs, "&"))
	}
	return b.String()
}

func rpsSign(secret, s string) string {
	m := hmac.New(sha256.New, []byte(secret))
	_, _ = m.Write([]byte(s))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// ymcs is Yealink's YMCS v2 API (OAuth2 client credentials). Adding a
// device needs its serial number unless Yealink enabled MAC-only
// registration on the account (settings macOnly).
type ymcs struct {
	conn
	id, secret string
	base       string
	macOnly    bool

	mu      sync.Mutex
	token   string
	expires time.Time
}

func (*ymcs) Vendor() prov.Vendor { return prov.Yealink }

func (y *ymcs) Capabilities() Caps {
	return Caps{RegistersURL: true, NeedsSerial: !y.macOnly, Supported: true}
}

func (y *ymcs) Check(ctx context.Context) error {
	_, err := y.accessToken(ctx)
	return y.clean("check", err)
}

// Register adds the device with the phone's URL; a device YMCS already
// holds is deleted and added again, since the API documents no edit.
func (y *ymcs) Register(ctx context.Context, mac, serial, u string) error {
	m, err := normMAC(mac)
	if err != nil {
		return y.clean("register", err)
	}
	if !y.macOnly && strings.TrimSpace(serial) == "" {
		return y.clean("register", errors.New("YMCS needs the phone's serial number"))
	}
	err = y.add(ctx, m, serial, u)
	if ymcsCode(err) == ymcsExists {
		if err = y.del(ctx, m); err == nil {
			err = y.add(ctx, m, serial, u)
		}
	}
	return y.clean("register", err, y.tok())
}

func (y *ymcs) add(ctx context.Context, m, serial, u string) error {
	if y.macOnly {
		var res struct {
			SuccessCount int `json:"successCount"`
			Errors       []struct {
				Code      json.RawMessage `json:"code"`
				ErrorCode json.RawMessage `json:"errorCode"`
				ErrorInfo string          `json:"errorInfo"`
				Msg       string          `json:"msg"`
			} `json:"errors"`
		}
		if err := y.call(ctx, "/v2/rps/addDevicesByMac", []map[string]string{{"mac": m, "uniqueServerUrl": u}}, &res, http.StatusOK); err != nil {
			return err
		}
		if res.SuccessCount == 0 {
			if len(res.Errors) == 0 {
				return &ymcsError{Msg: "no device added"}
			}
			e := res.Errors[0]
			code := firstNonEmpty(rawCode(e.ErrorCode), rawCode(e.Code))
			msg := firstNonEmpty(e.ErrorInfo, e.Msg)
			if code == ymcsExists || existsText(msg) {
				code = ymcsExists // route to delete-and-add like the serial path
			}
			return &ymcsError{Code: code, Msg: msg}
		}
		return nil
	}
	return y.call(ctx, "/v2/rps/devices", map[string]string{"mac": m, "sn": serial, "uniqueServerUrl": u}, nil, http.StatusCreated, http.StatusOK)
}

func (y *ymcs) del(ctx context.Context, m string) error {
	var res struct {
		FailureCount int `json:"failureCount"`
	}
	if err := y.call(ctx, "/v2/rps/delDevices", map[string]any{"deviceIdType": "mac", "deviceIds": []string{m}}, &res, http.StatusOK); err != nil {
		return err
	}
	if res.FailureCount > 0 {
		if _, found, err := y.find(ctx, m); err != nil || found {
			return firstErr(err, errors.New("YMCS did not delete the device"))
		}
	}
	return nil
}

// ymcsExists is YMCS's "resource already exists" code.
const ymcsExists = "800003"

// rawCode is a JSON code given as a string or a number.
func rawCode(raw json.RawMessage) string {
	return strings.Trim(strings.TrimSpace(string(raw)), `"`)
}

// existsText reports a vendor message saying the device already exists
// (and not that it does not).
func existsText(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "exist") && !strings.Contains(m, "not exist")
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func (y *ymcs) Unregister(ctx context.Context, mac string) error {
	m, err := normMAC(mac)
	if err != nil {
		return y.clean("unregister", err)
	}
	return y.clean("unregister", y.del(ctx, m), y.tok())
}

func (y *ymcs) Lookup(ctx context.Context, mac string) (string, bool, error) {
	m, err := normMAC(mac)
	if err != nil {
		return "", false, y.clean("lookup", err)
	}
	u, found, err := y.find(ctx, m)
	return u, found, y.clean("lookup", err, y.tok())
}

func (y *ymcs) find(ctx context.Context, m string) (string, bool, error) {
	var res struct {
		Data []struct {
			MAC             string `json:"mac"`
			UniqueServerURL string `json:"uniqueServerUrl"`
			ServerURL       string `json:"serverUrl"`
		} `json:"data"`
	}
	if err := y.call(ctx, "/v2/rps/listDevices", map[string]any{"skip": 0, "limit": 10, "autoCount": false, "filter": map[string]string{"mac": m}}, &res, http.StatusOK); err != nil {
		return "", false, err
	}
	for _, d := range res.Data {
		if yealinkMAC(d.MAC) == m {
			u := firstNonEmpty(d.UniqueServerURL, d.ServerURL)
			return u, u != "", nil
		}
	}
	return "", false, nil
}

func (y *ymcs) tok() string {
	y.mu.Lock()
	defer y.mu.Unlock()
	return y.token
}

// accessToken returns the cached bearer token, fetching a new one a minute
// before it expires.
func (y *ymcs) accessToken(ctx context.Context) (string, error) {
	y.mu.Lock()
	defer y.mu.Unlock()
	if y.token != "" && time.Now().Before(y.expires) {
		return y.token, nil
	}
	req, err := y.request(ctx, "/v2/token", map[string]string{"grant_type": "client_credentials"})
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(y.id, y.secret)
	status, body, err := y.do(req)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", ymcsErr(status, body)
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &t); err != nil || t.AccessToken == "" {
		return "", errors.New("YMCS returned no access token")
	}
	y.token = t.AccessToken
	y.expires = time.Now().Add(time.Duration(max(t.ExpiresIn-60, 0)) * time.Second)
	return y.token, nil
}

// request builds a POST with the JSON body and the timestamp and nonce
// headers every YMCS call carries.
func (y *ymcs) request(ctx context.Context, path string, body any) (*http.Request, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, y.base+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	req.Header.Set("nonce", hex.EncodeToString(nonce))
	return req, nil
}

// call sends an authorised call; a refusal of the access token (revoked
// or expired before its time) drops it and retries once with a new one.
func (y *ymcs) call(ctx context.Context, path string, body, dst any, ok ...int) error {
	err := y.callOnce(ctx, path, body, dst, ok...)
	var e *ymcsError
	if errors.As(err, &e) && (e.Status == http.StatusUnauthorized || e.Code == "900401") {
		y.dropToken()
		err = y.callOnce(ctx, path, body, dst, ok...)
	}
	return err
}

func (y *ymcs) dropToken() {
	y.mu.Lock()
	defer y.mu.Unlock()
	y.token = ""
}

func (y *ymcs) callOnce(ctx context.Context, path string, body, dst any, ok ...int) error {
	tok, err := y.accessToken(ctx)
	if err != nil {
		return err
	}
	req, err := y.request(ctx, path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	status, rb, err := y.do(req)
	if err != nil {
		return err
	}
	if !slices.Contains(ok, status) {
		return ymcsErr(status, rb)
	}
	if dst != nil && len(rb) > 0 {
		if err := json.Unmarshal(rb, dst); err != nil {
			return fmt.Errorf("decoding the YMCS answer: %w", err)
		}
	}
	return nil
}

// ymcsError is a YMCS refusal with its documented code.
type ymcsError struct {
	Status    int
	Code, Msg string
}

func (e *ymcsError) Error() string {
	return fmt.Sprintf("YMCS refused (HTTP %d): %s %s", e.Status, e.Code, e.Msg)
}

func ymcsErr(status int, body []byte) error {
	var e struct {
		Code    string `json:"code"`
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &e)
	return &ymcsError{Status: status, Code: firstNonEmpty(e.Code, e.Error), Msg: e.Message}
}

func ymcsCode(err error) string {
	var e *ymcsError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
