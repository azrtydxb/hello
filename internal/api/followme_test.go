package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestFollowMeAPI walks the follow-me routes against PostgreSQL: a profile
// that starts as the defaults, a save that keeps what it does not name, every
// rule the profile enforces (self target, unusable number, missing extension
// or group, six targets, turned on without a target), the references that keep
// a target extension or group from being deleted or renamed, the feature codes
// that flip the profile, and the legs of a call in its CDR (S-5).
func TestFollowMeAPI(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()
	path := fmt.Sprintf("/api/v1/extensions/%v/follow-me", e.ext101["id"])

	def := c.must(http.StatusOK, "GET", path, nil).json(t)
	if def["enabled"] != false || def["strategy"] != "simultaneous" || def["ringOwnDevices"] != true || def["timeoutSeconds"] != float64(30) ||
		def["confirmExternal"] != true || def["dndBlocks"] != true || def["number"] != "101" || len(def["targets"].([]any)) != 0 {
		t.Fatalf("default profile = %v", def)
	}
	c.must(http.StatusNotFound, "GET", "/api/v1/extensions/99999/follow-me", nil)
	c.must(http.StatusNotFound, "PUT", "/api/v1/extensions/99999/follow-me", map[string]any{"enabled": false})

	saved := c.must(http.StatusOK, "PUT", path, map[string]any{
		"enabled": true, "strategy": "sequential", "timeoutSeconds": 40,
		"targets": []map[string]any{
			{"kind": "external", "target": "+32475123456", "delaySeconds": 15},
			{"kind": "extension", "target": "102"},
		},
	}).json(t)
	if saved["enabled"] != true || saved["strategy"] != "sequential" || saved["timeoutSeconds"] != float64(40) {
		t.Fatalf("saved = %v", saved)
	}
	targets := saved["targets"].([]any)
	if len(targets) != 2 || targets[0].(map[string]any)["target"] != "+32475123456" || targets[0].(map[string]any)["delaySeconds"] != float64(15) ||
		targets[1].(map[string]any)["target"] != "102" {
		t.Fatalf("targets = %v, want the order they were sent in", targets)
	}
	// A save that names one field keeps the rest, targets included.
	kept := c.must(http.StatusOK, "PUT", path, map[string]any{"confirmExternal": false}).json(t)
	if kept["confirmExternal"] != false || kept["strategy"] != "sequential" || len(kept["targets"].([]any)) != 2 {
		t.Fatalf("after a partial save = %v", kept)
	}
	// Targets replace the whole list.
	repl := c.must(http.StatusOK, "PUT", path, map[string]any{"targets": []map[string]any{{"kind": "extension", "target": "102"}}}).json(t)
	if len(repl["targets"].([]any)) != 1 {
		t.Fatalf("replaced targets = %v", repl["targets"])
	}

	six := make([]map[string]any, 6)
	for i := range six {
		six[i] = map[string]any{"kind": "external", "target": fmt.Sprintf("+3247500000%d", i)}
	}
	for name, body := range map[string]map[string]any{
		"empty body":          {},
		"unknown field":       {"colour": "red"},
		"bad strategy":        {"strategy": "random"},
		"timeout too short":   {"timeoutSeconds": 4},
		"timeout too long":    {"timeoutSeconds": 121},
		"six targets":         {"targets": six},
		"self target":         {"targets": []map[string]any{{"kind": "extension", "target": "101"}}},
		"self as external":    {"targets": []map[string]any{{"kind": "external", "target": "101"}}},
		"bad external":        {"targets": []map[string]any{{"kind": "external", "target": "0475 12 34"}}},
		"missing extension":   {"targets": []map[string]any{{"kind": "extension", "target": "999"}}},
		"missing group":       {"targets": []map[string]any{{"kind": "ring_group", "target": "nope"}}},
		"bad kind":            {"targets": []map[string]any{{"kind": "sip_uri", "target": "102"}}},
		"delay too long":      {"targets": []map[string]any{{"kind": "extension", "target": "102", "delaySeconds": 61}}},
		"on without a target": {"enabled": true, "targets": []map[string]any{}},
	} {
		if r := c.do("PUT", path, body); r.code != http.StatusBadRequest {
			t.Errorf("%s: status %d %s, want 400", name, r.code, r.body)
		}
	}
	// Nothing a refused save sent was kept.
	after := c.must(http.StatusOK, "GET", path, nil).json(t)
	if len(after["targets"].([]any)) != 1 || after["timeoutSeconds"] != float64(40) {
		t.Fatalf("profile after refused saves = %v", after)
	}

	// A ring group target, and the references that pin its target.
	group := c.must(http.StatusCreated, "POST", "/api/v1/ring-groups", map[string]any{
		"name": "700", "strategy": "ring-all", "ringTimeout": 30, "memberDelay": 5,
		"failureKind": "none", "failureTarget": "",
		"members": []map[string]any{{"extensionId": e.ext102["id"], "position": 1}},
	}).json(t)
	c.must(http.StatusOK, "PUT", path, map[string]any{"targets": []map[string]any{
		{"kind": "ring_group", "target": "700"}, {"kind": "extension", "target": "102"}}})
	c.must(http.StatusConflict, "DELETE", fmt.Sprintf("/api/v1/ring-groups/%v", group["id"]), nil)
	c.must(http.StatusConflict, "DELETE", fmt.Sprintf("/api/v1/extensions/%v", e.ext102["id"]), nil)
	c.must(http.StatusConflict, "PATCH", fmt.Sprintf("/api/v1/extensions/%v", e.ext102["id"]), map[string]any{"number": "112"})
	c.must(http.StatusOK, "PUT", path, map[string]any{"targets": []map[string]any{{"kind": "external", "target": "+32475123456"}}})
	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/ring-groups/%v", group["id"]), nil)
	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/extensions/%v", e.ext102["id"]), nil)

	// Deleting the extension takes its profile with it.
	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/extensions/%v", e.ext101["id"]), nil)
	var n int
	if err := e.db.QueryRow(`SELECT count(*) FROM follow_me`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d follow-me rows after the extension went (%v), want 0", n, err)
	}
}

