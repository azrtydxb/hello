package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestRingGroupFailoverTargets: a ring group may fall back to an extension or
// another ring group that exists; a group cannot name itself, and what a
// group falls back to cannot be deleted, renamed or renumbered.
func TestRingGroupFailoverTargets(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	var ext int64
	if err := s.db.QueryRowContext(ctx, `INSERT INTO extensions (number, name) VALUES ('300', 'Desk') RETURNING id`).Scan(&ext); err != nil {
		t.Fatal(err)
	}
	var other int64
	if err := s.db.QueryRowContext(ctx, `INSERT INTO ring_groups (name, strategy) VALUES ('support', 'ring-all') RETURNING id`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	in := func(name, kind, target string) RingGroupInput {
		return RingGroupInput{Name: name, Strategy: "ring-all", RingTimeout: 30, FailureKind: kind, FailureTarget: target}
	}
	wantField := func(what string, err error, substr string) {
		t.Helper()
		var ve *ValidationError
		if !errors.As(err, &ve) || len(ve.Fields) == 0 || !strings.Contains(ve.Fields[0].Message, substr) {
			t.Errorf("%s: %v, want a field error containing %q", what, err, substr)
		}
	}
	_, err := s.CreateRingGroup(ctx, "t", in("sales", "extension", "999"), nil)
	wantField("unknown extension", err, "does not exist")
	_, err = s.CreateRingGroup(ctx, "t", in("sales", "ring_group", "nope"), nil)
	wantField("unknown group", err, "does not exist")
	_, err = s.CreateRingGroup(ctx, "t", in("sales", "ring_group", "sales"), nil)
	wantField("itself", err, "itself")

	toExt, err := s.CreateRingGroup(ctx, "t", in("sales", "extension", "300"), nil)
	if err != nil {
		t.Fatalf("extension fallback: %v", err)
	}
	if toExt.FailureKind != "extension" || toExt.FailureTarget != "300" {
		t.Fatalf("stored %q %q", toExt.FailureKind, toExt.FailureTarget)
	}
	toGroup, err := s.CreateRingGroup(ctx, "t", in("billing", "ring_group", "support"), nil)
	if err != nil {
		t.Fatalf("group fallback: %v", err)
	}

	var inUse *InUseError
	refuse := func(what string, err error, name string) {
		t.Helper()
		if !errors.As(err, &inUse) || !strings.Contains(inUse.Msg, "ring group "+name) {
			t.Errorf("%s: %v, want an in-use error naming ring group %s", what, err, name)
		}
	}
	number := "309"
	_, err = s.UpdateExtension(ctx, "t", ext, ExtensionChange{Number: &number}, nil)
	refuse("renumber extension", err, "sales")
	refuse("delete extension", s.DeleteExtension(ctx, "t", ext, nil), "sales")
	_, err = s.UpdateRingGroup(ctx, "t", other, in("support2", "none", ""), nil)
	refuse("rename group", err, "billing")
	refuse("delete group", s.DeleteRingGroup(ctx, "t", other, nil), "billing")

	// Released: the fallbacks are cleared, and then the objects can go.
	if _, err := s.UpdateRingGroup(ctx, "t", toExt.ID, in("sales", "none", ""), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateRingGroup(ctx, "t", toGroup.ID, in("billing", "none", ""), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteExtension(ctx, "t", ext, nil); err != nil {
		t.Fatalf("delete extension after release: %v", err)
	}
	if err := s.DeleteRingGroup(ctx, "t", other, nil); err != nil {
		t.Fatalf("delete group after release: %v", err)
	}
}
