package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
)

type fakeParked struct{ calls []livestate.ParkedCall }

func (f fakeParked) ParkList(context.Context) ([]livestate.ParkedCall, error) { return f.calls, nil }

// TestParkLotCRUD: create, read, replace and delete a park lot, with the
// validation that keeps ranges, timers and the dial space honest. It fails
// if a range that overlaps another lot, an extension number or a ring group
// name is stored, a duplicate name is accepted, a malformed field is
// accepted, or a replace leaves the old range behind.
func TestParkLotCRUD(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()

	// A fresh install carries the default lot, 701-720 (migration 00031).
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(c.must(http.StatusOK, "GET", "/api/v1/park-lots", nil).body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0]["name"] != "default" || list.Items[0]["slotStart"] != float64(701) ||
		list.Items[0]["slotEnd"] != float64(720) || list.Items[0]["returnSecs"] != float64(120) || list.Items[0]["voicemailSecs"] != float64(60) {
		t.Fatalf("default lot = %v", list.Items)
	}
	defID := fmt.Sprint(int64(list.Items[0]["id"].(float64)))

	created := c.must(http.StatusCreated, "POST", "/api/v1/park-lots",
		map[string]any{"name": "upstairs", "slotStart": 801, "slotEnd": 810}).json(t)
	id := fmt.Sprint(int64(created["id"].(float64)))
	if created["returnSecs"] != float64(120) || created["voicemailSecs"] != float64(60) || created["enabled"] != true {
		t.Fatalf("created lot = %v (want the defaults 120, 60, enabled)", created)
	}
	c.must(http.StatusOK, "GET", "/api/v1/park-lots/"+id, nil)
	c.must(http.StatusNotFound, "GET", "/api/v1/park-lots/987654", nil)

	// The name is unique.
	c.must(http.StatusConflict, "POST", "/api/v1/park-lots", map[string]any{"name": "upstairs", "slotStart": 901, "slotEnd": 905})

	// The slots must not overlap another lot, an extension or a ring group.
	for name, body := range map[string]map[string]any{
		"overlaps a lot":     {"name": "x1", "slotStart": 805, "slotEnd": 815},
		"inside the default": {"name": "x2", "slotStart": 710, "slotEnd": 710},
	} {
		if r := c.do("POST", "/api/v1/park-lots", body); r.code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400: %s", name, r.code, r.body)
		}
	}
	c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "850", "name": "Slot clash"})
	c.must(http.StatusBadRequest, "POST", "/api/v1/park-lots", map[string]any{"name": "x3", "slotStart": 845, "slotEnd": 855})
	// And an extension cannot take a number a lot's slots use.
	c.must(http.StatusBadRequest, "POST", "/api/v1/extensions", map[string]string{"number": "805", "name": "Clash"})

	// Field validation.
	for name, body := range map[string]map[string]any{
		"bad name":         {"name": "a b", "slotStart": 901, "slotEnd": 905},
		"end before start": {"name": "a", "slotStart": 905, "slotEnd": 901},
		"too many slots":   {"name": "a", "slotStart": 1000, "slotEnd": 1200},
		"start out":        {"name": "a", "slotStart": 0, "slotEnd": 5},
		"short return":     {"name": "a", "slotStart": 901, "slotEnd": 905, "returnSecs": 3},
		"long voicemail":   {"name": "a", "slotStart": 901, "slotEnd": 905, "voicemailSecs": 9000},
		"missing end":      {"name": "a", "slotStart": 901},
		"unknown property": {"name": "a", "slotStart": 901, "slotEnd": 905, "extra": 1},
	} {
		if r := c.do("POST", "/api/v1/park-lots", body); r.code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, r.code)
		}
	}

	// PUT replaces the whole lot.
	put := map[string]any{"name": "upstairs", "slotStart": 821, "slotEnd": 830, "returnSecs": 90, "voicemailSecs": 30, "enabled": false}
	replaced := c.must(http.StatusOK, "PUT", "/api/v1/park-lots/"+id, put).json(t)
	if replaced["slotStart"] != float64(821) || replaced["returnSecs"] != float64(90) || replaced["enabled"] != false {
		t.Fatalf("replaced = %v", replaced)
	}
	c.must(http.StatusNotFound, "PUT", "/api/v1/park-lots/987654", put)
	c.must(http.StatusBadRequest, "PUT", "/api/v1/park-lots/"+id, map[string]any{"name": "upstairs", "slotStart": 705, "slotEnd": 725})
	c.must(http.StatusConflict, "PUT", "/api/v1/park-lots/"+id, map[string]any{"name": "default", "slotStart": 821, "slotEnd": 830})

	c.must(http.StatusNoContent, "DELETE", "/api/v1/park-lots/"+id, nil)
	c.must(http.StatusNotFound, "DELETE", "/api/v1/park-lots/"+id, nil)
	c.must(http.StatusNoContent, "DELETE", "/api/v1/park-lots/"+defID, nil)

	// The audit trail records the changes.
	var n int
	if err := e.db.QueryRow(`SELECT count(*) FROM audit_events WHERE resource = 'park_lot'`).Scan(&n); err != nil || n < 4 {
		t.Fatalf("park_lot audit events = %d, %v", n, err)
	}
}

