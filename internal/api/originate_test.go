package api

// Click-to-call routes (spec integrations I-37 to I-39): who may ring which
// extension, what is refused, the limits, idempotency, state and hang-up.
// They run through Handler over the in-memory user store with in-memory
// click-to-call stores, so they need no services.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/originate"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// origStore adds what the originate routes read beyond the account fake: the
// extension list, devices, the routing configuration and two service tokens.
type origStore struct {
	*acctFake
	cfg routing.Config
}

func (s origStore) ListExtensions(context.Context) ([]store.Extension, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.Extension
	for _, e := range s.exts {
		out = append(out, *e)
	}
	return out, nil
}

func (s origStore) GetDevice(_ context.Context, id int64) (store.Device, error) {
	if id == 77 {
		return store.Device{ID: 77, ExtensionID: extAlice, SIPUsername: "alice-desk"}, nil
	}
	return store.Device{}, store.ErrNotFound
}

func (s origStore) RoutingConfig(context.Context) (store.RoutingSnapshot, error) {
	return store.RoutingSnapshot{Config: s.cfg}, nil
}

func (s origStore) TokenActor(ctx context.Context, h []byte) (auth.Actor, error) {
	switch hex.EncodeToString(h) {
	case hex.EncodeToString(auth.HashToken("svc-scoped")):
		return auth.Actor{Username: "", Role: auth.RoleOperator, Kind: auth.KindService, ServiceName: "zap", ClientID: "svc1",
			Scopes: auth.Scopes{auth.ScopeCalls}, ExtensionScope: []string{"101"}, Audience: []string{"https://hello.example/api/v1"}}, nil
	case hex.EncodeToString(auth.HashToken("svc-all")):
		return auth.Actor{Role: auth.RoleOperator, Kind: auth.KindService, ServiceName: "n8n", ClientID: "svc2",
			Scopes: auth.Scopes{auth.ScopeCalls}, Audience: []string{"https://hello.example/api/v1"}}, nil
	case hex.EncodeToString(auth.HashToken("tok-noscope")):
		return auth.Actor{UserID: uAlice, Username: "alice", Role: auth.RoleUser, Kind: auth.KindPersonalToken, TokenID: 9,
			Scopes: auth.Scopes{auth.ScopeRead}}, nil
	}
	return s.acctFake.TokenActor(ctx, h)
}

// fakeOriginateStore is OriginateStore in memory.
type fakeOriginateStore struct {
	mu       sync.Mutex
	recs     map[string]store.OriginateRecord
	settings store.OriginateSettings
}

func (f *fakeOriginateStore) CreateOriginate(_ context.Context, o store.OriginateRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	o.State, o.CreatedAt, o.UpdatedAt = "queued", time.Now(), time.Now()
	f.recs[o.ID] = o
	return nil
}

func (f *fakeOriginateStore) GetOriginate(_ context.Context, id string) (store.OriginateRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if o, ok := f.recs[id]; ok {
		return o, nil
	}
	return store.OriginateRecord{}, store.ErrNotFound
}

func (f *fakeOriginateStore) SetOriginateState(_ context.Context, id, state, reason, corr string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	o := f.recs[id]
	o.State, o.Reason = state, reason
	if corr != "" {
		o.CorrelationID = corr
	}
	f.recs[id] = o
	return nil
}

func (f *fakeOriginateStore) GetOriginateSettings(context.Context) (store.OriginateSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.settings, nil
}

func (f *fakeOriginateStore) PutOriginateSettings(_ context.Context, _ string, in store.OriginateSettings) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settings = in
	return nil
}

// fakeOriginateLive is OriginateLive in memory.
type fakeOriginateLive struct {
	*originate.Memory
	mu        sync.Mutex
	queue     []livestate.OriginateRequest
	states    map[string]livestate.OriginateState
	cancelled map[string]bool
	idem      map[string]string
	down      bool
}

