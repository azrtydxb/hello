package api

// Tests of the browser softphone's routes (web devices and ICE servers) over
// the in-memory account store, so they run without services. The SQL behind
// them (the cap under concurrency, revocation reaching the snapshot) is
// covered by clients_db_test.go.

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // the TURN REST API scheme under test
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/internal/turn"
)

const (
	testClientKey  = "0123456789abcdef0123456789abcdef0123"
	testTurnSecret = "turn-shared-secret-with-coturn"
)

func testClientAuth() ClientConfig {
	return ClientConfig{
		AuthKey: testClientKey, MaxDevices: 3, WSSURL: "wss://hello.test/ws",
		ICE: turn.Config{STUN: []string{"stun:turn1.test:3478"}, TURN: []string{"turn:turn1.test:3478?transport=udp", "turns:turn1.test:5349?transport=tcp"},
			Secret: testTurnSecret, TTL: time.Hour},
	}
}

// clientRow is a web device in the fake store.
type clientRow struct {
	store.ClientDevice
	user    int64
	enabled bool
}

func (f *acctFake) CreateClientDevice(_ context.Context, actor string, in store.NewClientDevice) (store.ClientDevice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.owns(in.UserID, in.ExtensionID) {
		return store.ClientDevice{}, store.ErrNotFound
	}
	n := 0
	for _, c := range f.clients {
		if c.user == in.UserID && c.enabled {
			n++
		}
	}
	if n >= in.Max {
		return store.ClientDevice{}, store.ErrDeviceLimit
	}
	id := int64(len(f.clients) + 1)
	d := store.ClientDevice{ID: id, Kind: in.Kind, Label: in.Label, Platform: in.Platform, AppVersion: in.AppVersion,
		ExtensionID: in.ExtensionID, ExtensionNumber: f.exts[in.ExtensionID].Number, SIPUsername: auth.DerivedBase(in.Kind, id), CreatedAt: time.Now()}
	f.clients = append(f.clients, clientRow{ClientDevice: d, user: in.UserID, enabled: true})
	f.audit = append(f.audit, actor+" create device "+strconv.FormatInt(id, 10))
	return d, nil
}

func (f *acctFake) ListClientDevices(_ context.Context, uid int64) ([]store.ClientDevice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.ClientDevice{}
	for _, c := range f.clients {
		if c.user == uid && c.enabled {
			out = append(out, c.ClientDevice)
		}
	}
	return out, nil
}

func (f *acctFake) RefreshClientDevice(_ context.Context, uid, id int64) (store.ClientDevice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, c := range f.clients {
		if c.ID == id && c.user == uid && c.enabled {
			now := time.Now()
			f.clients[i].LastSeenAt = &now
			return f.clients[i].ClientDevice, nil
		}
	}
	return store.ClientDevice{}, store.ErrNotFound
}

func (f *acctFake) RevokeClientDevice(_ context.Context, actor string, uid, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, c := range f.clients {
		if c.ID == id && c.user == uid && c.enabled {
			f.clients[i].enabled = false
			f.audit = append(f.audit, actor+" revoke device "+strconv.FormatInt(id, 10))
			return nil
		}
	}
	return store.ErrNotFound
}

var derivedName = regexp.MustCompile(`^w[0-9]+\.[0-9]+$`)

