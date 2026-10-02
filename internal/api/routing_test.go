package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/valkey-io/valkey-go"
)

// fakeRouter stands in for the routing engine (Task 2) so the control plane
// can be tested now. Its one whole-configuration rule: an inbound route to
// an extension must name an existing one. Its tables record each decision.
type fakeRouter struct {
	mu        sync.Mutex
	compiles  int
	lastCfg   routing.Config
	lastCall  routing.Call
	lastTrace routing.Trace
}

func (r *fakeRouter) Compile(cfg routing.Config) (RouteTable, []routing.FieldError) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.compiles++
	r.lastCfg = cfg
	var errs []routing.FieldError
	for i, in := range cfg.Inbound {
		if _, ok := cfg.Extensions[in.Destination]; in.DestinationKind == "extension" && !ok {
			errs = append(errs, routing.FieldError{Path: fmt.Sprintf("inbound[%d].destination", i), Message: "no such extension"})
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return fakeTable{r: r, cfg: cfg}, nil
}

type fakeTable struct {
	r   *fakeRouter
	cfg routing.Config
}

func (t fakeTable) Decide(c routing.Call, usable routing.TrunkUsability) routing.Decision {
	d := t.decide(c, usable)
	t.r.mu.Lock()
	t.r.lastCall, t.r.lastTrace = c, slices.Clone(d.Trace)
	t.r.mu.Unlock()
	return d
}

func (t fakeTable) decide(c routing.Call, usable routing.TrunkUsability) routing.Decision {
	var d routing.Decision
	if _, ok := t.cfg.Extensions[c.Number]; ok && c.FromExtension != "" {
		d.Trace.Add(fmt.Sprintf("Internal extension lookup %q -> match", c.Number))
		d.Kind, d.Extension = routing.KindInternal, c.Number
		return d
	}
	d.Trace.Add(fmt.Sprintf("Internal extension lookup %q -> no match", c.Number))
	routes := slices.Clone(t.cfg.Outbound)
	slices.SortFunc(routes, func(a, b routing.OutboundRoute) int { return a.Position - b.Position })
	for _, o := range routes {
		if !o.Enabled || !strings.HasPrefix(c.Number, o.Match) {
			continue
		}
		d.Trace.Add(fmt.Sprintf("Route %q matched (prefix %s)", o.Name, o.Match))
		d.Route, d.Number = o.Name, o.Number.Prefix+c.Number[min(o.Number.Strip, len(c.Number)):]
		for _, id := range o.Trunks {
			var tr *routing.Trunk
			for i := range t.cfg.Trunks {
				if t.cfg.Trunks[i].ID == id {
					tr = &t.cfg.Trunks[i]
				}
			}
			if ok, why := usable(id, o.Emergency); !ok {
				d.Trace.Add(fmt.Sprintf("Trunk %s skipped: %s", tr.Name, why))
				continue
			}
			d.Candidates = append(d.Candidates, routing.Candidate{Trunk: tr, Destinations: tr.Destinations})
		}
		if len(d.Candidates) == 0 {
			d.Kind, d.RejectCode, d.Reason = routing.KindReject, 503, "no usable trunk"
			return d
		}
		d.Kind, d.CallerID = routing.KindOutbound, t.cfg.Extensions[c.FromExtension]
		return d
	}
	d.Trace.Add("No route matched")
	d.Kind, d.RejectCode, d.Reason = routing.KindReject, 404, "no route matched"
	return d
}

// switchLive is trunk live state that can be made unreachable.
type switchLive struct {
	down  atomic.Bool
	inner TrunkLive
}

func (s *switchLive) TrunkStatus(ctx context.Context, id int64) (livestate.TrunkStatus, error) {
	if s.down.Load() {
		return livestate.TrunkStatus{}, errors.New("valkey: connection refused")
	}
	return s.inner.TrunkStatus(ctx, id)
}

func testBox(t *testing.T) *secret.Box {
	t.Helper()
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	b, err := secret.New(base64.StdEncoding.EncodeToString(k))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// trunkPassword is built at runtime so secret scanners see no literal.
func trunkPassword(tag string) string {
	b := make([]byte, 9)
	_, _ = rand.Read(b)
	return "pw-" + tag + "-" + base64.RawURLEncoding.EncodeToString(b)
}

// p2Env is an API with the fake router, a secret box and captured logs.
type p2Env struct {
	*env
	router *fakeRouter
	box    *secret.Box
	live   *switchLive
	logs   *syncBuffer
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newP2Env(t *testing.T, trunks TrunkLive) *p2Env {
	t.Helper()
	p := &p2Env{router: &fakeRouter{}, box: testBox(t), live: &switchLive{inner: trunks}, logs: &syncBuffer{}}
	if trunks == nil {
		p.live.down.Store(true)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("server log:\n%s", p.logs.String())
		}
	})
	p.env = newEnvConfig(t, Config{
		Live: noLive{}, Router: p.router, Trunks: p.live,
		Log: slog.New(slog.NewTextHandler(p.logs, nil)),
	}, p.box)
	return p
}

// fieldPaths returns the paths of a 400's error.fields.
func fieldPaths(t *testing.T, r response) []string {
	t.Helper()
	var body struct {
		Error struct {
			Code   string               `json:"code"`
			Fields []routing.FieldError `json:"fields"`
		} `json:"error"`
	}
	if err := json.Unmarshal(r.body, &body); err != nil || r.code != http.StatusBadRequest || body.Error.Code != "bad_request" {
		t.Fatalf("response %d %s is not a 400 bad_request", r.code, r.body)
	}
	var out []string
	for _, f := range body.Error.Fields {
		out = append(out, f.Path)
	}
	return out
}

// dbFingerprint hashes the full content of every table plus the revision,
// so any write shows up.
func (e *env) dbFingerprint() string {
	e.t.Helper()
	var parts []string
	for _, tbl := range []string{"users", "sessions", "api_tokens", "extensions", "devices", "audit_events", "cdrs",
		"trunks", "trunk_destinations", "outbound_routes", "outbound_route_trunks", "inbound_routes", "schema_info"} {
		var n int
		var h string
		if err := e.db.QueryRow(`SELECT count(*), md5(coalesce(string_agg(t::text, '|' ORDER BY t::text), '')) FROM `+tbl+` t`).Scan(&n, &h); err != nil {
			e.t.Fatal(err)
		}
		parts = append(parts, fmt.Sprintf("%s=%d:%s", tbl, n, h))
	}
	return strings.Join(parts, " ")
}

func (e *env) auditCount() int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(`SELECT count(*) FROM audit_events`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestTrunkCRUDSecretHidden(t *testing.T) {
	p := newP2Env(t, nil)
	c := p.login()
	pw := trunkPassword("primary")

	// A registration trunk needs credentials.
	if got := fieldPaths(t, c.do("POST", "/api/v1/trunks", map[string]any{
		"name": "carrier-primary", "mode": "registration", "username": "acct",
		"destinations": []map[string]any{{"host": "10.0.0.5"}},
	})); !slices.Contains(got, "password") {
		t.Fatalf("registration trunk without password: fields %v", got)
	}

	created := c.must(http.StatusCreated, "POST", "/api/v1/trunks", map[string]any{
		"name": "carrier-primary", "mode": "registration", "username": "acct", "password": pw,
		"realm": "carrier.example", "sourceCidrs": []string{"203.0.113.7/24", "198.51.100.9"},
		"destinations": []map[string]any{{"host": "10.0.0.5", "port": 5060, "priority": 1}, {"host": "sip.carrier.example", "weight": 3}},
	})
	tr := created.json(t)
	id := fmt.Sprint(tr["id"])
	if tr["hasPassword"] != true || tr["registerExpires"] != float64(3600) || tr["optionsInterval"] != float64(30) || tr["enabled"] != true {
		t.Fatalf("created trunk = %v", tr)
	}
	if got := fmt.Sprint(tr["sourceCidrs"]); got != "[203.0.113.0/24 198.51.100.9/32]" {
		t.Fatalf("sourceCidrs = %s, want normalised CIDRs", got)
	}
	if ds := tr["destinations"].([]any); len(ds) != 2 || ds[1].(map[string]any)["weight"] != float64(3) || ds[0].(map[string]any)["weight"] != float64(1) {
		t.Fatalf("destinations = %v", ds)
	}
	c.must(http.StatusConflict, "POST", "/api/v1/trunks", map[string]any{
		"name": "carrier-primary", "mode": "ip", "destinations": []map[string]any{{"host": "10.0.0.6"}},
	})

	// The password never comes back, in any response.
	hidden := func(r response, secrets ...string) {
		t.Helper()
		for _, s := range secrets {
			if bytes.Contains(r.body, []byte(s)) {
				t.Fatalf("response reveals a trunk password: %s", r.body)
			}
		}
		if bytes.Contains(r.body, []byte(`"password"`)) {
			t.Fatalf("response has a password field: %s", r.body)
		}
	}
	hidden(created, pw)
	hidden(c.must(http.StatusOK, "GET", "/api/v1/trunks", nil), pw)
	hidden(c.must(http.StatusOK, "GET", "/api/v1/trunks/"+id, nil), pw)
	patched := c.must(http.StatusOK, "PATCH", "/api/v1/trunks/"+id, map[string]any{"maxCalls": 4})
	hidden(patched, pw)
	if patched.json(t)["hasPassword"] != true {
		t.Fatal("omitting password on PATCH dropped it")
	}

	// Stored sealed under HELLO_SECRET_KEY, bound to its row, and opens.
	sealed := func() []byte {
		t.Helper()
		var enc []byte
		if err := p.db.QueryRow(`SELECT password_enc FROM trunks WHERE id = $1`, id).Scan(&enc); err != nil {
			t.Fatal(err)
		}
		return enc
	}
	enc := sealed()
	if bytes.Contains(enc, []byte(pw)) {
		t.Fatal("password stored in clear")
	}
	if got, err := p.box.Open(enc, "trunk:"+id); err != nil || got != pw {
		t.Fatalf("sealed password does not open with the key and trunk:%s: %v", id, err)
	}
	if _, err := p.box.Open(enc, "trunk:999"); err == nil {
		t.Fatal("sealed password opens under another row's binding")
	}
	var plainRows int
	if err := p.db.QueryRow(`SELECT (SELECT count(*) FROM trunks t WHERE position($1 in row_to_json(t)::text) > 0)
		+ (SELECT count(*) FROM audit_events a WHERE position($1 in row_to_json(a)::text) > 0)`, pw).Scan(&plainRows); err != nil || plainRows != 0 {
		t.Fatalf("password in clear in %d rows (%v)", plainRows, err)
	}

	// PATCH: a value replaces it, omitted keeps it, "" clears it.
	pw2 := trunkPassword("rotated")
	hidden(c.must(http.StatusOK, "PATCH", "/api/v1/trunks/"+id, map[string]any{"password": pw2}), pw, pw2)
	if got, err := p.box.Open(sealed(), "trunk:"+id); err != nil || got != pw2 {
		t.Fatalf("replaced password opens to the old value or not at all (%v)", err)
	}
	before := sealed()
	c.must(http.StatusOK, "PATCH", "/api/v1/trunks/"+id, map[string]any{"enabled": false})
	if !bytes.Equal(before, sealed()) {
		t.Fatal("PATCH without password changed the stored password")
	}
	if got := fieldPaths(t, c.do("PATCH", "/api/v1/trunks/"+id, map[string]any{"password": ""})); !slices.Contains(got, "password") {
		t.Fatalf("clearing a registration trunk's password: fields %v", got)
	}
	cleared := c.must(http.StatusOK, "PATCH", "/api/v1/trunks/"+id, map[string]any{"mode": "ip", "password": ""}).json(t)
	if cleared["hasPassword"] != false || sealed() != nil {
		t.Fatalf("explicit empty password did not clear it: %v", cleared)
	}

	// Validation names the fields.
	got := fieldPaths(t, c.do("POST", "/api/v1/trunks", map[string]any{
		"name": "bad name", "mode": "ip", "sourceCidrs": []string{"10.0.0.0/33"}, "registerExpires": 5,
		"destinations": []map[string]any{{"host": "bad host!", "port": 70000}},
	}))
	for _, want := range []string{"name", "sourceCidrs[0]", "registerExpires", "destinations[0].host", "destinations[0].port"} {
		if !slices.Contains(got, want) {
			t.Errorf("invalid trunk: fields %v lack %s", got, want)
		}
	}

	// A trunk in use by a route cannot be deleted.
	route := c.must(http.StatusCreated, "POST", "/api/v1/routes/outbound", map[string]any{
		"name": "All", "matchKind": "prefix", "match": "0", "trunks": []any{tr["id"]},
	}).json(t)
	c.must(http.StatusConflict, "DELETE", "/api/v1/trunks/"+id, nil)
	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/routes/outbound/%v", route["id"]), nil)
	c.must(http.StatusNoContent, "DELETE", "/api/v1/trunks/"+id, nil)
	c.must(http.StatusNotFound, "GET", "/api/v1/trunks/"+id, nil)

	if logs := p.logs.String(); strings.Contains(logs, pw) || strings.Contains(logs, pw2) {
		t.Fatal("a trunk password reached the log")
	}
}

func TestRouteValidation(t *testing.T) {
	p := newP2Env(t, nil)
	c := p.login()
	trunk := c.must(http.StatusCreated, "POST", "/api/v1/trunks", map[string]any{
		"name": "peer", "mode": "ip", "destinations": []map[string]any{{"host": "10.0.0.5"}},
	}).json(t)["id"]
	c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]any{"number": "101", "name": "Desk"})

	ok := c.must(http.StatusCreated, "POST", "/api/v1/routes/outbound", map[string]any{
		"name": "UAE Mobile", "matchKind": "regex", "match": `^0(?P<rest>5[0-9]{8})$`, "trunks": []any{trunk},
		"numberTransform":   map[string]any{"regex": `^0(?P<rest>5[0-9]{8})$`, "template": "+971${rest}"},
		"callerIdTransform": map[string]any{"regex": `^(\+?)([0-9]+)$`, "template": "$1$2"},
		"schedule":          map[string]any{"timeZone": "Asia/Dubai", "windows": []map[string]any{{"days": []int{0, 1, 2, 3, 4}, "start": "08:00", "end": "18:00"}}},
	}).json(t)
	for _, k := range []string{"id", "position", "name", "matchKind", "match", "sourceExtensions", "schedule", "numberTransform",
		"callerIdTransform", "trunks", "failoverCodes", "emergency", "enabled"} {
		if _, has := ok[k]; !has {
			t.Fatalf("outbound route lacks %q: %v", k, ok)
		}
	}
	if fmt.Sprint(ok["failoverCodes"]) != "[408 480 500 502 503 504]" || ok["position"] != float64(1) {
		t.Fatalf("defaults: %v", ok)
	}
	okPath := fmt.Sprintf("/api/v1/routes/outbound/%v", ok["id"])

	base := func(over map[string]any) map[string]any {
		m := map[string]any{"name": "R", "matchKind": "prefix", "match": "0", "trunks": []any{trunk}}
		for k, v := range over {
			m[k] = v
		}
		return m
	}
	tz := func(z string) map[string]any {
		return map[string]any{"timeZone": z, "windows": []map[string]any{{"days": []int{1}, "start": "08:00", "end": "18:00"}}}
	}
	cases := []struct {
		name string
		body map[string]any
		path string
	}{
		{"uncompilable regex", base(map[string]any{"matchKind": "regex", "match": "([0-9]"}), "match"},
		{"oversized regex", base(map[string]any{"matchKind": "regex", "match": strings.Repeat("1", 501)}), "match"},
		{"template missing numbered group", base(map[string]any{"numberTransform": map[string]any{"regex": "^0([0-9]+)$", "template": "+971${2}"}}), "numberTransform.template"},
		{"template missing named group", base(map[string]any{"numberTransform": map[string]any{"regex": "^0([0-9]+)$", "template": "+971${area}"}}), "numberTransform.template"},
		{"template ambiguous name", base(map[string]any{"numberTransform": map[string]any{"regex": "^0([0-9]+)$", "template": "$1x"}}), "numberTransform.template"},
		{"template letters", base(map[string]any{"numberTransform": map[string]any{"regex": "^0([0-9]+)$", "template": "tel:${1}"}}), "numberTransform.template"},
		{"template without regex", base(map[string]any{"callerIdTransform": map[string]any{"template": "${1}"}}), "callerIdTransform.template"},
		{"no trunks", base(map[string]any{"trunks": []any{}}), "trunks"},
		{"unknown trunk", base(map[string]any{"trunks": []any{999999}}), "trunks[0]"},
		{"bad time zone", base(map[string]any{"schedule": tz("Mars/Olympus_Mons")}), "schedule.timeZone"},
		{"time zone path", base(map[string]any{"schedule": tz("../../etc/passwd")}), "schedule.timeZone"},
		{"bad window", base(map[string]any{"schedule": map[string]any{"timeZone": "UTC", "windows": []map[string]any{{"days": []int{7}, "start": "25:00", "end": "x"}}}}), "schedule.windows[0].start"},
		{"failover code", base(map[string]any{"failoverCodes": []int{200}}), "failoverCodes[0]"},
		{"source extension", base(map[string]any{"sourceExtensions": []string{"x"}}), "sourceExtensions[0]"},
		{"match kind", base(map[string]any{"matchKind": "glob"}), "matchKind"},
	}
	for _, tc := range cases {
		before := p.dbFingerprint()
		if got := fieldPaths(t, c.do("POST", "/api/v1/routes/outbound", tc.body)); !slices.Contains(got, tc.path) {
			t.Errorf("%s: fields %v, want %s", tc.name, got, tc.path)
		}
		if p.dbFingerprint() != before {
			t.Errorf("%s: rejected route changed the database", tc.name)
		}
	}
	before := p.dbFingerprint()
	if got := fieldPaths(t, c.do("PATCH", okPath, map[string]any{"numberTransform": map[string]any{"regex": "^(5)$", "template": "${9}"}})); !slices.Contains(got, "numberTransform.template") {
		t.Fatalf("PATCH bad template: fields %v", got)
	}
	c.must(http.StatusBadRequest, "POST", "/api/v1/routes/outbound", base(map[string]any{"position": 3}))
	c.must(http.StatusBadRequest, "PATCH", okPath, map[string]any{})
	if p.dbFingerprint() != before {
		t.Fatal("a rejected PATCH changed the database")
	}
	// PATCH null clears the schedule.
	if got := c.must(http.StatusOK, "PATCH", okPath, map[string]any{"schedule": nil}).json(t); got["schedule"] != nil {
		t.Fatalf("schedule after null = %v", got["schedule"])
	}

	// Inbound: field-level, then the whole-configuration compile.
	inCases := []struct {
		name string
		body map[string]any
		path string
	}{
		{"did kind", map[string]any{"name": "I", "didKind": "glob", "destinationKind": "extension", "destination": "101"}, "didKind"},
		{"exact did", map[string]any{"name": "I", "didKind": "exact", "did": "abc", "destinationKind": "extension", "destination": "101"}, "did"},
		{"header pair", map[string]any{"name": "I", "headerName": "X-Lang", "destinationKind": "extension", "destination": "101"}, "headerName"},
		{"header regex", map[string]any{"name": "I", "headerName": "X-Lang", "headerRegex": "(", "destinationKind": "extension", "destination": "101"}, "headerRegex"},
		{"sip uri", map[string]any{"name": "I", "destinationKind": "sip_uri", "destination": "http://agent"}, "destination"},
		{"unknown trunk", map[string]any{"name": "I", "trunkId": 999999, "destinationKind": "extension", "destination": "101"}, "trunkId"},
		{"whole config: no such extension", map[string]any{"name": "I", "destinationKind": "extension", "destination": "555"}, "inbound[0].destination"},
	}
	for _, tc := range inCases {
		before := p.dbFingerprint()
		if got := fieldPaths(t, c.do("POST", "/api/v1/routes/inbound", tc.body)); !slices.Contains(got, tc.path) {
			t.Errorf("inbound %s: fields %v, want %s", tc.name, got, tc.path)
		}
		if p.dbFingerprint() != before {
			t.Errorf("inbound %s: rejected route changed the database", tc.name)
		}
	}
	in := c.must(http.StatusCreated, "POST", "/api/v1/routes/inbound", map[string]any{
		"name": "Main DID", "didKind": "exact", "did": "+97142000000", "trunkId": trunk,
		"destinationKind": "extension", "destination": "101",
	}).json(t)
	if in["trunkId"] != trunk || in["schedule"] != nil || in["position"] != float64(1) {
		t.Fatalf("inbound route = %v", in)
	}
	c.must(http.StatusCreated, "POST", "/api/v1/routes/inbound", map[string]any{
		"name": "AI agent", "destinationKind": "sip_uri", "destination": "sip:agent@ai.example:5070;transport=udp",
	})
	// The whole-configuration check also guards extension changes: deleting
	// the extension an inbound route rings is rejected and rolled back.
	ext := c.must(http.StatusOK, "GET", "/api/v1/extensions", nil).json(t)["items"].([]any)[0].(map[string]any)
	before = p.dbFingerprint()
	if got := fieldPaths(t, c.do("DELETE", fmt.Sprintf("/api/v1/extensions/%v", ext["id"]), nil)); !slices.Contains(got, "inbound[0].destination") {
		t.Fatalf("deleting a routed extension: fields %v", got)
	}
	if p.dbFingerprint() != before {
		t.Fatal("rejected extension delete changed the database")
	}
}

