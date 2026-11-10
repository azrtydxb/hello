package api

import (
	"testing"

	"github.com/azrtydxb/hello/internal/store"
)

// TestValidateExtensionKind fails if an extension kind other than user or
// door is accepted, if a user extension may carry door targets, or if a
// station may list more targets than the limit (spec S-45).
func TestValidateExtensionKind(t *testing.T) {
	many := store.DoorTargets{Extensions: make([]int64, store.MaxDoorTargets+1)}
	for _, tc := range []struct {
		name    string
		kind    string
		targets *store.DoorTargets
		field   string
	}{
		{"user", "user", nil, ""},
		{"door", "door", &store.DoorTargets{Extensions: []int64{1}}, ""},
		{"unknown kind", "gate", nil, "kind"},
		{"targets on a user", "user", &store.DoorTargets{RingGroups: []int64{3}}, "doorTargets"},
		{"empty targets on a user", "user", &store.DoorTargets{}, ""},
		{"too many targets", "door", &many, "doorTargets"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var f fieldErrs
			validateKind(&f, tc.kind, tc.targets)
			switch {
			case tc.field == "" && len(f) != 0:
				t.Errorf("unexpected errors %v", f)
			case tc.field != "" && len(f) == 0:
				t.Errorf("no error on %s", tc.field)
			}
		})
	}
}
