package flow

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/internal/timecond"
)

var update = flag.Bool("update", false, "rewrite the golden graph files")

func s64(v int64) *int64 { return &v }

// goldenFixtures are the configurations whose graphs are stored under
// testdata/. Each one is a fixed Input, so a change to the builder shows as
// a diff of the stored JSON.
func goldenFixtures() map[string]Input {
	ann := func(id int64, name string) store.Announcement {
		return store.Announcement{ID: id, Name: name, Object: "a/" + name + ".wav"}
	}
	ext := func(id int64, num string, vm bool) store.Extension {
		return store.Extension{ID: id, Number: num, Name: "E" + num, VoicemailEnabled: vm}
	}
	fixtures := map[string]Input{}

	fixtures["simple_office"] = Input{
		Revision: 3,
		Trunks:   []store.Trunk{{ID: 1, Name: "carrier", Mode: "ip", Enabled: true}},
		Extensions: []store.Extension{
			ext(10, "101", true), ext(11, "102", true), {ID: 12, Number: "103", Name: "E103", ForwardNoAnswer: "102"},
		},
		RingGroups: []store.RingGroup{{ID: 1, Name: "sales", Strategy: "ring-all", RingTimeout: 20,
			Members:     []store.RingGroupMember{{ExtensionID: 10, Position: 1, Weight: 1}, {ExtensionID: 11, Position: 2, Weight: 1, Delay: 5}},
			FailureKind: "voicemail", FailureTarget: "101"}},
		Inbound: []store.InboundRoute{
			{ID: 1, Name: "main", DIDKind: "exact", DID: "+3225551111", TrunkID: s64(1), DestinationKind: "ring_group", Destination: "sales", Enabled: true},
		},
		Outbound: []store.OutboundRoute{{ID: 1, Name: "out", MatchKind: "prefix", Match: "0", Trunks: []int64{1}, Enabled: true}},
	}

	fixtures["multilevel_ivr"] = Input{
		Revision:      4,
		Trunks:        []store.Trunk{{ID: 1, Name: "carrier", Mode: "ip", Enabled: true}},
		Extensions:    []store.Extension{ext(10, "101", true), ext(11, "102", false)},
		Announcements: []store.Announcement{ann(1, "welcome"), ann(2, "sales-menu"), ann(3, "bad-key")},
		RingGroups: []store.RingGroup{{ID: 1, Name: "support", Strategy: "ring-all",
			Members: []store.RingGroupMember{{ExtensionID: 10, Position: 1, Weight: 1}}, FailureKind: "ivr", FailureTarget: "main"}},
		IVRMenus: []store.IVRMenu{
			{ID: 1, Name: "main", Number: "800", Enabled: true, GreetingID: 1, InvalidPromptID: s64(3), DigitTimeout: 5, MaxRetries: 3,
				Timeout: store.IVRDestination{Kind: "hangup"}, Invalid: store.IVRDestination{Kind: "ring_group", Target: "support"},
				Options: []store.IVROption{
					{Digit: "1", Kind: "ivr", Target: "sales"}, {Digit: "2", Kind: "ring_group", Target: "support"},
					{Digit: "9", Kind: "voicemail", Target: "102"}, {Digit: "0", Kind: "extension", Target: "101"},
				}},
			{ID: 2, Name: "sales", Enabled: true, GreetingID: 2, DigitTimeout: 5,
				Timeout: store.IVRDestination{Kind: "ivr", Target: "main"}, Invalid: store.IVRDestination{Kind: "hangup"},
				Options: []store.IVROption{{Digit: "1", Kind: "extension", Target: "101"}, {Digit: "*", Kind: "ivr", Target: "main"}}},
		},
		Inbound: []store.InboundRoute{
			{ID: 1, Name: "main", DIDKind: "any", TrunkID: s64(1), DestinationKind: "ivr", Destination: "main", Enabled: true},
		},
	}

	fixtures["dangling"] = Input{
		Revision:   5,
		Extensions: []store.Extension{ext(10, "101", false)},
		RingGroups: []store.RingGroup{{ID: 1, Name: "empty", Strategy: "ring-all", FailureKind: "queue", FailureTarget: "gone"}},
		Inbound: []store.InboundRoute{
			{ID: 1, Name: "lost", DIDKind: "exact", DID: "1", TrunkID: s64(9), DestinationKind: "announcement", Destination: "nope", Enabled: true},
			{ID: 2, Name: "to-vm", DIDKind: "exact", DID: "2", DestinationKind: "voicemail", Destination: "101", Enabled: true},
		},
		FeatureCodes: []store.FeatureCode{{Code: "*60", Action: "ivr", Argument: "nowhere"}},
	}

	fixtures["ring_group_names"] = Input{
		Revision:   6,
		Extensions: []store.Extension{ext(10, "101", true), {ID: 11, Number: "102", Name: "E102", ForwardAlways: "200"}, {ID: 12, Number: "103", Name: "E103", ForwardAlways: "sales"}},
		RingGroups: []store.RingGroup{
			{ID: 1, Name: "200", Strategy: "ring-all", Members: []store.RingGroupMember{{ExtensionID: 10, Position: 1, Weight: 1}}},
			{ID: 2, Name: "sales", Strategy: "ring-all", Members: []store.RingGroupMember{{ExtensionID: 10, Position: 1, Weight: 1}}},
		},
		Inbound: []store.InboundRoute{
			// The older way: kind extension naming a numeric group, and the direct way.
			{ID: 1, Name: "legacy", DIDKind: "exact", DID: "1", DestinationKind: "extension", Destination: "200", Enabled: true},
			{ID: 2, Name: "direct", DIDKind: "exact", DID: "2", DestinationKind: "ring_group", Destination: "sales", Enabled: true},
		},
	}

	fixtures["any_trunk_route"] = Input{
		Revision: 7,
		Trunks:   []store.Trunk{{ID: 1, Name: "a", Enabled: true}, {ID: 2, Name: "b", Enabled: true}},
		Extensions: []store.Extension{
			ext(10, "101", true),
		},
		Inbound: []store.InboundRoute{
			{ID: 1, Name: "anywhere", DIDKind: "any", DestinationKind: "extension", Destination: "101", Enabled: true},
			{ID: 2, Name: "on-b", DIDKind: "exact", DID: "5", TrunkID: s64(2), DestinationKind: "extension", Destination: "101", Enabled: true},
			{ID: 3, Name: "ext", DIDKind: "exact", DID: "6", TrunkID: s64(1), DestinationKind: "external", Destination: "+3225550000", Enabled: true},
		},
	}

	open := timecond.Dest{Kind: "queue", Target: "support"}
	fixtures["full_feature_set"] = Input{
		Revision: 8,
		Trunks:   []store.Trunk{{ID: 1, Name: "carrier", Mode: "ip", Enabled: true}},
		Extensions: []store.Extension{
			{ID: 10, Number: "101", Name: "Ann", VoicemailEnabled: true, MOHClassID: s64(1)},
			ext(11, "102", true),
		},
		Announcements: []store.Announcement{
			ann(1, "welcome"), {ID: 2, Name: "tts-closed", Object: "a/c.wav", SourceText: "We are closed", VoiceID: "v1", ModelID: "m1", TextHash: "h", Generated: true},
			ann(3, "hold1"), ann(4, "periodic"),
		},
		MOHClasses: []store.MOHClass{{ID: 1, Name: "jazz", Mode: "loop", Tracks: []store.MOHTrack{{AnnouncementID: 3, Name: "hold1"}}}},
		Calendars:  []store.Calendar{{ID: 1, Name: "holidays", Schedules: []string{"office"}}},
		Schedules:  []store.Schedule{{ID: 1, Name: "office", TimeZone: "Europe/Brussels", CalendarIDs: []int64{1}}},
		TimeConditions: []store.TimeCondition{{ID: 1, Name: "hours", ScheduleID: 1, ScheduleName: "office",
			Open: open, Closed: timecond.Dest{Kind: "announcement", Target: "tts-closed", ThenVoicemail: "101"},
			Holiday:  &timecond.Dest{Kind: "ivr", Target: "menu"},
			Override: "closed", OverrideAt: ptrTime(time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)), ToggleNumber: "1"}},
		IVRMenus: []store.IVRMenu{{ID: 1, Name: "menu", Number: "810", Enabled: true, GreetingID: 1, DigitTimeout: 5,
			Timeout: store.IVRDestination{Kind: "time_condition", Target: "hours"}, Invalid: store.IVRDestination{Kind: "hangup"},
			Options: []store.IVROption{{Digit: "1", Kind: "queue", Target: "support"}}}},
		Queues: []store.Queue{{ID: 1, Name: "support", Number: "820", Enabled: true, Strategy: "ring_all", MOHClassID: s64(1),
			PeriodicPromptID: s64(4), Timeout: store.IVRDestination{Kind: "voicemail", Target: "101"},
			Full: store.IVRDestination{Kind: "hangup"}, NoAgents: store.IVRDestination{Kind: "announcement", Target: "tts-closed"},
			BreakoutKey: "1", BreakoutVoicemail: "101",
			Members: []store.QueueMember{{ExtensionID: 10, Number: "101", Position: 1, Penalty: 0}, {ExtensionID: 11, Number: "102", Position: 2, Dynamic: true, Penalty: 1}}}},
		PagingGroups:      []store.PagingGroup{{ID: 1, Name: "all", Code: "*88", Enabled: true, Members: []store.PagingExtension{{ExtensionID: 10}}, MulticastChannelID: s64(1)}},
		MulticastChannels: []phonebook.Channel{{ID: 1, Label: "lobby", Address: "224.0.1.1", Port: 5001, Codec: "PCMU", Kind: "paging", Enabled: true}},
		Conferences:       []store.Conference{{ID: 1, Name: "weekly", Number: "800", Enabled: true, EntryAnnouncement: "welcome"}},
		ParkLots:          []store.ParkLot{{ID: 1, Name: "lot", SlotStart: 701, SlotEnd: 709, Enabled: true}},
		PickupGroups:      []store.PickupGroup{{ID: 1, Name: "front", Members: []store.PagingExtension{{ExtensionID: 10}, {ExtensionID: 11}}}},
		FeatureCodes: []store.FeatureCode{
			{Code: "*50", Action: "time_condition", Argument: "hours"}, {Code: "*51", Action: "ivr", Argument: "menu"},
			{Code: "*28", Action: "tc_override", Argument: "toggle"}, {Code: "*74", Action: "park"},
		},
		Inbound: []store.InboundRoute{
			{ID: 1, Name: "main", DIDKind: "exact", DID: "1", TrunkID: s64(1), DestinationKind: "time_condition", Destination: "hours", Enabled: true},
			{ID: 2, Name: "meet", DIDKind: "exact", DID: "2", TrunkID: s64(1), DestinationKind: "conference", Destination: "800", Enabled: true},
			{ID: 3, Name: "page", DIDKind: "exact", DID: "3", TrunkID: s64(1), DestinationKind: "paging", Destination: "*88", Enabled: true},
		},
	}

	fixtures["loops_and_shadows"] = Input{
		Revision:      9,
		Trunks:        []store.Trunk{{ID: 1, Name: "carrier", Enabled: true}},
		Extensions:    []store.Extension{{ID: 10, Number: "101", Name: "A", ForwardAlways: "102"}, {ID: 11, Number: "102", Name: "B", ForwardAlways: "101"}},
		Announcements: []store.Announcement{{ID: 1, Name: "silent"}},
		IVRMenus: []store.IVRMenu{
			{ID: 1, Name: "a", Enabled: true, GreetingID: 1, Timeout: store.IVRDestination{Kind: "ivr", Target: "b"}, Invalid: store.IVRDestination{Kind: "ivr", Target: "b"},
				Options: []store.IVROption{{Digit: "1", Kind: "ivr", Target: "b"}}},
			{ID: 2, Name: "b", Enabled: true, GreetingID: 1, Timeout: store.IVRDestination{Kind: "ivr", Target: "a"}, Invalid: store.IVRDestination{Kind: "ivr", Target: "a"},
				Options: []store.IVROption{{Digit: "1", Kind: "ivr", Target: "a"}}},
			{ID: 3, Name: "self", Enabled: true, GreetingID: 0, Number: "900", Timeout: store.IVRDestination{Kind: "hangup"}, Invalid: store.IVRDestination{Kind: "hangup"},
				Options: []store.IVROption{{Digit: "1", Kind: "ivr", Target: "self"}}},
		},
		Inbound: []store.InboundRoute{
			{ID: 1, Name: "catch-all", Position: 1, DIDKind: "any", DestinationKind: "ivr", Destination: "a", Enabled: true},
			{ID: 2, Name: "later", Position: 2, DIDKind: "exact", DID: "5", TrunkID: s64(1), DestinationKind: "extension", Destination: "101", Enabled: true},
			{ID: 3, Name: "prefix", Position: 3, DIDKind: "prefix", DID: "32", TrunkID: s64(1), DestinationKind: "extension", Destination: "101", Enabled: true},
		},
	}
	return fixtures
}

func ptrTime(t time.Time) *time.Time { return &t }

// TestGraphGolden fails if a fixture configuration no longer produces its
// stored graph, if an edge lacks its owner, or if a target resolves
// differently from snapshot lookup order. Run with -update to rewrite the
// files after a deliberate change, and read the diff.
func TestGraphGolden(t *testing.T) {
	for name, in := range goldenFixtures() {
		t.Run(name, func(t *testing.T) {
			g, err := Build(in, Options{Devices: true})
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range g.Edges {
				if e.Owner.Node == "" || (e.Owner.Field == "" && !e.Implicit) {
					t.Errorf("edge %s has no owner field: %+v", e.ID, e.Owner)
				}
			}
			got, err := json.MarshalIndent(g, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join("testdata", name+".graph.json")
			if *update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run go test ./internal/flow -run TestGraphGolden -update)", err)
			}
			if string(want) != string(got) {
				t.Errorf("graph for %s changed; run with -update and review the diff\n--- got ---\n%s", name, got)
			}
		})
	}
}
