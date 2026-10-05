package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/store"
)

// fakeDiag is DiagnosticsLive in memory.
type fakeDiag struct {
	mu       sync.Mutex
	down     bool
	bindings map[string][]livestate.Binding
	attempts map[string][]livestate.RegisterAttempt
	fails    map[string]livestate.AuthFailure
}

func newFakeDiag() *fakeDiag {
	return &fakeDiag{bindings: map[string][]livestate.Binding{}, attempts: map[string][]livestate.RegisterAttempt{},
		fails: map[string]livestate.AuthFailure{}}
}

// set changes the fake under its lock (the server reads it concurrently).
func (f *fakeDiag) set(change func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change()
}

var errFakeDown = errors.New("valkey down")

func (f *fakeDiag) Bindings(_ context.Context, aor string) ([]livestate.Binding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errFakeDown
	}
	return append([]livestate.Binding(nil), f.bindings[aor]...), nil
}

func (f *fakeDiag) RegisterAttempts(_ context.Context, device string) ([]livestate.RegisterAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]livestate.RegisterAttempt(nil), f.attempts[device]...), nil
}

func (f *fakeDiag) AuthFailures(_ context.Context, ip string) (livestate.AuthFailure, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.fails[ip]
	return a, ok, nil
}

func (f *fakeDiag) AllAuthFailures(context.Context) ([]livestate.AuthFailure, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []livestate.AuthFailure{}
	for _, a := range f.fails {
		out = append(out, a)
	}
	return out, nil
}

func (f *fakeDiag) ClearAuthFailures(_ context.Context, ip string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.fails, ip)
	return nil
}

// TestVerdictFor fails if a verdict is given for a registered device, if
// the decisive fact (disabled, blocked source, no attempts) is not
// preferred over the last response, or if a response code is explained
// as something other than what it measured.
func TestVerdictFor(t *testing.T) {
	on := store.Device{SIPUsername: "desk", Enabled: true}
	att := func(code int, creds, stale bool) []livestate.RegisterAttempt {
		return []livestate.RegisterAttempt{{Code: code, Reason: http.StatusText(code), Credentials: creds, Stale: stale, IP: "192.0.2.1"}}
	}
	blocked := &authSource{AuthFailure: livestate.AuthFailure{IP: "192.0.2.1", Failures: 12}, Blocked: true}
	cases := []struct {
		name       string
		dev        store.Device
		registered bool
		attempts   []livestate.RegisterAttempt
		src        *authSource
		want       string // "" = no verdict
		mentions   string
	}{
		{"registered", on, true, att(401, true, false), nil, "", ""},
		{"disabled", store.Device{SIPUsername: "desk"}, false, nil, nil, "disabled", "desk is disabled"},
		{"throttled beats last code", on, false, att(401, true, false), blocked, "throttled", "12 failed attempts"},
		{"nothing received", on, false, nil, nil, "no_register", "no REGISTER for sip:desk@hello.test"},
		{"expired", on, false, att(200, true, false), nil, "expired", "200"},
		{"stale", on, false, att(401, true, true), nil, "stale_nonce", "stale=true"},
		{"bad password", on, false, att(401, true, false), nil, "auth_failed", "hello.test"},
		{"no answer to challenge", on, false, att(401, false, false), nil, "challenge_unanswered", "did not answer"},
		{"forbidden", on, false, att(403, true, false), nil, "forbidden", "403 Forbidden"},
		{"too brief", on, false, att(423, true, false), nil, "interval_too_brief", "423"},
		{"unavailable", on, false, att(503, true, false), nil, "unavailable", "503"},
		{"other", on, false, att(400, true, false), nil, "rejected", "400 Bad Request"},
	}
	for _, c := range cases {
		got := verdictFor(c.dev, "sip:desk@hello.test", "hello.test", c.registered, c.attempts, c.src, 10)
		switch {
		case c.want == "" && got != nil:
			t.Errorf("%s: verdict %+v, want none", c.name, got)
		case c.want != "" && (got == nil || got.Code != c.want || !strings.Contains(got.Message, c.mentions)):
			t.Errorf("%s: verdict %+v, want %s mentioning %q", c.name, got, c.want, c.mentions)
		}
	}
}