// TestPickupGroupCRUD: create, read, replace and delete a pickup group. It
// fails if a duplicate name, an unknown or repeated member or an empty
// member list is accepted, or a replace leaves the old members behind.
func TestPickupGroupCRUD(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()
	m1 := json.Number(fmt.Sprint(e.ext101["id"]))
	m2 := json.Number(fmt.Sprint(e.ext102["id"]))

	created := c.must(http.StatusCreated, "POST", "/api/v1/pickup-groups",
		map[string]any{"name": "sales", "members": []json.Number{m1, m2}}).json(t)
	id := fmt.Sprint(int64(created["id"].(float64)))
	ms := created["members"].([]any)
	if len(ms) != 2 || ms[0].(map[string]any)["number"] != "101" {
		t.Fatalf("members = %v", ms)
	}
	c.must(http.StatusConflict, "POST", "/api/v1/pickup-groups", map[string]any{"name": "sales", "members": []json.Number{m1}})
	for name, body := range map[string]map[string]any{
		"no members":     {"name": "a", "members": []json.Number{}},
		"dup member":     {"name": "a", "members": []json.Number{m1, m1}},
		"unknown member": {"name": "a", "members": []int{999999}},
		"bad name":       {"name": "a b", "members": []json.Number{m1}},
		"missing name":   {"members": []json.Number{m1}},
		"extra":          {"name": "a", "members": []json.Number{m1}, "extra": 1},
	} {
		if r := c.do("POST", "/api/v1/pickup-groups", body); r.code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, r.code)
		}
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(c.must(http.StatusOK, "GET", "/api/v1/pickup-groups", nil).body, &list); err != nil || len(list.Items) != 1 {
		t.Fatalf("list = %v, %v", list.Items, err)
	}
	c.must(http.StatusOK, "GET", "/api/v1/pickup-groups/"+id, nil)
	c.must(http.StatusNotFound, "GET", "/api/v1/pickup-groups/987654", nil)

	replaced := c.must(http.StatusOK, "PUT", "/api/v1/pickup-groups/"+id,
		map[string]any{"name": "support", "members": []json.Number{m2}}).json(t)
	if ms := replaced["members"].([]any); replaced["name"] != "support" || len(ms) != 1 || ms[0].(map[string]any)["number"] != "102" {
		t.Fatalf("replaced = %v", replaced)
	}
	c.must(http.StatusNotFound, "PUT", "/api/v1/pickup-groups/987654", map[string]any{"name": "support", "members": []json.Number{m2}})
	other := c.must(http.StatusCreated, "POST", "/api/v1/pickup-groups", map[string]any{"name": "other", "members": []json.Number{m1}}).json(t)
	c.must(http.StatusConflict, "PUT", fmt.Sprintf("/api/v1/pickup-groups/%d", int64(other["id"].(float64))),
		map[string]any{"name": "support", "members": []json.Number{m1}})

	// Deleting an extension removes it from the group (cascade).
	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/extensions/%v", e.ext102["id"]), nil)
	got := c.must(http.StatusOK, "GET", "/api/v1/pickup-groups/"+id, nil).json(t)
	if ms := got["members"].([]any); len(ms) != 0 {
		t.Fatalf("members after deleting the extension = %v", ms)
	}
	c.must(http.StatusNoContent, "DELETE", "/api/v1/pickup-groups/"+id, nil)
	c.must(http.StatusNotFound, "DELETE", "/api/v1/pickup-groups/"+id, nil)
}

