package snapshot

import (
	"strconv"
	"testing"
)

// TestBLFList fails if a device gets another device's keys, a device without
// a provisioned phone gets a list, or a list is not capped at BLFListMax.
func TestBLFList(t *testing.T) {
	long := make([]string, BLFListMax+20)
	for i := range long {
		long[i] = strconv.Itoa(1000 + i)
	}
	s := New(1, "d", nil).WithBLFLists(map[string][]string{"a": {"100", "101"}, "b": {}, "big": long})
	if got, ok := s.BLFList("a"); !ok || len(got) != 2 || got[0] != "100" {
		t.Fatalf("a = %v, %v", got, ok)
	}
	if got, ok := s.BLFList("b"); !ok || len(got) != 0 {
		t.Fatalf("b = %v, %v", got, ok)
	}
	if _, ok := s.BLFList("nobody"); ok {
		t.Fatal("a device without a phone has a list")
	}
	if got, _ := s.BLFList("big"); len(got) != BLFListMax {
		t.Fatalf("big = %d members, want %d", len(got), BLFListMax)
	}
}
