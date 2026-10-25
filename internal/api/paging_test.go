package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// TestPagingGroupCRUD: create, read, replace and delete a paging group, and
// the validation that keeps codes, priorities and member lists honest. It
// fails if a page code that shadows a feature code is stored, a duplicate
// code or name is accepted, an unknown extension id is stored, or a replace
// leaves the old member list behind.
func TestPagingGroupCRUD(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()
	m1 := json.Number(fmt.Sprint(e.ext101["id"]))
	m2 := json.Number(fmt.Sprint(e.ext102["id"]))

	c.must(http.StatusOK, "PUT", "/api/v1/feature-codes", map[string]any{"items": []map[string]any{
		{"code": "*80", "action": "intercom", "argument": ""},
		{"code": "*78", "action": "dnd_on", "argument": ""},
	}})

	good := map[string]any{"name": "kitchen", "code": "*60", "members": []json.Number{m1, m2}}
	created := c.must(http.StatusCreated, "POST", "/api/v1/paging-groups", good).json(t)
	id := fmt.Sprint(created["id"])
	if created["code"] != "*60" || created["priority"] != "normal" || created["enabled"] != true {
		t.Fatalf("created group = %v (want defaults normal and enabled)", created)
	}
	members := created["members"].([]any)
	if len(members) != 2 || members[0].(map[string]any)["number"] != "101" {
		t.Fatalf("members = %v", members)
	}
	if callers := created["callers"].([]any); len(callers) != 0 {
		t.Fatalf("callers = %v, want none (any internal extension)", callers)
	}

	// Name and code are unique.
	c.must(http.StatusConflict, "POST", "/api/v1/paging-groups", good)
	dupCode := map[string]any{"name": "other", "code": "*60", "members": []json.Number{m1}}
	c.must(http.StatusConflict, "POST", "/api/v1/paging-groups", dupCode)

	// A page code that equals or is shadowed by a feature code is refused.
	for _, code := range []string{"*80", "*8012", "*78"} {
		r := c.must(http.StatusBadRequest, "POST", "/api/v1/paging-groups",
			map[string]any{"name": "clash" + code[1:], "code": code, "members": []json.Number{m1}})
		if string(r.body) == "" {
			t.Fatalf("code %s: empty error body", code)
		}
	}
	// Feature codes cannot later shadow a page code either.
	c.must(http.StatusBadRequest, "PUT", "/api/v1/feature-codes", map[string]any{"items": []map[string]any{
		{"code": "*60", "action": "dnd_off", "argument": ""},
	}})

	// Field validation.
	for name, body := range map[string]map[string]any{
		"bad code":         {"name": "a", "code": "60", "members": []json.Number{m1}},
		"bad priority":     {"name": "a", "code": "*61", "priority": "urgent", "members": []json.Number{m1}},
		"no members":       {"name": "a", "code": "*61", "members": []json.Number{}},
		"dup member":       {"name": "a", "code": "*61", "members": []json.Number{m1, m1}},
		"unknown member":   {"name": "a", "code": "*61", "members": []int{999999}},
		"unknown caller":   {"name": "a", "code": "*61", "members": []json.Number{m1}, "callers": []int{999999}},
		"bad name":         {"name": "a b", "code": "*61", "members": []json.Number{m1}},
		"missing code":     {"name": "a", "members": []json.Number{m1}},
		"unknown property": {"name": "a", "code": "*61", "members": []json.Number{m1}, "extra": 1},
	} {
		if r := c.do("POST", "/api/v1/paging-groups", body); r.code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, r.code)
		}
	}

	// List and get.
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(c.must(http.StatusOK, "GET", "/api/v1/paging-groups", nil).body, &list); err != nil || len(list.Items) != 1 {
		t.Fatalf("list = %v, %v", list.Items, err)
	}
	c.must(http.StatusOK, "GET", "/api/v1/paging-groups/"+id, nil)
	c.must(http.StatusNotFound, "GET", "/api/v1/paging-groups/987654", nil)

	// PUT replaces the whole group: members, callers, priority, enabled.
	put := map[string]any{
		"name": "kitchen", "code": "*60", "priority": "emergency", "enabled": false,
		"members": []json.Number{m2}, "callers": []json.Number{m1},
	}
	replaced := c.must(http.StatusOK, "PUT", "/api/v1/paging-groups/"+id, put).json(t)
	if replaced["priority"] != "emergency" || replaced["enabled"] != false {
		t.Fatalf("replaced = %v", replaced)
	}
	if ms := replaced["members"].([]any); len(ms) != 1 || ms[0].(map[string]any)["number"] != "102" {
		t.Fatalf("members after replace = %v", ms)
	}
	if cs := replaced["callers"].([]any); len(cs) != 1 || cs[0].(map[string]any)["number"] != "101" {
		t.Fatalf("callers after replace = %v", cs)
	}
	c.must(http.StatusNotFound, "PUT", "/api/v1/paging-groups/987654", put)

	// Deleting an extension removes it from the lists (cascade).
	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/extensions/%v", e.ext101["id"]), nil)
	got := c.must(http.StatusOK, "GET", "/api/v1/paging-groups/"+id, nil).json(t)
	if cs := got["callers"].([]any); len(cs) != 0 {
		t.Fatalf("callers after extension delete = %v", cs)
	}

	c.must(http.StatusNoContent, "DELETE", "/api/v1/paging-groups/"+id, nil)
	c.must(http.StatusNotFound, "DELETE", "/api/v1/paging-groups/"+id, nil)
}

// TestExtensionAllowIntercom: the intercom switch is on by default and
// changes through PATCH. It fails if a new extension does not auto-answer
// intercom calls by default or the change is not stored.
func TestExtensionAllowIntercom(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()
	if e.ext101["allowIntercom"] != true {
		t.Fatalf("new extension allowIntercom = %v, want true", e.ext101["allowIntercom"])
	}
	got := c.must(http.StatusOK, "PATCH", fmt.Sprintf("/api/v1/extensions/%v", e.ext101["id"]),
		map[string]any{"allowIntercom": false}).json(t)
	if got["allowIntercom"] != false {
		t.Fatalf("after PATCH allowIntercom = %v", got["allowIntercom"])
	}
}
