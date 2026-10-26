package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
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

	// Renaming onto another group's name or code is a conflict, as on create.
	other := c.must(http.StatusCreated, "POST", "/api/v1/paging-groups",
		map[string]any{"name": "garage", "code": "*62", "members": []json.Number{m2}}).json(t)
	otherID := fmt.Sprint(other["id"])
	c.must(http.StatusConflict, "PUT", "/api/v1/paging-groups/"+otherID,
		map[string]any{"name": "kitchen", "code": "*62", "members": []json.Number{m2}})
	c.must(http.StatusConflict, "PUT", "/api/v1/paging-groups/"+otherID,
		map[string]any{"name": "garage", "code": "*60", "members": []json.Number{m2}})
	c.must(http.StatusNoContent, "DELETE", "/api/v1/paging-groups/"+otherID, nil)

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

// TestExtensionCallWaiting: call waiting is on by default and changes
// through PATCH, GET returns it, and the feature-code actions accept the
// two toggles (spec S-18). It fails if a new extension does not ring while
// busy by default or the change is not stored.
func TestExtensionCallWaiting(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()
	if e.ext101["callWaiting"] != true {
		t.Fatalf("new extension callWaiting = %v, want true", e.ext101["callWaiting"])
	}
	path := fmt.Sprintf("/api/v1/extensions/%v", e.ext101["id"])
	got := c.must(http.StatusOK, "PATCH", path, map[string]any{"callWaiting": false}).json(t)
	if got["callWaiting"] != false {
		t.Fatalf("after PATCH callWaiting = %v", got["callWaiting"])
	}
	if got := c.must(http.StatusOK, "GET", path, nil).json(t); got["callWaiting"] != false {
		t.Fatalf("GET callWaiting = %v, want false", got["callWaiting"])
	}
	c.must(http.StatusOK, "PUT", "/api/v1/feature-codes", map[string]any{"items": []map[string]any{
		{"code": "*70", "action": "call_waiting_off"}, {"code": "*71", "action": "call_waiting_on"},
	}})
}

// TestExtensionNoAnswerRings: the ring count before the no-answer forward
// starts at 0 (system default), changes through PATCH, and a count outside 0
// to 20 is a field error. It fails if any of those differ.
func TestExtensionNoAnswerRings(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()
	path := fmt.Sprintf("/api/v1/extensions/%v", e.ext101["id"])
	if e.ext101["noAnswerRings"] != float64(0) {
		t.Fatalf("new extension noAnswerRings = %v, want 0", e.ext101["noAnswerRings"])
	}
	got := c.must(http.StatusOK, "PATCH", path, map[string]any{"noAnswerRings": 5}).json(t)
	if got["noAnswerRings"] != float64(5) {
		t.Fatalf("after PATCH noAnswerRings = %v", got["noAnswerRings"])
	}
	c.must(http.StatusBadRequest, "PATCH", path, map[string]any{"noAnswerRings": 21})
	c.must(http.StatusBadRequest, "PATCH", path, map[string]any{"noAnswerRings": -1})
}

// fakePageTester answers a test page at once with a canned result.
type fakePageTester struct {
	mu   sync.Mutex
	reqs []livestate.PageTestRequest
}

func (f *fakePageTester) EnqueuePageTest(_ context.Context, r livestate.PageTestRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r)
	return nil
}

func (f *fakePageTester) GetPageTestResult(_ context.Context, id string) (livestate.PageTestResult, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.reqs {
		if r.ID == id {
			return livestate.PageTestResult{ID: id, Extension: r.Extension, Result: "answered", Node: "sip-1",
				Contacts: []livestate.PageTestContact{{Device: "d1", Result: "answered", Headers: []string{"Alert-Info: auto-answer"}}}}, true, nil
		}
	}
	return livestate.PageTestResult{}, false, nil
}

// neverPageTester queues a request that no node ever takes.
type neverPageTester struct{ fakePageTester }

