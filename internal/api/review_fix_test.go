package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/routing"
)

// TestPreexistingInvalidConfig fails if an error already in the saved
// configuration (a manual SQL edit, a stricter engine release) blocks the
// change that fixes it, its deletion, or an unrelated change — or if a
// change that introduces a new error is no longer rejected.
func TestPreexistingInvalidConfig(t *testing.T) {
	p := newP2Env(t, nil, false)
	c := p.login()
	c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]any{"number": "101", "name": "Desk"})
	trunk := c.must(http.StatusCreated, "POST", "/api/v1/trunks", map[string]any{
		"name": "peer", "mode": "ip", "destinations": []map[string]any{{"host": "10.0.0.5"}},
	}).json(t)["id"]
	plant := func(pos int, name string) string {
		t.Helper()
		var id int64
		if err := p.db.QueryRow(`INSERT INTO inbound_routes (position, name, did_kind, destination_kind, destination)
			VALUES ($1, $2, 'any', 'extension', '555') RETURNING id`, pos, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprint(id)
	}
	early := c.must(http.StatusCreated, "POST", "/api/v1/routes/inbound", map[string]any{
		"name": "Early", "destinationKind": "extension", "destination": "101",
	}).json(t)["id"]
	broken := plant(100, "Broken")
	if _, errs := routing.Compile(mustConfig(t, p)); len(errs) == 0 {
		t.Fatal("the planted route should make the saved configuration invalid")
	}

	// Unrelated changes go through.
	c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]any{"number": "102", "name": "Sales"})
	c.must(http.StatusCreated, "POST", "/api/v1/routes/outbound", map[string]any{
		"name": "All", "matchKind": "prefix", "match": "0", "trunks": []any{trunk},
	})
	// A change that adds an error of its own is still rejected, relative to it.
	before := p.dbFingerprint()
	if got := fieldPaths(t, c.do("POST", "/api/v1/routes/inbound", map[string]any{
		"name": "Also broken", "destinationKind": "extension", "destination": "556",
	})); fmt.Sprint(got) != "[destination]" {
		t.Fatalf("new error: fields %v, want [destination]", got)
	}
	if p.dbFingerprint() != before {
		t.Fatal("a rejected change wrote to the database")
	}
	// Deleting an earlier route moves the broken one up the list; its error
	// is the same error, not a new one.
	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/routes/inbound/%v", early), nil)
	// The fix itself goes through.
	if got := c.must(http.StatusOK, "PATCH", "/api/v1/routes/inbound/"+broken, map[string]any{"destination": "101"}).json(t); got["destination"] != "101" {
		t.Fatalf("fixed route = %v", got)
	}
	// So does deleting a broken route.
	broken2 := plant(101, "Broken again")
	c.must(http.StatusNoContent, "DELETE", "/api/v1/routes/inbound/"+broken2, nil)
	if _, errs := routing.Compile(mustConfig(t, p)); len(errs) != 0 {
		t.Fatalf("configuration still invalid: %v", errs)
	}
}

func mustConfig(t *testing.T, p *p2Env) routing.Config {
	t.Helper()
	snap, err := p.st.RoutingConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return snap.Config
}

// fixedLive is trunk live state with a fixed status per trunk.
type fixedLive map[int64]livestate.TrunkStatus

func (f fixedLive) TrunkStatus(_ context.Context, id int64) (livestate.TrunkStatus, error) {
	return f[id], nil
}

