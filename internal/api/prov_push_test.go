package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
)

// TestPhoneResyncAPI fails if a viewer can queue a resync or reboot (single,
// group or all), if an operator's request is not queued (202) and audited
// with its kind, if a second resync while one is pending adds another row,
// if a phone with no device is not refused 409, if a missing phone or group
// is not 404, if the bulk counts do not separate queued from skipped phones,
// if the claim does not honour the per-minute rate, or if the result a SIP
// node writes is not what the pushes lists return.
func TestPhoneResyncAPI(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	c := p.c
	viewer, _ := p.asRole("vera", auth.RoleViewer)
	operator, _ := p.asRole("oscar", auth.RoleOperator)

	a := p.phone("0004f2000101", nil)
	b := p.phone("0004f2000102", nil)
	d := p.phone("0004f2000103", nil)
	aid, did := rowID(a), rowID(d)
	_ = b
	c.must(http.StatusOK, "PATCH", "/api/v1/phones/"+did, map[string]any{"deviceId": nil})
	g := c.must(http.StatusCreated, "POST", "/api/v1/phone-groups", map[string]any{"name": "Dock"}).json(t)
	gid := rowID(g)
	for _, id := range []string{aid, did} {
		c.must(http.StatusOK, "PATCH", "/api/v1/phones/"+id, map[string]any{"groupId": g["id"]})
	}

	// A viewer reads but never pushes.
	for _, path := range []string{
		"/api/v1/phones/" + aid + "/resync", "/api/v1/phones/" + aid + "/reboot",
		"/api/v1/phones/resync", "/api/v1/phones/reboot",
		"/api/v1/phone-groups/" + gid + "/resync", "/api/v1/phone-groups/" + gid + "/reboot",
	} {
		if r := viewer.do("POST", path, nil); r.code != http.StatusForbidden {
			t.Errorf("viewer POST %s = %d, want 403", path, r.code)
		}
	}
	viewer.must(http.StatusOK, "GET", "/api/v1/phones/"+aid+"/pushes", nil)

	// One phone: queued, audited, and a pending twin is not added again.
	first := operator.must(http.StatusAccepted, "POST", "/api/v1/phones/"+aid+"/resync", nil).json(t)
	if first["status"] != "pending" || first["kind"] != "resync" || first["source"] != "manual" {
		t.Fatalf("queued push = %v", first)
	}
	again := operator.must(http.StatusAccepted, "POST", "/api/v1/phones/"+aid+"/resync", nil).json(t)
	if again["id"] != first["id"] {
		t.Errorf("a second resync while one is pending added row %v, want %v", again["id"], first["id"])
	}
	if n := p.scalar(`SELECT count(*) FROM audit_events WHERE action = 'resync' AND resource = 'phone' AND resource_id = $1`, aid); n != "2" {
		t.Errorf("resync audit rows = %s, want 2", n)
	}
	rb := operator.must(http.StatusAccepted, "POST", "/api/v1/phones/"+aid+"/reboot", nil).json(t)
	if rb["kind"] != "reboot" || rb["id"] == first["id"] {
		t.Errorf("reboot push = %v", rb)
	}
	if n := p.scalar(`SELECT count(*) FROM audit_events WHERE action = 'reboot' AND resource_id = $1`, aid); n != "1" {
		t.Errorf("reboot audit rows = %s, want 1", n)
	}

	// Refusals.
	operator.must(http.StatusConflict, "POST", "/api/v1/phones/"+did+"/resync", nil)
	operator.must(http.StatusNotFound, "POST", "/api/v1/phones/99999/resync", nil)
	operator.must(http.StatusNotFound, "POST", "/api/v1/phone-groups/99999/resync", nil)

	// Bulk: the group has one pushable phone and one without a device; all
	// has two pushable phones.
	got := operator.must(http.StatusAccepted, "POST", "/api/v1/phone-groups/"+gid+"/reboot", nil).json(t)
	if got["queued"] != float64(1) || got["skipped"] != float64(1) {
		t.Errorf("group reboot = %v, want 1 queued 1 skipped", got)
	}
	got = operator.must(http.StatusAccepted, "POST", "/api/v1/phones/resync", nil).json(t)
	if got["queued"] != float64(2) || got["skipped"] != float64(1) {
		t.Errorf("all resync = %v, want 2 queued 1 skipped", got)
	}
	if n := p.scalar(`SELECT count(*) FROM audit_events WHERE resource = 'phone_group' AND resource_id = $1 AND action = 'reboot'`, gid); n != "1" {
		t.Errorf("group audit rows = %s, want 1", n)
	}
	if n := p.scalar(`SELECT count(*) FROM audit_events WHERE resource = 'phone' AND resource_id = 'all' AND action = 'resync'`); n != "1" {
		t.Errorf("bulk audit rows = %s, want 1", n)
	}

	// The claim never exceeds the rate within a minute, oldest first.
	ctx := context.Background()
	jobs, err := p.st.ClaimPushes(ctx, "sip-1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || jobs[0].ID >= jobs[1].ID || jobs[0].Device == "" || jobs[0].Vendor != "yealink" {
		t.Fatalf("claimed %+v, want the 2 oldest with device and vendor", jobs)
	}
	more, err := p.st.ClaimPushes(ctx, "sip-2", 2)
	if err != nil || len(more) != 0 {
		t.Fatalf("second claim within the minute = %+v, %v; want none", more, err)
	}
	if n := p.scalar(`SELECT count(*) FROM phone_pushes WHERE status = 'sending'`); n != "2" {
		t.Errorf("sending rows = %s, want 2", n)
	}
	rest, err := p.st.ClaimPushes(ctx, "sip-1", 100)
	if err != nil || len(rest) == 0 {
		t.Fatalf("claim with room = %+v, %v", rest, err)
	}

	// The result a node writes is what the API reports.
	if err := p.st.FinishPush(ctx, jobs[0].ID, "ok", "200", 2); err != nil {
		t.Fatal(err)
	}
	if err := p.st.FinishPush(ctx, jobs[0].ID, "failed", "again", 1); err == nil {
		t.Error("a finished push was finished again")
	}
	var pid string
	if err := p.db.QueryRowContext(ctx, `SELECT phone_id::text FROM phone_pushes WHERE id = $1`, jobs[0].ID).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	items := listItems(t, viewer.must(http.StatusOK, "GET", "/api/v1/phones/"+pid+"/pushes?limit=50", nil))
	var found bool
	for _, it := range items {
		m := it.(map[string]any)
		if m["id"] == float64(jobs[0].ID) {
			found = true
			if m["status"] != "ok" || m["detail"] != "200" || m["contacts"] != float64(2) || m["finishedAt"] == nil {
				t.Errorf("finished push = %v", m)
			}
		}
	}
	if !found {
		t.Errorf("push %d missing from %v", jobs[0].ID, items)
	}
	viewer.must(http.StatusBadRequest, "GET", "/api/v1/phones/"+aid+"/pushes?limit=0", nil)
	viewer.must(http.StatusNotFound, "GET", "/api/v1/phones/99999/pushes", nil)
	latest := listItems(t, viewer.must(http.StatusOK, "GET", "/api/v1/phone-pushes", nil))
	if len(latest) != 2 {
		t.Errorf("latest pushes = %d phones, want 2 (the phone without a device never had one)", len(latest))
	}
}
