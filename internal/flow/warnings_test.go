package flow

import (
	"slices"
	"testing"
)

func warningSet(g Graph) []string {
	var out []string
	for _, w := range g.Warnings {
		out = append(out, w.Node+" "+w.Code)
	}
	slices.Sort(out)
	return out
}

func mustBuild(t *testing.T, name string) Graph {
	t.Helper()
	in, ok := goldenFixtures()[name]
	if !ok {
		t.Fatalf("no fixture %q", name)
	}
	g, err := Build(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// TestWarningLoop fails if two menus that only lead to each other, or two
// extensions that always forward to each other, are not reported, or if a
// menu whose key returns to itself (and can still hang up) is.
func TestWarningLoop(t *testing.T) {
	got := warningSet(mustBuild(t, "loops_and_shadows"))
	for _, want := range []string{"ivr:1 loop", "ivr:2 loop", "extension:10 loop", "extension:11 loop"} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	if slices.Contains(got, "ivr:3 loop") {
		t.Error("a menu that can hang up was reported as a loop")
	}
	// A cycle with a way out is not a loop.
	if g := warningSet(mustBuild(t, "multilevel_ivr")); slices.Contains(g, "ivr:1 loop") || slices.Contains(g, "ivr:2 loop") {
		t.Errorf("menus that can leave were reported: %v", g)
	}
}

// TestWarningShadowedRoute fails if a route behind a catch-all, or behind an
// any-trunk route, is not reported, or if a route behind a narrower one is.
func TestWarningShadowedRoute(t *testing.T) {
	got := warningSet(mustBuild(t, "loops_and_shadows"))
	for _, want := range []string{"inboundRoute:2 shadowed_route", "inboundRoute:3 shadowed_route"} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	if slices.Contains(got, "inboundRoute:1 shadowed_route") {
		t.Error("the first route cannot be shadowed")
	}
	in := goldenFixtures()["loops_and_shadows"]
	// Give the first route a schedule: it no longer always matches.
	in.Inbound[0].SIPDomain = "pbx.example.com"
	g, _ := Build(in, Options{})
	if slices.Contains(warningSet(g), "inboundRoute:2 shadowed_route") {
		t.Error("a route behind a conditional one was reported")
	}
	// An exact route does not shadow a prefix route, a prefix route shadows a longer one.
	cases := []struct {
		e, r       string
		ek, rk     string
		wantCovers bool
	}{
		{"32", "3225", "prefix", "exact", true},
		{"32", "32", "prefix", "prefix", true},
		{"3225", "32", "prefix", "prefix", false},
		{"32", "32", "exact", "prefix", false},
		{"+32", "32", "exact", "exact", true},
	}
	for _, c := range cases {
		in := goldenFixtures()["loops_and_shadows"]
		e, r := in.Inbound[1], in.Inbound[2]
		e.DID, e.DIDKind, r.DID, r.DIDKind = c.e, c.ek, c.r, c.rk
		e.TrunkID, r.TrunkID = nil, nil
		if routeCovers(e, r) != c.wantCovers {
			t.Errorf("%s %s covers %s %s = %v, want %v", c.ek, c.e, c.rk, c.r, !c.wantCovers, c.wantCovers)
		}
	}
}

// TestWarningNoGreeting fails if a menu without a greeting, or whose greeting
// announcement has no audio, is not reported.
func TestWarningNoGreeting(t *testing.T) {
	got := warningSet(mustBuild(t, "loops_and_shadows"))
	for _, want := range []string{"ivr:1 no_greeting", "ivr:3 no_greeting"} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	if g := warningSet(mustBuild(t, "multilevel_ivr")); slices.Contains(g, "ivr:1 no_greeting") {
		t.Errorf("a menu with a greeting was reported: %v", g)
	}
}

// TestWarningUnreachable fails if a route whose trunk is gone, a ring group
// nobody sends calls to, or a menu with no number and no way in is not
// reported, or if a dialled number counts as a way in.
func TestWarningUnreachable(t *testing.T) {
	got := warningSet(mustBuild(t, "dangling"))
	for _, want := range []string{"inboundRoute:1 unreachable", "ringGroup:1 unreachable"} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	// "support" is not numeric and nothing but an IVR option reaches it.
	if g := warningSet(mustBuild(t, "multilevel_ivr")); slices.Contains(g, "ringGroup:1 unreachable") || slices.Contains(g, "ivr:2 unreachable") {
		t.Errorf("reachable nodes were reported: %v", g)
	}
	// The fully connected configuration has none.
	for _, w := range warningSet(mustBuild(t, "full_feature_set")) {
		t.Errorf("unexpected warning %s", w)
	}
}

// TestGraphNewObjects fails if an object type of a merged feature has no node,
// or its edge is missing its owner field.
func TestGraphNewObjects(t *testing.T) {
	g := mustBuild(t, "full_feature_set")
	kinds := map[string]bool{}
	for _, n := range g.Nodes {
		kinds[n.Kind] = true
	}
	for _, k := range []string{KindIVR, KindQueue, KindTimeCond, KindSchedule, KindHolidayCal, KindMOHClass,
		KindConference, KindParkLot, KindPickupGroup, KindMulticast, KindPagingGroup, KindFeatureCode} {
		if !kinds[k] {
			t.Errorf("no %s node in the full fixture", k)
		}
	}
	owners := map[string]string{}
	for _, e := range g.Edges {
		owners[e.Target.Node+"."+e.Target.Port] = e.Owner.Node + "." + e.Owner.Field
	}
	for dst, owner := range map[string]string{
		"queue:1.holdMusic":              "queue:1.mohClassId",
		"extension:10.holdMusic":         "extension:10.mohClassId",
		"mohClass:1.tracks":              "mohClass:1.announcementIds",
		"schedule:1.calendars":           "schedule:1.calendarIds",
		"timeCondition:1.schedule":       "timeCondition:1.scheduleId",
		"multicastChannel:1.page":        "pagingGroup:1.multicastChannelId",
		"queue:1.periodicPrompt":         "queue:1.periodicPromptId",
		"conference:1.entryAnnouncement": "conference:1.entryAnnouncement",
	} {
		if owners[dst] != owner {
			t.Errorf("edge into %s owned by %q, want %q", dst, owners[dst], owner)
		}
	}
	// The TTS badge and the override badge.
	for _, n := range g.Nodes {
		if n.ID == "announcement:2" && n.Summary["generated"] != true {
			t.Error("generated announcement is not badged")
		}
		if n.ID == "timeCondition:1" && n.Summary["forced"] != true {
			t.Error("a forced time condition is not badged")
		}
	}
	// The queue's agent edge carries its penalty and kind.
	for _, e := range g.Edges {
		if e.ID == "extension:11.member->queue:1.members" && (e.Data["penalty"] != 1 || e.Data["dynamic"] != true) {
			t.Errorf("agent data %v", e.Data)
		}
	}
}