// TestWebDeviceEnrolment fails if enrolling does not hand back credentials
// that are the derived ones for the key (so hello-sip accepts them), if the
// password reaches a log or an audit event, if a device can be enrolled on
// an extension the caller does not manage, if the cap, the validation or the
// listing are wrong, or if a user can see, refresh or revoke someone else's
// device.
func TestWebDeviceEnrolment(t *testing.T) {
	f, h, _, logs := newAcctFake(t)

	var first map[string]any
	t.Run("alice enrols and gets derived credentials", func(t *testing.T) {
		first = wantJSON(t, acctCall(h, "alice", "POST", "/api/v1/me/devices", map[string]any{"label": "Firefox on the desk", "platform": "Firefox 130"}), 201, "enrol")
		user, _ := first["sipUsername"].(string)
		if !derivedName.MatchString(user) || !strings.HasPrefix(user, "w1.") {
			t.Fatalf("sipUsername = %q, want w<id>.<expiry>", user)
		}
		if first["realm"] != testDomain || first["wssUrl"] != "wss://hello.test/ws" {
			t.Fatalf("realm/wssUrl = %v %v", first["realm"], first["wssUrl"])
		}
		want := auth.DerivedSecret([]byte(testClientKey), user, testDomain)
		if first["sipPassword"] != want {
			t.Fatal("sipPassword is not the secret derived from the key for that username")
		}
		exp, err := time.Parse(time.RFC3339, first["expiresAt"].(string))
		if err != nil || time.Until(exp) < 11*time.Hour || time.Until(exp) > 12*time.Hour+time.Minute {
			t.Fatalf("expiresAt = %v, want about 12h ahead", first["expiresAt"])
		}
		if n, _, _ := auth.SplitDerived(user); n != "w1" {
			t.Fatalf("base = %q", n)
		}
		dev := first["device"].(map[string]any)
		if dev["extensionNumber"] != "101" || dev["label"] != "Firefox on the desk" || dev["kind"] != "web" {
			t.Fatalf("device = %v", dev)
		}
		ice := first["ice"].(map[string]any)["iceServers"].([]any)
		if len(ice) != 2 {
			t.Fatalf("ice servers = %v", ice)
		}
	})

	t.Run("the password is never stored or logged", func(t *testing.T) {
		pw := first["sipPassword"].(string)
		if strings.Contains(logs.String(), pw) {
			t.Fatal("the SIP password reached the log")
		}
		for _, a := range f.audit {
			if strings.Contains(a, pw) {
				t.Fatalf("audit carries the password: %q", a)
			}
		}
		if !slices.Contains(f.audit, "alice create device 1") && !slices.ContainsFunc(f.audit, func(s string) bool { return strings.HasSuffix(s, "create device 1") }) {
			t.Fatalf("no audit event for the enrolment: %v", f.audit)
		}
		// Listing and refreshing a device never return the secret of the
		// listing; a listing has no password field at all.
		rec := acctCall(h, "alice", "GET", "/api/v1/me/devices", nil)
		if strings.Contains(rec.Body.String(), pw) || strings.Contains(strings.ToLower(rec.Body.String()), "password") {
			t.Fatalf("listing leaks a credential: %s", rec.Body)
		}
	})

	t.Run("validation", func(t *testing.T) {
		for name, body := range map[string]map[string]any{
			"no label":         {},
			"blank label":      {"label": "   "},
			"long label":       {"label": strings.Repeat("x", 65)},
			"control in label": {"label": "a\x00b"},
			"mobile":           {"label": "phone", "kind": "mobile"},
			"unknown field":    {"label": "x", "secret": "y"},
			"bad extension id": {"label": "x", "extensionId": -3},
		} {
			if rec := acctCall(h, "alice", "POST", "/api/v1/me/devices", body); rec.Code != 400 {
				t.Errorf("%s = %d %s, want 400", name, rec.Code, rec.Body)
			}
		}
	})

	t.Run("only the caller's own extension", func(t *testing.T) {
		for _, ext := range []int{extBob, extFree, 99999} {
			if rec := acctCall(h, "alice", "POST", "/api/v1/me/devices", map[string]any{"label": "x", "extensionId": ext}); rec.Code != 404 {
				t.Errorf("extension %d = %d %s, want 404", ext, rec.Code, rec.Body)
			}
		}
		rec := acctCall(h, "noext", "POST", "/api/v1/me/devices", map[string]any{"label": "x"})
		if rec.Code != 409 || acctErrCode(rec) != "no_extension" {
			t.Errorf("user without an extension = %d %s, want 409 no_extension", rec.Code, rec.Body)
		}
		if got := wantJSON(t, acctCall(h, "alice", "POST", "/api/v1/me/devices", map[string]any{"label": "second", "extensionId": extAlice}), 201, "explicit own extension"); got["device"].(map[string]any)["extensionNumber"] != "101" {
			t.Fatalf("device = %v", got["device"])
		}
	})

	t.Run("the cap, and revoking frees a place", func(t *testing.T) {
		wantJSON(t, acctCall(h, "alice", "POST", "/api/v1/me/devices", map[string]any{"label": "third"}), 201, "third")
		rec := acctCall(h, "alice", "POST", "/api/v1/me/devices", map[string]any{"label": "fourth"})
		if rec.Code != 409 || acctErrCode(rec) != "device_limit" {
			t.Fatalf("over the cap = %d %s, want 409 device_limit", rec.Code, rec.Body)
		}
		if rec := acctCall(h, "alice", "DELETE", "/api/v1/me/devices/2", nil); rec.Code != 204 {
			t.Fatalf("revoke = %d %s", rec.Code, rec.Body)
		}
		wantJSON(t, acctCall(h, "alice", "POST", "/api/v1/me/devices", map[string]any{"label": "fourth"}), 201, "after revoking")
	})

	t.Run("listing is scoped to the caller", func(t *testing.T) {
		mine := nextOf(wantJSON(t, acctCall(h, "alice", "GET", "/api/v1/me/devices", nil), 200, "list"))
		if len(mine) != 3 {
			t.Fatalf("alice's devices = %v", mine)
		}
		for _, d := range mine {
			if d["id"] == float64(2) {
				t.Errorf("a revoked device is listed: %v", d)
			}
			if _, ok := d["registered"]; !ok {
				t.Errorf("no registered field: %v", d)
			}
		}
		if got := nextOf(wantJSON(t, acctCall(h, "bob", "GET", "/api/v1/me/devices", nil), 200, "bob list")); len(got) != 0 {
			t.Fatalf("bob sees alice's devices: %v", got)
		}
	})

	t.Run("another user's device is 404", func(t *testing.T) {
		for _, c := range [][2]string{{"POST", "/api/v1/me/devices/1/credentials"}, {"DELETE", "/api/v1/me/devices/1"},
			{"POST", "/api/v1/me/devices/99/credentials"}, {"DELETE", "/api/v1/me/devices/0"}, {"DELETE", "/api/v1/me/devices/x"}} {
			if rec := acctCall(h, "bob", c[0], c[1], nil); rec.Code != 404 {
				t.Errorf("bob %s %s = %d %s, want 404", c[0], c[1], rec.Code, rec.Body)
			}
		}
		if !f.clients[0].enabled {
			t.Fatal("bob revoked alice's device")
		}
	})

	t.Run("refresh derives new credentials, and a revoked device cannot", func(t *testing.T) {
		got := wantJSON(t, acctCall(h, "alice", "POST", "/api/v1/me/devices/1/credentials", nil), 200, "refresh")
		user := got["sipUsername"].(string)
		if !strings.HasPrefix(user, "w1.") || got["sipPassword"] != auth.DerivedSecret([]byte(testClientKey), user, testDomain) || got["deviceId"] != float64(1) {
			t.Fatalf("credentials = %v", got)
		}
		if f.clients[0].LastSeenAt == nil {
			t.Fatal("refresh did not note the device as seen")
		}
		if rec := acctCall(h, "alice", "POST", "/api/v1/me/devices/2/credentials", nil); rec.Code != 404 {
			t.Fatalf("refresh of a revoked device = %d, want 404", rec.Code)
		}
		if rec := acctCall(h, "alice", "DELETE", "/api/v1/me/devices/2", nil); rec.Code != 404 {
			t.Fatalf("second revoke = %d, want 404", rec.Code)
		}
	})
}

