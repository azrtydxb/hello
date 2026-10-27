package store

import (
	"context"
	"errors"
	"testing"
)

// TestDoorStationStore fails if a door station can be a member of a ring
// group, paging group, pickup group or queue (or become one while it is), if
// it gets a voicemail box, is listed in the extensions phonebook by default,
// can name itself or another station as a target, or if the Door group does
// not default to the doorbell tag.
func TestDoorStationStore(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	user := mustExtension(t, s, "100")
	member := mustExtension(t, s, "101")

	refused := func(what string, err error, path string) {
		t.Helper()
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("%s: err = %v, want a validation error on %s", what, err, path)
		}
		for _, f := range ve.Fields {
			if f.Path == path || len(f.Path) > len(path) && f.Path[:len(path)] == path {
				return
			}
		}
		t.Fatalf("%s: validation error %v does not blame %s", what, ve.Fields, path)
	}

	// A new station: kind door, no voicemail, hidden from the phonebook.
	door, err := s.CreateExtensionKind(ctx, "t", "900", "Front door", "", ExtensionDoor,
		DoorTargets{Extensions: []int64{user.ID}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if door.Kind != ExtensionDoor || door.VoicemailEnabled || !door.DirectoryHidden || len(door.DoorTargets.Extensions) != 1 {
		t.Fatalf("door station = %+v", door)
	}
	if _, err := s.GetVoicemailBox(ctx, door.ID); err == nil {
		t.Error("a door station has a voicemail box")
	}

	// Targets: not itself, not another station, not a missing extension, not on a user.
	_, err = s.UpdateExtension(ctx, "t", door.ID, ExtensionChange{DoorTargets: &DoorTargets{Extensions: []int64{door.ID}}}, nil)
	refused("self target", err, "doorTargets.extensions[0]")
	other, err := s.CreateExtensionKind(ctx, "t", "901", "Back door", "", ExtensionDoor, DoorTargets{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.UpdateExtension(ctx, "t", door.ID, ExtensionChange{DoorTargets: &DoorTargets{Extensions: []int64{other.ID}}}, nil)
	refused("station as target", err, "doorTargets.extensions[0]")
	_, err = s.UpdateExtension(ctx, "t", door.ID, ExtensionChange{DoorTargets: &DoorTargets{RingGroups: []int64{999}}}, nil)
	refused("missing group target", err, "doorTargets.ringGroups[0]")
	_, err = s.CreateExtensionKind(ctx, "t", "902", "User with targets", "", ExtensionUser, DoorTargets{Extensions: []int64{user.ID}}, nil)
	refused("targets on a user", err, "doorTargets")
	if _, err := s.UpdateExtension(ctx, "t", door.ID, ExtensionChange{VoicemailEnabled: ptr(true)}, nil); err == nil {
		t.Error("voicemail was enabled on a door station")
	}

	// A station is no member of anything.
	_, err = s.CreateRingGroup(ctx, "t", RingGroupInput{Name: "sales", Strategy: "ring-all", RingTimeout: 30, FailureKind: "none",
		Members: []RingGroupMember{{ExtensionID: door.ID, Position: 1, Weight: 1}}}, nil)
	refused("ring group member", err, "members")
	_, err = s.CreatePagingGroup(ctx, "t", PagingGroupInput{Name: "all", Code: "*85", Priority: "normal", Enabled: true,
		Members: []int64{door.ID}}, nil)
	refused("paging group member", err, "members")
	_, err = s.CreatePickupGroup(ctx, "t", PickupGroupInput{Name: "desk", Members: []int64{door.ID}}, nil)
	refused("pickup group member", err, "members")

	// An extension in a group cannot become a station.
	if _, err := s.CreateRingGroup(ctx, "t", RingGroupInput{Name: "support", Strategy: "ring-all", RingTimeout: 30, FailureKind: "none",
		Members: []RingGroupMember{{ExtensionID: member.ID, Position: 1, Weight: 1}}}, nil); err != nil {
		t.Fatal(err)
	}
	kind := ExtensionDoor
	_, err = s.UpdateExtension(ctx, "t", member.ID, ExtensionChange{Kind: &kind}, nil)
	refused("member becoming a station", err, "kind")

	// A free extension becomes one: voicemail and intercom off, hidden.
	free := mustExtension(t, s, "103")
	got, err := s.UpdateExtension(ctx, "t", free.ID, ExtensionChange{Kind: &kind}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != ExtensionDoor || got.VoicemailEnabled || got.AllowIntercom || !got.DirectoryHidden {
		t.Fatalf("converted extension = %+v", got)
	}

	// The Door group defaults to the doorbell tag.
	g, err := s.CreateRingGroup(ctx, "t", RingGroupInput{Name: "800", Strategy: "ring-all", RingTimeout: 30, FailureKind: "none",
		IsDoor: true, Members: []RingGroupMember{{ExtensionID: user.ID, Position: 1, Weight: 1}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !g.IsDoor || g.RingTag != "doorbell" {
		t.Fatalf("Door group = door %v tag %q, want door with doorbell", g.IsDoor, g.RingTag)
	}
	// And a station turned back into a user gets its box back.
	back := ExtensionUser
	got, err = s.UpdateExtension(ctx, "t", free.ID, ExtensionChange{Kind: &back}, nil)
	if err != nil || got.Kind != ExtensionUser || len(got.DoorTargets.Extensions) != 0 {
		t.Fatalf("back to user: %+v %v", got, err)
	}
	if _, err := s.GetVoicemailBox(ctx, free.ID); err != nil {
		t.Errorf("voicemail box after turning back into a user: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }
