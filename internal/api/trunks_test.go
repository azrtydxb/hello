package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// trunkName hands out a unique trunk name: the server rejects duplicates.
var trunkName = func() func() string {
	n := 0
	return func() string { n++; return fmt.Sprintf("carrier-dialing-%d", n) }
}()

// trunkWith builds a valid ip trunk body with an internalDialing policy.
func trunkWith(dialing map[string]any) map[string]any {
	return map[string]any{
		"name":            trunkName(),
		"mode":            "ip",
		"destinations":    []map[string]any{{"host": "10.0.0.5"}},
		"internalDialing": dialing,
	}
}

// refusedDialing posts an invalid internalDialing policy and checks the 400
// names the failing field.
func refusedDialing(t *testing.T, c *client, what string, body map[string]any, want string) {
	t.Helper()
	got := fieldPaths(t, c.do("POST", "/api/v1/trunks", trunkWith(body)))
	if !slices.Contains(got, want) {
		t.Errorf("%s: fields %v lack %s", what, got, want)
	}
}

// TestTrunkDialingValidation covers the trunk API's internalDialing
// validation (trunk plan Task 2, contract 5): the mode set, the engine's
// pattern rules (compile, length, count, non-empty iff mode patterns), the
// confirm guard on mode "all", the stored policy round-tripping, and the
// Task 1 rule that an internal-destination inbound route carries an empty
// destination.
func TestTrunkDialingValidation(t *testing.T) {
	p := newP2Env(t, nil, false)
	c := p.login()

	// Every refusal names the failing field.
	refusedDialing(t, c, "unknown mode", map[string]any{"mode": "exact"}, "internalDialing.mode")
	refusedDialing(t, c, "patterns mode without patterns", map[string]any{"mode": "patterns"}, "internalDialing.patterns")
	refusedDialing(t, c, "patterns with mode off", map[string]any{"mode": "off", "patterns": []string{"1XX"}},
		"internalDialing.patterns")
	refusedDialing(t, c, "mode all without confirm", map[string]any{"mode": "all"}, "internalDialing.confirm")
	refusedDialing(t, c, "confirm false", map[string]any{"mode": "all", "confirm": false}, "internalDialing.confirm")
	refusedDialing(t, c, "pattern over 500 characters", map[string]any{"mode": "patterns",
		"patterns": []string{strings.Repeat("X", 501)}}, "internalDialing.patterns[0]")
	tooMany := make([]string, 101)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("%03d", i)
	}
	refusedDialing(t, c, "over 100 patterns", map[string]any{"mode": "patterns", "patterns": tooMany},
		"internalDialing.patterns")
	refusedDialing(t, c, "pattern that does not compile", map[string]any{"mode": "patterns", "patterns": []string{"("}},
		"internalDialing.patterns[0]")

	// A patterns policy round-trips: response, database, PATCH replace and
	// PATCH-keep.
	created := c.must(http.StatusCreated, "POST", "/api/v1/trunks", trunkWith(map[string]any{
		"mode":     "patterns",
		"patterns": []string{"1XX", "^\\+9715[0-9]{8}$"},
	})).json(t)
	want := map[string]any{"mode": "patterns", "patterns": []any{"1XX", "^\\+9715[0-9]{8}$"}}
	if got := created["internalDialing"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("created internalDialing = %v, want %v", got, want)
	}
	id := fmt.Sprint(created["id"])
	var stored string
	if err := p.db.QueryRow(`SELECT to_json(internal_dialing) FROM trunks WHERE id = $1`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	// jsonb renders with a space after each colon, so parse instead of substring.
	var storedDialing map[string]any
	if err := json.Unmarshal([]byte(stored), &storedDialing); err != nil {
		t.Fatal(err)
	}
	if storedDialing["mode"] != "patterns" {
		t.Fatalf("stored internal_dialing mode = %v, want patterns", storedDialing["mode"])
	}
	pats, ok := storedDialing["patterns"].([]any)
	if !ok || len(pats) != 2 || pats[0] != "1XX" {
		t.Fatalf("stored internal_dialing patterns = %v, want [1XX ...]", storedDialing["patterns"])
	}
	if got := c.must(http.StatusOK, "GET", "/api/v1/trunks/"+id, nil).json(t)["internalDialing"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("GET internalDialing = %v, want %v", got, want)
	}

	// Mode all needs confirm true and stores without patterns.
	all := c.must(http.StatusCreated, "POST", "/api/v1/trunks",
		trunkWith(map[string]any{"mode": "all", "confirm": true})).json(t)
	if got := all["internalDialing"].(map[string]any)["mode"]; got != "all" {
		t.Fatalf("mode all internalDialing = %v", got)
	}
	allID := fmt.Sprint(all["id"])

	// PATCH replaces the policy; a PATCH without it keeps it.
	patched := c.must(http.StatusOK, "PATCH", "/api/v1/trunks/"+id,
		map[string]any{"internalDialing": map[string]any{"mode": "off"}}).json(t)
	if got := patched["internalDialing"].(map[string]any)["mode"]; got != "off" {
		t.Fatalf("PATCHed mode = %v, want off", got)
	}
	// A PATCH to mode all without confirm is refused and saves nothing.
	if got := fieldPaths(t, c.do("PATCH", "/api/v1/trunks/"+id,
		map[string]any{"internalDialing": map[string]any{"mode": "all"}})); !slices.Contains(got, "internalDialing.confirm") {
		t.Fatalf("PATCH to mode all without confirm: fields %v lack internalDialing.confirm", got)
	}
	if got := c.must(http.StatusOK, "GET", "/api/v1/trunks/"+id, nil).json(t)["internalDialing"].(map[string]any)["mode"]; got != "off" {
		t.Fatalf("refused PATCH to mode all left mode %v, want off", got)
	}
	kept := c.must(http.StatusOK, "PATCH", "/api/v1/trunks/"+allID, map[string]any{"maxCalls": 7}).json(t)
	if got := kept["internalDialing"].(map[string]any)["mode"]; got != "all" {
		t.Fatalf("PATCH of another field changed the policy: %v", got)
	}

	// Default: a trunk created without internalDialing dials nothing.
	if got := c.must(http.StatusCreated, "POST", "/api/v1/trunks", map[string]any{
		"name": "carrier-plain", "mode": "ip", "destinations": []map[string]any{{"host": "10.0.0.6"}},
	}).json(t)["internalDialing"]; !reflect.DeepEqual(got, map[string]any{"mode": "off", "patterns": []any{}}) {
		t.Fatalf("default internalDialing = %v, want mode off with no patterns", got)
	}

	// An internal-destination route names no destination of its own (Task 1's
	// rule).
	route := map[string]any{"name": "Internal", "destinationKind": "internal"}
	if got := fieldPaths(t, c.do("POST", "/api/v1/routes/inbound", map[string]any{
		"name": "Internal", "destinationKind": "internal", "destination": "101",
	})); !slices.Contains(got, "destination") {
		t.Fatalf("internal route with a destination: fields %v", got)
	}
	c.must(http.StatusCreated, "POST", "/api/v1/routes/inbound", route)
}