func TestRoutingTestEndpoint(t *testing.T) {
	addr := os.Getenv("HELLO_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("HELLO_TEST_VALKEY_ADDR not set")
	}
	vc, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, ForceSingleClient: true, SelectDB: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vc.Close)
	ctx := context.Background()
	if err := vc.Do(ctx, vc.B().Flushdb().Build()).Error(); err != nil {
		t.Fatal(err)
	}
	live := livestate.New(vc)
	p := newP2Env(t, live)
	c := p.login()

	c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]any{"number": "101", "name": "Desk", "externalNumber": "+97142000101"})
	mk := func(body map[string]any) float64 {
		return c.must(http.StatusCreated, "POST", "/api/v1/trunks", body).json(t)["id"].(float64)
	}
	pw := trunkPassword("b")
	a := mk(map[string]any{"name": "a", "mode": "ip", "destinations": []map[string]any{{"host": "10.0.0.5"}}})
	b := mk(map[string]any{"name": "b", "mode": "registration", "username": "u", "password": pw, "destinations": []map[string]any{{"host": "10.0.0.6"}}})
	cc := mk(map[string]any{"name": "c", "mode": "ip", "enabled": false, "destinations": []map[string]any{{"host": "10.0.0.7"}}})
	c.must(http.StatusCreated, "POST", "/api/v1/routes/outbound", map[string]any{
		"name": "Mobile", "matchKind": "prefix", "match": "05", "trunks": []any{a, b, cc},
		"numberTransform": map[string]any{"strip": 1, "prefix": "+971"},
	})
	// Trunk a's only destination is down.
	if err := live.PutDestinationHealth(ctx, int64(a), livestate.DestinationHealth{Destination: "10.0.0.5:5060", Up: false, CheckedAt: time.Now()}, time.Minute); err != nil {
		t.Fatal(err)
	}
	dbsize := func() int64 {
		n, err := vc.Do(ctx, vc.B().Dbsize().Build()).AsInt64()
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	type result struct {
		Decision testDecision   `json:"decision"`
		Trace    []routing.Step `json:"trace"`
	}
	run := func(body map[string]any) result {
		t.Helper()
		var r result
		if err := json.Unmarshal(c.must(http.StatusOK, "POST", "/api/v1/routing/test", body).body, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}

	before, keys, audits := p.dbFingerprint(), dbsize(), p.auditCount()
	at := "2026-10-05T09:30:00+04:00"
	r := run(map[string]any{"from": "101", "number": "0501234567", "at": at})
	if r.Decision.Kind != routing.KindOutbound || fmt.Sprint(r.Decision.Trunks) != "[b]" || r.Decision.Route != "Mobile" ||
		r.Decision.Number != "+971501234567" || r.Decision.CallerID != "+97142000101" {
		t.Fatalf("decision = %+v", r.Decision)
	}
	// The trace is the engine's, verbatim, and the call and configuration
	// are what hello-sip's snapshot would hold.
	p.router.mu.Lock()
	wantTrace, call, cfg := p.router.lastTrace, p.router.lastCall, p.router.lastCfg
	p.router.mu.Unlock()
	if fmt.Sprint(r.Trace) != fmt.Sprint([]routing.Step(wantTrace)) {
		t.Fatalf("trace %v differs from the engine's %v", r.Trace, wantTrace)
	}
	for _, want := range []string{"Trunk a skipped: unhealthy", "Trunk c skipped: disabled"} {
		if !strings.Contains(fmt.Sprint(r.Trace), want) {
			t.Fatalf("trace %v lacks %q", r.Trace, want)
		}
	}
	wantAt, _ := time.Parse(time.RFC3339, at)
	if call.FromExtension != "101" || call.FromTrunk != 0 || call.Number != "0501234567" || !call.At.Equal(wantAt) || call.SIPDomain != testDomain {
		t.Fatalf("call = %+v", call)
	}
	var gotPW string
	for _, tr := range cfg.Trunks {
		if tr.ID == int64(b) {
			gotPW = tr.Password
		}
	}
	if gotPW != pw || cfg.Extensions["101"] != "+97142000101" || len(cfg.Outbound) != 1 || len(cfg.Outbound[0].Trunks) != 3 {
		t.Fatal("the tester's configuration is not the saved one (password opened, extensions, routes)")
	}
	if strings.Contains(fmt.Sprint(r), pw) {
		t.Fatal("tester output reveals a trunk password")
	}

	// Valkey unreachable: every enabled trunk counts, and the trace says so.
	p.live.down.Store(true)
	r = run(map[string]any{"from": "101", "number": "0501234567"})
	if len(r.Trace) == 0 || r.Trace[0].Text != stateUnavailableStep || r.Trace[0].N != 1 || fmt.Sprint(r.Decision.Trunks) != "[a b]" {
		t.Fatalf("Valkey down: decision %+v trace %v", r.Decision, r.Trace)
	}
	for i, st := range r.Trace {
		if st.N != i+1 {
			t.Fatalf("trace numbering %v", r.Trace)
		}
	}
	p.live.down.Store(false)

	// From a trunk, and rejections.
	run(map[string]any{"from": fmt.Sprintf("trunk:%v", a), "number": "+97142000000"})
	p.router.mu.Lock()
	if p.router.lastCall.FromTrunk != int64(a) || p.router.lastCall.FromExtension != "" {
		t.Errorf("from trunk: call %+v", p.router.lastCall)
	}
	p.router.mu.Unlock()
	if r := run(map[string]any{"from": "101", "number": "999"}); r.Decision.Kind != routing.KindReject || r.Decision.RejectCode != 404 {
		t.Fatalf("unmatched = %+v", r.Decision)
	}
	for _, bad := range []map[string]any{
		{"from": "trunk:x", "number": "1"}, {"from": "nobody", "number": "1"},
		{"from": "101", "number": ""}, {"from": "101", "number": "1", "at": "tomorrow"},
	} {
		if got := fieldPaths(t, c.do("POST", "/api/v1/routing/test", bad)); len(got) == 0 {
			t.Errorf("tester accepted %v", bad)
		}
	}

	// Nothing was written: not the database, not the audit log, not Valkey.
	if p.dbFingerprint() != before || p.auditCount() != audits || dbsize() != keys {
		t.Fatal("the route tester has side effects")
	}

	// Trunk status: live state with names; 503 while Valkey is down.
	if err := live.PutTrunkRegistration(ctx, int64(b), livestate.TrunkRegistration{State: "registered", Node: "sip-1", UpdatedAt: time.Now()}, time.Minute); err != nil {
		t.Fatal(err)
	}
	var st struct {
		Items []namedTrunkStatus `json:"items"`
	}
	if err := json.Unmarshal(c.must(http.StatusOK, "GET", "/api/v1/trunks/status", nil).body, &st); err != nil || len(st.Items) != 3 {
		t.Fatalf("status = %+v (%v)", st, err)
	}
	if st.Items[0].Name != "a" || len(st.Items[0].Destinations) != 1 || st.Items[0].Destinations[0].Up ||
		st.Items[1].Registration == nil || st.Items[1].Registration.State != "registered" {
		t.Fatalf("status items = %+v", st.Items)
	}
	p.live.down.Store(true)
	c.must(http.StatusServiceUnavailable, "GET", "/api/v1/trunks/status", nil)
}

func TestCDRDetail(t *testing.T) {
	p := newP2Env(t, nil)
	c := p.login()
	start := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	insert := func(corr string, status int, reason, trace string) string {
		t.Helper()
		var id int64
		if err := p.db.QueryRow(`INSERT INTO cdrs (correlation_id, sip_call_id, source, destination, start_time, end_time,
			duration_ms, billable_ms, sip_node, final_status, termination_side, failure_reason, direction,
			original_destination, rewritten_destination, route_name, trunk_name, trace)
			VALUES ($1, $1, '101', '0501234567', $2, $3, 1000, 0, 'sip-1', $4, 'system', $5, 'outbound',
			'0501234567', '+971501234567', 'UAE Mobile', 'carrier-backup', $6::jsonb) RETURNING id`,
			corr, start, start.Add(time.Second), status, reason, trace).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprint(id)
	}
	failedTrace := `[{"n":1,"text":"Route \"UAE Mobile\" matched (regex ^05[0-9]{8}$)"},{"n":2,"text":"carrier-primary (10.0.0.5:5060) -> 503 Service Unavailable"},{"n":3,"text":"carrier-backup -> 486 Busy Here"}]`
	failed := insert("c-failed", 486, "busy", failedTrace)
	answered := insert("c-ok", 200, "", `[{"n":1,"text":"carrier-backup -> 200 OK"},{"n":2,"text":"Call established"}]`)
	bare := insert("c-bare", 503, "no usable trunk", `[]`)

	d := c.must(http.StatusOK, "GET", "/api/v1/cdrs/"+failed, nil).json(t)
	if d["explanation"] != "carrier-backup -> 486 Busy Here" || d["direction"] != "outbound" || d["route"] != "UAE Mobile" ||
		d["trunk"] != "carrier-backup" || d["originalDestination"] != "0501234567" || d["rewrittenDestination"] != "+971501234567" {
		t.Fatalf("failed CDR detail = %v", d)
	}
	steps := d["trace"].([]any)
	if len(steps) != 3 || steps[1].(map[string]any)["text"] != "carrier-primary (10.0.0.5:5060) -> 503 Service Unavailable" || steps[2].(map[string]any)["n"] != float64(3) {
		t.Fatalf("trace = %v", steps)
	}
	if d := c.must(http.StatusOK, "GET", "/api/v1/cdrs/"+answered, nil).json(t); d["explanation"] != "" || len(d["trace"].([]any)) != 2 {
		t.Fatalf("answered CDR detail = %v", d)
	}
	if d := c.must(http.StatusOK, "GET", "/api/v1/cdrs/"+bare, nil).json(t); d["explanation"] != "no usable trunk" || len(d["trace"].([]any)) != 0 {
		t.Fatalf("CDR without trace = %v", d)
	}
	c.must(http.StatusNotFound, "GET", "/api/v1/cdrs/999999", nil)
	list := c.must(http.StatusOK, "GET", "/api/v1/cdrs", nil)
	if bytes.Contains(list.body, []byte(`"trace"`)) || !bytes.Contains(list.body, []byte(`"rewrittenDestination":"+971501234567"`)) {
		t.Fatalf("CDR list = %s", list.body)
	}
}

func TestExtensionExternalNumber(t *testing.T) {
	p := newP2Env(t, nil)
	c := p.login()
	e := c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]any{"number": "101", "name": "Desk", "externalNumber": "+97142000101"}).json(t)
	if e["externalNumber"] != "+97142000101" {
		t.Fatalf("created = %v", e)
	}
	path := fmt.Sprintf("/api/v1/extensions/%v", e["id"])
	if got := c.must(http.StatusOK, "PATCH", path, map[string]any{"externalNumber": ""}).json(t); got["externalNumber"] != "" || got["name"] != "Desk" {
		t.Fatalf("patched = %v", got)
	}
	c.must(http.StatusBadRequest, "PATCH", path, map[string]any{"externalNumber": "call-me"})
	if n := c.must(http.StatusOK, "GET", "/api/v1/extensions", nil).json(t)["items"].([]any)[0].(map[string]any); n["externalNumber"] != "" {
		t.Fatalf("listed = %v", n)
	}
}

// TestNoRouterNoSave fails if a build without the routing engine saves a
// trunk or route unvalidated, or runs the tester.
func TestNoRouterNoSave(t *testing.T) {
	e := newEnvConfig(t, Config{Live: noLive{}}, testBox(t))
	c := e.login()
	before := e.dbFingerprint()
	for _, req := range []struct{ method, path string }{
		{"POST", "/api/v1/trunks"}, {"POST", "/api/v1/routes/outbound"}, {"POST", "/api/v1/routes/inbound"},
		{"PUT", "/api/v1/routes/outbound/order"}, {"POST", "/api/v1/routing/test"},
	} {
		r := c.do(req.method, req.path, map[string]any{})
		if r.code != http.StatusServiceUnavailable {
			t.Errorf("%s %s without a router = %d, want 503", req.method, req.path, r.code)
		}
	}
	if e.dbFingerprint() != before {
		t.Fatal("a request without a router wrote to the database")
	}
}
