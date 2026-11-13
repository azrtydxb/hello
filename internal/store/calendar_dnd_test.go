package store

import (
	"context"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
)

// TestSetCalendarDND runs against the real schema (it needs
// HELLO_TEST_DATABASE_URL). It fails if the calendar overrides or clears a DND
// the user set by hand, if a calendar DND is not cleared at the end, if the
// source of a DND is not recorded by every manual path, or if a no-op bumps
// the configuration revision hello-sip reloads on.
func TestSetCalendarDND(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	uid, err := s.CreateUser(ctx, "test", "anna", "x", auth.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	a, b := mustExtension(t, s, "101"), mustExtension(t, s, "102")
	other := mustExtension(t, s, "103")
	for _, e := range []Extension{a, b} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO user_extensions (user_id, extension_id) VALUES ($1, $2)`, uid, e.ID); err != nil {
			t.Fatal(err)
		}
	}
	state := func(e Extension) (bool, string) {
		got, err := s.GetExtension(ctx, e.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.DND, got.DNDSource
	}
	rev := func() int64 {
		r, err := s.ConfigRevision(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return int64(r)
	}

	// The user turned DND on at 102 by hand (a feature code).
	on := true
	if err := s.ApplyFeatureUpdate(ctx, "hello-sip", FeatureUpdate{Number: "102", DND: &on}); err != nil {
		t.Fatal(err)
	}
	if d, src := state(b); !d || src != "manual" {
		t.Fatalf("102 = %v %s", d, src)
	}
	before := rev()
	n, err := s.SetCalendarDND(ctx, uid, true)
	if err != nil || n != 1 {
		t.Fatalf("calendar on: n=%d err=%v, want 1 (only 101)", n, err)
	}
	if rev() != before+1 {
		t.Fatalf("revision = %d, want %d", rev(), before+1)
	}
	if d, src := state(a); !d || src != "calendar" {
		t.Fatalf("101 = %v %s", d, src)
	}
	if d, src := state(b); !d || src != "manual" {
		t.Fatalf("the manual DND of 102 was changed: %v %s", d, src)
	}
	if d, _ := state(other); d {
		t.Fatal("another user's extension got DND")
	}
	// Already on: nothing changes and the revision stays.
	before = rev()
	if n, err := s.SetCalendarDND(ctx, uid, true); err != nil || n != 0 || rev() != before {
		t.Fatalf("repeat on: n=%d err=%v rev %d -> %d", n, err, before, rev())
	}
	// The end of the meeting clears 101 only.
	if n, err := s.SetCalendarDND(ctx, uid, false); err != nil || n != 1 {
		t.Fatalf("calendar off: n=%d err=%v", n, err)
	}
	if d, src := state(a); d || src != "manual" {
		t.Fatalf("101 = %v %s", d, src)
	}
	if d, src := state(b); !d || src != "manual" {
		t.Fatalf("102 = %v %s: the manual DND was cleared", d, src)
	}
	before = rev()
	if n, err := s.SetCalendarDND(ctx, uid, false); err != nil || n != 0 || rev() != before {
		t.Fatalf("repeat off: n=%d err=%v", n, err)
	}

	// The user takes over a calendar DND by turning it on again by hand: it
	// is theirs now and the end of the meeting leaves it.
	if _, err := s.SetCalendarDND(ctx, uid, true); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyFeatureUpdate(ctx, "hello-sip", FeatureUpdate{Number: "101", DND: &on}); err != nil {
		t.Fatal(err)
	}
	if d, src := state(a); !d || src != "manual" {
		t.Fatalf("101 after a manual set = %v %s", d, src)
	}
	if n, _ := s.SetCalendarDND(ctx, uid, false); n != 0 {
		t.Fatalf("the calendar cleared a DND the user took over (%d)", n)
	}
	// The API path records the source too.
	off := false
	if _, err := s.UpdateExtension(ctx, "test", a.ID, ExtensionChange{DND: &off}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetCalendarDND(ctx, uid, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateExtension(ctx, "test", a.ID, ExtensionChange{DND: &on}, nil); err != nil {
		t.Fatal(err)
	}
	if d, src := state(a); !d || src != "manual" {
		t.Fatalf("101 after an API set = %v %s", d, src)
	}
	// A rename leaves the source alone.
	name := "Renamed"
	_, _ = s.SetCalendarDND(ctx, uid, false)
	if _, err := s.UpdateExtension(ctx, "test", a.ID, ExtensionChange{DND: &off}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetCalendarDND(ctx, uid, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateExtension(ctx, "test", a.ID, ExtensionChange{Name: &name}, nil); err != nil {
		t.Fatal(err)
	}
	if d, src := state(a); !d || src != "calendar" {
		t.Fatalf("101 after a rename = %v %s, want the calendar DND kept", d, src)
	}
}
