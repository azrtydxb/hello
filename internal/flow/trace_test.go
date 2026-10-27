package flow

import (
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/internal/timecond"
)

func traceFixture(override string) Input {
	week := []timecond.DayRange{}
	for d := 1; d <= 5; d++ {
		week = append(week, timecond.DayRange{Day: d, Start: "09:00", End: "17:00"})
	}
	return Input{
		Revision: 1,
		Trunks:   []store.Trunk{{ID: 1, Name: "carrier", Mode: "ip", Enabled: true}},
		Extensions: []store.Extension{
			{ID: 10, Number: "101", Name: "Ann", VoicemailEnabled: true},
			{ID: 11, Number: "102", Name: "Bob"},
		},
		Announcements: []store.Announcement{
			{ID: 1, Name: "welcome", Object: "a/welcome.wav"},
			{ID: 2, Name: "closed", Object: "a/closed.wav"},
		},
		RingGroups: []store.RingGroup{{ID: 1, Name: "sales", Strategy: "ring-all", RingTimeout: 20,
			Members:     []store.RingGroupMember{{ExtensionID: 10, Position: 1}, {ExtensionID: 11, Position: 2}},
			FailureKind: "voicemail", FailureTarget: "101"}},
		IVRMenus: []store.IVRMenu{{ID: 1, Name: "menu", Enabled: true, GreetingID: 1, MaxRetries: 2,
			Timeout: store.IVRDestination{Kind: "hangup"}, Invalid: store.IVRDestination{Kind: "hangup"},
			Options: []store.IVROption{{Digit: "1", Kind: "ring_group", Target: "sales"}, {Digit: "2", Kind: "extension", Target: "102"}}}},
		Schedules: []store.Schedule{{ID: 1, Name: "office", TimeZone: "Europe/Brussels", Ranges: week}},
		TimeConditions: []store.TimeCondition{{ID: 1, Name: "hours", ScheduleID: 1, ScheduleName: "office",
			Open:     timecond.Dest{Kind: "ivr", Target: "menu"},
			Closed:   timecond.Dest{Kind: "announcement", Target: "closed", ThenVoicemail: "101"},
			Override: override}},
		Inbound: []store.InboundRoute{{ID: 1, Name: "main", DIDKind: "any", TrunkID: func() *int64 { v := int64(1); return &v }(),
			DestinationKind: "time_condition", Destination: "hours", Enabled: true}},
	}
}

func routeStart() TraceStart {
	return TraceStart{
		Steps:  []TraceStep{{Node: "trunk:1", Port: "inbound", Text: "arrives"}, {Node: "inboundRoute:1", Port: "destination", Text: "matches"}},
		Follow: &Ref{Node: "inboundRoute:1", Port: "destination"},
	}
}

// pathOf renders steps as node:port for comparison.
func pathOf(steps []TraceStep) string {
	var p []string
	for _, s := range steps {
		p = append(p, s.Node+":"+s.Port)
	}
	return strings.Join(p, " > ")
}

