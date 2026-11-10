package api

import (
	"fmt"
	"net/http"
	"testing"
)

// TestRingMapAPI walks the ring map API against PostgreSQL: the vendors'
// ringer lists and defaults, the global map, a phone group's overrides, the
// refusals (unknown tag, vendor or tone, a repeated entry, a missing group)
// and the guard on a tag the map uses. It fails if a default is missing for
// a tag and vendor, a tone of another vendor is accepted, a refused replace
// changes the map, or a tag in the map can be deleted.
func TestRingMapAPI(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()

	view := c.must(http.StatusOK, "GET", "/api/v1/ring-map", nil).json(t)
	vendors := view["vendors"].([]any)
	if len(vendors) != 5 {
		t.Fatalf("vendors = %v, want five", vendors)
	}
	if defaults := view["defaults"].([]any); len(defaults) != 5*7 {
		t.Fatalf("defaults = %d, want a tone for each of 7 tags and 5 vendors", len(defaults))
	}
	if len(view["global"].([]any)) != 0 || len(view["groups"].([]any)) != 0 {
		t.Fatalf("a new map has choices: %v", view)
	}

	put := c.must(http.StatusOK, "PUT", "/api/v1/ring-map", map[string]any{"entries": []map[string]any{
		{"tag": "doorbell", "vendor": "yealink", "tone": "bellcore2"},
		{"tag": "alarm", "vendor": "poly", "tone": "silent"},
	}}).json(t)
	if g := put["global"].([]any); len(g) != 2 {
		t.Fatalf("global = %v", put)
	}

	for name, body := range map[string]map[string]any{
		"no entries":     {},
		"unknown tag":    {"entries": []map[string]any{{"tag": "ghost", "vendor": "yealink", "tone": "1"}}},
		"unknown vendor": {"entries": []map[string]any{{"tag": "alarm", "vendor": "nokia", "tone": "1"}}},
		"other vendor":   {"entries": []map[string]any{{"tag": "alarm", "vendor": "yealink", "tone": "ringer1"}}},
		"no silent":      {"entries": []map[string]any{{"tag": "alarm", "vendor": "fanvil", "tone": "silent"}}},
		"twice":          {"entries": []map[string]any{{"tag": "alarm", "vendor": "snom", "tone": "Ringer1"}, {"tag": "alarm", "vendor": "snom", "tone": "Ringer2"}}},
		"extra field":    {"entries": []any{}, "x": 1},
	} {
		if r := c.do("PUT", "/api/v1/ring-map", body); r.code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, r.code)
		}
	}
	if kept := c.must(http.StatusOK, "GET", "/api/v1/ring-map", nil).json(t); len(kept["global"].([]any)) != 2 {
		t.Fatalf("a refused replace changed the map: %v", kept)
	}

	// A phone group's overrides.
	g := c.must(http.StatusCreated, "POST", "/api/v1/phone-groups", map[string]any{"name": "Warehouse"}).json(t)
	path := fmt.Sprintf("/api/v1/ring-map/groups/%v", g["id"])
	got := c.must(http.StatusOK, "PUT", path, map[string]any{"entries": []map[string]any{
		{"tag": "doorbell", "vendor": "yealink", "tone": "3"}}}).json(t)
	if gs := got["groups"].([]any); len(gs) != 1 || len(got["global"].([]any)) != 2 {
		t.Fatalf("after a group override = %v", got)
	}
	c.must(http.StatusNotFound, "PUT", "/api/v1/ring-map/groups/999999", map[string]any{"entries": []any{}})
	c.must(http.StatusNotFound, "PUT", "/api/v1/ring-map/groups/abc", map[string]any{"entries": []any{}})
	cleared := c.must(http.StatusOK, "PUT", path, map[string]any{"entries": []any{}}).json(t)
	if len(cleared["groups"].([]any)) != 0 {
		t.Fatalf("an empty list left overrides: %v", cleared)
	}

	// A tag the map uses cannot be deleted until it is unassigned.
	c.must(http.StatusCreated, "POST", "/api/v1/ring-tags", map[string]any{"name": "vip"})
	c.must(http.StatusOK, "PUT", "/api/v1/ring-map", map[string]any{"entries": []map[string]any{
		{"tag": "vip", "vendor": "snom", "tone": "Ringer4"}}})
	c.must(http.StatusConflict, "DELETE", "/api/v1/ring-tags/vip", nil)
	c.must(http.StatusOK, "PUT", "/api/v1/ring-map", map[string]any{"entries": []any{}})
	c.must(http.StatusNoContent, "DELETE", "/api/v1/ring-tags/vip", nil)
}