// TestTrunkCodecs covers the trunk codec allow-list: the G.711 default, the
// canonical spelling, the refusals, and PATCH keeping or replacing it.
func TestTrunkCodecs(t *testing.T) {
	p := newP2Env(t, nil, false)
	c := p.login()
	plain := func(extra map[string]any) map[string]any {
		b := map[string]any{"name": trunkName(), "mode": "ip", "destinations": []map[string]any{{"host": "10.0.0.5"}}}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}

	// Default: most carriers accept G.711 only.
	created := c.must(http.StatusCreated, "POST", "/api/v1/trunks", plain(nil)).json(t)
	if got := created["codecs"]; !reflect.DeepEqual(got, []any{"PCMU", "PCMA"}) {
		t.Fatalf("default codecs = %v, want PCMU and PCMA", got)
	}

	// Names are stored in their canonical spelling, in the order given.
	wide := c.must(http.StatusCreated, "POST", "/api/v1/trunks", plain(map[string]any{"codecs": []string{"g722", "pcmu", "OPUS"}})).json(t)
	if got := wide["codecs"]; !reflect.DeepEqual(got, []any{"G722", "PCMU", "opus"}) {
		t.Fatalf("codecs = %v, want G722 PCMU opus", got)
	}
	id := fmt.Sprint(wide["id"])

	for what, codecs := range map[string][]string{
		"unknown codec": {"PCMU", "G729"},
		"empty list":    {},
		"duplicate":     {"PCMU", "pcmu"},
	} {
		if got := fieldPaths(t, c.do("POST", "/api/v1/trunks", plain(map[string]any{"codecs": codecs}))); !slices.Contains(got, "codecs") {
			t.Errorf("%s: fields %v lack codecs", what, got)
		}
	}

	// PATCH of another field keeps the list; PATCH of codecs replaces it.
	kept := c.must(http.StatusOK, "PATCH", "/api/v1/trunks/"+id, map[string]any{"maxCalls": 3}).json(t)
	if got := kept["codecs"]; !reflect.DeepEqual(got, []any{"G722", "PCMU", "opus"}) {
		t.Fatalf("PATCH of maxCalls changed codecs to %v", got)
	}
	replaced := c.must(http.StatusOK, "PATCH", "/api/v1/trunks/"+id, map[string]any{"codecs": []string{"PCMA"}}).json(t)
	if got := replaced["codecs"]; !reflect.DeepEqual(got, []any{"PCMA"}) {
		t.Fatalf("PATCH of codecs = %v", got)
	}
}

