package snapshot

import (
	"testing"

	"github.com/azrtydxb/hello/internal/routing"
)

// TestCheckDialing covers the snapshot's judgment of a stored internal
// dialing policy: one that compiles is untouched, one that does not (only
// reachable by writing the column directly) marks the trunk misconfigured
// and drops the policy to "off" so the rest of the configuration still
// compiles — routing skips the trunk either way (trunk plan Task 2).
func TestCheckDialing(t *testing.T) {
	t.Run("a compiling policy is untouched", func(t *testing.T) {
		tr := &routing.Trunk{ID: 1, Name: "carrier", Mode: "ip",
			InternalDialing: routing.Dialing{Mode: "patterns", Patterns: []string{"1XX", "^\\+971"}}}
		if why := checkDialing(tr); why != "" {
			t.Fatalf("checkDialing = %q, want untouched", why)
		}
		if tr.InternalDialing.Mode != "patterns" || len(tr.InternalDialing.Patterns) != 2 {
			t.Fatalf("policy changed: %+v", tr.InternalDialing)
		}
	})
	t.Run("a miscompiling policy marks the trunk misconfigured", func(t *testing.T) {
		tr := &routing.Trunk{ID: 1, Name: "carrier", Mode: "ip",
			InternalDialing: routing.Dialing{Mode: "patterns", Patterns: []string{"1XX", "("}}}
		if why := checkDialing(tr); why == "" {
			t.Fatal("checkDialing = empty, want a misconfigured reason")
		}
		if tr.InternalDialing.Mode != "off" || len(tr.InternalDialing.Patterns) != 0 {
			t.Fatalf("policy not dropped to off: %+v", tr.InternalDialing)
		}
	})
	t.Run("off and all need nothing compiled", func(t *testing.T) {
		for _, mode := range []string{"", "off", "all"} {
			tr := &routing.Trunk{ID: 1, Name: "carrier", Mode: "ip", InternalDialing: routing.Dialing{Mode: mode}}
			if why := checkDialing(tr); why != "" {
				t.Fatalf("mode %q: checkDialing = %q, want untouched", mode, why)
			}
		}
	})
}
