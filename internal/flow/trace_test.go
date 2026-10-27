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