func (f *fakeOriginateLive) EnqueueOriginate(_ context.Context, r livestate.OriginateRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return errValkeyDown
	}
	f.queue = append(f.queue, r)
	return nil
}

func (f *fakeOriginateLive) PutOriginateState(_ context.Context, st livestate.OriginateState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return errValkeyDown
	}
	if st.UpdatedAt.IsZero() {
		st.UpdatedAt = time.Now()
	}
	f.states[st.ID] = st
	return nil
}

func (f *fakeOriginateLive) GetOriginateState(_ context.Context, id string) (livestate.OriginateState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.states[id]
	return st, ok, nil
}

func (f *fakeOriginateLive) CancelOriginate(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled[id] = true
	return nil
}

func (f *fakeOriginateLive) LookupIdempotent(_ context.Context, key string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.idem[key]
	return id, ok, nil
}

func (f *fakeOriginateLive) Idempotent(_ context.Context, key, id string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cur, ok := f.idem[key]; ok {
		return cur, false, nil
	}
	f.idem[key] = id
	return id, true, nil
}

var errValkeyDown = fmt.Errorf("valkey down")

type origEnv struct {
	t       *testing.T
	f       *acctFake
	h       http.Handler
	recs    *fakeOriginateStore
	live    *fakeOriginateLive
	metrics *OriginateMetrics
	// free releases the extension's setup slot after every accepted request,
	// as a node does once the call is up, so a test can place several.
	free bool
}

func newOrigAPI(t *testing.T, cfg routing.Config, limits *originate.Limits) *origEnv {
	t.Helper()
	f, _, _, _ := newAcctFake(t)
	recs := &fakeOriginateStore{recs: map[string]store.OriginateRecord{}, settings: store.OriginateSettings{Destinations: "inherit", CountryPrefixes: []string{}}}
	live := &fakeOriginateLive{Memory: originate.NewMemory(), states: map[string]livestate.OriginateState{}, cancelled: map[string]bool{}, idem: map[string]string{}}
	metrics := NewOriginateMetrics(prometheus.NewRegistry())
	h := Handler(Config{Store: origStore{f, cfg}, AI: fakeAI{}, Originate: recs, OriginateLive: live, OriginateLimits: limits, OriginateMetrics: metrics,
		Log: slog.New(slog.DiscardHandler), SessionTTL: time.Hour})
	return &origEnv{t: t, f: f, h: h, recs: recs, live: live, metrics: metrics}
}

func (e *origEnv) as(who, method, path string, body any, hdr ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	rec := acctCallHdr(e.h, who, method, path, body, hdr...)
	if e.free && method == "POST" && rec.Code == 202 {
		var m map[string]any
		if json.Unmarshal(rec.Body.Bytes(), &m) == nil {
			if id, ok := m["originateId"].(string); ok {
				_ = e.live.ReleaseOriginateSetup(context.Background(), m["extension"].(string), id)
			}
		}
	}
	return rec
}

// acctCallHdr is acctCall with a bearer token when who starts with "tok:" and
// extra headers as name, value pairs.
func acctCallHdr(h http.Handler, who, method, path string, body any, hdr ...string) *httptest.ResponseRecorder {
	var rd *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	r := httptest.NewRequest(method, path, rd)
	if tok, ok := strings.CutPrefix(who, "tok:"); ok {
		r.Header.Set("Authorization", "Bearer "+tok)
	} else if who != "" {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: who + cookieSuffix})
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func code(rec *httptest.ResponseRecorder) string {
	var m struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return m.Error.Code
}

func body(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body %q: %v", rec.Body, err)
	}
	return m
}

