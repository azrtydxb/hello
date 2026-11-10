package api

// Tests for the webhook routes. They run through Handler over the in-memory
// account fake and webhook.Mem, so they need no services.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/events"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/azrtydxb/hello/internal/webhook"
)

// whStore adds two API tokens: one with the admin scope only (no secrets)
// and one that only reads.
type whStore struct{ *acctFake }

func (s whStore) TokenActor(ctx context.Context, h []byte) (auth.Actor, error) {
	switch hex.EncodeToString(h) {
	case hex.EncodeToString(auth.HashToken("tok-admin-only")):
		return auth.Actor{UserID: uCarol, Username: "carol", Role: auth.RoleAdmin, Kind: auth.KindPersonalToken,
			Scopes: auth.Scopes{auth.ScopeAdmin}}, nil
	case hex.EncodeToString(auth.HashToken("tok-read")):
		return auth.Actor{UserID: uCarol, Username: "carol", Role: auth.RoleAdmin, Kind: auth.KindPersonalToken,
			Scopes: auth.Scopes{auth.ScopeRead}}, nil
	}
	return s.acctFake.TokenActor(ctx, h)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type whEnv struct {
	h    http.Handler
	mem  *webhook.Mem
	svc  *webhook.Service
	seen []*http.Request
	// answer is what the receiver says to a test ping.
	status int
	body   string
	n      int
}

func newWhEnv(t *testing.T) *whEnv {
	t.Helper()
	f, _, _, _ := newAcctFake(t)
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, err := secret.New(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	e := &whEnv{mem: webhook.NewMem(), status: 200}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		e.seen = append(e.seen, r)
		return &http.Response{StatusCode: e.status, Body: io.NopCloser(strings.NewReader(e.body)), Header: http.Header{}}, nil
	})}
	e.svc = &webhook.Service{Store: e.mem, Box: box, PublicClient: client, PrivateClient: client,
		AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}
	e.h = Handler(Config{Store: whStore{f}, Webhooks: e.svc, Log: slog.New(slog.DiscardHandler), SessionTTL: time.Hour})
	return e
}

func (e *whEnv) call(who, bearer, method, path string, body any) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, rd)
	if who != "" {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: who + cookieSuffix})
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func (e *whEnv) admin(method, path string, body any) *httptest.ResponseRecorder {
	return e.call("carol", "", method, path, body)
}

func (e *whEnv) create(t *testing.T, body map[string]any) (map[string]any, string) {
	t.Helper()
	rec := e.admin("POST", "/api/v1/webhooks", body)
	m := wantJSON(t, rec, http.StatusCreated, "create webhook")
	sec, _ := m["secret"].(string)
	if !strings.HasPrefix(sec, "whsec_") || len(sec) < 40 {
		t.Fatalf("secret = %q", sec)
	}
	return m, sec
}