// TestTesterCapacityAndInboundInputs fails if the tester judges a trunk full
// from the cached call count (hello-sip decides at attempt time), omits the
// capacity note, ignores the sipDomain and headers inputs, or does not say
// which inbound conditions it could not evaluate.
func TestTesterCapacityAndInboundInputs(t *testing.T) {
	live := fixedLive{}
	p := newP2Env(t, live, false)
	c := p.login()
	for _, n := range []string{"101", "102", "103"} {
		c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]any{"number": n, "name": "E" + n})
	}
	capped := c.must(http.StatusCreated, "POST", "/api/v1/trunks", map[string]any{
		"name": "capped", "mode": "ip", "maxCalls": 1, "destinations": []map[string]any{{"host": "10.0.0.5"}},
	}).json(t)["id"].(float64)
	live[int64(capped)] = livestate.TrunkStatus{TrunkID: int64(capped), ActiveCalls: 5}
	c.must(http.StatusCreated, "POST", "/api/v1/routes/outbound", map[string]any{
		"name": "Mobile", "matchKind": "prefix", "match": "05", "trunks": []any{capped},
	})
	for _, r := range []map[string]any{
		{"name": "Domain", "sipDomain": "pbx.example", "destinationKind": "extension", "destination": "101"},
		{"name": "Arabic", "headerName": "X-Lang", "headerRegex": "^ar$", "destinationKind": "extension", "destination": "102"},
		{"name": "Rest", "destinationKind": "extension", "destination": "103"},
	} {
		c.must(http.StatusCreated, "POST", "/api/v1/routes/inbound", r)
	}

	type result struct {
		Decision testDecision   `json:"decision"`
		Trace    []routing.Step `json:"trace"`
	}
	run := func(body map[string]any) (result, string) {
		t.Helper()
		var r result
		if err := json.Unmarshal(c.must(http.StatusOK, "POST", "/api/v1/routing/test", body).body, &r); err != nil {
			t.Fatal(err)
		}
		var texts []string
		for _, s := range r.Trace {
			texts = append(texts, s.Text)
		}
		return r, strings.Join(texts, "\n")
	}

	r, trace := run(map[string]any{"from": "101", "number": "0501234567"})
	if fmt.Sprint(r.Decision.Trunks) != "[capped]" || strings.Contains(trace, "skipped: full") ||
		!strings.Contains(trace, "Capacity is checked when the call is placed: trunk capped allows 1 concurrent calls") {
		t.Fatalf("capacity: decision %+v trace:\n%s", r.Decision, trace)
	}

	from := fmt.Sprintf("trunk:%v", capped)
	domainNote := `Route "Domain" SIP domain condition not evaluated`
	headerNote := `Route "Arabic" header condition not evaluated`
	r, trace = run(map[string]any{"from": from, "number": "+97142000000"})
	if r.Decision.Extension != "103" || !strings.Contains(trace, domainNote) || !strings.Contains(trace, headerNote) {
		t.Fatalf("no inputs: decision %+v trace:\n%s", r.Decision, trace)
	}
	r, trace = run(map[string]any{"from": from, "number": "+97142000000", "sipDomain": "pbx.example"})
	if r.Decision.Extension != "101" || strings.Contains(trace, domainNote) || !strings.Contains(trace, headerNote) {
		t.Fatalf("sipDomain: decision %+v trace:\n%s", r.Decision, trace)
	}
	r, trace = run(map[string]any{"from": from, "number": "+97142000000", "headers": map[string]string{"x-lang": "ar"}})
	if r.Decision.Extension != "102" || !strings.Contains(trace, domainNote) || strings.Contains(trace, headerNote) {
		t.Fatalf("headers: decision %+v trace:\n%s", r.Decision, trace)
	}
	if got := fieldPaths(t, c.do("POST", "/api/v1/routing/test", map[string]any{"from": from, "number": "1", "headers": map[string]string{"bad name": "x"}})); !slices.Contains(got, "headers") {
		t.Fatalf("bad header name: fields %v", got)
	}
	if got := fieldPaths(t, c.do("POST", "/api/v1/routing/test", map[string]any{"from": from, "number": "1", "sipDomain": "not a host"})); !slices.Contains(got, "sipDomain") {
		t.Fatalf("bad sipDomain: fields %v", got)
	}
}

// TestSourceCIDRBreadth fails if a trunk may trust a source range broader
// than /8 (IPv4) or /32 (IPv6).
func TestSourceCIDRBreadth(t *testing.T) {
	p := newP2Env(t, nil, false)
	c := p.login()
	trunk := func(cidrs ...string) response {
		return c.do("POST", "/api/v1/trunks", map[string]any{
			"name": "t" + fmt.Sprint(len(cidrs)), "mode": "ip", "sourceCidrs": cidrs,
			"destinations": []map[string]any{{"host": "10.0.0.5"}},
		})
	}
	for _, bad := range []string{"10.0.0.0/7", "0.0.0.0/0", "2001:db8::/31", "::/0"} {
		r := trunk(bad)
		if got := fieldPaths(t, r); fmt.Sprint(got) != "[sourceCidrs[0]]" || !strings.Contains(string(r.body), "spoofed") {
			t.Errorf("%s: %s", bad, r.body)
		}
	}
	if r := trunk("10.0.0.0/8", "2001:db8::/32"); r.code != http.StatusCreated {
		t.Fatalf("/8 and /32 rejected: %s", r.body)
	}
}

