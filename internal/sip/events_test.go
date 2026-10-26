package sip

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/events"
)

// fakeEvents collects the call events a node emits.
type fakeEvents struct {
	mu  sync.Mutex
	got []events.Event
}

func (f *fakeEvents) Emit(e events.Event) {
	f.mu.Lock()
	f.got = append(f.got, e)
	f.mu.Unlock()
}

// of lists the extensions an event type was emitted for, in order.
func (f *fakeEvents) of(typ string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.got {
		if e.Type == typ {
			out = append(out, e.Data.Extension)
		}
	}
	return out
}

func (f *fakeEvents) first(typ, ext string) (events.Event, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.got {
		if e.Type == typ && e.Data.Extension == ext {
			return e, true
		}
	}
	return events.Event{}, false
}

func withEvents(f *fakeEvents) pbxOpt {
	return func(_ *Config, d *Deps) { d.Events = f }
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}

// TestCallEventsAnswered fails if an answered call between two extensions
// does not emit call.ringing for the callee, call.answered and call.ended for
// both extensions and a call.completed per extension on the CDR, or if it
// reports the call missed.
func TestCallEventsAnswered(t *testing.T) {
	ev := &fakeEvents{}
	pbx := startPBX(t, ringAllDevices(), withEvents(ev))
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	answer := make(chan struct{})
	b.setCallee(answerAfter(answer))
	res := dial(t.Context(), a, "200")
	waitReq(t, b.invites, "callee INVITE")
	eventually(t, "call.ringing", func() bool { return len(ev.of(events.CallRinging)) == 1 })
	if got := ev.of(events.CallRinging); !slices.Equal(got, []string{"200"}) {
		t.Fatalf("ringing for %v, want [200]", got)
	}
	close(answer)
	r := waitCall(t, res)
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, b.acks, "callee ACK")
	eventually(t, "call.answered", func() bool { return len(ev.of(events.CallAnswered)) == 2 })
	time.Sleep(50 * time.Millisecond)
	hangup(t, r.dcs)
	cd := pbx.nextCDR(t)

	if got := sorted(ev.of(events.CallAnswered)); !slices.Equal(got, []string{"100", "200"}) {
		t.Fatalf("answered for %v, want [100 200]", got)
	}
	eventually(t, "call.ended", func() bool { return len(ev.of(events.CallEnded)) == 2 })
	if got := sorted(ev.of(events.CallEnded)); !slices.Equal(got, []string{"100", "200"}) {
		t.Fatalf("ended for %v, want [100 200]", got)
	}
	if got := ev.of(events.CallMissed); len(got) != 0 {
		t.Fatalf("an answered call was reported missed for %v", got)
	}
	e, ok := ev.first(events.CallEnded, "200")
	if !ok {
		t.Fatal("no call.ended for 200")
	}
	d := e.Data
	if d.CallID != cd.CorrelationID || d.Direction != "internal" || d.Disposition != ResultAnswered ||
		d.From.Number != "100" || d.From.Extension != "100" || d.To.Number != "200" || d.To.Extension != "200" ||
		d.AnsweredAt.IsZero() || d.EndedAt.IsZero() || d.BillableMs <= 0 || d.DurationMs < d.BillableMs {
		t.Fatalf("call.ended data = %+v", d)
	}
	if e.ID == "" || e.APIVersion != events.APIVersion || e.CreatedAt.IsZero() {
		t.Fatalf("envelope = %+v", e)
	}
	// The CDR carries the call.completed events the writer publishes.
	var completed []string
	for _, ce := range cd.Events {
		if ce.Type != events.CallCompleted || ce.Data.CallID != cd.CorrelationID {
			t.Fatalf("CDR event = %+v", ce)
		}
		completed = append(completed, ce.Data.Extension)
	}
	if !slices.Equal(sorted(completed), []string{"100", "200"}) {
		t.Fatalf("call.completed for %v, want [100 200]", completed)
	}
}