// dead puts one dead letter of the endpoint in the store.
func (e *whEnv) dead(t *testing.T, endpoint int64) webhook.Delivery {
	t.Helper()
	ctx := context.Background()
	e.n++
	ev := events.New(events.CallEnded, events.Data{CallID: "call-" + strconv.Itoa(e.n), Extension: "101"}, "", time.Now())
	if err := e.mem.Enqueue(ctx, nil, events.Entry{Cursor: "1-0", Event: ev}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cl, err := e.mem.Claim(ctx, now, 10, 10, time.Minute)
	if err != nil || len(cl) == 0 {
		t.Fatalf("Claim = %v, %v", cl, err)
	}
	d := cl[len(cl)-1]
	if _, err := e.mem.Finish(ctx, webhook.Finish{DeliveryID: d.ID, Now: now, Ref: "dlv_x", Outcome: webhook.OutcomeDead, Status: 400, Error: "http 400"}); err != nil {
		t.Fatal(err)
	}
	return d.Delivery
}

// TestWebhookEndpointLifecycle fails if the webhook endpoints cannot be
// created, read, changed, rotated and deleted as documented: the signing
// secret is shown once (create, rotate) and never again, a repeated
// subscription returns the same endpoint, and secrets reach no other
// response.
func TestWebhookEndpointLifecycle(t *testing.T) {
	e := newWhEnv(t)
	all := &strings.Builder{}
	seen := func(rec *httptest.ResponseRecorder) { all.Write(rec.Body.Bytes()) }

	rec := e.admin("GET", "/api/v1/webhooks", nil)
	if m := wantJSON(t, rec, 200, "empty list"); len(nextOf(m)) != 0 {
		t.Fatalf("list = %s", rec.Body)
	}

	created, sec := e.create(t, map[string]any{"name": "CRM", "url": "https://hooks.example.com/in", "events": []string{"call.missed", "call.*"}, "extensions": []string{"101"}})
	id := int64(created["id"].(float64))
	if created["enabled"] != true || created["allowPrivate"] != false || created["disabledReason"] != "" || created["pending"] != float64(0) {
		t.Fatalf("created = %v", created)
	}
	path := "/api/v1/webhooks/" + strconv.FormatInt(id, 10)

	// Subscribing the same URL and events again returns the endpoint, not a
	// second one, and does not show its secret.
	rec = e.admin("POST", "/api/v1/webhooks", map[string]any{"name": "again", "url": "https://hooks.example.com/in", "events": []string{"call.*", "call.missed"}})
	again := wantJSON(t, rec, 200, "repeated subscription")
	if again["id"] != created["id"] || again["secret"] != nil {
		t.Fatalf("repeated subscription = %v", again)
	}
	seen(rec)

	rec = e.admin("GET", path, nil)
	got := wantJSON(t, rec, 200, "get")
	seen(rec)
	if _, has := got["secret"]; has || got["name"] != "CRM" || got["url"] != "https://hooks.example.com/in" {
		t.Fatalf("get = %v", got)
	}
	seen(e.admin("GET", "/api/v1/webhooks", nil))
	wantJSON(t, e.admin("GET", "/api/v1/webhooks/999", nil), 404, "get unknown")

	rec = e.admin("PATCH", path, map[string]any{"name": "CRM sync", "enabled": false, "events": []string{"call.completed"}})
	up := wantJSON(t, rec, 200, "patch")
	seen(rec)
	if up["name"] != "CRM sync" || up["enabled"] != false || up["disabledReason"] != "manual" || up["events"].([]any)[0] != "call.completed" {
		t.Fatalf("patched = %v", up)
	}
	rec = e.admin("PATCH", path, map[string]any{"enabled": true})
	if up = wantJSON(t, rec, 200, "enable"); up["enabled"] != true || up["disabledReason"] != "" {
		t.Fatalf("enabled = %v", up)
	}
	for name, body := range map[string]map[string]any{
		"http url":      {"url": "http://hooks.example.com/in"},
		"metadata url":  {"url": "https://169.254.169.254/latest"},
		"bad type":      {"events": []string{"call.nope"}},
		"bad extension": {"extensions": []string{"abc"}},
	} {
		wantJSON(t, e.admin("PATCH", path, body), 400, name)
	}
	wantJSON(t, e.admin("PATCH", "/api/v1/webhooks/999", map[string]any{"name": "x"}), 404, "patch unknown")
	wantJSON(t, e.admin("PATCH", path, map[string]any{"bogus": 1}), 400, "patch unknown field")

	// Rotation returns a new secret, once.
	rec = e.admin("POST", path+"/rotate-secret", nil)
	rot := wantJSON(t, rec, 200, "rotate")
	fresh, _ := rot["secret"].(string)
	if !strings.HasPrefix(fresh, "whsec_") || fresh == sec || rot["previousSecretExpiresAt"] == nil {
		t.Fatalf("rotated = %v", rot)
	}
	wantJSON(t, e.admin("POST", "/api/v1/webhooks/999/rotate-secret", nil), 404, "rotate unknown")

	if strings.Contains(all.String(), sec) || strings.Contains(all.String(), fresh) || strings.Contains(strings.ToLower(all.String()), "whsec_") {
		t.Fatal("a signing secret reached a response other than create and rotate")
	}

	if rec = e.admin("DELETE", path, nil); rec.Code != 204 {
		t.Fatalf("delete = %d", rec.Code)
	}
	wantJSON(t, e.admin("DELETE", path, nil), 404, "delete again")
	wantJSON(t, e.admin("GET", path, nil), 404, "get deleted")
}

// TestWebhookCreateValidation fails if an endpoint can be created that points
// at an internal address, uses a scheme or credentials it must not, names an
// unknown event type, or is missing a required field, or if a private target
// is allowed outside the administrator's allow-list.
func TestWebhookCreateValidation(t *testing.T) {
	e := newWhEnv(t)
	for name, body := range map[string]map[string]any{
		"no name":            {"url": "https://hooks.example.com/in"},
		"no url":             {"name": "x"},
		"http":               {"name": "x", "url": "http://hooks.example.com/in"},
		"credentials":        {"name": "x", "url": "https://u:p@hooks.example.com/in"},
		"loopback":           {"name": "x", "url": "https://127.0.0.1/in"},
		"localhost":          {"name": "x", "url": "https://localhost/in"},
		"metadata":           {"name": "x", "url": "https://169.254.169.254/latest/meta-data"},
		"private address":    {"name": "x", "url": "https://10.0.0.5/in"},
		"cgnat":              {"name": "x", "url": "https://100.64.1.1/in"},
		"private not listed": {"name": "x", "url": "http://192.168.1.1/in", "allowPrivate": true},
		"private loopback":   {"name": "x", "url": "http://127.0.0.1/in", "allowPrivate": true},
		"private metadata":   {"name": "x", "url": "http://169.254.169.254/in", "allowPrivate": true},
		"unknown type":       {"name": "x", "url": "https://hooks.example.com/in", "events": []string{"call.nope"}},
		"bad extension":      {"name": "x", "url": "https://hooks.example.com/in", "extensions": []string{"1"}},
		"long name":          {"name": strings.Repeat("n", 65), "url": "https://hooks.example.com/in"},
		"unknown field":      {"name": "x", "url": "https://hooks.example.com/in", "secret": "mine"},
	} {
		if rec := e.admin("POST", "/api/v1/webhooks", body); rec.Code != 400 {
			t.Errorf("%s: %d %s, want 400", name, rec.Code, rec.Body)
		}
	}
	// A private target inside the allow-list is accepted, with http.
	m, _ := e.create(t, map[string]any{"name": "lan", "url": "http://10.1.2.3:8080/in", "allowPrivate": true})
	if m["allowPrivate"] != true {
		t.Fatalf("created = %v", m)
	}
	wantJSON(t, e.admin("POST", "/api/v1/webhooks", map[string]any{"name": "x", "url": "http://10.1.2.3:8080/in2", "allowPrivate": false}), 400, "http without allowPrivate")
}

// TestWebhookCreateIsGatedOnSecrets fails if a credential without the secrets
// scope (an admin-scope token) can create an endpoint or rotate a secret,
// both of which return the signing secret, or if a viewer or a read-only
// token reaches any webhook route.
func TestWebhookCreateIsGatedOnSecrets(t *testing.T) {
	e := newWhEnv(t)
	created, _ := e.create(t, map[string]any{"name": "CRM", "url": "https://hooks.example.com/in"})
	id := strconv.FormatInt(int64(created["id"].(float64)), 10)
	body := map[string]any{"name": "x", "url": "https://hooks.example.com/other"}
	if rec := e.call("", "tok-admin-only", "POST", "/api/v1/webhooks", body); rec.Code != 403 {
		t.Errorf("create with the admin scope only = %d, want 403", rec.Code)
	}
	if rec := e.call("", "tok-admin-only", "POST", "/api/v1/webhooks/"+id+"/rotate-secret", nil); rec.Code != 403 {
		t.Errorf("rotate with the admin scope only = %d, want 403", rec.Code)
	}
	if rec := e.call("", "tok-admin-only", "GET", "/api/v1/webhooks/"+id, nil); rec.Code != 200 {
		t.Errorf("get with the admin scope = %d, want 200", rec.Code)
	}
	for _, rt := range Routes() {
		if !strings.HasPrefix(rt.Pattern, "/api/v1/webhooks") {
			continue
		}
		path := strings.NewReplacer("{id}", id, "{deliveryId}", "1").Replace(rt.Pattern)
		for who, bearer := range map[string]string{"dave": "", "alice": "", "": "tok-read"} {
			if rec := e.call(who, bearer, rt.Method, path, nil); rec.Code != 403 {
				t.Errorf("%s %s as %q%s = %d, want 403", rt.Method, rt.Pattern, who, bearer, rec.Code)
			}
		}
		if rec := e.call("", "", rt.Method, path, nil); rec.Code != 401 {
			t.Errorf("%s %s without credentials = %d, want 401", rt.Method, rt.Pattern, rec.Code)
		}
	}
}

// TestWebhookTest fails if the test ping does not report the receiver's
// status and body (at most a kilobyte), reports a failure as delivered, or
// mints a delivery.
func TestWebhookTest(t *testing.T) {
	e := newWhEnv(t)
	created, _ := e.create(t, map[string]any{"name": "CRM", "url": "https://hooks.example.com/in"})
	id := strconv.FormatInt(int64(created["id"].(float64)), 10)
	e.body = "pong"
	m := wantJSON(t, e.admin("POST", "/api/v1/webhooks/"+id+"/test", nil), 200, "test")
	if m["delivered"] != true || m["status"] != float64(200) || m["body"] != "pong" || m["error"] != "" {
		t.Fatalf("test = %v", m)
	}
	if len(e.seen) != 1 || e.seen[0].Header.Get(webhook.HeaderEventType) != "webhook.test" || e.seen[0].Header.Get("Date") == "" ||
		!strings.HasPrefix(e.seen[0].Header.Get(webhook.HeaderSignature), "t=") {
		t.Fatalf("ping = %+v", e.seen)
	}
	e.status, e.body = 500, strings.Repeat("x", 3000)
	m = wantJSON(t, e.admin("POST", "/api/v1/webhooks/"+id+"/test", nil), 200, "test failing")
	if m["delivered"] != false || m["status"] != float64(500) || len(m["body"].(string)) != 1024 {
		t.Fatalf("failing test = %v", m)
	}
	wantJSON(t, e.admin("POST", "/api/v1/webhooks/999/test", nil), 404, "test unknown")
	rec := e.admin("GET", "/api/v1/webhooks/"+id+"/deliveries", nil)
	if m = wantJSON(t, rec, 200, "deliveries"); len(nextOf(m)) != 0 {
		t.Fatalf("a test ping created deliveries: %s", rec.Body)
	}
}

// TestWebhookDeliveriesAndDeadLetters fails if the delivery log cannot be
// listed, filtered and paged, if a delivery's attempt log is missing, if a
// dead letter cannot be redelivered singly or in bulk, or if a delivery of
// another endpoint is readable through this one.
func TestWebhookDeliveriesAndDeadLetters(t *testing.T) {
	e := newWhEnv(t)
	a, _ := e.create(t, map[string]any{"name": "A", "url": "https://hooks.example.com/a"})
	aid := int64(a["id"].(float64))
	for range 3 {
		e.dead(t, aid) // three dead letters of A
	}
	b, _ := e.create(t, map[string]any{"name": "B", "url": "https://hooks.example.com/b"})
	bid := int64(b["id"].(float64))
	pathA := "/api/v1/webhooks/" + strconv.FormatInt(aid, 10)
	pathB := "/api/v1/webhooks/" + strconv.FormatInt(bid, 10)

	rec := e.admin("GET", pathA+"/deliveries?status=dead&limit=2", nil)
	page := wantJSON(t, rec, 200, "dead page")
	rows := nextOf(page)
	if len(rows) != 2 || page["nextCursor"] == "" || rows[0]["status"] != "dead" || rows[0]["lastStatus"] != float64(400) || rows[0]["nextAttemptAt"] != nil {
		t.Fatalf("page = %s", rec.Body)
	}
	rest := nextOf(wantJSON(t, e.admin("GET", pathA+"/deliveries?status=dead&before="+page["nextCursor"].(string), nil), 200, "next page"))
	if len(rest) == 0 || rest[0]["id"].(float64) >= rows[1]["id"].(float64) {
		t.Fatalf("next page = %v", rest)
	}
	if got := nextOf(wantJSON(t, e.admin("GET", pathA+"/deliveries?status=succeeded", nil), 200, "none succeeded")); len(got) != 0 {
		t.Fatalf("succeeded = %v", got)
	}
	for _, q := range []string{"status=bogus", "limit=0", "limit=501", "limit=x", "before=0", "before=x"} {
		if rec := e.admin("GET", pathA+"/deliveries?"+q, nil); rec.Code != 400 {
			t.Errorf("?%s = %d, want 400", q, rec.Code)
		}
	}
	wantJSON(t, e.admin("GET", "/api/v1/webhooks/999/deliveries", nil), 404, "deliveries of an unknown endpoint")

	did := strconv.FormatInt(int64(rows[0]["id"].(float64)), 10)
	rec = e.admin("GET", pathA+"/deliveries/"+did, nil)
	det := wantJSON(t, rec, 200, "delivery detail")
	log, _ := det["log"].([]any)
	if len(log) != 1 || log[0].(map[string]any)["deliveryId"] != "dlv_x" || log[0].(map[string]any)["status"] != float64(400) || det["event"] == nil {
		t.Fatalf("detail = %s", rec.Body)
	}
	wantJSON(t, e.admin("GET", pathB+"/deliveries/"+did, nil), 404, "delivery through another endpoint")
	wantJSON(t, e.admin("GET", pathA+"/deliveries/999", nil), 404, "unknown delivery")
	wantJSON(t, e.admin("GET", pathA+"/deliveries/x", nil), 404, "bad delivery id")

	// Redeliver one, then the rest.
	rd := wantJSON(t, e.admin("POST", "/api/v1/webhooks/deliveries/"+did+"/redeliver", nil), 200, "redeliver")
	if rd["status"] != "pending" || rd["nextAttemptAt"] == nil {
		t.Fatalf("redelivered = %v", rd)
	}
	wantJSON(t, e.admin("POST", "/api/v1/webhooks/deliveries/999/redeliver", nil), 404, "redeliver unknown")
	bulk := wantJSON(t, e.admin("POST", pathA+"/deliveries/redeliver-dead", nil), 200, "redeliver dead")
	if bulk["redelivered"] != float64(2) {
		t.Fatalf("bulk = %v", bulk)
	}
	wantJSON(t, e.admin("POST", "/api/v1/webhooks/999/deliveries/redeliver-dead", nil), 404, "redeliver dead of an unknown endpoint")
	if got := nextOf(wantJSON(t, e.admin("GET", pathA+"/deliveries?status=dead", nil), 200, "no dead left")); len(got) != 0 {
		t.Fatalf("dead after the bulk redelivery = %v", got)
	}

	// A delivery a worker is sending cannot be redelivered.
	cl, err := e.mem.Claim(context.Background(), time.Now(), 10, 10, time.Minute)
	if err != nil || len(cl) == 0 {
		t.Fatalf("Claim = %v, %v", cl, err)
	}
	wantJSON(t, e.admin("POST", "/api/v1/webhooks/deliveries/"+strconv.FormatInt(cl[0].ID, 10)+"/redeliver", nil), 409, "redeliver in flight")
}

// TestWebhooksUnavailable fails if the webhook routes do not answer 503 while
// the service is not configured.
func TestWebhooksUnavailable(t *testing.T) {
	f, _, _, _ := newAcctFake(t)
	h := Handler(Config{Store: whStore{f}, Log: slog.New(slog.DiscardHandler), SessionTTL: time.Hour})
	for _, rt := range Routes() {
		if !strings.HasPrefix(rt.Pattern, "/api/v1/webhooks") {
			continue
		}
		path := strings.NewReplacer("{id}", "1", "{deliveryId}", "1").Replace(rt.Pattern)
		if rec := acctCall(h, "carol", rt.Method, path, map[string]any{}); rec.Code != 503 {
			t.Errorf("%s %s = %d, want 503", rt.Method, rt.Pattern, rec.Code)
		}
	}
}