// TestDialSpaceOverlap: no feature code, paging code or park slot may
// overlap another (spec: the pickup defaults must not collide with a paging
// group). It fails if any of the overlapping writes is accepted, or if a
// write that adds no new overlap is blocked.
func TestDialSpaceOverlap(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()
	m1 := json.Number(fmt.Sprint(e.ext101["id"]))

	codes := func(items ...map[string]any) map[string]any { return map[string]any{"items": items} }
	set := func(code, action string) map[string]any {
		return map[string]any{"code": code, "action": action, "argument": ""}
	}

	// A paging group on *60, the way the live system has one.
	c.must(http.StatusCreated, "POST", "/api/v1/paging-groups",
		map[string]any{"name": "kitchen", "code": "*60", "members": []json.Number{m1}})

	// The pickup actions on the defaults coexist with it.
	c.must(http.StatusOK, "PUT", "/api/v1/feature-codes", codes(
		set("*40", "pickup_group"), set("*41", "pickup_directed"), set("*74", "park"), set("*75", "park_retrieve")))

	// A pickup code on the paging code, or one a prefix of it, is refused.
	for _, code := range []string{"*60", "*601"} {
		r := c.do("PUT", "/api/v1/feature-codes", codes(set("*40", "pickup_group"), set(code, "pickup_directed")))
		if r.code != http.StatusBadRequest {
			t.Errorf("pickup code %s over the *60 paging group: status %d, want 400", code, r.code)
		}
	}
	// Two feature codes where one is a prefix of the other.
	c.must(http.StatusBadRequest, "PUT", "/api/v1/feature-codes", codes(set("*74", "park"), set("*741", "park_retrieve")))
	// A paging code over a pickup or park code, equal or by prefix.
	c.must(http.StatusOK, "PUT", "/api/v1/feature-codes", codes(set("*40", "pickup_group"), set("*74", "park")))
	for _, code := range []string{"*40", "*401", "*74"} {
		r := c.do("POST", "/api/v1/paging-groups", map[string]any{"name": "p" + code[1:], "code": code, "members": []json.Number{m1}})
		if r.code != http.StatusBadRequest {
			t.Errorf("paging code %s over a feature code: status %d, want 400", code, r.code)
		}
	}
	// A park lot over an extension number or a ring group name.
	c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "960", "name": "ext960"})
	c.must(http.StatusBadRequest, "POST", "/api/v1/park-lots", map[string]any{"name": "clash", "slotStart": 955, "slotEnd": 965})
	c.must(http.StatusCreated, "POST", "/api/v1/ring-groups", map[string]any{"name": "980", "strategy": "ring-all",
		"members": []map[string]any{{"extensionId": json.RawMessage(fmt.Sprint(e.ext101["id"])), "position": 1}}})
	c.must(http.StatusBadRequest, "POST", "/api/v1/park-lots", map[string]any{"name": "clash2", "slotStart": 975, "slotEnd": 985})
}

// TestParkedCalls: the live parked-calls view lists what the store holds.
func TestParkedCalls(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	e := newEnvConfig(t, Config{Parked: fakeParked{calls: []livestate.ParkedCall{{
		Slot: "701", Lot: "default", Node: "sip-1", CallID: "c1", Correlation: "x", ParkedBy: "101", Caller: "102",
		CallerName: "Support", ParkedAt: now, ReturnAt: now.Add(2 * time.Minute), Phase: livestate.ParkParked}}}}, nil)
	c := e.login()
	var got struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(c.must(http.StatusOK, "GET", "/api/v1/parked-calls", nil).body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0]["slot"] != "701" || got.Items[0]["parkedBy"] != "101" || got.Items[0]["phase"] != "parked" {
		t.Fatalf("parked calls = %v", got.Items)
	}
	// Without Valkey the view says so.
	e2 := newEnvConfig(t, Config{}, nil)
	e2.login().must(http.StatusServiceUnavailable, "GET", "/api/v1/parked-calls", nil)
}