// TestWebDeviceRoutesNeedABrowserSessionOfAUser fails if a service account, a
// personal token or an anonymous caller can reach the device and ICE routes,
// or if they answer 503 elsewhere than when the feature is not configured.
func TestWebDeviceRoutesNeedABrowserSessionOfAUser(t *testing.T) {
	f, h, _, _ := newAcctFake(t)
	f.actors[hexOf("pat")] = auth.Actor{UserID: uAlice, Username: "alice", Role: auth.RoleUser, Kind: auth.KindPersonalToken, Scopes: auth.Scopes{auth.ScopeRead, auth.ScopeWrite}}
	routes := [][2]string{{"GET", "/api/v1/me/devices"}, {"POST", "/api/v1/me/devices"}, {"DELETE", "/api/v1/me/devices/1"},
		{"POST", "/api/v1/me/devices/1/credentials"}, {"GET", "/api/v1/me/ice-servers"}}
	for _, rt := range routes {
		var body any
		if rt[0] == "POST" && rt[1] == "/api/v1/me/devices" {
			body = map[string]any{"label": "x"}
		}
		if rec := acctCall(h, "svc", rt[0], rt[1], body); rec.Code != 403 || acctErrCode(rec) != "no_user" {
			t.Errorf("service account %s %s = %d %s, want 403 no_user", rt[0], rt[1], rec.Code, rec.Body)
		}
		if rec := acctCall(h, "", rt[0], rt[1], body); rec.Code != 401 {
			t.Errorf("anonymous %s %s = %d, want 401", rt[0], rt[1], rec.Code)
		}
		r := httptest.NewRequest(rt[0], rt[1], nil)
		r.Header.Set("Authorization", "Bearer pat")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != 403 {
			t.Errorf("personal token %s %s = %d %s, want 403 (no session scope)", rt[0], rt[1], rec.Code, rec.Body)
		}
	}
	if len(f.clients) != 0 {
		t.Fatalf("a refused request created a device: %v", f.clients)
	}

	// Not configured: 503, never a credential minted with an empty key.
	for name, cfg := range map[string]Config{
		"no key":   {Store: f, SIPDomain: testDomain, Clients: f},
		"no store": {Store: f, SIPDomain: testDomain, ClientAuth: testClientAuth()},
	} {
		h2 := Handler(cfg)
		rec := acctCall(h2, "alice", "POST", "/api/v1/me/devices", map[string]any{"label": "x"})
		if rec.Code != 503 || strings.Contains(rec.Body.String(), "sipPassword") {
			t.Errorf("%s: enrol = %d %s, want 503", name, rec.Code, rec.Body)
		}
	}
}

