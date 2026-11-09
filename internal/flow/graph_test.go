package flow

import (
	"errors"
	"slices"
	"testing"

	"github.com/azrtydxb/hello/internal/store"
)

func i64(v int64) *int64 { return &v }

func office() Input {
	return Input{
		Revision: 7,
		Trunks:   []store.Trunk{{ID: 1, Name: "carrier-a", Enabled: true}, {ID: 2, Name: "carrier-b"}},
		Extensions: []store.Extension{
			{ID: 10, Number: "101", Name: "Ann", VoicemailEnabled: true, ForwardNoAnswer: "200"},
			{ID: 11, Number: "102", Name: "Bob", ForwardBusy: "+3225550000"},
			{ID: 12, Number: "103", Name: "Cy", VoicemailEnabled: false, ForwardAlways: "999x"},
		},
		RingGroups: []store.RingGroup{
			{ID: 1, Name: "200", Members: []store.RingGroupMember{{ExtensionID: 10, Position: 1, Weight: 1, Delay: 0}, {ExtensionID: 11, Position: 2}},
				FailureKind: "voicemail", FailureTarget: "103"},
			{ID: 2, Name: "sales", FailureKind: "announcement", FailureTarget: "closed"},
		},
		Announcements: []store.Announcement{{ID: 1, Name: "closed"}},
		PagingGroups:  []store.PagingGroup{{ID: 1, Name: "all", Code: "*88", Members: []store.PagingExtension{{ExtensionID: 10}}, Callers: []store.PagingExtension{{ExtensionID: 11}}}},
		FeatureCodes:  []store.FeatureCode{{Code: "*97", Action: "voicemail"}, {Code: "*60", Action: "announcement", Argument: "gone"}},
		Inbound: []store.InboundRoute{
			{ID: 1, Name: "main", DID: "+3225551111", TrunkID: i64(1), DestinationKind: "extension", Destination: "200", Enabled: true},
			{ID: 2, Name: "any", DID: "5", DestinationKind: "extension", Destination: "555"},
			{ID: 3, Name: "ext", DID: "6", TrunkID: i64(2), DestinationKind: "external", Destination: "0032123"},
			{ID: 4, Name: "int", DID: "7", TrunkID: i64(9), DestinationKind: "internal"},
			{ID: 5, Name: "uri", DID: "8", DestinationKind: "sip_uri", Destination: "sip:x@y"},
		},
		Outbound: []store.OutboundRoute{{ID: 1, Name: "out", Trunks: []int64{2, 1, 77}}},
	}
}

func edgeIDs(g Graph) []string {
	var ids []string
	for _, e := range g.Edges {
		ids = append(ids, e.ID)
	}
	return ids
}

