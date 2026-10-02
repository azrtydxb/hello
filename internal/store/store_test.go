package store

import (
	"fmt"
	"testing"

	"github.com/azrtydxb/hello/internal/routing"
)

// TestRelativePaths fails if the changed item's FieldErrors keep their
// configuration prefix, or if another item's errors lose theirs.
func TestRelativePaths(t *testing.T) {
	in := []routing.FieldError{
		{Path: "outbound[2].numberTransform.template"},
		{Path: "outbound[2]"},
		{Path: "outbound[21].match"},
		{Path: "trunks[0].name"},
	}
	got := fmt.Sprint(relative(in, "outbound[2]"))
	if want := "[{numberTransform.template } {outbound[2] } {outbound[21].match } {trunks[0].name }]"; got != want {
		t.Fatalf("relative = %s, want %s", got, want)
	}
	if fmt.Sprint(relative(in, "")) != fmt.Sprint(in) {
		t.Fatal("an empty prefix changed the paths")
	}
}