// TestDeviceDiagnostics fails if the endpoint does not combine the
// device's bindings, attempts and its last source's throttle state into
// one answer with a verdict, answers for an unknown device, or does not
// answer 503 while live state is down.
func TestDeviceDiagnostics(t *testing.T) {
	diag := newFakeDiag()
	e := newEnvConfig(t, Config{Live: noLive{}, Diagnostics: diag, AuthFailLimit: 10}, nil)
	c := e.login()
	ext := c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "101", "name": "Desk"}).json(t)
	d := c.must(http.StatusCreated, "POST", "/api/v1/devices", map[string]any{"extensionId": ext["id"], "sipUsername": "101-desk"}).json(t)
	path := fmt.Sprintf("/api/v1/diagnostics/devices/%v", d["id"])

	got := c.must(http.StatusOK, "GET", path, nil).json(t)
	if got["registered"] != false || got["aor"] != "sip:101-desk@"+testDomain || len(got["attempts"].([]any)) != 0 || got["source"] != nil {
		t.Fatalf("fresh device = %v", got)
	}
	if v := got["verdict"].(map[string]any); v["code"] != "no_register" {
		t.Fatalf("fresh verdict = %v, want no_register", v)
	}

	now := time.Now().UTC()
	diag.set(func() {
		diag.attempts["101-desk"] = []livestate.RegisterAttempt{
			{At: now, Device: "101-desk", Source: "192.0.2.9:5060", IP: "192.0.2.9", Node: "sip-1", Credentials: true, Code: 403, Reason: "Forbidden"},
			{At: now.Add(-time.Second), Device: "101-desk", Source: "192.0.2.9:5060", IP: "192.0.2.9", Node: "sip-1", Code: 401, Reason: "Unauthorized"},
		}
		diag.fails["192.0.2.9"] = livestate.AuthFailure{IP: "192.0.2.9", Failures: 11, WindowEndsAt: now.Add(time.Minute)}
	})
	got = c.must(http.StatusOK, "GET", path, nil).json(t)
	src, _ := got["source"].(map[string]any)
	if len(got["attempts"].([]any)) != 2 || src["blocked"] != true || src["failures"] != float64(11) || got["authFailLimit"] != float64(10) {
		t.Fatalf("throttled device = %v", got)
	}
	if v := got["verdict"].(map[string]any); v["code"] != "throttled" {
		t.Fatalf("verdict = %v, want throttled", v)
	}

	diag.set(func() {
		diag.bindings["sip:101-desk@"+testDomain] = []livestate.Binding{{AOR: "sip:101-desk@" + testDomain, Device: "101-desk",
			ContactURI: "sip:101-desk@192.0.2.9:5060", Path: []string{"<sip:edge;lr;hflow=secret>"}, Expires: now.Add(time.Hour)}}
	})
	got = c.must(http.StatusOK, "GET", path, nil).json(t)
	if got["registered"] != true || got["verdict"] != nil {
		t.Fatalf("registered device = %v, want no verdict", got)
	}
	if b := got["bindings"].([]any)[0].(map[string]any); fmt.Sprint(b["path"]) != "[<sip:edge;lr;hflow=REDACTED>]" {
		t.Fatalf("binding path = %v, want the flow token redacted", b["path"])
	}

	c.must(http.StatusNotFound, "GET", "/api/v1/diagnostics/devices/99999", nil)
	diag.set(func() { diag.down = true })
	c.must(http.StatusServiceUnavailable, "GET", path, nil)
}

// TestAuthFailuresListAndClear fails if throttled sources are not listed
// with whether they are blocked, a source cannot be cleared, or a
// malformed IP is accepted.
func TestAuthFailuresListAndClear(t *testing.T) {
	diag := newFakeDiag()
	diag.fails["192.0.2.1"] = livestate.AuthFailure{IP: "192.0.2.1", Failures: 3}
	diag.fails["192.0.2.2"] = livestate.AuthFailure{IP: "192.0.2.2", Failures: 10}
	e := newEnvConfig(t, Config{Live: noLive{}, Diagnostics: diag, AuthFailLimit: 10}, nil)
	c := e.login()

	got := c.must(http.StatusOK, "GET", "/api/v1/diagnostics/auth-failures", nil).json(t)
	blocked := map[string]any{}
	for _, it := range got["items"].([]any) {
		m := it.(map[string]any)
		blocked[m["ip"].(string)] = m["blocked"]
	}
	if got["limit"] != float64(10) || blocked["192.0.2.1"] != false || blocked["192.0.2.2"] != true {
		t.Fatalf("auth failures = %v", got)
	}
	c.must(http.StatusNoContent, "DELETE", "/api/v1/diagnostics/auth-failures/192.0.2.2", nil)
	if _, ok, _ := diag.AuthFailures(context.Background(), "192.0.2.2"); ok {
		t.Fatal("192.0.2.2 still has a counter after DELETE")
	}
	c.must(http.StatusNotFound, "DELETE", "/api/v1/diagnostics/auth-failures/not-an-ip", nil)

	none := newEnvConfig(t, Config{Live: noLive{}}, nil).login()
	none.must(http.StatusServiceUnavailable, "GET", "/api/v1/diagnostics/auth-failures", nil)
}
