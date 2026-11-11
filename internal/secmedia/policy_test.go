package secmedia

import "testing"

// TestPolicy fails if the three policy names do not parse in any case, if
// anything else is accepted, or if an unknown stored value does not fall back
// to the caller's default.
func TestPolicy(t *testing.T) {
	for in, want := range map[string]Policy{"off": Off, "Optional": Optional, " REQUIRED ": Required} {
		if got, ok := Parse(in); !ok || got != want {
			t.Errorf("Parse(%q) = %q, %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "on", "maybe", "forced"} {
		if Valid(bad) {
			t.Errorf("%q is valid", bad)
		}
	}
	if got := Normalize("", Optional); got != Optional {
		t.Errorf("Normalize empty = %q", got)
	}
	if got := Normalize("off", Optional); got != Off {
		t.Errorf("Normalize off = %q", got)
	}
}
