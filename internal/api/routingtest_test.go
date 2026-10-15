// Route tester with a trunk source (trunk plan Task 3, spec S-6).
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/routing"
)

// TestRoutingTestFromTrunk fails if "from": "trunk:<id>" does not give the
// same trace a real call with the same input would produce (the engine's,
// verbatim), if the internal dialing policy does not speak its trace steps
// there, if a malformed from is not a 400 on from, or if the test writes
// anything.
//
// It needs a scratch PostgreSQL (newP2Env) and skips without
// HELLO_TEST_DATABASE_URL. Trunk live state is unreachable in this test, so
// every enabled trunk counts as usable and the trace's first step says so —
// the direct engine comparison below applies the same usability.
func TestRoutingTestFromTrunk(t *testing.T) {
	p := newP2Env(t, nil, false) // the real engine; trunk state unreachable
	c := p.login()
	c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]any{"number": "101", "name": "Desk", "externalNumber": "+97142000101"})
	a := c.must(http.StatusCreated, "POST", "/api/v1/trunks", map[string]any{
		"name": "a", "mode": "ip", "destinations": []map[string]any{{"host": "10.0.0.5"}},
	}).json(t)["id"].(float64)
	b := c.must(http.StatusCreated, "POST", "/api/v1/trunks", map[string]any{
		"name": "b", "mode": "ip", "destinations": []map[string]any{{"host": "10.0.0.6"}},
		"internalDialing": map[string]any{"mode": "patterns", "patterns": []string{"1XX"}},
	}).json(t)["id"].(float64)
	c.must(http.StatusCreated, "POST", "/api/v1/routes/inbound", map[string]any{
		"name": "Main DID", "didKind": "exact", "did": "212345",
		"destinationKind": "extension", "destination": "101",
	})
	c.must(http.StatusCreated, "POST", "/api/v1/routes/inbound", map[string]any{
		"name": "Any internal", "didKind": "any", "destinationKind": "internal",
	})
	before, audits := p.dbFingerprint(), p.auditCount()

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

	at := "2026-10-05T09:30:00+04:00"
	r := run(map[string]any{"from": fmt.Sprint("trunk:", a), "number": "212345", "callerId": "+971501112222", "at": at})
	if r.Decision.Kind != routing.KindInbound || r.Decision.Extension != "101" || r.Decision.Route != "Main DID" ||
		r.Decision.CallerID != "+971501112222" {
		t.Fatalf("decision = %+v", r.Decision)
	}
	for _, want := range []string{
		"Internal dialing off on trunk a",
		`Route "Main DID" matched (DID exact 212345)`,
		"Caller ID +971501112222 as received",
	} {
		if !strings.Contains(fmt.Sprint(r.Trace), want) {
			t.Fatalf("trace %v lacks %q", r.Trace, want)
		}
	}

	// The same decision, made directly on the saved configuration with the
	// same usability, gives the same trace: the tester is the engine.
	engineTrace(t, p, int64(a), "212345", "+971501112222", r.Trace, at)

	// The policy verdict also gates: with internal dialing off, an internal
	// route is skipped with its traced reason, and the DID falls through to
	// 404 — again exactly the engine's trace.
	r = run(map[string]any{"from": fmt.Sprint("trunk:", a), "number": "1012", "at": at})
	if r.Decision.Kind != routing.KindReject || r.Decision.RejectCode != 404 {
		t.Fatalf("out-of-policy DID = %+v", r.Decision)
	}
	if !strings.Contains(fmt.Sprint(r.Trace), `Route "Any internal" skipped: internal dialing off on trunk a`) {
		t.Fatalf("trace %v lacks the internal skip", r.Trace)
	}
	engineTrace(t, p, int64(a), "1012", "", r.Trace, at)

	// A patterns policy: the verdict names the pattern, the internal route
	// rings the extension the DID names, and the trace is the engine's for
	// the same call; a DID outside the pattern is 404, also the engine's.
	r = run(map[string]any{"from": fmt.Sprint("trunk:", b), "number": "101", "at": at})
	if r.Decision.Kind != routing.KindInbound || r.Decision.Extension != "101" || r.Decision.Route != "Any internal" {
		t.Fatalf("in-policy internal DID = %+v", r.Decision)
	}
	if !strings.Contains(fmt.Sprint(r.Trace), "Trunk b may dial 101: pattern 1XX") {
		t.Fatalf("trace %v lacks the pattern verdict", r.Trace)
	}
	engineTrace(t, p, int64(b), "101", "", r.Trace, at)
	r = run(map[string]any{"from": fmt.Sprint("trunk:", b), "number": "1012", "at": at})
	if r.Decision.Kind != routing.KindReject || r.Decision.RejectCode != 404 {
		t.Fatalf("out-of-pattern DID = %+v", r.Decision)
	}
	engineTrace(t, p, int64(b), "1012", "", r.Trace, at)

	// A malformed from is a 400 naming the field.
	for _, bad := range []string{"trunk:0", "trunk:x", "trunk:", "nobody", ""} {
		if got := fieldPaths(t, c.do("POST", "/api/v1/routing/test", map[string]any{"from": bad, "number": "1"})); !slices.Contains(got, "from") {
			t.Errorf("from %q: fields %v lack from", bad, got)
		}
	}

	// Nothing was written: not the database, not the audit log.
	if p.dbFingerprint() != before || p.auditCount() != audits {
		t.Fatal("the route tester has side effects")
	}
}

// engineTrace fails unless the tester's trace for a trunk-sourced call is
// the engine's on the saved configuration: a direct Decide with the same
// call and usability (the state-unavailable note prepended, as the tester
// prints it) must read the same.
func engineTrace(t *testing.T, p *p2Env, trunkID int64, number, callerID string, got []routing.Step, at string) {
	t.Helper()
	ctx := context.Background()
	snap, err := p.st.RoutingConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tbl, errs := routing.Compile(snap.Config)
	if len(errs) > 0 || tbl == nil {
		t.Fatalf("saved configuration does not compile: %v", errs)
	}
	atAt, err := time.Parse(time.RFC3339, at)
	if err != nil {
		t.Fatal(err)
	}
	s := &server{Config: Config{Trunks: p.live, Log: slog.New(slog.DiscardHandler)}}
	usable, note := s.usability(ctx, snap)
	d := tbl.Decide(routing.Call{FromTrunk: trunkID, Number: number, CallerID: callerID,
		SIPDomain: testDomain, Header: func(string) string { return "" }, At: atAt}, usable)
	want := routing.Trace{}
	if note != "" {
		want.Add(note)
	}
	for _, st := range d.Trace {
		want.Add(st.Text)
	}
	if fmt.Sprint(want) != fmt.Sprint(got) {
		t.Fatalf("tester trace %v differs from the engine's %v (note %q)", got, d.Trace, note)
	}
}