// TestCallEventsMissed fails if a call the caller cancels while the callee
// rings does not emit call.ringing, call.ended for both extensions and one
// call.missed for the callee only, with reason cancelled and no call.answered.
func TestCallEventsMissed(t *testing.T) {
	ev := &fakeEvents{}
	pbx := startPBX(t, ringAllDevices(), withEvents(ev))
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	b.setCallee(ringForever())
	ctx, cancel := context.WithCancel(t.Context())
	res := dial(ctx, a, "200")
	waitReq(t, b.invites, "callee INVITE")
	eventually(t, "call.ringing", func() bool { return len(ev.of(events.CallRinging)) == 1 })
	cancel()
	waitCall(t, res)
	pbx.nextCDR(t)
	eventually(t, "call.missed", func() bool { return len(ev.of(events.CallMissed)) == 1 })
	if got := ev.of(events.CallMissed); !slices.Equal(got, []string{"200"}) {
		t.Fatalf("missed for %v, want [200]", got)
	}
	if e, _ := ev.first(events.CallMissed, "200"); e.Data.Reason != "cancelled" || !e.Data.AnsweredAt.IsZero() {
		t.Fatalf("call.missed data = %+v", e.Data)
	}
	if got := sorted(ev.of(events.CallEnded)); !slices.Equal(got, []string{"100", "200"}) {
		t.Fatalf("ended for %v, want [100 200]", got)
	}
	if got := ev.of(events.CallAnswered); len(got) != 0 {
		t.Fatalf("an unanswered call emitted call.answered for %v", got)
	}
}

// TestCallEventsManyContacts fails if an extension whose two contacts both
// ring emits more than one call.ringing, or if the call is reported missed
// although one contact answered (the other was picked up elsewhere).
func TestCallEventsManyContacts(t *testing.T) {
	ev := &fakeEvents{}
	pbx := startPBX(t, ringAllDevices(), withEvents(ev))
	a, b1, b2 := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1"), newPhone(t, pbx, "b2", "pb2")
	for _, p := range []*phone{a, b1, b2} {
		p.register(t)
	}
	// b1 and b2 are devices of the same extension 200 in ringAllDevices: a
	// call to 200 rings both, which is one ringing event for the extension.
	answer := make(chan struct{})
	b2.setCallee(answerAfter(answer))
	b1.setCallee(ringForever())
	res := dial(t.Context(), a, "200")
	waitReq(t, b1.invites, "b1 INVITE")
	waitReq(t, b2.invites, "b2 INVITE")
	eventually(t, "call.ringing", func() bool { return len(ev.of(events.CallRinging)) >= 1 })
	close(answer)
	r := waitCall(t, res)
	if r.err != nil {
		t.Fatal(r.err)
	}
	hangup(t, r.dcs)
	pbx.nextCDR(t)
	if got := ev.of(events.CallRinging); !slices.Equal(got, []string{"200"}) {
		t.Fatalf("two contacts of one extension rang %v, want one event [200]", got)
	}
	if got := ev.of(events.CallMissed); len(got) != 0 {
		t.Fatalf("missed for %v on an answered call", got)
	}
}

// TestCallEventsOff fails if a node without an event sink changes behaviour.
func TestCallEventsOff(t *testing.T) {
	pbx := startPBX(t, ringAllDevices())
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, b.acks, "callee ACK")
	hangup(t, r.dcs)
	if cd := pbx.nextCDR(t); len(cd.Events) != 0 {
		t.Fatalf("a node without an event sink put events on the CDR: %+v", cd.Events)
	}
}

// TestMissedReason fails if a call outcome maps to the wrong missed reason.
func TestMissedReason(t *testing.T) {
	for _, tc := range []struct {
		result, reason, want string
		ok                   bool
	}{
		{ResultNoAnswer, "ring timeout", "no_answer", true},
		{ResultBusy, "busy", "busy", true},
		{ResultBusy, "DND", "dnd", true},
		{ResultCancelled, "cancelled by caller", "cancelled", true},
		{ResultUnavailable, "no registered device", "unreachable", true},
		{ResultNotFound, "no such extension", "", false},
		{ResultFailed, "boom", "", false},
	} {
		got, ok := missedReason(tc.result, tc.reason)
		if got != tc.want || ok != tc.ok {
			t.Errorf("missedReason(%q, %q) = %q, %v; want %q, %v", tc.result, tc.reason, got, ok, tc.want, tc.ok)
		}
	}
}