func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTrace(t *testing.T) {
	sunday := at(t, "2026-10-25T20:00:00+01:00")
	monday := at(t, "2026-10-26T10:00:00+01:00")

	t.Run("closed on Sunday evening goes to the announcement then voicemail", func(t *testing.T) {
		steps, err := Trace(traceFixture(""), routeStart(), TraceOptions{At: sunday})
		if err != nil {
			t.Fatal(err)
		}
		want := "trunk:1:inbound > inboundRoute:1:destination > timeCondition:1:closed > announcement:2: > extension:10:voicemail"
		if got := pathOf(steps); got != want {
			t.Fatalf("path\n got %s\nwant %s", got, want)
		}
		if steps[2].At == "" || !strings.Contains(steps[2].Text, "closed") {
			t.Errorf("time condition step %+v", steps[2])
		}
	})

	t.Run("an override forces the open branch on Sunday", func(t *testing.T) {
		steps, err := Trace(traceFixture("open"), routeStart(), TraceOptions{At: sunday, Digits: "1"})
		if err != nil {
			t.Fatal(err)
		}
		want := "trunk:1:inbound > inboundRoute:1:destination > timeCondition:1:open > ivr:1:key:1 > ringGroup:1:noAnswer > extension:10:voicemail"
		if got := pathOf(steps); got != want {
			t.Fatalf("path\n got %s\nwant %s", got, want)
		}
		if !strings.Contains(steps[2].Text, "forced") {
			t.Errorf("the forced override is not shown: %q", steps[2].Text)
		}
	})

	t.Run("an override closes the office on Monday morning", func(t *testing.T) {
		steps, _ := Trace(traceFixture("closed"), routeStart(), TraceOptions{At: monday})
		if got := steps[2].Node + ":" + steps[2].Port; got != "timeCondition:1:closed" || !strings.Contains(steps[2].Text, "forced") {
			t.Errorf("step %+v", steps[2])
		}
	})

	t.Run("open on Monday without override reaches the menu", func(t *testing.T) {
		steps, _ := Trace(traceFixture(""), routeStart(), TraceOptions{At: monday, Digits: "2"})
		if got := pathOf(steps); !strings.HasPrefix(got, "trunk:1:inbound > inboundRoute:1:destination > timeCondition:1:open > ivr:1:key:2 > extension:11:") {
			t.Fatalf("path %s", got)
		}
	})

	t.Run("an IVR without digits lists every key", func(t *testing.T) {
		steps, _ := Trace(traceFixture(""), routeStart(), TraceOptions{At: monday})
		menu := steps[len(steps)-1]
		if menu.Node != "ivr:1" || len(menu.Branches) != 3 {
			t.Fatalf("menu step %+v", menu)
		}
		var ports []string
		for _, b := range menu.Branches {
			ports = append(ports, b.Port)
		}
		if strings.Join(ports, ",") != "key:1,key:2,timeout" {
			t.Errorf("branches %v", ports)
		}
		if got := pathOf(menu.Branches[0].Steps); got != "ringGroup:1:noAnswer > extension:10:voicemail" {
			t.Errorf("key 1 branch %s", got)
		}
	})

	t.Run("an unknown key takes the invalid branch", func(t *testing.T) {
		steps, _ := Trace(traceFixture(""), routeStart(), TraceOptions{At: monday, Digits: "7"})
		last := steps[len(steps)-1]
		if last.Node != "ivr:1" || !strings.Contains(last.Text, "not an option") {
			t.Errorf("last step %+v", last)
		}
	})

	t.Run("ring group failover to voicemail", func(t *testing.T) {
		steps, _ := Trace(traceFixture(""), TraceStart{Enter: &Ref{Node: "ringGroup:1", Port: "ring"}}, TraceOptions{At: monday})
		if got := pathOf(steps); got != "ringGroup:1:noAnswer > extension:10:voicemail" {
			t.Errorf("path %s", got)
		}
	})

	t.Run("an extension forwards, then rings", func(t *testing.T) {
		in := traceFixture("")
		in.Extensions[1].ForwardAlways = "101"
		steps, _ := Trace(in, TraceStart{Enter: &Ref{Node: "extension:11", Port: "ring"}}, TraceOptions{At: monday})
		if got := pathOf(steps); got != "extension:11:forwardAlways > extension:10:ring > extension:10:voicemail" {
			t.Errorf("path %s", got)
		}
	})

	t.Run("a menu loop is cut", func(t *testing.T) {
		in := traceFixture("")
		in.IVRMenus[0].Options = []store.IVROption{{Digit: "1", Kind: "ivr", Target: "menu"}}
		steps, _ := Trace(in, TraceStart{Enter: &Ref{Node: "ivr:1", Port: "call"}}, TraceOptions{At: monday})
		if len(steps) == 0 || len(steps) > 40 {
			t.Fatalf("steps %d", len(steps))
		}
	})
}

func TestTraceExtensionBranches(t *testing.T) {
	monday := at(t, "2026-10-26T10:00:00+01:00")
	in := traceFixture("")
	in.Extensions[0].ForwardBusy = "102"
	steps, _ := Trace(in, TraceStart{Enter: &Ref{Node: "extension:10", Port: "ring"}}, TraceOptions{At: monday})
	ring := steps[0]
	if len(ring.Branches) != 2 {
		t.Fatalf("branches %+v", ring.Branches)
	}
	busy, unreg := ring.Branches[0], ring.Branches[1]
	if busy.Label != "If busy" || busy.Port != "forwardBusy" || pathOf(busy.Steps) != "extension:10:forwardBusy > extension:11:ring" {
		t.Errorf("busy branch %s %s", busy.Label, pathOf(busy.Steps))
	}
	if unreg.Label != "If not registered" || pathOf(unreg.Steps) != "extension:10:voicemail > extension:10:voicemail" {
		t.Errorf("unregistered branch %s", pathOf(unreg.Steps))
	}
	// Without a busy forward the busy branch goes to voicemail; without
	// voicemail either, the caller hears busy.
	in.Extensions[0].ForwardBusy = ""
	steps, _ = Trace(in, TraceStart{Enter: &Ref{Node: "extension:10", Port: "ring"}}, TraceOptions{At: monday})
	if got := pathOf(steps[0].Branches[0].Steps); got != "extension:10:voicemail > extension:10:voicemail" {
		t.Errorf("busy to voicemail %s", got)
	}
	steps, _ = Trace(in, TraceStart{Enter: &Ref{Node: "extension:11", Port: "ring"}}, TraceOptions{At: monday})
	if b := steps[0].Branches; !strings.Contains(b[0].Steps[0].Text, "hears busy") || !strings.Contains(b[1].Steps[0].Text, "temporarily unavailable") {
		t.Errorf("no voicemail: %+v", b)
	}
}

