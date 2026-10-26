package livestate

import (
	"context"
	"testing"
	"time"
)

// TestConferenceOwnership fails if two nodes can own a room at once, if the
// owner cannot renew, if a dead owner's lease cannot be taken atomically (and
// only from the node the caller saw), or if a released lease stays.
func TestConferenceOwnership(t *testing.T) {
	s, ctx := store(t), context.Background()
	key := ConfOwnerKey("800")
	if ok, err := s.AcquireLease(ctx, key, "sip-1", ConfOwnerTTL); err != nil || !ok {
		t.Fatalf("first node = %v, %v; want the lease", ok, err)
	}
	if ok, _ := s.AcquireLease(ctx, key, "sip-2", ConfOwnerTTL); ok {
		t.Fatal("a second node took a held lease")
	}
	if ok, _ := s.AcquireLease(ctx, key, "sip-1", ConfOwnerTTL); !ok {
		t.Fatal("the owner could not renew")
	}
	if o, err := s.ConfOwner(ctx, "800"); err != nil || o != "sip-1" {
		t.Fatalf("owner = %q, %v", o, err)
	}
	if ok, _ := s.TakeConfOwner(ctx, "800", "sip-3", "sip-2", ConfOwnerTTL); ok {
		t.Fatal("a lease was taken from a node that does not hold it")
	}
	if ok, err := s.TakeConfOwner(ctx, "800", "sip-1", "sip-2", ConfOwnerTTL); err != nil || !ok {
		t.Fatalf("take from the named owner = %v, %v", ok, err)
	}
	if ok, _ := s.AcquireLease(ctx, key, "sip-1", ConfOwnerTTL); ok {
		t.Fatal("the old owner renewed a lease it lost")
	}
	if err := s.ReleaseLease(ctx, key, "sip-1"); err != nil {
		t.Fatal(err)
	}
	if o, _ := s.ConfOwner(ctx, "800"); o != "sip-2" {
		t.Fatalf("a non-owner's release dropped the lease: owner %q", o)
	}
	if err := s.ReleaseLease(ctx, key, "sip-2"); err != nil {
		t.Fatal(err)
	}
	if o, _ := s.ConfOwner(ctx, "800"); o != "" {
		t.Fatalf("owner after release = %q", o)
	}
	short := "hello:conf:owner:short"
	if ok, _ := s.AcquireLease(ctx, short, "sip-1", 300*time.Millisecond); !ok {
		t.Fatal("short lease refused")
	}
	time.Sleep(450 * time.Millisecond)
	if ok, _ := s.AcquireLease(ctx, short, "sip-2", ConfOwnerTTL); !ok {
		t.Fatal("an expired lease was not free")
	}
}

// TestConferenceStateAndCommands fails if a published room state does not
// read back, expires on its own, or is not removed; or if control commands
// are not delivered once, in order, to their own node only.
func TestConferenceStateAndCommands(t *testing.T) {
	s, ctx := store(t), context.Background()
	st := ConfRoomState{Room: "800", Node: "sip-1", Locked: true, Participants: []ConfParticipant{{ID: "a", Number: "101", Muted: true}}}
	if err := s.PutConfState(ctx, st); err != nil {
		t.Fatal(err)
	}
	got, err := s.ConfStates(ctx, []string{"800", "801"})
	if err != nil || len(got) != 1 || !got["800"].Locked || got["800"].Participants[0].ID != "a" || got["800"].UpdatedAt.IsZero() {
		t.Fatalf("states = %+v, %v", got, err)
	}
	if err := s.DeleteConfState(ctx, "800"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ConfStates(ctx, []string{"800"}); len(got) != 0 {
		t.Fatalf("a deleted state read back: %+v", got)
	}
	if got, err := s.ConfStates(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("no rooms = %+v, %v", got, err)
	}

	for i, a := range []string{"mute", "kick"} {
		c := ConfCommand{ID: string(rune('1' + i)), Room: "800", Action: a, Participant: "a", Actor: "user:x", At: time.Now()}
		if err := s.EnqueueConfCommand(ctx, "sip-1", c); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EnqueueConfCommand(ctx, "sip-2", ConfCommand{ID: "9", Room: "801", Action: "lock", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	cmds, err := s.TakeConfCommands(ctx, "sip-1", 10)
	if err != nil || len(cmds) != 2 || cmds[0].Action != "mute" || cmds[1].Action != "kick" {
		t.Fatalf("sip-1 commands = %+v, %v", cmds, err)
	}
	if again, _ := s.TakeConfCommands(ctx, "sip-1", 10); len(again) != 0 {
		t.Fatalf("commands delivered twice: %+v", again)
	}
	if other, _ := s.TakeConfCommands(ctx, "sip-2", 10); len(other) != 1 || other[0].ID != "9" {
		t.Fatalf("sip-2 commands = %+v", other)
	}
	// A command older than its lifetime is dropped.
	_ = s.EnqueueConfCommand(ctx, "sip-1", ConfCommand{ID: "old", Room: "800", Action: "end", At: time.Now().Add(-2 * ConfCommandTTL)})
	if old, _ := s.TakeConfCommands(ctx, "sip-1", 10); len(old) != 0 {
		t.Fatalf("a stale command was delivered: %+v", old)
	}
}
