package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// numberKinds lists every object that answers to a plain dialled number. Each
// entry creates one on number, and moves an existing one (made on a spare
// number first) onto number, so the table can drive both directions of every
// pair.
type numberKind struct {
	name   string
	field  string // the validation path the refusal names
	create func(t *testing.T, s *Store, number string) error
	// move makes an object on spare and renumbers it onto number.
	move func(t *testing.T, s *Store, spare, number string) error
}

func numberKinds() []numberKind {
	ctx := context.Background()
	return []numberKind{
		{"extension", "number",
			func(t *testing.T, s *Store, n string) error {
				_, err := s.CreateExtension(ctx, "t", n, "Ext "+n, "", nil)
				return err
			},
			func(t *testing.T, s *Store, spare, n string) error {
				e, err := s.CreateExtension(ctx, "t", spare, "Spare", "", nil)
				if err != nil {
					t.Fatal(err)
				}
				_, err = s.UpdateExtension(ctx, "t", e.ID, ExtensionChange{Number: &n}, nil)
				return err
			}},
		{"ring group", "name",
			func(t *testing.T, s *Store, n string) error {
				_, err := s.CreateRingGroup(ctx, "t", RingGroupInput{Name: n, Strategy: "ring-all", RingTimeout: 30, FailureKind: "none"}, nil)
				return err
			},
			func(t *testing.T, s *Store, spare, n string) error {
				g, err := s.CreateRingGroup(ctx, "t", RingGroupInput{Name: "g" + spare, Strategy: "ring-all", RingTimeout: 30, FailureKind: "none"}, nil)
				if err != nil {
					t.Fatal(err)
				}
				_, err = s.UpdateRingGroup(ctx, "t", g.ID, RingGroupInput{Name: n, Strategy: "ring-all", RingTimeout: 30, FailureKind: "none"}, nil)
				return err
			}},
		{"IVR menu", "number",
			func(t *testing.T, s *Store, n string) error {
				in := ivrInput("m"+n, ivrGreeting(t, s))
				in.Number = n
				_, err := s.CreateIVRMenu(ctx, "t", in, nil)
				return err
			},
			func(t *testing.T, s *Store, spare, n string) error {
				in := ivrInput("m"+spare, ivrGreeting(t, s))
				in.Number = spare
				m, err := s.CreateIVRMenu(ctx, "t", in, nil)
				if err != nil {
					t.Fatal(err)
				}
				in.Number = n
				_, err = s.UpdateIVRMenu(ctx, "t", m.ID, in, nil)
				return err
			}},
		{"conference room", "number",
			func(t *testing.T, s *Store, n string) error {
				_, err := s.CreateConference(ctx, "t", ConferenceInput{Name: "r" + n, Number: n, MaxParticipants: 10,
					EntryExit: ConferenceSoundTone, Enabled: true}, PINChange{}, PINChange{}, nil)
				return err
			},
			func(t *testing.T, s *Store, spare, n string) error {
				c, err := s.CreateConference(ctx, "t", ConferenceInput{Name: "r" + spare, Number: spare, MaxParticipants: 10,
					EntryExit: ConferenceSoundTone, Enabled: true}, PINChange{}, PINChange{}, nil)
				if err != nil {
					t.Fatal(err)
				}
				_, err = s.UpdateConference(ctx, "t", c.ID, ConferenceInput{Name: "r" + spare, Number: n, MaxParticipants: 10,
					EntryExit: ConferenceSoundTone, Enabled: true}, PINChange{}, PINChange{}, nil)
				return err
			}},
		{"queue", "number",
			func(t *testing.T, s *Store, n string) error {
				_, err := s.CreateQueue(ctx, "t", queueInput("q"+n, n), nil)
				return err
			},
			func(t *testing.T, s *Store, spare, n string) error {
				q, err := s.CreateQueue(ctx, "t", queueInput("q"+spare, spare), nil)
				if err != nil {
					t.Fatal(err)
				}
				_, err = s.UpdateQueue(ctx, "t", q.ID, queueInput("q"+spare, n), nil)
				return err
			}},
	}
}

// ivrGreeting returns the id of a greeting for a menu, creating it once.
func ivrGreeting(t *testing.T, s *Store) int64 {
	t.Helper()
	ctx := context.Background()
	as, err := s.ListAnnouncements(ctx)
	if err == nil && len(as) > 0 {
		return as[0].ID
	}
	a, err := s.CreateAnnouncement(ctx, "t", "greet", "ann/greet.wav", nil)
	if err != nil {
		t.Fatal(err)
	}
	return a.ID
}

// TestDialNumbersAreExclusive: extensions, ring groups, IVR menus, conference
// rooms and queues are all dialled as a plain number, so no two of them (nor
// a park slot) may share one. For every ordered pair it creates the first,
// then expects the second to be refused on its number field both when created
// and when moved onto the number, and the same on a park lot's slots. It
// fails if any pair's check is missing.
func TestDialNumbersAreExclusive(t *testing.T) {
	kinds := numberKinds()
	for i, a := range kinds {
		for j, b := range kinds {
			if i == j {
				continue // a duplicate within a kind is the table's unique index
			}
			base := 300 + (i*len(kinds)+j)*10
			taken := fmt.Sprint(base)
			t.Run(a.name+" then "+b.name+" create", func(t *testing.T) {
				s := scratchStore(t)
				if err := a.create(t, s, taken); err != nil {
					t.Fatal(err)
				}
				assertNumberRefused(t, b.create(t, s, taken), b.field)
			})
			t.Run(a.name+" then "+b.name+" renumber", func(t *testing.T) {
				s := scratchStore(t)
				if err := a.create(t, s, taken); err != nil {
					t.Fatal(err)
				}
				assertNumberRefused(t, b.move(t, s, fmt.Sprint(base+1), taken), b.field)
			})
		}
	}
}

// TestDialNumbersVersusParkSlots: every kind refuses a park slot's number
// (default lot 701-720), and a park lot refuses a range over any kind's
// number.
func TestDialNumbersVersusParkSlots(t *testing.T) {
	for i, k := range numberKinds() {
		t.Run(k.name+" on a slot", func(t *testing.T) {
			s := scratchStore(t)
			assertNumberRefused(t, k.create(t, s, "705"), k.field)
		})
		t.Run(k.name+" renumbered onto a slot", func(t *testing.T) {
			s := scratchStore(t)
			assertNumberRefused(t, k.move(t, s, "650", "706"), k.field)
		})
		t.Run("park lot over a "+k.name, func(t *testing.T) {
			s := scratchStore(t)
			n := 900 + i*10
			if err := k.create(t, s, fmt.Sprint(n)); err != nil {
				t.Fatal(err)
			}
			_, err := s.CreateParkLot(context.Background(), "t",
				ParkLotInput{Name: "upstairs", SlotStart: n - 1, SlotEnd: n + 1, ReturnSecs: 120, VoicemailSecs: 60, Enabled: true}, nil)
			assertNumberRefused(t, err, "slotStart")
		})
	}
}

func assertNumberRefused(t *testing.T, err error, field string) {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) || len(ve.Fields) == 0 || ve.Fields[0].Path != field {
		t.Fatalf("got %v, want a %s validation error", err, field)
	}
}