func (*neverPageTester) GetPageTestResult(context.Context, string) (livestate.PageTestResult, bool, error) {
	return livestate.PageTestResult{}, false, nil
}

// TestPagingGroupTestEndpoint: POST /paging-groups/{id}/test queues a test
// page for one member and returns the SIP node's result. It fails if the
// default member is not the first, a non-member is paged, a disabled group
// is tested, an unknown group is not 404, a missing SIP node does not time
// out as 504, or a missing queue is not 503.
func TestPagingGroupTestEndpoint(t *testing.T) {
	pt := &fakePageTester{}
	e := newEnvConfig(t, Config{Objects: newMemObjects(), PageTests: pt, PageTestWait: time.Second}, nil)
	c := e.login()
	m1 := c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "101", "name": "Sales"}).json(t)
	m2 := c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "102", "name": "Support"}).json(t)
	g := c.must(http.StatusCreated, "POST", "/api/v1/paging-groups", map[string]any{
		"name": "kitchen", "code": "*60", "members": []json.Number{json.Number(fmt.Sprint(m1["id"])), json.Number(fmt.Sprint(m2["id"]))},
	}).json(t)
	path := fmt.Sprintf("/api/v1/paging-groups/%v/test", g["id"])

	res := c.must(http.StatusOK, "POST", path, map[string]any{}).json(t)
	if res["result"] != "answered" || res["extension"] != "101" {
		t.Fatalf("default test = %v, want the first member answered", res)
	}
	if got := c.must(http.StatusOK, "POST", path, map[string]any{"extension": "102"}).json(t); got["extension"] != "102" {
		t.Fatalf("test of 102 paged %v", got["extension"])
	}
	if len(pt.reqs) != 2 || pt.reqs[0].GroupCode != "*60" || pt.reqs[1].Extension != "102" {
		t.Fatalf("queued requests = %+v", pt.reqs)
	}
	c.must(http.StatusBadRequest, "POST", path, map[string]any{"extension": "999"})
	c.must(http.StatusBadRequest, "POST", path, map[string]any{"bogus": 1})
	c.must(http.StatusNotFound, "POST", "/api/v1/paging-groups/987654/test", map[string]any{})

	c.must(http.StatusOK, "PUT", fmt.Sprintf("/api/v1/paging-groups/%v", g["id"]), map[string]any{
		"name": "kitchen", "code": "*60", "enabled": false,
		"members": []json.Number{json.Number(fmt.Sprint(m1["id"]))},
	})
	c.must(http.StatusConflict, "POST", path, map[string]any{})

	// No node takes the request: 504 after the wait.
	e2 := newEnvConfig(t, Config{PageTests: &neverPageTester{}, PageTestWait: 600 * time.Millisecond}, nil)
	c2 := e2.login()
	x := c2.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "101", "name": "Sales"}).json(t)
	g2 := c2.must(http.StatusCreated, "POST", "/api/v1/paging-groups", map[string]any{
		"name": "a", "code": "*61", "members": []json.Number{json.Number(fmt.Sprint(x["id"]))},
	}).json(t)
	c2.must(http.StatusGatewayTimeout, "POST", fmt.Sprintf("/api/v1/paging-groups/%v/test", g2["id"]), map[string]any{})

	// No queue configured: 503.
	e3 := newEnvConfig(t, Config{}, nil)
	c3 := e3.login()
	y := c3.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "101", "name": "Sales"}).json(t)
	g3 := c3.must(http.StatusCreated, "POST", "/api/v1/paging-groups", map[string]any{
		"name": "a", "code": "*61", "members": []json.Number{json.Number(fmt.Sprint(y["id"]))},
	}).json(t)
	c3.must(http.StatusServiceUnavailable, "POST", fmt.Sprintf("/api/v1/paging-groups/%v/test", g3["id"]), map[string]any{})
}
