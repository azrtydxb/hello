package livestate

import (
	"testing"

	"github.com/azrtydxb/hello/internal/routing"
)

// TestTrunkUsability fails if the shared usability rule gives a result
// other than this table.
func TestTrunkUsability(t *testing.T) {
	a := routing.Destination{Host: "10.0.0.1", Port: 5060}
	b := routing.Destination{Host: "sip.carrier.test", Port: 0}
	trunk := routing.Trunk{ID: 1, Name: "c", Enabled: true, MaxCalls: 2, Destinations: []routing.Destination{a, b}}
	up := func(d routing.Destination, ok bool) DestinationHealth {
		return DestinationHealth{Destination: DestinationKey(d), Up: ok}
	}
	disabled := trunk
	disabled.Enabled = false
	unlimited := trunk
	unlimited.MaxCalls = 0
	for _, tc := range []struct {
		name      string
		t         routing.Trunk
		s         TrunkStatus
		emergency bool
		ok        bool
		reason    string
	}{
		{"nothing known", trunk, TrunkStatus{}, false, true, ""},
		{"disabled", disabled, TrunkStatus{}, false, false, "disabled"},
		{"misconfigured", trunk, TrunkStatus{Registration: &TrunkRegistration{State: "misconfigured"}}, false, false, "misconfigured"},
		{"registration failed is not health", trunk, TrunkStatus{Registration: &TrunkRegistration{State: "failed"}}, false, true, ""},
		{"one down one up", trunk, TrunkStatus{Destinations: []DestinationHealth{up(a, false), up(b, true)}}, false, true, ""},
		{"one down one unknown", trunk, TrunkStatus{Destinations: []DestinationHealth{up(a, false)}}, false, true, ""},
		{"all down", trunk, TrunkStatus{Destinations: []DestinationHealth{up(a, false), up(b, false)}}, false, false, "unhealthy"},
		{"all down, emergency", trunk, TrunkStatus{Destinations: []DestinationHealth{up(a, false), up(b, false)}}, true, false, "unhealthy"},
		{"full", trunk, TrunkStatus{ActiveCalls: 2}, false, false, "full"},
		{"full, emergency", trunk, TrunkStatus{ActiveCalls: 2}, true, true, ""},
		{"below the limit", trunk, TrunkStatus{ActiveCalls: 1}, false, true, ""},
		{"unlimited", unlimited, TrunkStatus{ActiveCalls: 99}, false, true, ""},
		{"stale foreign destination ignored", trunk, TrunkStatus{Destinations: []DestinationHealth{{Destination: "old:5060"}}}, false, true, ""},
	} {
		ok, reason := tc.s.Usability(tc.t, tc.emergency)
		if ok != tc.ok || reason != tc.reason {
			t.Errorf("%s: (%v, %q), want (%v, %q)", tc.name, ok, reason, tc.ok, tc.reason)
		}
	}
	if DestinationKey(b) != "sip.carrier.test:0" {
		t.Fatalf("DestinationKey = %s", DestinationKey(b))
	}
}
