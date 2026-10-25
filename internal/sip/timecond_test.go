package sip

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/azrtydxb/hello/internal/timecond"
)

// allWeek is a schedule that is open around the clock, in UTC.
func allWeek() timecond.Spec {
	s := timecond.Spec{TimeZone: "UTC"}
	for d := 0; d <= 6; d++ {
		s.Ranges = append(s.Ranges, timecond.DayRange{Day: d, Start: "00:00", End: "24:00"})
	}
	return s
}

// fakeTCSettings records the toggles a feature code asks for.
type fakeTCSettings struct {
	mu    sync.Mutex
	calls [][2]string // number, mode
	fail  bool
}

func (f *fakeTCSettings) Toggle(_ context.Context, _, number, mode string, _ time.Time) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return "", "", errors.New("store down")
	}
	f.calls = append(f.calls, [2]string{number, mode})
	return "office", "closed", nil
}

func (f *fakeTCSettings) got() [][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]string(nil), f.calls...)
}

func tcPBX(t *testing.T, override string, tcs ...snapshot.TimeCondition) *testPBX {
	t.Helper()
	for i := range tcs {
		if tcs[i].Override == "" {
			tcs[i].Override = override
		}
	}
	return startPBX(t, threeDevices(), shortRing(2*time.Second), withFeatures(func(s *snapshot.Snapshot) {
		s.WithTimeConditions(tcs)
		s.WithFeatureCodes([]snapshot.FeatureCode{
			{Code: "*77", Action: ActionTimeCondition, Argument: "office"},
			{Code: "*28", Action: ActionTCOverride, Argument: "toggle"},
		})
	}))
}

func officeTC(override string) snapshot.TimeCondition {
	return snapshot.TimeCondition{ToggleNumber: "1", Condition: timecond.Condition{
		Name: "office", Spec: allWeek(), Override: override,
		Open:   timecond.Dest{Kind: timecond.KindExtension, Target: "300"},
		Closed: timecond.Dest{Kind: timecond.KindHangup},
	}}
}

// TestTimeConditionOpenRingsOpenBranch: a call to a time condition (a feature
// code that names one, the same path an inbound route takes) rings the open
// destination, and the decision lands in the CDR trace with the reason and in
// hello_time_condition_total{result="open"}. It fails if the call is not
// handed to the open branch, or the decision is not traced or counted.
func TestTimeConditionOpenRingsOpenBranch(t *testing.T) {
	pbx := tcPBX(t, "", officeTC(""))
	a, _, c := registerABC(t, pbx)
	c.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "*77"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	waitReq(t, c.invites, "INVITE to the open destination")
	hangup(t, r.dcs)
	rec := lastCDRFor(t, pbx, "*77")
	wantTrace(t, rec.Trace, "Time condition office: open (open: within weekly hours 00:00-24:00) -> extension 300")
	if v := pbx.metric(t, "hello_time_condition_total", map[string]string{"result": "open"}); v != 1 {
		t.Fatalf("open decisions = %v", v)
	}
}

// TestTimeConditionOverrideBeatsSchedule: the same open schedule, forced
// closed, goes to the closed branch (a hang up, 603), nothing rings, and the
// trace says it was the override. It fails if the override is ignored.
func TestTimeConditionOverrideBeatsSchedule(t *testing.T) {
	pbx := tcPBX(t, "", officeTC(timecond.OverrideClosed))
	a, _, c := registerABC(t, pbx)
	r := waitCall(t, dial(t.Context(), a, "*77"))
	if got := responseCode(r.err); got != 603 {
		t.Fatalf("status = %d (%v), want 603", got, r.err)
	}
	noReq(t, c.invites, 200*time.Millisecond, "a forced-closed condition must not ring the open branch")
	rec := lastCDRFor(t, pbx, "*77")
	wantTrace(t, rec.Trace, "Time condition office: closed (manual override: forced closed) -> hangup")
	if v := pbx.metric(t, "hello_time_condition_total", map[string]string{"result": "forced_closed"}); v != 1 {
		t.Fatalf("forced_closed decisions = %v", v)
	}
}