func TestFeatureCodeStart(t *testing.T) {
	in := traceFixture("")
	in.FeatureCodes = []store.FeatureCode{
		{Code: "*97", Action: "voicemail"},
		{Code: "*72", Action: "forward_always"},
		{Code: "*60", Action: "ivr", Argument: "menu"},
		{Code: "*61", Action: "time_condition"},
		{Code: "*8", Action: "park_retrieve"},
		{Code: "*2", Action: "blind_transfer"},
	}
	in.ParkLots = []store.ParkLot{{ID: 1, Name: "lot", SlotStart: 701, SlotEnd: 709, Enabled: true}}
	monday := at(t, "2026-10-26T10:00:00+01:00")
	trace := func(dialled string) ([]TraceStep, bool) {
		s, ok := FeatureCodeStart(in, "102", dialled, nil)
		if !ok {
			return nil, false
		}
		steps, err := Trace(in, s, TraceOptions{At: monday})
		if err != nil {
			t.Fatal(err)
		}
		return steps, true
	}
	if _, ok := trace("5551"); ok {
		t.Error("a plain number is not a feature code")
	}
	if s, _ := trace("*97101"); pathOf(s) != "extension:11: > extension:10:voicemail" {
		t.Errorf("*97101 %s", pathOf(s))
	}
	if s, _ := trace("*97"); pathOf(s) != "extension:11:voicemail" {
		t.Errorf("*97 %s", pathOf(s))
	}
	if s, _ := trace("*97999"); !strings.Contains(s[len(s)-1].Text, "404") {
		t.Errorf("*97999 %+v", s)
	}
	if s, _ := trace("*60"); !strings.HasPrefix(pathOf(s), "featureCode:*60:target > ivr:1:") {
		t.Errorf("*60 %s", pathOf(s))
	}
	if s, _ := trace("*61hours"); s[0].Node != "extension:11" || s[1].Node != "timeCondition:1" || s[1].At == "" {
		t.Errorf("*61hours %+v", s)
	}
	if s, _ := trace("*61menu"); s[len(s)-1].Node != "missing:menu" {
		t.Errorf("*61menu is no time condition: %+v", s)
	}
	if s, _ := trace("*7211"); !strings.Contains(s[0].Text, "11") || s[0].Node != "extension:11" {
		t.Errorf("*7211 %+v", s)
	}
	if s, _ := trace("*72"); !strings.Contains(s[0].Text, "400") {
		t.Errorf("*72 %+v", s)
	}
	if s, _ := trace("*8705"); s[0].Node != "parkLot:1" || !strings.Contains(s[0].Text, "slot 705") {
		t.Errorf("*8705 %+v", s)
	}
	if s, _ := trace("705"); s[0].Node != "parkLot:1" {
		t.Errorf("slot 705 %+v", s)
	}
	if s, _ := trace("*2"); !strings.Contains(s[0].Text, "403") {
		t.Errorf("*2 %+v", s)
	}
	// The longest code wins: *97 is not read as *9 plus an argument.
	in.FeatureCodes = append(in.FeatureCodes, store.FeatureCode{Code: "*9", Action: "dnd_on"})
	if s, _ := trace("*97101"); !strings.Contains(s[0].Text, "voicemail") {
		t.Errorf("longest code %+v", s)
	}
}

func queueFixture() Input {
	in := traceFixture("")
	in.Queues = []store.Queue{{ID: 1, Name: "support", Number: "820", Enabled: true, Strategy: "ring_all", MaxWait: 60,
		Timeout:  store.IVRDestination{Kind: "voicemail", Target: "101"},
		NoAgents: store.IVRDestination{Kind: "announcement", Target: "closed"},
		Members: []store.QueueMember{
			{ExtensionID: 10, Number: "101"},                // static
			{ExtensionID: 11, Number: "102", Dynamic: true}, // must log in
		}}}
	return in
}

