package store

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/azrtydxb/hello/internal/secret"
)

// TestConferenceStore fails if a PIN is stored in clear or does not open under
// its own room and column, if a kept PIN is lost on replace, if equal PINs or
// a number an extension holds are accepted, or if a room an inbound route
// targets can be deleted.
func TestConferenceStore(t *testing.T) {
	st := scratchStore(t)
	box, err := secret.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	st = st.WithSecretBox(box)
	ctx := context.Background()
	in := ConferenceInput{Name: "Weekly", Number: "800", MaxParticipants: 10, EntryExit: ConferenceSoundTone, Enabled: true}
	c, err := st.CreateConference(ctx, "test", in, PINChange{Set: true, Value: "1234"}, PINChange{Set: true, Value: "9876"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !c.HasUserPIN || !c.HasLeaderPIN {
		t.Fatalf("room = %+v, want both PINs set", c)
	}
	var enc []byte
	if err := st.db.QueryRowContext(ctx, `SELECT user_pin_enc FROM conference_rooms WHERE id = $1`, c.ID).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if string(enc) == "1234" || len(enc) < 20 {
		t.Fatal("the PIN is stored in clear")
	}
	if got, err := box.Open(enc, secret.ConferencePINAAD(c.ID, "user")); err != nil || got != "1234" {
		t.Fatalf("PIN does not open under its own AAD: %q, %v", got, err)
	}
	if _, err := box.Open(enc, secret.ConferencePINAAD(c.ID, "leader")); err == nil {
		t.Fatal("the user PIN opened as the leader PIN")
	}
	// Replace keeps a PIN that is not set again and clears one set empty.
	in.Name = "Weekly sync"
	c, err = st.UpdateConference(ctx, "test", c.ID, in, PINChange{}, PINChange{Set: true}, nil)
	if err != nil || !c.HasUserPIN || c.HasLeaderPIN || c.Name != "Weekly sync" {
		t.Fatalf("replaced = %+v, %v", c, err)
	}
	if _, err := st.UpdateConference(ctx, "test", c.ID, in, PINChange{}, PINChange{Set: true, Value: "1234"}, nil); err == nil {
		t.Fatal("a leader PIN equal to the user PIN was accepted")
	}
	// Numbers.
	if _, err := st.CreateExtension(ctx, "test", "100", "Desk", "", nil); err != nil {
		t.Fatal(err)
	}
	clash := in
	clash.Name, clash.Number = "Clash", "100"
	if _, err := st.CreateConference(ctx, "test", clash, PINChange{}, PINChange{}, nil); err == nil {
		t.Fatal("a room took an extension's number")
	}
	if _, err := st.CreateExtension(ctx, "test", "800", "Desk 2", "", nil); err == nil {
		t.Fatal("an extension took a room's number")
	}
	// A route targeting the room keeps it.
	if _, err := st.db.ExecContext(ctx, `INSERT INTO inbound_routes (position, name, did_kind, destination_kind, destination)
		VALUES (1, 'meet', 'any', 'conference', '800')`); err != nil {
		t.Fatal(err)
	}
	var used *InUseError
	if err := st.DeleteConference(ctx, "test", c.ID, nil); !errors.As(err, &used) {
		t.Fatalf("delete of a routed room = %v, want InUseError", err)
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM inbound_routes`); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteConference(ctx, "test", c.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetConference(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted room = %v, want ErrNotFound", err)
	}
}

// TestConferenceNumberDialSpace: a room number is dialled like an
// extension, so it shares the plain-number dial space with extensions, ring
// group names, IVR menu numbers and park slots. It fails if a room can take a
// park slot or an IVR menu's number, or if an extension (created or
// renumbered), a ring group (created or renamed), an IVR menu or a park lot
// can take a room's number.
func TestConferenceNumberDialSpace(t *testing.T) {
	st := scratchStore(t)
	ctx := context.Background()
	var ve *ValidationError
	refused := func(what string, err error, path string) {
		t.Helper()
		if !errors.As(err, &ve) || ve.Fields[0].Path != path {
			t.Errorf("%s: %v, want a %s validation error", what, err, path)
		}
	}
	room := func(name, number string) (Conference, error) {
		return st.CreateConference(ctx, "t", ConferenceInput{Name: name, Number: number, MaxParticipants: 10,
			EntryExit: ConferenceSoundTone, Enabled: true}, PINChange{}, PINChange{}, nil)
	}
	// The default park lot holds 701-720.
	_, err := room("slot", "705")
	refused("room on a park slot", err, "number")
	greet, err := st.CreateAnnouncement(ctx, "t", "greet", "ann/greet.wav", nil)
	if err != nil {
		t.Fatal(err)
	}
	menu := ivrInput("main", greet.ID)
	menu.Number = "850"
	if _, err := st.CreateIVRMenu(ctx, "t", menu, nil); err != nil {
		t.Fatal(err)
	}
	_, err = room("menu", "850")
	refused("room on an IVR number", err, "number")

	if _, err := room("weekly", "800"); err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateExtension(ctx, "t", "800", "Clash", "", nil)
	refused("create extension", err, "number")
	e, err := st.CreateExtension(ctx, "t", "801", "Fine", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	n := "800"
	_, err = st.UpdateExtension(ctx, "t", e.ID, ExtensionChange{Number: &n}, nil)
	refused("renumber extension", err, "number")
	_, err = st.CreateRingGroup(ctx, "t", RingGroupInput{Name: "800", Strategy: "ring-all", RingTimeout: 30, FailureKind: "none"}, nil)
	refused("create ring group", err, "name")
	g, err := st.CreateRingGroup(ctx, "t", RingGroupInput{Name: "sales", Strategy: "ring-all", RingTimeout: 30, FailureKind: "none"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpdateRingGroup(ctx, "t", g.ID, RingGroupInput{Name: "800", Strategy: "ring-all", RingTimeout: 30, FailureKind: "none"}, nil)
	refused("rename ring group", err, "name")
	menu2 := ivrInput("second", greet.ID)
	menu2.Number = "800"
	_, err = st.CreateIVRMenu(ctx, "t", menu2, nil)
	refused("IVR menu on a room", err, "number")
	_, err = st.CreateParkLot(ctx, "t", ParkLotInput{Name: "upstairs", SlotStart: 795, SlotEnd: 805, ReturnSecs: 120, VoicemailSecs: 60, Enabled: true}, nil)
	if !errors.As(err, &ve) {
		t.Errorf("park lot over room 800: %v, want a validation error", err)
	}
}