// TestGraphOffice fails if an edge is missing, lacks its owner, or a target
// resolves differently from hello-sip's lookup order (ring group name, then
// extension number).
func TestGraphOffice(t *testing.T) {
	g, err := Build(office(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if g.ConfigRevision != 7 {
		t.Errorf("revision %d", g.ConfigRevision)
	}
	want := []string{
		"anyTrunk:any.inbound->inboundRoute:2.trunk",
		"anyTrunk:any.inbound->inboundRoute:5.trunk",
		"dialPlan:internal.dial->outboundRoute:1.dial",
		"extension:10.forwardNoAnswer->ringGroup:1.ring", // "200" is a ring group name
		"extension:10.member->pagingGroup:1.members",
		"extension:10.member->ringGroup:1.members",
		"extension:11.forwardBusy->external:+3225550000.call",
		"extension:11.member->pagingGroup:1.callers",
		"extension:11.member->ringGroup:1.members",
		"featureCode:*60.target->missing:gone.call",
		"inboundRoute:1.destination->ringGroup:1.ring",
		"inboundRoute:2.destination->missing:555.call",
		"inboundRoute:3.destination->external:0032123.call",
		"inboundRoute:4.destination->internalDid:4.call",
		"inboundRoute:5.destination->sipUri:sip:x@y.call",
		"outboundRoute:1.trunks->trunk:1.carrier",
		"outboundRoute:1.trunks->trunk:2.carrier",
		"ringGroup:1.noAnswer->extension:12.voicemail",
		"ringGroup:2.noAnswer->announcement:1.call",
		"trunk:1.inbound->inboundRoute:1.trunk",
		"trunk:2.inbound->inboundRoute:3.trunk",
	}
	got := edgeIDs(g)
	// forwardAlways "999x" is not a number: a missing node, not an external one.
	want = append(want, "extension:12.forwardAlways->missing:999x.call")
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("edges\n got %v\nwant %v", got, want)
	}
	for _, e := range g.Edges {
		if e.Owner.Node == "" {
			t.Errorf("%s has no owner", e.ID)
		}
		if e.Owner.Field == "" && !e.Implicit {
			t.Errorf("%s has no owner field and is not implicit", e.ID)
		}
	}
	// Outbound trunk order is kept on the edge.
	for _, e := range g.Edges {
		if e.ID == "outboundRoute:1.trunks->trunk:2.carrier" && e.Data["order"] != 1 {
			t.Errorf("trunk 2 order = %v, want 1", e.Data["order"])
		}
		if e.ID == "ringGroup:1.noAnswer->extension:12.voicemail" && e.Owner.Node != "ringGroup:1" {
			t.Errorf("owner %v", e.Owner)
		}
	}
	// No devices unless asked for.
	for _, n := range g.Nodes {
		if n.Kind == KindDevice || n.Kind == KindPhone {
			t.Errorf("unexpected %s", n.ID)
		}
	}
}

func TestGraphWarnings(t *testing.T) {
	g, _ := Build(office(), Options{})
	have := map[string]bool{}
	for _, w := range g.Warnings {
		have[w.Node+" "+w.Code] = true
	}
	for _, k := range []string{
		"inboundRoute:2 dangling_reference",
		"inboundRoute:4 dangling_reference",  // trunk 9
		"outboundRoute:1 dangling_reference", // trunk 77
		"extension:12 dangling_reference",
		"featureCode:*60 dangling_reference",
		"ringGroup:2 empty_group",
		"ringGroup:1 voicemail_disabled_target",
	} {
		if !have[k] {
			t.Errorf("missing warning %q; have %v", k, g.Warnings)
		}
	}
	if have["ringGroup:1 empty_group"] {
		t.Error("ringGroup:1 has members")
	}
}

// TestGraphDeterministic fails if two builds differ, or ids repeat.
func TestGraphDeterministic(t *testing.T) {
	a, _ := Build(office(), Options{Devices: true})
	b, _ := Build(office(), Options{Devices: true})
	if !slices.Equal(edgeIDs(a), edgeIDs(b)) || len(a.Nodes) != len(b.Nodes) {
		t.Fatal("builds differ")
	}
	seen := map[string]bool{}
	for _, n := range a.Nodes {
		if seen[n.ID] {
			t.Errorf("node %s twice", n.ID)
		}
		seen[n.ID] = true
		if n.Version != b.Nodes[0].Version && n.ID == b.Nodes[0].ID {
			t.Errorf("version unstable")
		}
	}
}

// TestNodeVersionChanges fails if editing an object leaves its version alone.
func TestNodeVersionChanges(t *testing.T) {
	in := office()
	a, _ := Build(in, Options{})
	in.RingGroups[0].Strategy = "sequential"
	b, _ := Build(in, Options{})
	va, _ := a.NodeByID("ringGroup:1")
	vb, _ := b.NodeByID("ringGroup:1")
	if va.Version == "" || va.Version == vb.Version {
		t.Fatalf("versions %q %q", va.Version, vb.Version)
	}
	ea, _ := a.NodeByID("extension:10")
	eb, _ := b.NodeByID("extension:10")
	if ea.Version != eb.Version {
		t.Fatal("an untouched object changed version")
	}
}

func TestGraphDevices(t *testing.T) {
	in := office()
	in.Devices = []store.Device{{ID: 5, ExtensionID: 10, SIPUsername: "d5"}}
	in.Phones = []store.Phone{{ID: 3, MAC: "aa", DeviceID: i64(5)}}
	g, _ := Build(in, Options{Devices: true})
	got := edgeIDs(g)
	for _, w := range []string{"device:5.extension->extension:10.devices", "phone:3.device->device:5.phone"} {
		if !slices.Contains(got, w) {
			t.Errorf("missing %s", w)
		}
	}
}

func TestClosure(t *testing.T) {
	g, err := Build(office(), Options{Closure: "trunk:1"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, n := range g.Nodes {
		ids = append(ids, n.ID)
	}
	want := []string{"inboundRoute:1", "ringGroup:1", "extension:12", "missing:999x", "trunk:1"}
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Fatalf("closure nodes %v, want %v", ids, want)
	}
	if _, err := Build(office(), Options{Closure: "trunk:404"}); !errors.Is(err, ErrUnknownNode) {
		t.Fatalf("err %v", err)
	}
}

// TestGraphConference fails if an inbound route to a conference room has no
// edge to the room's node, or if a route to a missing room is not shown as a
// missing target.
func TestGraphConference(t *testing.T) {
	in := Input{
		Revision:    1,
		Conferences: []store.Conference{{ID: 3, Name: "Weekly", Number: "800", MaxParticipants: 10, Enabled: true}},
		Inbound: []store.InboundRoute{
			{ID: 1, Name: "meet", DID: "1", DestinationKind: "conference", Destination: "800", Enabled: true},
			{ID: 2, Name: "ghost", DID: "2", DestinationKind: "conference", Destination: "899", Enabled: true},
		},
	}
	g, err := Build(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ids := edgeIDs(g)
	for _, want := range []string{"inboundRoute:1.destination->conference:3.call", "inboundRoute:2.destination->missing:899.call"} {
		if !slices.Contains(ids, want) {
			t.Errorf("edge %q missing from %v", want, ids)
		}
	}
	var found bool
	for _, n := range g.Nodes {
		found = found || (n.ID == "conference:3" && n.Kind == KindConference)
	}
	if !found {
		t.Error("the room has no node")
	}
}

// TestGraphDirectory: the dial-by-name directory is one node that a route,
// a ring group fallback, an IVR key and a feature code all draw an edge to.
// It fails if the kind is unknown to the builder (a missing node and a
// dangling warning instead).
func TestGraphDirectory(t *testing.T) {
	in := Input{
		Extensions: []store.Extension{{ID: 1, Number: "101", Name: "Ann"}},
		RingGroups: []store.RingGroup{{ID: 1, Name: "sales", FailureKind: "directory",
			Members: []store.RingGroupMember{{ExtensionID: 1, Position: 1}}}},
		FeatureCodes: []store.FeatureCode{{Code: "*42", Action: "directory"}},
		Inbound:      []store.InboundRoute{{ID: 1, Name: "main", DID: "5", DestinationKind: "directory"}},
	}
	g, err := Build(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var nodes int
	for _, n := range g.Nodes {
		if n.Kind == KindDirectory {
			nodes++
		}
	}
	if nodes != 1 {
		t.Fatalf("%d directory nodes, want 1", nodes)
	}
	into := 0
	for _, e := range g.Edges {
		if e.Target.Node == KindDirectory+":names" {
			into++
		}
	}
	if into != 3 {
		t.Fatalf("%d edges into the directory, want 3 (route, group, feature code): %v", into, edgeIDs(g))
	}
	for _, w := range g.Warnings {
		if w.Code == WarnDangling {
			t.Fatalf("dangling warning: %s", w.Message)
		}
	}
}