func TestTraceQueueLiveState(t *testing.T) {
	monday := at(t, "2026-10-26T10:00:00+01:00")
	enter := TraceStart{Enter: &Ref{Node: "queue:1", Port: "call"}}
	run := func(live *Live) []TraceStep {
		steps, err := Trace(queueFixture(), enter, TraceOptions{At: monday, Live: live})
		if err != nil {
			t.Fatal(err)
		}
		return steps
	}

	// Unknown: both outcomes are branches and the step says so.
	s := run(nil)
	if len(s) != 1 || !strings.Contains(s[0].Text, Unknown) || len(s[0].Branches) != 2 {
		t.Fatalf("unknown: %+v", s)
	}
	if got := pathOf(s[0].Branches[0].Steps); got != "queue:1: > queue:1:timeout > extension:10:voicemail" {
		t.Errorf("available branch %s", got)
	}
	if got := pathOf(s[0].Branches[1].Steps); !strings.HasPrefix(got, "queue:1:noAgents > announcement:2:") {
		t.Errorf("no-agent branch %s", got)
	}
	// Known, the static agent is available: the timeout exit.
	s = run(&Live{AgentsKnown: true})
	if !strings.Contains(s[0].Text, "101 available, 102 logged out") || s[1].Port != "timeout" || len(s[0].Branches) != 0 {
		t.Errorf("static agent: %+v", s)
	}
	// Paused static agent and a logged-out dynamic one: nobody, so noAgents.
	s = run(&Live{AgentsKnown: true, Agents: map[string]AgentState{"101": {Paused: true, Reason: "lunch"}}})
	if !strings.Contains(s[0].Text, "101 paused (lunch)") || s[1].Port != "noAgents" {
		t.Errorf("paused: %+v", s)
	}
	// The dynamic agent logs in to another queue only.
	s = run(&Live{AgentsKnown: true, Agents: map[string]AgentState{
		"101": {Paused: true}, "102": {LoggedIn: true, Queues: []string{"sales"}}}})
	if s[1].Port != "noAgents" {
		t.Errorf("other queue: %+v", s)
	}
	s = run(&Live{AgentsKnown: true, Agents: map[string]AgentState{
		"101": {Paused: true}, "102": {LoggedIn: true, Queues: []string{"support"}}}})
	if s[1].Port != "timeout" || !strings.Contains(s[0].Text, "102 logged in") {
		t.Errorf("logged in: %+v", s)
	}
}

func TestFeatureCodeLiveState(t *testing.T) {
	in := queueFixture()
	in.FeatureCodes = []store.FeatureCode{{Code: "*8", Action: "park_retrieve"}, {Code: "*45", Action: "queue_pause"}}
	in.ParkLots = []store.ParkLot{{ID: 1, Name: "lot", SlotStart: 701, SlotEnd: 709, Enabled: true}}
	text := func(dialled string, live *Live) string {
		s, ok := FeatureCodeStart(in, "102", dialled, live)
		if !ok {
			t.Fatalf("%s is not a code", dialled)
		}
		return s.Steps[0].Text
	}
	if got := text("*8705", nil); !strings.Contains(got, Unknown) {
		t.Errorf("unknown slot: %s", got)
	}
	if got := text("*8705", &Live{}); !strings.Contains(got, Unknown) {
		t.Errorf("unread slot: %s", got)
	}
	known := &Live{ParkKnown: true, Parked: map[string]string{"705": "101"}}
	if got := text("*8705", known); !strings.Contains(got, "parked by extension 101") {
		t.Errorf("occupied: %s", got)
	}
	if got := text("*8706", known); !strings.Contains(got, "slot is empty") {
		t.Errorf("empty: %s", got)
	}
	if got := text("705", known); !strings.Contains(got, "parked by extension 101") {
		t.Errorf("slot dialled: %s", got)
	}
	if got := text("*45820", &Live{AgentsKnown: true, Agents: map[string]AgentState{"102": {LoggedIn: true, Paused: true}}}); !strings.Contains(got, "logged in and paused") {
		t.Errorf("agent code: %s", got)
	}
	if got := text("*45820", nil); !strings.Contains(got, Unknown) {
		t.Errorf("agent code unknown: %s", got)
	}
}
