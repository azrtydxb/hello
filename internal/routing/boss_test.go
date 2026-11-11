package routing

import (
	"strings"
	"testing"
)

// TestCallerIDExtension: an outbound call that names another extension for its
// caller ID (an assistant calling on behalf of a boss) presents that
// extension's external number, while the routes are still matched for the
// extension that dials. It fails if the caller ID stays the dialling
// extension's or the route match follows the boss.
func TestCallerIDExtension(t *testing.T) {
	tbl := mustCompile(t, fixtureConfig())
	// 103 has no external number of its own; 101 has +97142220101.
	own := tbl.Decide(Call{FromExtension: "103", Number: "0501234567", At: at(t, "2026-10-05 10:00")}, nil)
	if own.Kind != KindOutbound || own.CallerID != "+97140000001" {
		t.Fatalf("without the boss: kind %s caller ID %q; want the trunk default +97140000001", own.Kind, own.CallerID)
	}
	d := tbl.Decide(Call{FromExtension: "103", CallerIDExtension: "101", Number: "0501234567", At: at(t, "2026-10-05 10:00")}, nil)
	if d.Kind != KindOutbound || d.CallerID != "+97142220101" {
		t.Fatalf("with the boss: kind %s caller ID %q; want +97142220101", d.Kind, d.CallerID)
	}
	var trace strings.Builder
	for _, step := range d.Trace {
		trace.WriteString(step.Text + "\n")
	}
	if !strings.Contains(trace.String(), cidExt101) {
		t.Fatalf("trace lacks the boss's caller ID rule:\n%s", trace.String())
	}
	// The routes follow the dialling extension: International is limited to 101,
	// so 103 calling as 101 is still refused it.
	intl := tbl.Decide(Call{FromExtension: "103", CallerIDExtension: "101", Number: "0044123456", At: at(t, "2026-10-05 10:00")}, nil)
	if intl.Kind == KindOutbound && intl.Route == "International" {
		t.Fatalf("the route was matched for the boss's extension: %+v", intl)
	}
}
