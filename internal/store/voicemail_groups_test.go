package store

// Voicemail groups (spec pbx-gaps S-14). They need the real schema, so they
// skip unless HELLO_TEST_DATABASE_URL is set.

import (
	"context"
	"errors"
	"testing"
)

// groupBoxes creates extensions with a voicemail box each and returns their
// extension ids and box ids.
func groupBoxes(t *testing.T, s *Store, numbers ...string) (exts, boxes []int64) {
	t.Helper()
	ctx := context.Background()
	for _, n := range numbers {
		e := mustExtension(t, s, n)
		if _, err := s.UpdateVoicemailBox(ctx, "test", e.ID, VoicemailBoxChange{}, nil); err != nil {
			t.Fatalf("create box %s: %v", n, err)
		}
		b, ok, err := s.VoicemailBoxByNumber(ctx, n)
		if err != nil || !ok {
			t.Fatalf("box %s: ok=%v err=%v", n, ok, err)
		}
		exts, boxes = append(exts, e.ID), append(boxes, b.ID)
	}
	return exts, boxes
}

// TestVoicemailGroupDelivery fails if a message for a group is not one row per
// member box over one object, if deleting one member's row removes the audio
// while another row still references it, or if the last row does not release it.
func TestVoicemailGroupDelivery(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	exts, boxes := groupBoxes(t, s, "101", "102", "103")
	g, err := s.CreateVoicemailGroup(ctx, "test", VoicemailGroupInput{
		Number: "650", Name: "Sales", Enabled: true, MaxSeconds: 180, Members: exts[:2]}, nil)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if len(g.Members) != 2 {
		t.Fatalf("members = %+v", g.Members)
	}

	// A box that is not there is skipped; the others keep theirs.
	got, err := s.InsertVoicemailGroupMessage(ctx, append([]int64{999999}, boxes[:2]...), "box/1/1-a.wav", "103", "Jan", 8000)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("delivered to %v, want the two real boxes", got)
	}
	var ids []int64
	for _, b := range boxes[:2] {
		ms, err := s.ListVoicemailMessages(ctx, b, false)
		if err != nil || len(ms) != 1 || ms[0].Object != "box/1/1-a.wav" || ms[0].Caller != "103" {
			t.Fatalf("box %d messages = %+v, %v", b, ms, err)
		}
		ids = append(ids, ms[0].ID)
	}

	// The first member deletes its copy through the phone path: the audio stays.
	obj, err := s.RemoveVoicemailMessage(ctx, boxes[0], ids[0])
	if err != nil || obj != "" {
		t.Fatalf("remove first copy = %q, %v; want the object kept", obj, err)
	}
	// The second deletes through the console: the last reference releases it.
	obj, err = s.DeleteVoicemailMessage(ctx, "test", ids[1])
	if err != nil || obj != "box/1/1-a.wav" {
		t.Fatalf("delete last copy = %q, %v; want the object released", obj, err)
	}

	// A message not shared is released at once.
	id, err := s.InsertVoicemailMessage(ctx, boxes[2], "box/3/1-b.wav", "101", "", 5000)
	if err != nil {
		t.Fatal(err)
	}
	if obj, err = s.DeleteVoicemailMessage(ctx, "test", id); err != nil || obj != "box/3/1-b.wav" {
		t.Fatalf("delete unshared = %q, %v", obj, err)
	}
}

// TestVoicemailGroupRules fails if a number that is
// another object's, a number in use by a destination, or too many members is
// accepted.
func TestVoicemailGroupRules(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	exts, _ := groupBoxes(t, s, "101", "102")
	in := func(number, name string, members ...int64) VoicemailGroupInput {
		return VoicemailGroupInput{Number: number, Name: name, Enabled: true, MaxSeconds: 180, Members: members}
	}
	var ve *ValidationError
	if _, err := s.CreateVoicemailGroup(ctx, "test", in("101", "A", exts[0]), nil); !errors.As(err, &ve) {
		t.Fatalf("number of an extension: %v, want a validation error", err)
	}
	g, err := s.CreateVoicemailGroup(ctx, "test", in("650", "A", exts...), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateVoicemailGroup(ctx, "test", in("650", "B", exts[0]), nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate number: %v, want a conflict", err)
	}
	if _, err := s.CreateExtension(ctx, "test", "650", "Clash", "", nil); !errors.As(err, &ve) {
		t.Fatalf("extension on the group's number: %v, want a validation error", err)
	}
	many := make([]int64, MaxVoicemailGroupMembers+1)
	for i := range many {
		many[i] = exts[0]
	}
	if _, err := s.UpdateVoicemailGroup(ctx, "test", g.ID, in("650", "A", many...), nil); err == nil {
		t.Fatal("more than the member limit accepted")
	}

	// Deleting a member's extension leaves the group with one member.
	if err := s.DeleteExtension(ctx, "test", exts[1], nil); err != nil {
		t.Fatal(err)
	}
	if g, err = s.GetVoicemailGroup(ctx, g.ID); err != nil || len(g.Members) != 1 {
		t.Fatalf("group after deleting a member = %+v, %v", g, err)
	}
	if err := s.DeleteVoicemailGroup(ctx, "test", g.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetVoicemailGroup(ctx, g.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted group: %v, want not found", err)
	}
}