// TestTimeConditionChainAndLoop: a condition that names another is followed
// to its destination, and one that loops is refused with 482 and counted, not
// followed for ever. It fails if the chain is not followed or a loop rings.
func TestTimeConditionChainAndLoop(t *testing.T) {
	front := snapshot.TimeCondition{Condition: timecond.Condition{Name: "office", Spec: allWeek(),
		Open: timecond.Dest{Kind: timecond.KindTimeCondition, Target: "inner"}, Closed: timecond.Dest{Kind: timecond.KindHangup}}}
	inner := snapshot.TimeCondition{Condition: timecond.Condition{Name: "inner", Spec: allWeek(),
		Open: timecond.Dest{Kind: timecond.KindExtension, Target: "300"}, Closed: timecond.Dest{Kind: timecond.KindHangup}}}
	pbx := tcPBX(t, "auto", front, inner)
	a, _, c := registerABC(t, pbx)
	c.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "*77"))
	if r.err != nil {
		t.Fatalf("chained call: %v", r.err)
	}
	waitReq(t, c.invites, "INVITE through the chain")
	hangup(t, r.dcs)
	wantTrace(t, lastCDRFor(t, pbx, "*77").Trace, "Time condition office: open (open: within weekly hours 00:00-24:00) -> time_condition inner", "Time condition inner: open (open: within weekly hours 00:00-24:00) -> extension 300")

	loopA := snapshot.TimeCondition{Condition: timecond.Condition{Name: "office", Spec: allWeek(),
		Open: timecond.Dest{Kind: timecond.KindTimeCondition, Target: "b"}}}
	loopB := snapshot.TimeCondition{Condition: timecond.Condition{Name: "b", Spec: allWeek(),
		Open: timecond.Dest{Kind: timecond.KindTimeCondition, Target: "office"}}}
	pbx2 := tcPBX(t, "auto", loopA, loopB)
	a2, _, _ := registerABC(t, pbx2)
	r2 := waitCall(t, dial(t.Context(), a2, "*77"))
	if got := responseCode(r2.err); got != 482 {
		t.Fatalf("loop status = %d (%v), want 482", got, r2.err)
	}
	if v := pbx2.metric(t, "hello_time_condition_total", map[string]string{"result": "loop"}); v != 1 {
		t.Fatalf("loop counter = %v", v)
	}
}

// TestTimeConditionHolidayBranch: a holiday takes the holiday destination,
// and without one the closed destination. It fails if a holiday rings the
// open branch or ignores its own destination.
func TestTimeConditionHolidayBranch(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")
	spec := allWeek()
	spec.Entries = []timecond.CalendarEntry{{Calendar: "h", Entry: timecond.Entry{Label: "today", Start: today, End: today, AllDay: true, Mode: timecond.ModeClosed}}}
	holiday := timecond.Dest{Kind: timecond.KindExtension, Target: "210"}
	tc := snapshot.TimeCondition{Condition: timecond.Condition{Name: "office", Spec: spec,
		Open: timecond.Dest{Kind: timecond.KindExtension, Target: "300"}, Closed: timecond.Dest{Kind: timecond.KindHangup}, Holiday: &holiday}}
	pbx := tcPBX(t, "auto", tc)
	a, _, c := registerABC(t, pbx)
	d := newPhone(t, pbx, "d1", "pd")
	d.register(t)
	d.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "*77"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	waitReq(t, d.invites, "INVITE to the holiday destination")
	noReq(t, c.invites, 100*time.Millisecond, "the open branch must not ring on a holiday")
	hangup(t, r.dcs)
	wantTrace(t, lastCDRFor(t, pbx, "*77").Trace, "Time condition office: holiday (closed for today (calendar h)) -> extension 210")
	if v := pbx.metric(t, "hello_time_condition_total", map[string]string{"result": "holiday"}); v != 1 {
		t.Fatalf("holiday decisions = %v", v)
	}
}

// TestTimeConditionToggleCode: *28<n> asks the store to toggle the condition
// with that toggle number and answers 200; an unknown number is 404 and a
// store failure 503. It fails if the digits do not select the condition or
// the phone is not told.
func TestTimeConditionToggleCode(t *testing.T) {
	set := &fakeTCSettings{}
	pbx := startPBX(t, threeDevices(), withFeatures(func(s *snapshot.Snapshot) {
		s.WithTimeConditions([]snapshot.TimeCondition{officeTC("")})
		s.WithFeatureCodes([]snapshot.FeatureCode{{Code: "*28", Action: ActionTCOverride, Argument: "toggle"}})
	}), func(_ *Config, d *Deps) { d.TimeConditions = set })
	a, _, _ := registerABC(t, pbx)
	r := waitCall(t, dial(t.Context(), a, "*281"))
	if r.err != nil {
		t.Fatalf("*281: %v", r.err)
	}
	hangup(t, r.dcs)
	if got := set.got(); len(got) != 1 || got[0] != [2]string{"1", "toggle"} {
		t.Fatalf("toggles = %v", got)
	}
	if got := responseCode(waitCall(t, dial(t.Context(), a, "*289")).err); got != 404 {
		t.Fatalf("unknown toggle number = %d, want 404", got)
	}
	set.fail = true
	if got := responseCode(waitCall(t, dial(t.Context(), a, "*281")).err); got != 503 {
		t.Fatalf("store down = %d, want 503", got)
	}
	if v := pbx.metric(t, "hello_time_condition_total", map[string]string{"result": "toggled"}); v != 1 {
		t.Fatalf("toggled = %v", v)
	}
}