// TestExtensionReviewFixes fails if extension 400s lack error.fields, if
// regex length is counted in bytes, or if an extension an outbound route
// restricts to can be deleted or renumbered.
func TestExtensionReviewFixes(t *testing.T) {
	p := newP2Env(t, nil, false)
	c := p.login()
	if got := fieldPaths(t, c.do("POST", "/api/v1/extensions", map[string]any{"number": "1", "name": " ", "externalNumber": "x"})); fmt.Sprint(got) != "[number name externalNumber]" {
		t.Fatalf("create: fields %v", got)
	}
	ext := c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]any{"number": "101", "name": "Desk"}).json(t)
	path := fmt.Sprintf("/api/v1/extensions/%v", ext["id"])
	if got := fieldPaths(t, c.do("PATCH", path, map[string]any{"externalNumber": "x"})); fmt.Sprint(got) != "[externalNumber]" {
		t.Fatalf("patch: fields %v", got)
	}

	trunk := c.must(http.StatusCreated, "POST", "/api/v1/trunks", map[string]any{
		"name": "peer", "mode": "ip", "destinations": []map[string]any{{"host": "10.0.0.5"}},
	}).json(t)["id"]
	// 500 characters of two bytes each is within the limit; 501 is not.
	c.must(http.StatusCreated, "POST", "/api/v1/routes/outbound", map[string]any{
		"name": "Wide", "matchKind": "regex", "match": strings.Repeat("é", 500), "trunks": []any{trunk},
	})
	if got := fieldPaths(t, c.do("POST", "/api/v1/routes/outbound", map[string]any{
		"name": "Too wide", "matchKind": "regex", "match": strings.Repeat("é", 501), "trunks": []any{trunk},
	})); fmt.Sprint(got) != "[match]" {
		t.Fatalf("501 characters: fields %v", got)
	}

	c.must(http.StatusCreated, "POST", "/api/v1/routes/outbound", map[string]any{
		"name": "Desk only", "matchKind": "prefix", "match": "00", "sourceExtensions": []string{"101"}, "trunks": []any{trunk},
	})
	before := p.dbFingerprint()
	for _, req := range []struct {
		method string
		body   any
	}{{"DELETE", nil}, {"PATCH", map[string]any{"number": "109"}}} {
		r := c.must(http.StatusConflict, req.method, path, req.body)
		if !strings.Contains(string(r.body), `extension 101 is used by route \"Desk only\"`) {
			t.Fatalf("%s source extension: %s", req.method, r.body)
		}
	}
	if p.dbFingerprint() != before {
		t.Fatal("a refused extension change wrote to the database")
	}
	c.must(http.StatusOK, "PATCH", path, map[string]any{"name": "Front desk"})
}

// TestInboundSIPSRejected fails if a sips: destination is accepted while
// Hello speaks UDP only.
func TestInboundSIPSRejected(t *testing.T) {
	p := newP2Env(t, nil, false)
	c := p.login()
	r := c.do("POST", "/api/v1/routes/inbound", map[string]any{"name": "Agent", "destinationKind": "sip_uri", "destination": "sips:agent@ai.example"})
	if got := fieldPaths(t, r); fmt.Sprint(got) != "[destination]" || !strings.Contains(string(r.body), "UDP-only") {
		t.Fatalf("sips: %s", r.body)
	}
	c.must(http.StatusCreated, "POST", "/api/v1/routes/inbound", map[string]any{"name": "Agent", "destinationKind": "sip_uri", "destination": "sip:agent@ai.example"})
}

// TestTesterEmergencyFlag fails if the route tester does not say whether
// the matched outbound route is an emergency route.
func TestTesterEmergencyFlag(t *testing.T) {
	live := fixedLive{}
	p := newP2Env(t, live, false)
	c := p.login()
	c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]any{"number": "101", "name": "Desk"})
	trunk := c.must(http.StatusCreated, "POST", "/api/v1/trunks", map[string]any{
		"name": "carrier", "mode": "ip", "destinations": []map[string]any{{"host": "10.0.0.5"}},
	}).json(t)["id"].(float64)
	live[int64(trunk)] = livestate.TrunkStatus{TrunkID: int64(trunk)}
	for _, r := range []map[string]any{
		{"name": "Emergency", "matchKind": "prefix", "match": "112", "trunks": []any{trunk}, "emergency": true},
		{"name": "Mobile", "matchKind": "prefix", "match": "05", "trunks": []any{trunk}},
	} {
		c.must(http.StatusCreated, "POST", "/api/v1/routes/outbound", r)
	}
	for number, want := range map[string]bool{"112": true, "0501234567": false} {
		var r struct {
			Decision map[string]any `json:"decision"`
		}
		if err := json.Unmarshal(c.must(http.StatusOK, "POST", "/api/v1/routing/test", map[string]any{"from": "101", "number": number}).body, &r); err != nil {
			t.Fatal(err)
		}
		if got, ok := r.Decision["emergency"].(bool); !ok || got != want || r.Decision["kind"] != "outbound" {
			t.Errorf("%s: decision %v, want emergency %v", number, r.Decision, want)
		}
	}
}
