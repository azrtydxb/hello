package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

// TestRingTagAPI walks the ring tag API against PostgreSQL: the seven
// built-in tags, custom tags with their limit, the ordered rules (replace,
// validation, order), a ring group's tag, the evaluate endpoint, and the
// places a tag can be deleted from. It fails if a built-in tag can be
// deleted, a used tag can be deleted, a rule names a tag that does not
// exist, an invalid pattern or window is accepted, the order of the rules is
// lost, or the evaluation disagrees with S-40.
func TestRingTagAPI(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()

	type tagList struct {
		Items []map[string]any `json:"items"`
	}
	var tags tagList
	if err := json.Unmarshal(c.must(http.StatusOK, "GET", "/api/v1/ring-tags", nil).body, &tags); err != nil || len(tags.Items) != 7 {
		t.Fatalf("tags = %v (%v), want the seven built-ins", tags, err)
	}
	for _, it := range tags.Items {
		if it["builtin"] != true {
			t.Fatalf("tag %v is not built in", it)
		}
	}
	door := c.must(http.StatusOK, "GET", "/api/v1/ring-tags/doorbell", nil).json(t)
	if door["bellcoreSlot"] != float64(4) {
		t.Fatalf("doorbell = %v, want slot 4", door)
	}
	c.must(http.StatusNotFound, "GET", "/api/v1/ring-tags/nope", nil)

	// Custom tags.
	vip := c.must(http.StatusCreated, "POST", "/api/v1/ring-tags", map[string]any{"name": "vip", "bellcoreSlot": 4, "description": "Directors"}).json(t)
	if vip["builtin"] != false || vip["bellcoreSlot"] != float64(4) {
		t.Fatalf("vip = %v", vip)
	}
	c.must(http.StatusConflict, "POST", "/api/v1/ring-tags", map[string]any{"name": "vip"})
	c.must(http.StatusConflict, "POST", "/api/v1/ring-tags", map[string]any{"name": "internal"})
	for name, body := range map[string]map[string]any{
		"no name":     {},
		"upper case":  {"name": "VIP"},
		"too long":    {"name": "abcdefghijklmnopqrstuvwxyz0123456"},
		"underscore":  {"name": "a_b"},
		"slot zero":   {"name": "x", "bellcoreSlot": 0},
		"slot six":    {"name": "x", "bellcoreSlot": 6},
		"unknown":     {"name": "x", "extra": 1},
		"description": {"name": "x", "description": string(make([]byte, 201))},
	} {
		if r := c.do("POST", "/api/v1/ring-tags", body); r.code != http.StatusBadRequest {
			t.Errorf("tag %s: status %d, want 400", name, r.code)
		}
	}
	changed := c.must(http.StatusOK, "PUT", "/api/v1/ring-tags/vip", map[string]any{"bellcoreSlot": 5}).json(t)
	if changed["bellcoreSlot"] != float64(5) || changed["description"] != "Directors" {
		t.Fatalf("changed = %v (absent fields keep their value)", changed)
	}
	c.must(http.StatusOK, "PUT", "/api/v1/ring-tags/alarm", map[string]any{"description": "Fire"})
	c.must(http.StatusBadRequest, "PUT", "/api/v1/ring-tags/vip", map[string]any{"name": "other"})
	c.must(http.StatusBadRequest, "PUT", "/api/v1/ring-tags/vip", map[string]any{"bellcoreSlot": 9})
	c.must(http.StatusNotFound, "PUT", "/api/v1/ring-tags/nope", map[string]any{"bellcoreSlot": 2})

	// At most 32 custom tags.
	for i := 1; i < 32; i++ {
		c.must(http.StatusCreated, "POST", "/api/v1/ring-tags", map[string]any{"name": fmt.Sprintf("t%d", i)})
	}
	c.must(http.StatusConflict, "POST", "/api/v1/ring-tags", map[string]any{"name": "one-too-many"})
	for i := 1; i < 32; i++ {
		c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/ring-tags/t%d", i), nil)
	}

	// A built-in tag cannot be deleted.
	c.must(http.StatusConflict, "DELETE", "/api/v1/ring-tags/internal", nil)
	c.must(http.StatusNotFound, "DELETE", "/api/v1/ring-tags/nope", nil)

	// Rules.
	if rules := c.must(http.StatusOK, "GET", "/api/v1/ring-rules", nil).json(t); len(rules["items"].([]any)) != 0 {
		t.Fatalf("rules = %v, want none", rules)
	}
	put := c.must(http.StatusOK, "PUT", "/api/v1/ring-rules", map[string]any{"rules": []map[string]any{
		{"name": "boss", "match": map[string]any{"callerPattern": "1XX"}, "tag": "vip"},
		{"match": map[string]any{"trunk": "main", "did": "+97140000XXX"}, "tag": "external"},
		{"name": "nights", "match": map[string]any{"window": map[string]any{"timeZone": "UTC",
			"ranges": []map[string]any{{"day": 1, "start": "20:00", "end": "23:00"}}}}, "tag": "alarm"},
	}}).json(t)
	items := put["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("rules = %v", put)
	}
	for i, it := range items {
		if it.(map[string]any)["position"] != float64(i+1) {
			t.Fatalf("rule %d = %v, want position %d", i, it, i+1)
		}
	}
	if items[0].(map[string]any)["tag"] != "vip" || items[2].(map[string]any)["name"] != "nights" {
		t.Fatalf("order lost: %v", items)
	}
	for name, body := range map[string]map[string]any{
		"unknown tag":    {"rules": []map[string]any{{"match": map[string]any{"trunk": "x"}, "tag": "ghost"}}},
		"bad tag name":   {"rules": []map[string]any{{"match": map[string]any{"trunk": "x"}, "tag": "Bad Tag"}}},
		"empty match":    {"rules": []map[string]any{{"match": map[string]any{}, "tag": "vip"}}},
		"bad pattern":    {"rules": []map[string]any{{"match": map[string]any{"callerPattern": "("}, "tag": "vip"}}},
		"bad caller":     {"rules": []map[string]any{{"match": map[string]any{"callerKind": "robot"}, "tag": "vip"}}},
		"bad zone":       {"rules": []map[string]any{{"match": map[string]any{"window": map[string]any{"timeZone": "Mars/Base", "ranges": []map[string]any{{"day": 1, "start": "08:00", "end": "09:00"}}}}, "tag": "vip"}}},
		"no rules field": {},
		"unknown field":  {"rules": []any{}, "extra": 1},
	} {
		if r := c.do("PUT", "/api/v1/ring-rules", body); r.code != http.StatusBadRequest {
			t.Errorf("rules %s: status %d, want 400", name, r.code)
		}
	}
	tooMany := make([]map[string]any, 101)
	for i := range tooMany {
		tooMany[i] = map[string]any{"match": map[string]any{"trunk": "x"}, "tag": "vip"}
	}
	c.must(http.StatusBadRequest, "PUT", "/api/v1/ring-rules", map[string]any{"rules": tooMany})
	if kept := c.must(http.StatusOK, "GET", "/api/v1/ring-rules", nil).json(t); len(kept["items"].([]any)) != 3 {
		t.Fatalf("a refused replace changed the rules: %v", kept)
	}

	// A tag a rule uses cannot be deleted.
	c.must(http.StatusConflict, "DELETE", "/api/v1/ring-tags/vip", nil)

	// A ring group's tag.
	m1 := fmt.Sprintf("%v", e.ext101["id"])
	group := map[string]any{"name": "sales", "failureKind": "none",
		"members": []map[string]any{{"extensionId": json.RawMessage(m1), "position": 1}}, "ringTag": "ghost"}
	c.must(http.StatusBadRequest, "POST", "/api/v1/ring-groups", group)
	c.must(http.StatusBadRequest, "POST", "/api/v1/ring-groups", withField(group, "ringTag", "Bad Tag"))
	made := c.must(http.StatusCreated, "POST", "/api/v1/ring-groups", withField(group, "ringTag", "vip")).json(t)
	if made["ringTag"] != "vip" {
		t.Fatalf("group = %v, want ringTag vip", made)
	}
	gid := fmt.Sprint(made["id"])
	if got := c.must(http.StatusOK, "GET", "/api/v1/ring-tags/vip", nil).json(t); got["usedBy"] != float64(2) {
		t.Fatalf("vip = %v, want used by 1 rule and 1 group", got)
	}
	kept := c.must(http.StatusOK, "PATCH", "/api/v1/ring-groups/"+gid, map[string]any{"strategy": "weighted"}).json(t)
	if kept["ringTag"] != "vip" {
		t.Fatalf("PATCH of another field dropped the ring tag: %v", kept)
	}
	cleared := c.must(http.StatusOK, "PATCH", "/api/v1/ring-groups/"+gid, map[string]any{"ringTag": ""}).json(t)
	if cleared["ringTag"] != "" {
		t.Fatalf("cleared = %v", cleared)
	}
	c.must(http.StatusBadRequest, "PATCH", "/api/v1/ring-groups/"+gid, map[string]any{"ringTag": "ghost"})
	c.must(http.StatusOK, "PATCH", "/api/v1/ring-groups/"+gid, map[string]any{"ringTag": "vip"})
	c.must(http.StatusConflict, "DELETE", "/api/v1/ring-tags/vip", nil)

	// Evaluate: S-40 order.
	eval := func(want int, q url.Values) map[string]any {
		if q.Get("at") == "" {
			q.Set("at", "2026-10-27T10:00:00Z") // a Tuesday morning: outside the night window
		}
		return c.must(want, "GET", "/api/v1/ring-rules/evaluate?"+q.Encode(), nil).json(t)
	}
	if got := eval(http.StatusOK, url.Values{"callerNumber": {"200"}}); got["tag"] != "internal" || got["why"] != "default" ||
		got["alertInfo"] != "<http://127.0.0.1/Bellcore-dr1>;info=hello-internal" {
		t.Fatalf("plain call = %v", got)
	}
	if got := eval(http.StatusOK, url.Values{"callerNumber": {"150"}, "groupTag": {"ringgroup"}}); got["tag"] != "vip" || got["why"] != "rule:1 boss" || got["bellcoreSlot"] != float64(5) {
		t.Fatalf("rule call = %v", got)
	}
	if got := eval(http.StatusOK, url.Values{"callerNumber": {"250"}, "groupTag": {"ringgroup"}}); got["tag"] != "ringgroup" || got["why"] != "group" {
		t.Fatalf("group call = %v", got)
	}
	if got := eval(http.StatusOK, url.Values{"callerNumber": {"250"}, "fromTrunk": {"true"}}); got["tag"] != "external" || got["why"] != "trunk" {
		t.Fatalf("trunk call = %v", got)
	}
	if got := eval(http.StatusOK, url.Values{"callerNumber": {"150"}, "kind": {"alarm"}}); got["tag"] != "alarm" || got["why"] != "kind:alarm" {
		t.Fatalf("alarm call = %v", got)
	}
	if got := eval(http.StatusOK, url.Values{"callerNumber": {"250"}, "at": {"2026-10-26T21:00:00Z"}}); got["tag"] != "alarm" || got["why"] != "rule:3 nights" {
		t.Fatalf("window call = %v", got)
	}
	for name, q := range map[string]url.Values{
		"bad kind":      {"kind": {"x"}},
		"bad caller":    {"callerKind": {"x"}},
		"bad group tag": {"groupTag": {"Bad Tag"}},
		"bad bool":      {"fromTrunk": {"maybe"}},
		"bad time":      {"at": {"yesterday"}},
	} {
		if r := c.do("GET", "/api/v1/ring-rules/evaluate?"+q.Encode(), nil); r.code != http.StatusBadRequest {
			t.Errorf("evaluate %s: status %d, want 400", name, r.code)
		}
	}

	// Clearing the rules and the group frees the tag.
	c.must(http.StatusOK, "PUT", "/api/v1/ring-rules", map[string]any{"rules": []any{}})
	c.must(http.StatusOK, "PATCH", "/api/v1/ring-groups/"+gid, map[string]any{"ringTag": ""})
	c.must(http.StatusNoContent, "DELETE", "/api/v1/ring-tags/vip", nil)
}

func withField(m map[string]any, k string, v any) map[string]any {
	out := map[string]any{}
	for kk, vv := range m {
		out[kk] = vv
	}
	out[k] = v
	return out
}