// TestTimeConditionBLF: a phone's BLF key on *281 is accepted, starts dark
// while the schedule decides, and lights when the override is forced (a
// snapshot change from the UI or API). It fails if the subscription is
// refused, the lamp starts lit, or a changed override sends no NOTIFY.
func TestTimeConditionBLF(t *testing.T) {
	pbx := startPBX(t, threeDevices(), withFeatures(func(s *snapshot.Snapshot) {
		s.WithTimeConditions([]snapshot.TimeCondition{officeTC("auto")})
		s.WithFeatureCodes([]snapshot.FeatureCode{{Code: "*28", Action: ActionTCOverride, Argument: "toggle"}})
	}))
	a, _, _ := registerABC(t, pbx)
	if res := a.subscribe(t, "*281", 120); res.StatusCode != 200 {
		t.Fatalf("SUBSCRIBE *281 = %d", res.StatusCode)
	}
	waitDialogState(t, a.notifies, "terminated", "initial lamp")
	if res := a.subscribe(t, "*289", 120); res.StatusCode != 404 {
		t.Fatalf("SUBSCRIBE of an unknown toggle = %d, want 404", res.StatusCode)
	}

	old := pbx.snaps.p.Load()
	cur := snapshot.New(2, testDomain, threeDevices()).WithExtensions(bareExtension).
		WithFeatureCodes([]snapshot.FeatureCode{{Code: "*28", Action: ActionTCOverride, Argument: "toggle"}}).
		WithTimeConditions([]snapshot.TimeCondition{officeTC(timecond.OverrideClosed)})
	pbx.snaps.p.Store(cur)
	pbx.srv.SnapshotChanged(old, cur)
	waitDialogState(t, a.notifies, "confirmed", "lamp after the override")
}

// TestTimeConditionAnnouncementThenVoicemail: the closed branch plays an
// announcement and then takes a message for a voicemail box, the example
// "after hours: announcement, then voicemail". It fails if the call does not
// reach the box, no message is stored in it, or the announcement is skipped.
func TestTimeConditionAnnouncementThenVoicemail(t *testing.T) {
	vm := newVMStore()
	vm.boxes["200"] = VoicemailBox{ID: 5, Extension: "200"}
	objs := &fakeObjects{}
	_ = objs.Put(context.Background(), "ann/closed.wav", media.EncodeWAV(make([]byte, 3200), 8000))
	tc := snapshot.TimeCondition{Condition: timecond.Condition{Name: "office", Spec: allWeek(), Override: timecond.OverrideClosed,
		Open:   timecond.Dest{Kind: timecond.KindExtension, Target: "300"},
		Closed: timecond.Dest{Kind: timecond.KindAnnouncement, Target: "closed-msg", ThenVoicemail: "200"}}}
	pbx := startPBX(t, threeDevices(), shortRing(time.Second), withFeatures(func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{{Number: "200", VoicemailEnabled: true, VoicemailBoxID: 5}})
		s.WithAnnouncements(map[string]string{"closed-msg": "ann/closed.wav"})
		s.WithTimeConditions([]snapshot.TimeCondition{tc})
		s.WithFeatureCodes([]snapshot.FeatureCode{{Code: "*77", Action: ActionTimeCondition, Argument: "office"}})
	}), withVoicemail(vm, objs))
	anchored(pbx)
	a, _, _ := registerABC(t, pbx)
	r := waitCall(t, dial(t.Context(), a, "*77"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	feed := startRTPFeed(t, r.dcs.InviteResponse.Body())
	feed.silence(4) // past the announcement and the beep, over a second of talk
	feed.digit('#')
	eventually(t, "message stored in the box", func() bool { return len(vm.snapshotMsgs()) == 1 })
	if msg := vm.snapshotMsgs()[0]; msg.BoxID != 5 {
		t.Fatalf("message row = %+v", msg)
	}
	wantTrace(t, lastCDRFor(t, pbx, "*77").Trace,
		"Time condition office: closed (manual override: forced closed) -> announcement closed-msg then voicemail 200",
		`Announcement "closed-msg" played, then voicemail`)
}

// TestTimeConditionHelpers pins the small mappings: the metric label of a
// step and the lamp state of an override.
func TestTimeConditionHelpers(t *testing.T) {
	for _, tc := range []struct {
		step timecond.Step
		want string
	}{
		{timecond.Step{Branch: "open"}, TCOpen},
		{timecond.Step{Branch: "closed"}, TCClosed},
		{timecond.Step{Branch: "holiday"}, TCHoliday},
		{timecond.Step{Branch: "open", Forced: true}, TCForcedOpen},
		{timecond.Step{Branch: "closed", Forced: true}, TCForcedClosed},
	} {
		if got := tcResult(tc.step); got != tc.want {
			t.Errorf("%+v = %s, want %s", tc.step, got, tc.want)
		}
	}
	if tcLampState(snapshot.TimeCondition{Condition: timecond.Condition{Override: "auto"}}) != StateIdle ||
		tcLampState(snapshot.TimeCondition{}) != StateIdle ||
		tcLampState(snapshot.TimeCondition{Condition: timecond.Condition{Override: "open"}}) != StateOnCall ||
		tcLampState(snapshot.TimeCondition{Condition: timecond.Condition{Override: "closed"}}) != StateOnCall {
		t.Error("lamp states")
	}
}