var emergencyCfg = routing.Config{
	Trunks: []routing.Trunk{{ID: 1, Name: "carrier", Mode: "ip", Enabled: true, Destinations: []routing.Destination{{Host: "10.0.0.5", Port: 5060}},
		OptionsInterval: time.Second, RegisterExpires: time.Minute}},
	Outbound: []routing.OutboundRoute{
		{ID: 1, Position: 1, Name: "Emergency", MatchKind: "prefix", Match: "999", Trunks: []int64{1}, Enabled: true, Emergency: true},
		{ID: 2, Position: 2, Name: "Out", MatchKind: "prefix", Match: "0", Trunks: []int64{1}, Enabled: true},
	},
	Extensions: map[string]string{"101": "+3211111", "102": "", "103": ""},
}

// TestOriginateWhoMayRing fails if click-to-call lets a user ring another
// extension, lets a user without an extension place a call, defaults to the
// wrong extension, or if an operator, a service account inside its
// extensionScope or a service account without one cannot name an extension.
func TestOriginateWhoMayRing(t *testing.T) {
	e := newOrigAPI(t, emergencyCfg, nil)
	rq := map[string]any{"destination": "0501234567"}

	rec := e.as("alice", "POST", "/api/v1/calls/originate", rq)
	if rec.Code != 202 {
		t.Fatalf("alice own extension = %d %s", rec.Code, rec.Body)
	}
	m := body(t, rec)
	if m["state"] != "queued" || m["extension"] != "101" || !strings.HasPrefix(m["originateId"].(string), "orig_") {
		t.Fatalf("response = %v", m)
	}
	if len(e.live.queue) != 1 || e.live.queue[0].Extension != "101" || e.live.queue[0].Destination != "0501234567" || e.live.queue[0].Actor != "user:alice" {
		t.Fatalf("queued = %+v", e.live.queue)
	}
	if !slices.ContainsFunc(e.f.audit, func(a string) bool {
		return strings.HasPrefix(a, "user:alice originate call orig_") && strings.Contains(a, "101 0501***")
	}) {
		t.Fatalf("audit = %v (want the extension and a redacted number)", e.f.audit)
	}

	for _, c := range []struct {
		name, who string
		body      map[string]any
		want      int
		wantCode  string
	}{
		{"user naming another extension", "alice", map[string]any{"destination": "0501234567", "extension": "102"}, 403, "forbidden_role"},
		{"user naming her own", "alice", map[string]any{"destination": "0501234567", "extension": "101"}, 202, ""},
		{"user without an extension", "noext", rq, 403, "no_extension"},
		{"admin without an extension and none named", "carol", rq, 400, "bad_request"},
		{"admin naming any extension", "carol", map[string]any{"destination": "0501234567", "extension": "102"}, 202, ""},
		{"unknown extension", "carol", map[string]any{"destination": "0501234567", "extension": "555"}, 404, "not_found"},
		{"service inside its scope", "tok:svc-scoped", map[string]any{"destination": "0501234567", "extension": "101"}, 202, ""},
		{"service outside its scope", "tok:svc-scoped", map[string]any{"destination": "0501234567", "extension": "102"}, 403, "forbidden_role"},
		{"service with one extension in scope defaults to it", "tok:svc-scoped", rq, 202, ""},
		{"service without a scope names any", "tok:svc-all", map[string]any{"destination": "0501234567", "extension": "103"}, 202, ""},
		{"service without a scope must name one", "tok:svc-all", rq, 400, "bad_request"},
		{"a token without the calls scope", "tok:tok-noscope", rq, 403, "insufficient_scope"},
	} {
		// A fresh environment per case: the limits must not interfere.
		e := newOrigAPI(t, emergencyCfg, &originate.Limits{})
		rec := e.as(c.who, "POST", "/api/v1/calls/originate", c.body)
		if rec.Code != c.want || (c.wantCode != "" && code(rec) != c.wantCode) {
			t.Errorf("%s: %d %s, want %d %s", c.name, rec.Code, rec.Body, c.want, c.wantCode)
		}
	}
}