// TestICEServers fails if the endpoint hands out a TURN credential that
// coturn would not accept (the TURN REST API: username "<expiry>:<name>",
// credential base64(HMAC-SHA1(secret, username))), one that does not
// expire, one for another user's device, or if it answers when no server is
// configured.
func TestICEServers(t *testing.T) {
	f, h, _, logs := newAcctFake(t)
	wantJSON(t, acctCall(h, "alice", "POST", "/api/v1/me/devices", map[string]any{"label": "x"}), 201, "enrol")

	t.Run("credential validates like coturn would", func(t *testing.T) {
		got := wantJSON(t, acctCall(h, "alice", "GET", "/api/v1/me/ice-servers?deviceId=1", nil), 200, "ice")
		servers := got["iceServers"].([]any)
		stun, relay := servers[0].(map[string]any), servers[1].(map[string]any)
		if stun["urls"].([]any)[0] != "stun:turn1.test:3478" || stun["username"] != nil || stun["credential"] != nil {
			t.Fatalf("stun entry = %v", stun)
		}
		if urls := relay["urls"].([]any); len(urls) != 2 || urls[1] != "turns:turn1.test:5349?transport=tcp" {
			t.Fatalf("turn urls = %v", urls)
		}
		user, cred := relay["username"].(string), relay["credential"].(string)
		exp, name, _ := strings.Cut(user, ":")
		ts, err := strconv.ParseInt(exp, 10, 64)
		if err != nil || name != "1" {
			t.Fatalf("username = %q, want <expiry>:<deviceId>", user)
		}
		if left := time.Until(time.Unix(ts, 0)); left < 59*time.Minute || left > 61*time.Minute {
			t.Fatalf("credential expires in %v, want about 1h", left)
		}
		m := hmac.New(sha1.New, []byte(testTurnSecret))
		m.Write([]byte(user))
		if want := base64.StdEncoding.EncodeToString(m.Sum(nil)); cred != want {
			t.Fatal("credential is not HMAC-SHA1 of the username under the shared secret")
		}
		if got["ttlSeconds"] != float64(3600) {
			t.Fatalf("ttlSeconds = %v", got["ttlSeconds"])
		}
		if strings.Contains(logs.String(), cred) || strings.Contains(logs.String(), testTurnSecret) {
			t.Fatal("a TURN credential or the shared secret reached the log")
		}
		for _, a := range f.audit {
			if strings.Contains(a, cred) || strings.Contains(a, testTurnSecret) {
				t.Fatalf("audit carries a TURN secret: %q", a)
			}
		}
	})

	t.Run("without a device it names the user", func(t *testing.T) {
		got := wantJSON(t, acctCall(h, "bob", "GET", "/api/v1/me/ice-servers", nil), 200, "ice")
		user := got["iceServers"].([]any)[1].(map[string]any)["username"].(string)
		if !strings.HasSuffix(user, ":u"+strconv.Itoa(uBob)) {
			t.Fatalf("username = %q", user)
		}
	})

	t.Run("another user's device is 404", func(t *testing.T) {
		for _, q := range []string{"deviceId=1", "deviceId=99", "deviceId=0", "deviceId=x"} {
			if rec := acctCall(h, "bob", "GET", "/api/v1/me/ice-servers?"+q, nil); rec.Code != 404 {
				t.Errorf("bob ?%s = %d %s, want 404", q, rec.Code, rec.Body)
			}
		}
	})

	t.Run("not configured is 503", func(t *testing.T) {
		cfg := testClientAuth()
		cfg.ICE = turn.Config{}
		h2 := Handler(Config{Store: f, SIPDomain: testDomain, Clients: f, ClientAuth: cfg})
		if rec := acctCall(h2, "alice", "GET", "/api/v1/me/ice-servers", nil); rec.Code != 503 {
			t.Fatalf("no servers = %d %s, want 503", rec.Code, rec.Body)
		}
		// TURN URIs without the shared secret never produce a credential.
		cfg.ICE = turn.Config{TURN: []string{"turn:turn1.test:3478"}}
		h3 := Handler(Config{Store: f, SIPDomain: testDomain, Clients: f, ClientAuth: cfg})
		if rec := acctCall(h3, "alice", "GET", "/api/v1/me/ice-servers", nil); rec.Code != 503 {
			t.Fatalf("TURN without a secret = %d %s, want 503", rec.Code, rec.Body)
		}
	})
}

