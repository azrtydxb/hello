package snapshot

import (
	"slices"
	"testing"
)

// TestDoorTargets fails if a door station may call a destination that is not
// on its list, may call another station, or if a door station takes part in
// pickup in either direction.
func TestDoorTargets(t *testing.T) {
	s := New(1, "d", nil).WithExtensionData([]Extension{
		{Number: "100"}, {Number: "200"}, {Number: "300"},
		{Number: "900", Door: true, DoorExtensions: []string{"200", "901"}, DoorGroups: []string{"810"}},
		{Number: "901", Door: true},
	}).WithRingGroups([]RingGroup{
		{Name: "800", IsDoor: true}, {Name: "810"}, {Name: "820"},
	}, [][]RingGroupMember{{{Extension: "200"}, {Extension: "900"}}, {{Extension: "200"}}, {{Extension: "200"}}})
	for _, tc := range []struct {
		dialled string
		want    bool
	}{
		{"200", true}, {"800", true}, {"810", true},
		{"100", false}, {"820", false}, {"901", false}, {"900", false}, {"0501234567", false}, {"*97", false},
	} {
		if got := s.DoorMayCall("900", tc.dialled); got != tc.want {
			t.Errorf("DoorMayCall(900, %q) = %v, want %v", tc.dialled, got, tc.want)
		}
	}
	if s.DoorMayCall("100", "200") {
		t.Error("a user extension is not a door station")
	}
	if got := s.PickupScope("900"); len(got) != 0 {
		t.Errorf("a door station's pickup scope = %v, want none", got)
	}
	if got := s.PickupScope("200"); slices.Contains(got, "900") {
		t.Errorf("pickup scope of 200 = %v contains the door station", got)
	}
}

// TestDoorGroups fails if DoorGroups misses a Door group, lists an ordinary
// one, or is not sorted.
func TestDoorGroups(t *testing.T) {
	s := New(1, "d", nil).WithRingGroups([]RingGroup{
		{Name: "820", IsDoor: true}, {Name: "810"}, {Name: "800", IsDoor: true},
	}, [][]RingGroupMember{nil, nil, nil})
	if got := s.DoorGroups(); !slices.Equal(got, []string{"800", "820"}) {
		t.Errorf("DoorGroups = %v, want [800 820]", got)
	}
}