// TestOriginateRefusals fails if a SIP URI, an over-long number, an emergency
// number, a foreign callerId or an unauthorised auto-answer is accepted, or if
// an allowed callerId or an enabled auto-answer is refused.
func TestOriginateRefusals(t *testing.T) {
	post := func(e *origEnv, b map[string]any) *httptest.ResponseRecorder {
		return e.as("alice", "POST", "/api/v1/calls/originate", b)
	}
	e := newOrigAPI(t, emergencyCfg, &originate.Limits{})
	e.free = true
	for _, dest := range []string{"sip:evil@host.example", "0501234567@host", "", strings.Repeat("1", 65), "05 01", "050;x", "<sip:a@b>"} {
		if rec := post(e, map[string]any{"destination": dest}); rec.Code != 400 {
			t.Errorf("destination %q = %d %s, want 400", dest, rec.Code, rec.Body)
		}
	}
	for _, dest := range []string{"102", "+971501234567", "*97", "#21", "0097150123", strings.Repeat("1", 64)} {
		if rec := post(e, map[string]any{"destination": dest}); rec.Code != 202 {
			t.Errorf("destination %q = %d %s, want 202", dest, rec.Code, rec.Body)
		}
	}
	rec := post(e, map[string]any{"destination": "999"})
	if rec.Code != 422 || code(rec) != "emergency_not_allowed" {
		t.Fatalf("emergency = %d %s", rec.Code, rec.Body)
	}
	for _, cid := range []string{"+999", "anonymous", "sip:x@y"} {
		if rec := post(e, map[string]any{"destination": "0501", "callerId": cid}); rec.Code != 422 || code(rec) != "caller_id_not_allowed" {
			t.Errorf("callerId %q = %d %s, want 422 caller_id_not_allowed", cid, rec.Code, rec.Body)
		}
	}
	for _, cid := range []string{"101", "+3211111"} {
		if rec := post(e, map[string]any{"destination": "0501", "callerId": cid}); rec.Code != 202 {
			t.Errorf("callerId %q = %d %s, want 202", cid, rec.Code, rec.Body)
		}
	}
	if rec := post(e, map[string]any{"destination": "0501", "autoAnswer": true}); rec.Code != 400 {
		t.Fatalf("auto-answer while off = %d %s, want 400", rec.Code, rec.Body)
	}
	e.recs.settings.AutoAnswerAllowed = true
	if rec := post(e, map[string]any{"destination": "0501", "autoAnswer": true}); rec.Code != 202 {
		t.Fatalf("auto-answer while on = %d %s", rec.Code, rec.Body)
	}
	last := e.live.queue[len(e.live.queue)-1]
	if !last.AutoAnswer {
		t.Fatal("the queued request lost autoAnswer")
	}
	if rec := post(e, map[string]any{"destination": "0501", "deviceId": 77}); rec.Code != 202 || e.live.queue[len(e.live.queue)-1].DeviceID != "alice-desk" {
		t.Fatalf("device = %d %s", rec.Code, rec.Body)
	}
	if rec := post(e, map[string]any{"destination": "0501", "deviceId": 5}); rec.Code != 400 {
		t.Fatalf("foreign device = %d %s, want 400", rec.Code, rec.Body)
	}
}

// TestOriginateRestrictedPolicy fails if the administrator's restriction does
// not travel with the queued request.
func TestOriginateRestrictedPolicy(t *testing.T) {
	e := newOrigAPI(t, emergencyCfg, &originate.Limits{})
	e.recs.settings = store.OriginateSettings{Destinations: "restricted", CountryPrefixes: []string{"+971", "00971"}}
	if rec := e.as("alice", "POST", "/api/v1/calls/originate", map[string]any{"destination": "+971501234567"}); rec.Code != 202 {
		t.Fatal(rec.Body)
	}
	q := e.live.queue[0]
	if !q.Restrict || !slices.Equal(q.Prefixes, []string{"+971", "00971"}) {
		t.Fatalf("queued = %+v, want the restriction", q)
	}
}