// TestDeviceRoutesRateLimited fails if a user can mint credentials without
// bound, if the limit is shared between users, or if a limited answer lacks
// Retry-After.
func TestDeviceRoutesRateLimited(t *testing.T) {
	_, h, _, _ := newAcctFake(t)
	var limited *httptest.ResponseRecorder
	for i := 0; i < credentialRate+5 && limited == nil; i++ {
		if rec := acctCall(h, "alice", "GET", "/api/v1/me/ice-servers", nil); rec.Code == 429 {
			limited = rec
		}
	}
	if limited == nil {
		t.Fatal("no limit after many requests")
	}
	if limited.Header().Get("Retry-After") == "" || acctErrCode(limited) != "rate_limited" {
		t.Fatalf("limited = %v %s", limited.Header(), limited.Body)
	}
	// The limit is per user and shared by the credential routes.
	for _, c := range [][2]string{{"GET", "/api/v1/me/devices"}, {"POST", "/api/v1/me/devices"}, {"DELETE", "/api/v1/me/devices/1"},
		{"POST", "/api/v1/me/devices/1/credentials"}, {"GET", "/api/v1/me/ice-servers"}} {
		var body any
		if c[0] == "POST" && c[1] == "/api/v1/me/devices" {
			body = map[string]any{"label": "x"}
		}
		if rec := acctCall(h, "alice", c[0], c[1], body); rec.Code != 429 {
			t.Errorf("alice %s %s = %d, want 429", c[0], c[1], rec.Code)
		}
	}
	if rec := acctCall(h, "bob", "GET", "/api/v1/me/ice-servers", nil); rec.Code != 200 {
		t.Fatalf("bob was limited by alice's requests: %d", rec.Code)
	}
}

func TestRateLimiterWindow(t *testing.T) {
	var l rateLimiter
	t0 := time.Unix(1000, 0)
	for i := 0; i < credentialRate; i++ {
		if !l.allow(1, t0) {
			t.Fatalf("request %d refused", i)
		}
	}
	if l.allow(1, t0.Add(time.Second)) {
		t.Fatal("over the limit accepted")
	}
	if !l.allow(2, t0.Add(time.Second)) {
		t.Fatal("another user limited")
	}
	if !l.allow(1, t0.Add(61*time.Second)) {
		t.Fatal("the limit did not reset with the window")
	}
}

// TestRingPreferences fails if a user cannot turn ringing of their web
// clients off and on through the profile, or if the profile does not report
// it.
func TestRingPreferences(t *testing.T) {
	_, h, _, _ := newAcctFake(t)
	me := wantJSON(t, acctCall(h, "alice", "GET", "/api/v1/auth/me", nil), 200, "me")
	if me["ringWeb"] != true || me["ringMobile"] != true {
		t.Fatalf("defaults = %v %v, want both on", me["ringWeb"], me["ringMobile"])
	}
	wantJSON(t, acctCall(h, "alice", "PATCH", "/api/v1/me/profile", map[string]any{"ringWeb": false}), 200, "off")
	me = wantJSON(t, acctCall(h, "alice", "GET", "/api/v1/auth/me", nil), 200, "me")
	if me["ringWeb"] != false || me["ringMobile"] != true {
		t.Fatalf("after turning ringWeb off = %v %v", me["ringWeb"], me["ringMobile"])
	}
	other := wantJSON(t, acctCall(h, "bob", "GET", "/api/v1/auth/me", nil), 200, "bob")
	if other["ringWeb"] != true {
		t.Fatal("alice's preference changed bob's")
	}
	if rec := acctCall(h, "alice", "PATCH", "/api/v1/me/profile", map[string]any{"ringWeb": "no"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("non-boolean = %d", rec.Code)
	}
}

func hexOf(s string) string { return hex.EncodeToString(auth.HashToken(s)) }