// TestFollowMeFeatureCodes fails if the default feature codes lack *76 and *77
// (the spec's *74 and *75 are the park codes), if the codes are accepted on
// top of a code they overlap, or if the codes cannot be saved with the new
// actions (S-5).
func TestFollowMeFeatureCodes(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()
	codes := func(items ...map[string]string) map[string]any { return map[string]any{"items": items} }
	set := func(code, action string) map[string]string { return map[string]string{"code": code, "action": action} }

	c.must(http.StatusOK, "PUT", "/api/v1/feature-codes", codes(
		set("*76", "follow_me_on"), set("*77", "follow_me_off"), set("*74", "park"), set("*75", "park_retrieve")))
	got := c.must(http.StatusOK, "GET", "/api/v1/feature-codes", nil).json(t)
	actions := map[string]string{}
	for _, it := range got["items"].([]any) {
		m := it.(map[string]any)
		actions[m["code"].(string)] = m["action"].(string)
	}
	if actions["*76"] != "follow_me_on" || actions["*77"] != "follow_me_off" {
		t.Fatalf("codes = %v", actions)
	}
	// A code that is a prefix of another is refused, as for every feature code.
	c.must(http.StatusBadRequest, "PUT", "/api/v1/feature-codes", codes(
		set("*76", "follow_me_on"), set("*761", "follow_me_off")))
	c.must(http.StatusBadRequest, "PUT", "/api/v1/feature-codes", codes(set("*76", "follow_me_sideways")))
}

// TestFollowMeCDRLegs fails if a call's follow-me legs (cdr_legs, role
// follow_me) are not in the CDR detail, in the order they rang with their
// kind, extension, trunk and result, and if a call without any has the field
// (S-22).
func TestFollowMeCDRLegs(t *testing.T) {
	e := newEnv(t, noLive{})
	c := e.login()
	var id, plain int64
	start := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	for i, dst := range []*int64{&id, &plain} {
		if err := e.db.QueryRow(`INSERT INTO cdrs (correlation_id, sip_call_id, source, destination, start_time, end_time,
			duration_ms, billable_ms, sip_node, final_status, termination_side)
			VALUES ($1, $2, '100', '200', $3, $4, 1000, 800, 'sip-1', 200, 'caller') RETURNING id`,
			fmt.Sprint("fm", i), fmt.Sprint("fm-call-", i), start, start.Add(time.Second)).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	for pos, l := range [][4]string{
		{"own", "200", "", "cancelled"}, {"external", "0475123456", "carrier", "declined"}, {"extension", "300", "", "answered"},
	} {
		if _, err := e.db.Exec(`INSERT INTO cdr_legs (cdr_id, position, role, target_kind, extension, trunk, result)
			VALUES ($1, $2, 'follow_me', $3, $4, $5, $6)`, id, pos+1, l[0], l[1], l[2], l[3]); err != nil {
			t.Fatal(err)
		}
	}
	d := c.must(http.StatusOK, "GET", fmt.Sprintf("/api/v1/cdrs/%d", id), nil).json(t)
	legs, _ := d["followMeLegs"].([]any)
	if len(legs) != 3 {
		t.Fatalf("followMeLegs = %v", d["followMeLegs"])
	}
	second := legs[1].(map[string]any)
	if second["kind"] != "external" || second["extension"] != "0475123456" || second["trunk"] != "carrier" || second["result"] != "declined" {
		t.Fatalf("second leg = %v", second)
	}
	if legs[2].(map[string]any)["result"] != "answered" {
		t.Fatalf("third leg = %v", legs[2])
	}
	if p := c.must(http.StatusOK, "GET", fmt.Sprintf("/api/v1/cdrs/%d", plain), nil).json(t); p["followMeLegs"] != nil {
		t.Fatalf("a call with no follow-me has followMeLegs = %v", p["followMeLegs"])
	}
}