// TestOriginateLimits fails if the eleventh request of a credential in a
// minute is not 429 with Retry-After and counted, if a second call in setup
// for one extension is not refused, if a fourth call to one destination is
// not refused, or if repeated refusals do not raise one originate.anomaly
// audit row.
func TestOriginateLimits(t *testing.T) {
	post := func(e *origEnv, who, dest string) *httptest.ResponseRecorder {
		return e.as(who, "POST", "/api/v1/calls/originate", map[string]any{"destination": dest, "extension": "102"})
	}
	t.Run("per credential per minute", func(t *testing.T) {
		l := originate.DefaultLimits()
		l.DestinationCount, l.ExtensionPerHour = 0, 0
		e := newOrigAPI(t, emergencyCfg, &l)
		for i := 0; i < 10; i++ {
			rec := post(e, "carol", fmt.Sprint("05000000", i))
			if rec.Code != 202 {
				t.Fatalf("request %d = %d %s", i+1, rec.Code, rec.Body)
			}
			// Free the setup slot as a node would once the call is up.
			_ = e.live.ReleaseOriginateSetup(context.Background(), "102", body(t, rec)["originateId"].(string))
		}
		rec := post(e, "carol", "0500000099")
		if rec.Code != 429 || code(rec) != "rate_limited" || rec.Header().Get("Retry-After") == "" {
			t.Fatalf("11th request = %d %s retry-after %q", rec.Code, rec.Body, rec.Header().Get("Retry-After"))
		}
		if n := testutil.ToFloat64(e.metrics.RateLimited.WithLabelValues(originate.ScopeCredentialMinute)); n != 1 {
			t.Fatalf("rate-limited counter = %v, want 1", n)
		}
	})
	t.Run("one call in setup per extension", func(t *testing.T) {
		e := newOrigAPI(t, emergencyCfg, nil)
		first := post(e, "carol", "0500000001")
		if first.Code != 202 {
			t.Fatal(first.Body)
		}
		second := post(e, "carol", "0500000002")
		if second.Code != 429 {
			t.Fatalf("second request while the first is in setup = %d %s", second.Code, second.Body)
		}
		_ = e.live.ReleaseOriginateSetup(context.Background(), "102", body(t, first)["originateId"].(string))
		if third := post(e, "carol", "0500000003"); third.Code != 202 {
			t.Fatalf("after the slot was freed = %d %s", third.Code, third.Body)
		}
	})
	t.Run("per destination", func(t *testing.T) {
		l := originate.DefaultLimits()
		l.ExtensionPerHour, l.CredentialPerMinute = 0, 0
		e := newOrigAPI(t, emergencyCfg, &l)
		for i := 0; i < 3; i++ {
			rec := post(e, "carol", "0509999999")
			if rec.Code != 202 {
				t.Fatalf("request %d = %d %s", i+1, rec.Code, rec.Body)
			}
			_ = e.live.ReleaseOriginateSetup(context.Background(), "102", body(t, rec)["originateId"].(string))
		}
		if rec := post(e, "carol", "0509999999"); rec.Code != 429 {
			t.Fatalf("4th call to one destination = %d %s", rec.Code, rec.Body)
		}
		if rec := post(e, "carol", "0508888888"); rec.Code != 202 {
			t.Fatalf("another destination = %d %s", rec.Code, rec.Body)
		}
	})
	t.Run("anomaly audit", func(t *testing.T) {
		e := newOrigAPI(t, emergencyCfg, nil)
		if rec := post(e, "carol", "0500000001"); rec.Code != 202 { // holds the slot
			t.Fatal(rec.Body)
		}
		for i := 0; i < 8; i++ {
			if rec := post(e, "carol", "0500000002"); rec.Code != 429 {
				t.Fatalf("refusal %d = %d", i, rec.Code)
			}
		}
		n := 0
		for _, a := range e.f.audit {
			if strings.Contains(a, "originate.anomaly") {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("anomaly rows = %d, want exactly 1 (audit %v)", n, e.f.audit)
		}
	})
}

// TestOriginateIdempotency fails if the same key from the same credential
// makes a second request, counts against the limits, or if another credential
// can read it.
func TestOriginateIdempotency(t *testing.T) {
	e := newOrigAPI(t, emergencyCfg, nil)
	b := map[string]any{"destination": "0501234567", "idempotencyKey": "abc-1"}
	first := e.as("alice", "POST", "/api/v1/calls/originate", b)
	second := e.as("alice", "POST", "/api/v1/calls/originate", b) // would be refused as a second call in setup
	if first.Code != 202 || second.Code != 202 {
		t.Fatalf("first %d %s, second %d %s", first.Code, first.Body, second.Code, second.Body)
	}
	if body(t, first)["originateId"] != body(t, second)["originateId"] || len(e.live.queue) != 1 {
		t.Fatalf("ids %v %v, queued %d, want one request", body(t, first)["originateId"], body(t, second)["originateId"], len(e.live.queue))
	}
	hdr := e.as("alice", "POST", "/api/v1/calls/originate", map[string]any{"destination": "0501234567"}, "Idempotency-Key", "abc-1")
	if hdr.Code != 202 || body(t, hdr)["originateId"] != body(t, first)["originateId"] {
		t.Fatalf("header key = %d %s", hdr.Code, hdr.Body)
	}
	if rec := e.as("alice", "POST", "/api/v1/calls/originate", map[string]any{"destination": "0501234567", "idempotencyKey": "bad key"}); rec.Code != 400 {
		t.Fatalf("key with a space = %d", rec.Code)
	}
}

// TestOriginateStateAndHangup fails if a request can be read by anyone but
// its owner and operators, if the live state is not what GET returns and is
// not copied into the row, if DELETE does not raise the cancel flag, or if a
// finished request can be cancelled.
func TestOriginateStateAndHangup(t *testing.T) {
	e := newOrigAPI(t, emergencyCfg, &originate.Limits{})
	e.free = true
	rec := e.as("alice", "POST", "/api/v1/calls/originate", map[string]any{"destination": "0501234567"})
	id := body(t, rec)["originateId"].(string)
	path := "/api/v1/calls/originate/" + id

	if rec := e.as("alice", "GET", path, nil); rec.Code != 200 || body(t, rec)["state"] != "queued" {
		t.Fatalf("owner GET = %d %s", rec.Code, rec.Body)
	}
	if rec := e.as("bob", "GET", path, nil); rec.Code != 404 {
		t.Fatalf("another user GET = %d, want 404", rec.Code)
	}
	if rec := e.as("carol", "GET", path, nil); rec.Code != 200 {
		t.Fatalf("admin GET = %d", rec.Code)
	}
	if rec := e.as("alice", "GET", "/api/v1/calls/originate/orig_nope", nil); rec.Code != 404 {
		t.Fatalf("unknown id = %d", rec.Code)
	}

	// A node moves the request on: GET shows it and the row follows.
	_ = e.live.PutOriginateState(context.Background(), livestate.OriginateState{ID: id, State: "connected", CorrelationID: "corr-1"})
	m := body(t, e.as("alice", "GET", path, nil))
	if m["state"] != "connected" || m["correlationId"] != "corr-1" {
		t.Fatalf("GET = %v", m)
	}
	if r, _ := e.recs.GetOriginate(context.Background(), id); r.State != "connected" || r.CorrelationID != "corr-1" {
		t.Fatalf("row = %+v, want the live state copied", r)
	}

	if rec := e.as("bob", "DELETE", path, nil); rec.Code != 404 || e.live.cancelled[id] {
		t.Fatalf("another user DELETE = %d, cancelled %v", rec.Code, e.live.cancelled[id])
	}
	if rec := e.as("alice", "DELETE", path, nil); rec.Code != 202 || !e.live.cancelled[id] {
		t.Fatalf("owner DELETE = %d %s, cancelled %v", rec.Code, rec.Body, e.live.cancelled[id])
	}
	_ = e.live.PutOriginateState(context.Background(), livestate.OriginateState{ID: id, State: "ended"})
	if rec := e.as("alice", "DELETE", path, nil); rec.Code != 409 {
		t.Fatalf("DELETE of a finished request = %d, want 409", rec.Code)
	}

	// A request no node has taken is closed by the cancel itself.
	id2 := body(t, e.as("alice", "POST", "/api/v1/calls/originate", map[string]any{"destination": "0501234568"}))["originateId"].(string)
	rec = e.as("alice", "DELETE", "/api/v1/calls/originate/"+id2, nil)
	if m := body(t, rec); rec.Code != 202 || m["state"] != "failed" || m["reason"] != "cancelled" {
		t.Fatalf("DELETE of a queued request = %d %v", rec.Code, m)
	}
}

// TestOriginateUnavailable fails if click-to-call without its stores, or with
// Valkey down, does not answer 503 and leave the request failed.
func TestOriginateUnavailable(t *testing.T) {
	f, _, _, _ := newAcctFake(t)
	h := Handler(Config{Store: origStore{f, emergencyCfg}, Log: slog.New(slog.DiscardHandler), SessionTTL: time.Hour})
	if rec := acctCall(h, "alice", "POST", "/api/v1/calls/originate", map[string]any{"destination": "0501"}); rec.Code != 503 {
		t.Fatalf("unwired = %d", rec.Code)
	}
	e := newOrigAPI(t, emergencyCfg, &originate.Limits{})
	e.live.down = true
	if rec := e.as("alice", "POST", "/api/v1/calls/originate", map[string]any{"destination": "0501"}); rec.Code != 503 {
		t.Fatalf("Valkey down = %d %s", rec.Code, rec.Body)
	}
	for id, r := range e.recs.recs {
		if r.State != "failed" || r.Reason != "node_unavailable" {
			t.Fatalf("request %s = %s/%s, want failed node_unavailable", id, r.State, r.Reason)
		}
	}
}

// TestClickToCallSettings fails if the policy is readable or writable by
// anyone but an administrator, accepts a bad value, or does not round-trip.
func TestClickToCallSettings(t *testing.T) {
	e := newOrigAPI(t, emergencyCfg, nil)
	p := "/api/v1/integrations/click-to-call"
	for _, who := range []string{"alice", "dave"} {
		if rec := e.as(who, "GET", p, nil); rec.Code != 403 {
			t.Errorf("%s GET = %d, want 403", who, rec.Code)
		}
		if rec := e.as(who, "PUT", p, map[string]any{"destinations": "inherit", "countryPrefixes": []string{}, "autoAnswerAllowed": true}); rec.Code != 403 {
			t.Errorf("%s PUT = %d, want 403", who, rec.Code)
		}
	}
	if m := body(t, e.as("carol", "GET", p, nil)); m["destinations"] != "inherit" || m["autoAnswerAllowed"] != false {
		t.Fatalf("default = %v", m)
	}
	for _, bad := range []map[string]any{
		{"destinations": "everything", "countryPrefixes": []string{}, "autoAnswerAllowed": false},
		{"destinations": "restricted", "countryPrefixes": []string{"sip:x"}, "autoAnswerAllowed": false},
	} {
		if rec := e.as("carol", "PUT", p, bad); rec.Code != 400 {
			t.Errorf("PUT %v = %d, want 400", bad, rec.Code)
		}
	}
	want := map[string]any{"destinations": "restricted", "countryPrefixes": []string{"+971"}, "autoAnswerAllowed": true}
	if rec := e.as("carol", "PUT", p, want); rec.Code != 200 {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body)
	}
	if m := body(t, e.as("carol", "GET", p, nil)); m["destinations"] != "restricted" || m["autoAnswerAllowed"] != true {
		t.Fatalf("round trip = %v", m)
	}
}
