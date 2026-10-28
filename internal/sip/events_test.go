package sip

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"

	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/events"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/queue"
	"github.com/azrtydxb/hello/internal/snapshot"
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

// seqOf lists a call's event sequence numbers in emission order.
func (f *fakeEvents) seqOf(callID string) []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int64
	for _, e := range f.got {
		if e.Data.CallID == callID {
			out = append(out, e.Data.Sequence)
		}
	}
	return out
}

func (f *fakeEvents) count(typ string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, e := range f.got {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// TestCallEventsSequence fails if the events of one call do not carry a
// sequence that starts at 1 and strictly increases in emission order across
// the call's subjects, or if call.completed (published by the CDR writer)
// does not follow call.ended in sequence.
func TestCallEventsSequence(t *testing.T) {
	ev := &fakeEvents{}
	pbx := startPBX(t, ringAllDevices(), withEvents(ev))
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	b.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, b.acks, "callee ACK")
	eventually(t, "call.answered", func() bool { return ev.count(events.CallAnswered) == 2 })
	hangup(t, r.dcs)
	cd := pbx.nextCDR(t)
	eventually(t, "call.ended", func() bool { return ev.count(events.CallEnded) == 2 })
	seq := ev.seqOf(cd.CorrelationID)
	if len(seq) < 4 || seq[0] != 1 {
		t.Fatalf("sequence of the call's events = %v, want it to start at 1", seq)
	}
	for i := 1; i < len(seq); i++ {
		if seq[i] <= seq[i-1] {
			t.Fatalf("sequence not increasing: %v", seq)
		}
	}
	var lastEnded, firstCompleted int64
	for _, e := range cd.Events {
		if firstCompleted == 0 || e.Data.Sequence < firstCompleted {
			firstCompleted = e.Data.Sequence
		}
	}
	ev.mu.Lock()
	for _, e := range ev.got {
		if e.Type == events.CallEnded {
			lastEnded = max(lastEnded, e.Data.Sequence)
		}
	}
	ev.mu.Unlock()
	if firstCompleted <= lastEnded {
		t.Fatalf("call.completed sequence %d does not follow call.ended %d", firstCompleted, lastEnded)
	}
}

// TestCallEventsQueueAgent fails if a call answered by a queue agent emits
// only the caller's events: the agent must get call.ringing, call.answered
// and call.ended, the caller call.answered, and nobody call.missed.
func TestCallEventsQueueAgent(t *testing.T) {
	ev := &fakeEvents{}
	st := queue.NewMemStore()
	pbx, _ := queuePBXWith(t, st, nil, []pbxOpt{withEvents(ev)}, testQueue("sales", "600", queue.RingAll, static("200", 0, 1)))
	a, b, _ := registerABC(t, pbx)
	answer := make(chan struct{})
	b.setCallee(answerAfter(answer))
	r := waitCall(t, dial(t.Context(), a, "600"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, b.invites, "INVITE to the agent")
	eventually(t, "call.ringing for the agent", func() bool { return len(ev.of(events.CallRinging)) == 1 })
	close(answer)
	eventually(t, "the agent's call.answered", func() bool { return len(ev.of(events.CallAnswered)) == 2 })
	hangup(t, r.dcs)
	lastCDRFor(t, pbx, "600")
	eventually(t, "call.ended", func() bool { return len(ev.of(events.CallEnded)) == 2 })
	if got := ev.of(events.CallRinging); !slices.Equal(got, []string{"200"}) {
		t.Fatalf("ringing for %v, want the agent [200]", got)
	}
	if got := sorted(ev.of(events.CallAnswered)); !slices.Equal(got, []string{"100", "200"}) {
		t.Fatalf("answered for %v, want [100 200]", got)
	}
	if got := sorted(ev.of(events.CallEnded)); !slices.Equal(got, []string{"100", "200"}) {
		t.Fatalf("ended for %v, want [100 200]", got)
	}
	if got := ev.of(events.CallMissed); len(got) != 0 {
		t.Fatalf("a call answered by an agent was reported missed for %v", got)
	}
	if e, ok := ev.first(events.CallAnswered, "200"); !ok || e.Data.To.Extension != "200" || e.Data.AnsweredAt.IsZero() {
		t.Fatalf("the agent's call.answered = %+v", e.Data)
	}
}

// TestCallEventsQueueAgentMissed fails if an agent that rang while the
// caller hung up is not told call.missed (cancelled).
func TestCallEventsQueueAgentMissed(t *testing.T) {
	ev := &fakeEvents{}
	st := queue.NewMemStore()
	pbx, _ := queuePBXWith(t, st, nil, []pbxOpt{withEvents(ev)}, testQueue("sales", "600", queue.RingAll, static("200", 0, 1)))
	a, b, _ := registerABC(t, pbx)
	b.setCallee(ringForever())
	r := waitCall(t, dial(t.Context(), a, "600"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, b.invites, "INVITE to the agent")
	eventually(t, "the agent rings", func() bool { return len(ev.of(events.CallRinging)) == 1 })
	hangup(t, r.dcs)
	lastCDRFor(t, pbx, "600")
	eventually(t, "call.missed for the agent", func() bool { return len(ev.of(events.CallMissed)) == 1 })
	if got := ev.of(events.CallMissed); !slices.Equal(got, []string{"200"}) {
		t.Fatalf("missed for %v, want the agent [200]", got)
	}
}

// TestCallEventsIVRExtension fails if a call a menu connects to an extension
// emits only the caller's events: the extension must get call.ringing,
// call.answered and call.ended and the caller call.answered.
func TestCallEventsIVRExtension(t *testing.T) {
	ev := &fakeEvents{}
	pbx, _ := ivrPBXWith(t, 1, []pbxOpt{withEvents(ev)},
		ivrMenu("main", "800", map[string]snapshot.IVRDest{"1": ext("300")}))
	a, _, c := registerABC(t, pbx)
	answer := make(chan struct{})
	c.setCallee(answerAfter(answer))
	r := waitCall(t, dial(t.Context(), a, "800"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	seq := r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, a, r.dcs, "1", &seq)
	waitReq(t, c.invites, "INVITE to the option's extension")
	eventually(t, "call.ringing for the extension", func() bool { return len(ev.of(events.CallRinging)) == 1 })
	close(answer)
	eventually(t, "call.answered for the extension and the caller", func() bool { return len(ev.of(events.CallAnswered)) == 2 })
	if err := r.dcs.Bye(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitReq(t, c.byes, "BYE reaches the extension")
	lastCDRFor(t, pbx, "800")
	eventually(t, "call.ended", func() bool { return len(ev.of(events.CallEnded)) == 2 })
	if got := ev.of(events.CallRinging); !slices.Equal(got, []string{"300"}) {
		t.Fatalf("ringing for %v, want [300]", got)
	}
	if got := sorted(ev.of(events.CallAnswered)); !slices.Equal(got, []string{"100", "300"}) {
		t.Fatalf("answered for %v, want [100 300]", got)
	}
	if got := sorted(ev.of(events.CallEnded)); !slices.Equal(got, []string{"100", "300"}) {
		t.Fatalf("ended for %v, want [100 300]", got)
	}
	if got := ev.of(events.CallMissed); len(got) != 0 {
		t.Fatalf("missed for %v on an answered call", got)
	}
}

// TestEventStateReplicates fails if a call's answering extension, the
// extensions that rang and its event sequence are not carried in its
// replicated dialog, or if a taker does not restore them and continue the
// sequence past the owner's.
func TestEventStateReplicates(t *testing.T) {
	c := &call{ringedExts: []string{"200", "201"}, answeredExt: "200", callerNum: "100", callerDevice: "a1", s: &Server{deps: Deps{Events: &fakeEvents{}}}}
	c.evSeq.Store(7)
	st := c.evState()
	if st == nil || st.AnsweredExt != "200" || st.CallerExt != "100" || !slices.Equal(st.Rung, []string{"200", "201"}) || st.Seq != 7 {
		t.Fatalf("evState = %+v", st)
	}
	taker := &call{}
	taker.restoreEvents(st)
	if taker.answeredExt != "200" || taker.callerExtLocked() != "100" || !slices.Equal(taker.ringedExts, []string{"200", "201"}) || taker.evSeq.Load() != 7+takeoverSeqJump || taker.evLegacy {
		t.Fatalf("restored call = %+v seq %d", taker, taker.evSeq.Load())
	}
	// A record from an older owner has no state: the call stays legacy and
	// replicates none, so it is never reported missed.
	old := &call{s: c.s}
	old.restoreEvents(nil)
	if !old.evLegacy || old.evState() != nil {
		t.Fatal("a call taken over from an older owner must stay legacy")
	}
}

// TestTakeoverKeepsEventState fails if a taken-over call is reported missed
// (the taker did not know who answered), loses call.ended for the answering
// extension, or restarts its event sequence below the owner's.
func TestTakeoverKeepsEventState(t *testing.T) {
	ha := newFakeHA()
	mem := &fakeMembership{}
	evOwner, evTaker := &fakeEvents{}, &fakeEvents{}
	owner := startPBX(t, ringAllDevices(), withNodeID("sip-1"), withHA(ha, mem), withEvents(evOwner))
	taker := startPBX(t, ringAllDevices(), withNodeID("sip-2"), withHA(ha, mem), withEvents(evTaker))
	a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1")
	a.register(t)
	b.register(t)
	b.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, b.invites, "callee INVITE")
	waitReq(t, b.acks, "callee ACK")
	eventually(t, "the owner's call.answered", func() bool { return len(evOwner.of(events.CallAnswered)) == 2 })
	var st livestate.DialogState
	eventually(t, "the event state is replicated", func() bool {
		ha.mu.Lock()
		defer ha.mu.Unlock()
		for _, s := range ha.dialogs {
			if s.OwnerNode == "sip-1" && s.Legs[1].LocalTag != "" && s.Events != nil && s.Events.AnsweredExt == "200" {
				st = s
				return true
			}
		}
		return false
	})
	if st.Events.Seq < 2 {
		t.Fatalf("replicated event state = %+v", st.Events)
	}
	haReinviteTimeout = time.Second
	owner.stop()
	mem.set(
		cluster.Member{ID: "sip-1", Kind: cluster.KindSIP, State: cluster.Offline, ActiveCalls: 1},
		cluster.Member{ID: "sip-2", Kind: cluster.KindSIP, State: cluster.Ready},
	)
	taker.srv.takeoverPass(t.Context())
	ra := waitReq(t, a.reinvites, "takeover re-INVITE to the caller")
	waitReq(t, b.reinvites, "takeover re-INVITE to the callee")
	waitReq(t, a.acks, "ACK to the caller's 200")
	waitReq(t, b.acks, "ACK to the callee's 200")
	eventually(t, "call connected on the taker", func() bool {
		cs := liveCalls(t, taker)
		return len(cs) == 1 && cs[0].State == "connected"
	})
	bye := sip.NewRequest(sip.BYE, sip.Uri{Scheme: "sip", Host: hostOf(taker.addr), Port: portOf(taker.addr)})
	bye.AppendHeader(&sip.FromHeader{Address: *ra.To().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: st.Legs[0].RemoteTag}}})
	bye.AppendHeader(&sip.ToHeader{Address: *ra.From().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: st.Legs[0].LocalTag}}})
	callID := sip.CallIDHeader(st.Legs[0].CallID)
	bye.AppendHeader(&callID)
	bye.AppendHeader(&sip.CSeqHeader{SeqNo: ra.CSeq().SeqNo + 1, MethodName: sip.BYE})
	bye.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	bye.SetTransport("UDP")
	if res := a.do(bye); res.StatusCode != 200 {
		t.Fatalf("BYE to the taker = %d", res.StatusCode)
	}
	waitReq(t, b.byes, "BYE to the callee")
	cd := taker.nextCDR(t)
	eventually(t, "the taker's call.ended", func() bool { return len(evTaker.of(events.CallEnded)) == 2 })
	if got := sorted(evTaker.of(events.CallEnded)); !slices.Equal(got, []string{"100", "200"}) {
		t.Fatalf("taker ended for %v, want [100 200]", got)
	}
	if got := evTaker.of(events.CallMissed); len(got) != 0 {
		t.Fatalf("a taken-over answered call was reported missed for %v", got)
	}
	for _, s := range evTaker.seqOf(cd.CorrelationID) {
		if s <= st.Events.Seq {
			t.Fatalf("the taker's sequence %d does not continue past the owner's %d", s, st.Events.Seq)
		}
	}
}
