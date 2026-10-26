package snapshot

import (
	"slices"
	"testing"
)

// TestPickupScope: an extension may pick up calls ringing the other members
// of its pickup groups; with none, of its ring groups (spec S-50, default
// 4). It fails if the extension itself is in its scope, a stranger is, an
// explicit group does not replace the ring-group default, or CanPickUp
// disagrees with the scope.
func TestPickupScope(t *testing.T) {
	s := New(1, "hello.test", nil).
		WithRingGroups([]RingGroup{{ID: 1, Name: "600"}},
			[][]RingGroupMember{{{Extension: "101"}, {Extension: "102"}, {Extension: "103"}}})

	if got := s.PickupScope("101"); !slices.Equal(got, []string{"102", "103"}) {
		t.Fatalf("ring-group default scope = %v", got)
	}
	if got := s.PickupScope("200"); len(got) != 0 {
		t.Fatalf("a stranger's scope = %v", got)
	}

	s.WithPickupGroups([]PickupGroup{{ID: 1, Name: "front", Members: []string{"101", "201", "202"}}})
	if got := s.PickupScope("101"); !slices.Equal(got, []string{"201", "202"}) {
		t.Fatalf("explicit scope = %v (the ring group no longer applies)", got)
	}
	if got := s.PickupScope("102"); !slices.Equal(got, []string{"101", "103"}) {
		t.Fatalf("scope of a member with no pickup group = %v", got)
	}
	if !s.CanPickUp("101", "201") || s.CanPickUp("101", "102") || s.CanPickUp("101", "101") {
		t.Fatal("CanPickUp disagrees with the scope")
	}
	// Two groups add up.
	s.WithPickupGroups([]PickupGroup{{Name: "a", Members: []string{"101", "201"}}, {Name: "b", Members: []string{"101", "301"}}})
	if got := s.PickupScope("101"); !slices.Equal(got, []string{"201", "301"}) {
		t.Fatalf("scope of two groups = %v", got)
	}
}

// TestParkLots: slots resolve to their enabled lot only, and a number that
// is not a plain slot does not.
func TestParkLots(t *testing.T) {
	s := New(1, "hello.test", nil).WithParkLots([]ParkLot{
		{ID: 2, Name: "up", Start: 801, End: 803}, {ID: 1, Name: "default", Start: 701, End: 720},
	})
	if got := s.ParkLots(); got[0].Name != "default" {
		t.Fatalf("lots = %v, want slot order", got)
	}
	for slot, want := range map[string]string{"701": "default", "720": "default", "802": "up", "804": "", "700": "", "0701": "", "70x": "", "": ""} {
		l, ok := s.ParkLotOf(slot)
		if ok != (want != "") || l.Name != want {
			t.Errorf("ParkLotOf(%q) = %q, %v; want %q", slot, l.Name, ok, want)
		}
	}
	if got := s.ParkLots()[1].Slots(); !slices.Equal(got, []string{"801", "802", "803"}) {
		t.Fatalf("slots = %v", got)
	}
}
