package webhook

import (
	"os"
	"regexp"
	"testing"

	"github.com/azrtydxb/hello/internal/events"
)

// TestZapierTriggersUseRealEvents fails if a trigger of the Zapier app
// (integrations/zapier, spec integrations I-52) names an event type Hello
// does not emit, or if one of the five call types the spec lists or one of
// the inbox types (spec messaging-channels M-23) has no trigger. A trigger on
// a mistyped type would subscribe a webhook that Hello rejects.
func TestZapierTriggersUseRealEvents(t *testing.T) {
	src, err := os.ReadFile("../../integrations/zapier/triggers/index.js")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, m := range regexp.MustCompile(`type: ["']([^"']+)["']`).FindAllStringSubmatch(string(src), -1) {
		got[m[1]] = true
		if _, err := events.ParseTypes(m[1]); err != nil {
			t.Errorf("trigger type %q: %v", m[1], err)
		}
	}
	want := []string{"call.ringing", "call.answered", "call.missed", "call.completed", "call.recording.ready"}
	for _, ti := range events.Registry {
		if events.IsInboxType(ti.Type) {
			want = append(want, ti.Type)
		}
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("no Zapier trigger for %s", w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("triggers = %v, want exactly %v", got, want)
	}
}
