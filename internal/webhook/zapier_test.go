package webhook

import (
	"os"
	"regexp"
	"testing"

	"github.com/azrtydxb/hello/internal/events"
)

// TestZapierTriggersUseRealEvents fails if a trigger of the Zapier app
// (integrations/zapier, spec integrations I-52) names an event type Hello
// does not emit, or if one of the five types the spec lists has no trigger.
// A trigger on a mistyped type would subscribe a webhook that Hello rejects.
func TestZapierTriggersUseRealEvents(t *testing.T) {
	src, err := os.ReadFile("../../integrations/zapier/triggers/index.js")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, m := range regexp.MustCompile(`type: '([^']+)'`).FindAllStringSubmatch(string(src), -1) {
		got[m[1]] = true
		if _, err := events.ParseTypes(m[1]); err != nil {
			t.Errorf("trigger type %q: %v", m[1], err)
		}
	}
	for _, want := range []string{"call.ringing", "call.answered", "call.missed", "call.completed", "call.recording.ready"} {
		if !got[want] {
			t.Errorf("no Zapier trigger for %s", want)
		}
	}
	if len(got) != 5 {
		t.Errorf("triggers = %v, want exactly the five of spec I-52", got)
	}
}
