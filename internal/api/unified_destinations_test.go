package api

import (
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/store"
)

func hasField(f fieldErrs, path string) bool {
	for _, e := range f {
		if e.Path == path {
			return true
		}
	}
	return false
}

// TestInboundUnifiedDestinations fails if an inbound route cannot name a ring
// group, a voicemail box, an announcement or a paging group (migration 00041),
// or if a malformed target for one of those kinds is accepted.
func TestInboundUnifiedDestinations(t *testing.T) {
	ok := []struct{ kind, dest string }{
		{"ring_group", "sales"}, {"ring_group", "200"}, {"voicemail", "101"}, {"announcement", "closed"}, {"paging", "*88"},
	}
	for _, c := range ok {
		in := store.InboundRoute{Name: "r", DIDKind: "any", DestinationKind: c.kind, Destination: c.dest, Enabled: true}
		if f := validateInbound(&in); hasField(f, "destination") || hasField(f, "destinationKind") {
			t.Errorf("%s %q refused: %v", c.kind, c.dest, f)
		}
	}
	bad := []struct{ kind, dest string }{
		{"ring_group", "not a name"}, {"ring_group", ""}, {"voicemail", "abc"}, {"announcement", ""}, {"paging", "88"}, {"paging", "*8"},
	}
	for _, c := range bad {
		in := store.InboundRoute{Name: "r", DIDKind: "any", DestinationKind: c.kind, Destination: c.dest, Enabled: true}
		if f := validateInbound(&in); !hasField(f, "destination") {
			t.Errorf("%s %q accepted", c.kind, c.dest)
		}
	}
	in := store.InboundRoute{Name: "r", DIDKind: "any", DestinationKind: "park", Destination: "x", Enabled: true}
	f := validateInbound(&in)
	if !hasField(f, "destinationKind") {
		t.Fatal("an unknown kind was accepted")
	}
	for _, k := range []string{"ring_group", "voicemail", "announcement", "paging"} {
		if !strings.Contains(f[0].Message, k) {
			t.Errorf("the refusal does not list %s: %s", k, f[0].Message)
		}
	}
}