// TestTrunkSessionTimers covers the trunk's RFC 4028 setting: the default,
// the three values, the refusal of any other, and PATCH keeping or replacing
// it.
func TestTrunkSessionTimers(t *testing.T) {
	p := newP2Env(t, nil, false)
	c := p.login()
	plain := func(extra map[string]any) map[string]any {
		b := map[string]any{"name": trunkName(), "mode": "ip", "destinations": []map[string]any{{"host": "10.0.0.5"}}}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	created := c.must(http.StatusCreated, "POST", "/api/v1/trunks", plain(nil)).json(t)
	if got := created["sessionTimers"]; got != "default" {
		t.Fatalf("default sessionTimers = %v, want default", got)
	}
	for _, v := range []string{"on", "off", "default"} {
		got := c.must(http.StatusCreated, "POST", "/api/v1/trunks", plain(map[string]any{"sessionTimers": v})).json(t)
		if got["sessionTimers"] != v {
			t.Fatalf("sessionTimers %q stored as %v", v, got["sessionTimers"])
		}
	}
	for _, bad := range []string{"yes", "ON", "true"} {
		if got := fieldPaths(t, c.do("POST", "/api/v1/trunks", plain(map[string]any{"sessionTimers": bad}))); !slices.Contains(got, "sessionTimers") {
			t.Errorf("sessionTimers %q: fields %v lack sessionTimers", bad, got)
		}
	}
	id := fmt.Sprint(created["id"])
	on := c.must(http.StatusOK, "PATCH", "/api/v1/trunks/"+id, map[string]any{"sessionTimers": "on"}).json(t)
	if on["sessionTimers"] != "on" {
		t.Fatalf("PATCH sessionTimers = %v, want on", on["sessionTimers"])
	}
	kept := c.must(http.StatusOK, "PATCH", "/api/v1/trunks/"+id, map[string]any{"maxCalls": 3}).json(t)
	if kept["sessionTimers"] != "on" {
		t.Fatalf("PATCH of maxCalls changed sessionTimers to %v", kept["sessionTimers"])
	}
	if got := fieldPaths(t, c.do("PATCH", "/api/v1/trunks/"+id, map[string]any{"sessionTimers": "sometimes"})); !slices.Contains(got, "sessionTimers") {
		t.Fatalf("PATCH with a bad sessionTimers: fields %v", got)
	}
}
