package directory

import (
	"errors"
	"testing"
	"time"
)

// TestPGPush runs a push connector on PostgreSQL: the migration's scim kind,
// the hour's SCIM run, the guard's count and the pause columns.
func TestPGPush(t *testing.T) {
	r := newPGRig(t)
	r.e.Drivers[KindSCIM] = &pushDriver{}
	c, err := r.e.Create(r.ctx, "admin", ConnectorInput{Kind: KindSCIM, Name: strPtr("Okta"), Enabled: boolPtr(true)})
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != KindSCIM || c.ScheduleMinutes != 0 || c.PausedAt != nil {
		t.Fatalf("connector = %+v", c)
	}
	var users []SourceUser
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		users = append(users, su("ok-"+n, n+"@corp.org", n+"@corp.org", "User "+n, ""))
	}
	res, err := r.e.Push(r.ctx, c, users, PushOptions{})
	if err != nil || len(res.Changes) != 7 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if got := r.scalar(`SELECT count(*) FROM users WHERE source_connector_id = $1 AND password_hash = '!'`, c.ID); got != "7" {
		t.Fatalf("pushed users = %s", got)
	}
	for i := 0; i < 5; i++ {
		res, err := r.e.Push(r.ctx, c, users[i:i+1], PushOptions{Strict: true, Delete: true})
		if err != nil || res.Changes[0].Action != ActionDisable {
			t.Fatalf("delete %d = %+v, %v", i, res, err)
		}
	}
	if n, err := r.pg.PushDisables(r.ctx, c.ID, time.Now().Add(-time.Hour)); err != nil || n != 5 {
		t.Fatalf("disables = %d, %v", n, err)
	}
	if got := r.scalar(`SELECT count(*) FROM directory_sync_runs WHERE connector_id = $1 AND kind = 'scim'`, c.ID); got != "1" {
		t.Fatalf("scim runs = %s", got)
	}
	if got := r.scalar(`SELECT counts->>'disable' FROM directory_sync_runs WHERE connector_id = $1 AND kind = 'scim'`, c.ID); got != "5" {
		t.Fatalf("run counts = %s", got)
	}
	if _, err := r.e.Push(r.ctx, c, users[5:6], PushOptions{Strict: true, Delete: true}); !errors.Is(err, ErrPaused) {
		t.Fatalf("over the guard: %v", err)
	}
	c, _ = r.e.Get(r.ctx, c.ID)
	if c.PausedAt == nil {
		t.Fatal("not paused")
	}
	var halted int64
	if err := r.db.QueryRowContext(r.ctx, `SELECT id FROM directory_sync_runs WHERE connector_id = $1 AND status = 'needs_confirmation'`, c.ID).Scan(&halted); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := r.e.Confirm(r.ctx, "admin", halted, Decisions{}); err != nil {
		t.Fatal(err)
	}
	c, _ = r.e.Get(r.ctx, c.ID)
	if c.PausedAt != nil || c.GuardSince == nil {
		t.Fatalf("after confirm = %+v", c)
	}
	if res, err := r.e.Push(r.ctx, c, users[5:6], PushOptions{Strict: true, Delete: true}); err != nil || res.Changes[0].Action != ActionDisable {
		t.Fatalf("after confirm = %+v, %v", res, err)
	}
	if err := r.pg.SetPaused(r.ctx, 999999, true, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pause of a missing connector: %v", err)
	}
}
