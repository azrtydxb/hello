package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// TestTimeConditionAPI walks the time-condition API against PostgreSQL:
// schedules, calendars (with an import), time conditions with the loop guard,
// the override, the evaluate endpoint, and the places a time condition can be
// named. It fails if a reference is not checked, a loop is accepted, a
// referenced object can be deleted, the override is lost on an update or the
// evaluation disagrees with the schedule.
func TestTimeConditionAPI(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()
	ctx := context.Background()
	greet, err := e.st.CreateAnnouncement(ctx, "test", "closed-msg", "ann/closed.wav", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = greet

	// The default zone.
	if s := c.must(http.StatusOK, "GET", "/api/v1/time-settings", nil).json(t); s["timeZone"] != "UTC" {
		t.Fatalf("time-settings = %v", s)
	}

	// Calendars: create, import, validate.
	cal := c.must(http.StatusCreated, "POST", "/api/v1/calendars", map[string]any{
		"name": "holidays", "entries": []map[string]any{{"label": "Christmas Day", "start": "2026-12-25"}},
	}).json(t)
	calID := fmt.Sprint(cal["id"])
	if es := cal["entries"].([]any); len(es) != 1 || es[0].(map[string]any)["allDay"] != true || es[0].(map[string]any)["mode"] != "closed" {
		t.Fatalf("calendar = %v", cal)
	}
	c.must(http.StatusConflict, "POST", "/api/v1/calendars", map[string]any{"name": "holidays"})
	for name, body := range map[string]map[string]any{
		"bad name":      {"name": "a b"},
		"bad date":      {"name": "x", "entries": []map[string]any{{"start": "2026-13-01"}}},
		"open no hours": {"name": "x", "entries": []map[string]any{{"start": "2026-12-24", "mode": "open"}}},
		"unknown field": {"name": "x", "extra": 1},
		"no name":       {},
	} {
		if r := c.do("POST", "/api/v1/calendars", body); r.code != http.StatusBadRequest {
			t.Errorf("calendar %s: status %d, want 400", name, r.code)
		}
	}
	imp := c.must(http.StatusOK, "POST", "/api/v1/calendars/"+calID+"/import", map[string]any{
		"text": "# Belgium\n07-21 National day\n2026-12-24 open=09:00-13:00 Christmas Eve\n",
	}).json(t)
	if imp["imported"] != float64(2) || len(imp["calendar"].(map[string]any)["entries"].([]any)) != 3 {
		t.Fatalf("import = %v", imp)
	}
	c.must(http.StatusBadRequest, "POST", "/api/v1/calendars/"+calID+"/import", map[string]any{"text": "Christmas\n"})
	c.must(http.StatusBadRequest, "POST", "/api/v1/calendars/"+calID+"/import", map[string]any{"text": "  "})
	c.must(http.StatusNotFound, "POST", "/api/v1/calendars/987654/import", map[string]any{"text": "2026-12-25 x"})
	c.must(http.StatusNotFound, "GET", "/api/v1/calendars/987654", nil)
	c.must(http.StatusOK, "GET", "/api/v1/calendars/"+calID, nil)
	c.must(http.StatusOK, "GET", "/api/v1/calendars", nil)

	// Schedules.
	weekdays := []map[string]any{}
	for d := 1; d <= 5; d++ {
		weekdays = append(weekdays, map[string]any{"day": d, "start": "09:00", "end": "17:00"})
	}
	sch := c.must(http.StatusCreated, "POST", "/api/v1/schedules", map[string]any{
		"name": "office", "timeZone": "Europe/Brussels", "ranges": weekdays, "calendarIds": []any{cal["id"]},
	}).json(t)
	schID := fmt.Sprint(sch["id"])
	if sch["timeZone"] != "Europe/Brussels" || len(sch["ranges"].([]any)) != 5 || fmt.Sprint(sch["calendars"]) != "[holidays]" {
		t.Fatalf("schedule = %v", sch)
	}
	if d := c.must(http.StatusCreated, "POST", "/api/v1/schedules", map[string]any{"name": "nightly"}).json(t); d["timeZone"] != "UTC" {
		t.Fatalf("default zone not applied: %v", d)
	}
	c.must(http.StatusConflict, "POST", "/api/v1/schedules", map[string]any{"name": "office"})
	for name, body := range map[string]map[string]any{
		"bad zone":    {"name": "z", "timeZone": "Nowhere/City"},
		"overlap":     {"name": "z", "ranges": []map[string]any{{"day": 1, "start": "09:00", "end": "12:00"}, {"day": 1, "start": "11:00", "end": "13:00"}}},
		"bad time":    {"name": "z", "ranges": []map[string]any{{"day": 1, "start": "9", "end": "12:00"}}},
		"bad day":     {"name": "z", "ranges": []map[string]any{{"day": 9, "start": "09:00", "end": "12:00"}}},
		"no calendar": {"name": "z", "calendarIds": []int{987654}},
		"twice":       {"name": "z", "calendarIds": []any{cal["id"], cal["id"]}},
	} {
		if r := c.do("POST", "/api/v1/schedules", body); r.code != http.StatusBadRequest {
			t.Errorf("schedule %s: status %d, want 400", name, r.code)
		}
	}
	c.must(http.StatusNotFound, "GET", "/api/v1/schedules/987654", nil)
	c.must(http.StatusOK, "GET", "/api/v1/schedules/"+schID, nil)
	c.must(http.StatusOK, "GET", "/api/v1/schedules", nil)
	c.must(http.StatusConflict, "DELETE", "/api/v1/calendars/"+calID, nil) // the schedule consults it
	c.must(http.StatusNotFound, "PUT", "/api/v1/schedules/987654", map[string]any{"name": "x"})
	c.must(http.StatusConflict, "PUT", "/api/v1/schedules/"+schID, map[string]any{"name": "nightly"})
	c.must(http.StatusBadRequest, "PUT", "/api/v1/schedules/"+schID, map[string]any{"name": "office", "timeZone": "No/Where"})
	c.must(http.StatusOK, "PUT", "/api/v1/schedules/"+schID, map[string]any{
		"name": "office", "timeZone": "Europe/Brussels", "ranges": weekdays, "calendarIds": []any{cal["id"]}})
	c.must(http.StatusOK, "PUT", "/api/v1/calendars/"+calID, map[string]any{"name": "holidays", "entries": cal["entries"]})
	other := c.must(http.StatusCreated, "POST", "/api/v1/calendars", map[string]any{"name": "other"}).json(t)
	c.must(http.StatusConflict, "PUT", fmt.Sprintf("/api/v1/calendars/%v", other["id"]), map[string]any{"name": "holidays"})
	c.must(http.StatusBadRequest, "PUT", fmt.Sprintf("/api/v1/calendars/%v", other["id"]), map[string]any{"name": "o o"})
	c.must(http.StatusNotFound, "PUT", "/api/v1/calendars/987654", map[string]any{"name": "ghost"})
}

// TestTimeConditionRouting covers the time condition objects and where they
// can be named.
func TestTimeConditionRouting(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()
	ctx := context.Background()
	if _, err := e.st.CreateAnnouncement(ctx, "test", "closed-msg", "ann/closed.wav", nil); err != nil {
		t.Fatal(err)
	}
	sch := c.must(http.StatusCreated, "POST", "/api/v1/schedules", map[string]any{
		"name": "office", "timeZone": "Europe/Brussels",
		"ranges": []map[string]any{{"day": 1, "start": "09:00", "end": "17:00"}}}).json(t)
	member := []map[string]any{{"extensionId": e.ext101["id"], "position": 1}}
	c.must(http.StatusCreated, "POST", "/api/v1/ring-groups", map[string]any{"name": "sales", "members": member})

	dest := func(kind, target string) map[string]any { return map[string]any{"kind": kind, "target": target} }
	body := func(name string) map[string]any {
		return map[string]any{"name": name, "scheduleId": sch["id"], "toggleNumber": "1",
			"open": dest("ring_group", "sales"), "closed": map[string]any{"kind": "announcement", "target": "closed-msg", "thenVoicemail": "102"},
			"holiday": dest("hangup", "")}
	}
	tc := c.must(http.StatusCreated, "POST", "/api/v1/time-conditions", body("main")).json(t)
	id := fmt.Sprint(tc["id"])
	if tc["override"] != "auto" || tc["scheduleName"] != "office" || tc["toggleNumber"] != "1" ||
		tc["closed"].(map[string]any)["thenVoicemail"] != "102" || tc["holiday"].(map[string]any)["kind"] != "hangup" {
		t.Fatalf("time condition = %v", tc)
	}
	c.must(http.StatusConflict, "POST", "/api/v1/time-conditions", body("main"))
	other := body("other")
	other["toggleNumber"] = "1"
	c.must(http.StatusBadRequest, "POST", "/api/v1/time-conditions", other) // toggle number taken

	// References must exist and have the right shape.
	for name, mut := range map[string]func(m map[string]any){
		"unknown schedule":  func(m map[string]any) { m["scheduleId"] = 987654 },
		"unknown group":     func(m map[string]any) { m["open"] = dest("ring_group", "nope") },
		"unknown extension": func(m map[string]any) { m["open"] = dest("extension", "999") },
		"unknown condition": func(m map[string]any) { m["open"] = dest("time_condition", "ghost") },
		"bad kind":          func(m map[string]any) { m["open"] = dest("fax", "") },
		"then on a group": func(m map[string]any) {
			m["open"] = map[string]any{"kind": "ring_group", "target": "sales", "thenVoicemail": "102"}
		},
		"then without box": func(m map[string]any) {
			m["closed"] = map[string]any{"kind": "announcement", "target": "closed-msg", "thenVoicemail": "999"}
		},
		"unknown holiday":  func(m map[string]any) { m["holiday"] = dest("voicemail", "999") },
		"self":             func(m map[string]any) { m["open"] = dest("time_condition", "x1") },
		"bad toggle":       func(m map[string]any) { m["toggleNumber"] = "12345" },
		"bad name":         func(m map[string]any) { m["name"] = "a b" },
		"unknown property": func(m map[string]any) { m["extra"] = 1 },
	} {
		b := body("x1")
		b["toggleNumber"] = ""
		mut(b)
		if r := c.do("POST", "/api/v1/time-conditions", b); r.code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, r.code)
		}
	}
	c.must(http.StatusBadRequest, "POST", "/api/v1/time-conditions", map[string]any{"name": "x"}) // no schedule

	// A chain of conditions that loops is refused, however long.
	b := body("second")
	b["toggleNumber"] = ""
	b["closed"] = dest("time_condition", "main")
	second := c.must(http.StatusCreated, "POST", "/api/v1/time-conditions", b).json(t)
	put := body("main")
	put["closed"] = dest("time_condition", "second")
	c.must(http.StatusBadRequest, "PUT", "/api/v1/time-conditions/"+id, put) // main -> second -> main
	put["closed"] = dest("time_condition", "main")
	c.must(http.StatusBadRequest, "PUT", "/api/v1/time-conditions/"+id, put) // main -> main
	c.must(http.StatusNotFound, "PUT", "/api/v1/time-conditions/987654", body("main"))
	put = body("main")
	put["closed"] = dest("announcement", "closed-msg")
	if got := c.must(http.StatusOK, "PUT", "/api/v1/time-conditions/"+id, put).json(t); got["closed"].(map[string]any)["thenVoicemail"] != nil && got["closed"].(map[string]any)["thenVoicemail"] != "" {
		t.Fatalf("put = %v", got)
	}

	// Referenced objects cannot be deleted or renamed.
	secondID := fmt.Sprint(second["id"])
	c.must(http.StatusConflict, "DELETE", "/api/v1/time-conditions/"+id, nil) // second names main
	renamed := body("main2")
	renamed["closed"] = dest("announcement", "closed-msg")
	c.must(http.StatusConflict, "PUT", "/api/v1/time-conditions/"+id, renamed)
	c.must(http.StatusConflict, "DELETE", fmt.Sprintf("/api/v1/schedules/%v", sch["id"]), nil)
	c.must(http.StatusNoContent, "DELETE", "/api/v1/time-conditions/"+secondID, nil)
	c.must(http.StatusNotFound, "DELETE", "/api/v1/time-conditions/"+secondID, nil)
	c.must(http.StatusNotFound, "GET", "/api/v1/time-conditions/"+secondID, nil)
	c.must(http.StatusOK, "GET", "/api/v1/time-conditions/"+id, nil)
	c.must(http.StatusOK, "GET", "/api/v1/time-conditions", nil)

	// The override: set, kept across an update, cleared.
	c.must(http.StatusBadRequest, "PUT", "/api/v1/time-conditions/"+id+"/override", map[string]any{"override": "maybe"})
	c.must(http.StatusBadRequest, "PUT", "/api/v1/time-conditions/"+id+"/override", map[string]any{})
	c.must(http.StatusNotFound, "PUT", "/api/v1/time-conditions/987654/override", map[string]any{"override": "open"})
	forced := c.must(http.StatusOK, "PUT", "/api/v1/time-conditions/"+id+"/override", map[string]any{"override": "closed"}).json(t)
	if forced["override"] != "closed" || forced["overrideAt"] == nil || forced["overrideBy"] == "" {
		t.Fatalf("override = %v", forced)
	}
	if got := c.must(http.StatusOK, "PUT", "/api/v1/time-conditions/"+id, put).json(t); got["override"] != "closed" {
		t.Fatalf("an update lost the override: %v", got)
	}

	// Evaluate: Monday 2026-06-01 10:00 Brussels is open; the override says
	// closed unless it is ignored; Sunday is closed.
	eval := func(q url.Values, status int) map[string]any {
		return c.must(status, "GET", "/api/v1/time-conditions/"+id+"/evaluate?"+q.Encode(), nil).json(t)
	}
	q := url.Values{"at": {"2026-06-01T10:00"}}
	got := eval(q, http.StatusOK)
	dec := got["decision"].(map[string]any)
	if dec["branch"] != "closed" || dec["forced"] != true || got["override"] != "closed" {
		t.Fatalf("forced evaluation = %v", got)
	}
	q.Set("ignoreOverride", "true")
	dec = eval(q, http.StatusOK)["decision"].(map[string]any)
	if dec["branch"] != "open" || dec["forced"] != false || dec["destination"].(map[string]any)["target"] != "sales" {
		t.Fatalf("schedule evaluation = %v", dec)
	}
	q = url.Values{"at": {time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC).Format(time.RFC3339)}, "ignoreOverride": {"true"}}
	if dec = eval(q, http.StatusOK)["decision"].(map[string]any); dec["branch"] != "closed" {
		t.Fatalf("sunday = %v", dec)
	}
	if now := c.must(http.StatusOK, "GET", "/api/v1/time-conditions/"+id+"/evaluate", nil).json(t); now["name"] != "main" {
		t.Fatalf("now = %v", now)
	}
	eval(url.Values{"at": {"yesterday"}}, http.StatusBadRequest)
	c.must(http.StatusNotFound, "GET", "/api/v1/time-conditions/987654/evaluate", nil)
	c.must(http.StatusOK, "PUT", "/api/v1/time-conditions/"+id+"/override", map[string]any{"override": "auto"})

	// Where it can be named: ring group failover, feature codes, an IVR
	// option and an inbound route.
	c.must(http.StatusBadRequest, "POST", "/api/v1/ring-groups", map[string]any{
		"name": "g2", "members": member, "failureKind": "time_condition", "failureTarget": "ghost"})
	g2 := c.must(http.StatusCreated, "POST", "/api/v1/ring-groups", map[string]any{
		"name": "g2", "members": member, "failureKind": "time_condition", "failureTarget": "main"}).json(t)
	c.must(http.StatusConflict, "DELETE", "/api/v1/time-conditions/"+id, nil)
	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/ring-groups/%v", g2["id"]), nil)
	c.must(http.StatusBadRequest, "PUT", "/api/v1/feature-codes", map[string]any{"items": []map[string]any{
		{"code": "*88", "action": "time_condition", "argument": "ghost"}}})
	c.must(http.StatusBadRequest, "PUT", "/api/v1/feature-codes", map[string]any{"items": []map[string]any{
		{"code": "*28", "action": "tc_override", "argument": "sideways"}}})
	c.must(http.StatusOK, "PUT", "/api/v1/feature-codes", map[string]any{"items": []map[string]any{
		{"code": "*88", "action": "time_condition", "argument": "main"},
		{"code": "*28", "action": "tc_override", "argument": "toggle"}}})
	c.must(http.StatusConflict, "DELETE", "/api/v1/time-conditions/"+id, nil)
	c.must(http.StatusOK, "PUT", "/api/v1/feature-codes", map[string]any{"items": []map[string]any{}})
	c.must(http.StatusBadRequest, "POST", "/api/v1/routes/inbound", map[string]any{
		"name": "in", "didKind": "any", "destinationKind": "time_condition", "destination": "ghost"})
	route := c.must(http.StatusCreated, "POST", "/api/v1/routes/inbound", map[string]any{
		"name": "in", "didKind": "any", "destinationKind": "time_condition", "destination": "main"}).json(t)
	c.must(http.StatusConflict, "DELETE", "/api/v1/time-conditions/"+id, nil)
	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/routes/inbound/%v", route["id"]), nil)

	// The feature-code toggle changes the override like the API does.
	got2, err := e.st.ApplyTimeConditionToggle(ctx, "ext:101", "1", "toggle", time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)) // 10:00 Brussels, open
	if err != nil || got2.Override != "closed" {
		t.Fatalf("toggle from open = %+v, %v", got2, err)
	}
	if got2, err = e.st.ApplyTimeConditionToggle(ctx, "ext:101", "1", "toggle", time.Now()); err != nil || got2.Override != "auto" {
		t.Fatalf("toggle from forced = %+v, %v", got2, err)
	}
	if got2, err = e.st.ApplyTimeConditionToggle(ctx, "ext:101", "1", "toggle", time.Date(2026, 6, 7, 8, 0, 0, 0, time.UTC)); err != nil || got2.Override != "open" {
		t.Fatalf("toggle from closed = %+v, %v", got2, err)
	}
	if _, err = e.st.ApplyTimeConditionToggle(ctx, "ext:101", "9", "toggle", time.Now()); err == nil {
		t.Fatal("an unknown toggle number was accepted")
	}

	// Deleting in dependency order works.
	c.must(http.StatusNoContent, "DELETE", "/api/v1/time-conditions/"+id, nil)
	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/schedules/%v", sch["id"]), nil)
}

// TestTimeConditionCleanup deletes a calendar once nothing consults it.
func TestTimeConditionCleanup(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()
	cal := c.must(http.StatusCreated, "POST", "/api/v1/calendars", map[string]any{"name": "h"}).json(t)
	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/calendars/%v", cal["id"]), nil)
	c.must(http.StatusNotFound, "DELETE", fmt.Sprintf("/api/v1/calendars/%v", cal["id"]), nil)
	sch := c.must(http.StatusCreated, "POST", "/api/v1/schedules", map[string]any{"name": "s"}).json(t)
	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/schedules/%v", sch["id"]), nil)
	c.must(http.StatusNotFound, "DELETE", fmt.Sprintf("/api/v1/schedules/%v", sch["id"]), nil)
}
