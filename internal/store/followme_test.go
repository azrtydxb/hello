package store

import (
	"context"
	"errors"
	"testing"
)

// TestFollowMeStore fails if a profile is not stored with its targets in
// order, if a save leaves old targets behind, if the store accepts a target
// that is the extension itself or one that does not exist, if the feature
// update flips a profile without a target, or if an extension or group a
// profile names can be deleted (S-5).
func TestFollowMeStore(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	e101, e102 := mustExtension(t, s, "101"), mustExtension(t, s, "102")

	def, err := s.GetFollowMe(ctx, e101.ID)
	if err != nil || def.Enabled || def.Strategy != FollowMeSimultaneous || !def.RingOwnDevices || def.TimeoutSeconds != 30 ||
		!def.ConfirmExternal || !def.DNDBlocks || len(def.Targets) != 0 || def.Number != "101" {
		t.Fatalf("default profile = %+v (%v)", def, err)
	}
	if _, err := s.GetFollowMe(ctx, 99999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("profile of a missing extension: %v, want ErrNotFound", err)
	}

	in := FollowMeInput{
		Enabled: true, Strategy: FollowMeSequential, RingOwnDevices: true, TimeoutSeconds: 40, ConfirmExternal: true, DNDBlocks: true,
		Targets: []FollowMeTarget{
			{Kind: FollowMeExternal, Target: "+32475123456", DelaySeconds: 10},
			{Kind: FollowMeExtension, Target: "102"},
		},
	}
	got, err := s.PutFollowMe(ctx, "test", e101.ID, in)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if !got.Enabled || got.Strategy != FollowMeSequential || len(got.Targets) != 2 ||
		got.Targets[0].Target != "+32475123456" || got.Targets[0].DelaySeconds != 10 || got.Targets[1].Target != "102" {
		t.Fatalf("stored = %+v", got)
	}
	// A second save replaces the targets.
	in.Targets = []FollowMeTarget{{Kind: FollowMeExternal, Target: "+32475999999"}}
	if got, err = s.PutFollowMe(ctx, "test", e101.ID, in); err != nil || len(got.Targets) != 1 || got.Targets[0].Target != "+32475999999" {
		t.Fatalf("second save = %+v (%v)", got, err)
	}

	// The store refuses what the API refuses too.
	for name, bad := range map[string]FollowMeTarget{
		"self":              {Kind: FollowMeExtension, Target: "101"},
		"self as external":  {Kind: FollowMeExternal, Target: "101"},
		"missing extension": {Kind: FollowMeExtension, Target: "999"},
		"missing group":     {Kind: FollowMeRingGroup, Target: "nope"},
		"unknown kind":      {Kind: "sip_uri", Target: "102"},
		"delay too long":    {Kind: FollowMeExtension, Target: "102", DelaySeconds: 61},
	} {
		in.Targets = []FollowMeTarget{bad}
		var v *ValidationError
		if _, err := s.PutFollowMe(ctx, "test", e101.ID, in); !errors.As(err, &v) {
			t.Errorf("%s: err = %v, want a validation error", name, err)
		}
	}
	in.Targets = make([]FollowMeTarget, 6)
	for i := range in.Targets {
		in.Targets[i] = FollowMeTarget{Kind: FollowMeExternal, Target: "+3247500000" + string(rune('0'+i))}
	}
	var v *ValidationError
	if _, err := s.PutFollowMe(ctx, "test", e101.ID, in); !errors.As(err, &v) {
		t.Errorf("six targets: err = %v, want a validation error", err)
	}
	if after, _ := s.GetFollowMe(ctx, e101.ID); len(after.Targets) != 1 {
		t.Fatalf("a refused save changed the profile: %+v", after)
	}

	// A feature code flips a profile that has a target, and only that.
	off := false
	rev0, _ := s.ConfigRevision(ctx)
	if err := s.ApplyFeatureUpdate(ctx, "hello-sip", FeatureUpdate{Number: "101", FollowMe: &off}); err != nil {
		t.Fatalf("feature update: %v", err)
	}
	if p, _ := s.GetFollowMe(ctx, e101.ID); p.Enabled {
		t.Fatal("*77 left the profile on")
	}
	on := true
	if err := s.ApplyFeatureUpdate(ctx, "hello-sip", FeatureUpdate{Number: "101", FollowMe: &on}); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.GetFollowMe(ctx, e101.ID); !p.Enabled {
		t.Fatal("*76 left the profile off")
	}
	if rev1, _ := s.ConfigRevision(ctx); rev1 <= rev0 {
		t.Fatalf("revision %d after %d: hello-sip would not reload the profile", rev1, rev0)
	}
	if err := s.ApplyFeatureUpdate(ctx, "hello-sip", FeatureUpdate{Number: "102", FollowMe: &on}); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.GetFollowMe(ctx, e102.ID); p.Enabled || len(p.Targets) != 0 {
		t.Fatalf("a code turned on an empty profile: %+v", p)
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM follow_me WHERE extension_id = $1`, e102.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a code created a profile row for 102 (%d, %v)", n, err)
	}

	// An extension or ring group a profile names stays.
	in.Targets = []FollowMeTarget{{Kind: FollowMeExtension, Target: "102"}}
	if _, err := s.PutFollowMe(ctx, "test", e101.ID, in); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteExtension(ctx, "test", e102.ID, nil); !errors.Is(err, ErrInUse) {
		t.Fatalf("delete of a target extension: %v, want in use", err)
	}
	// Deleting the extension that owns a profile takes the profile with it.
	if err := s.DeleteExtension(ctx, "test", e101.ID, nil); err != nil {
		t.Fatalf("delete of the owner: %v", err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM follow_me_targets`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d targets left after the owner went (%v)", n, err)
	}
	if err := s.DeleteExtension(ctx, "test", e102.ID, nil); err != nil {
		t.Fatalf("delete once nothing names it: %v", err)
	}
}
