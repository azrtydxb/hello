package sip

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/queue"
	"github.com/azrtydxb/hello/internal/snapshot"
)

// withQueues wires the queues' shared state; two PBXs given the same store
// behave as two nodes sharing Valkey.
func withQueues(st queue.Store) pbxOpt {
	return func(_ *Config, d *Deps) { d.Queues = st }
}

// static is a static queue member; position orders the linear strategy.
func static(ext string, penalty, position int) snapshot.QueueMember {
	return snapshot.QueueMember{Extension: ext, Penalty: penalty, Position: position}
}

func dynamic(ext string, position int) snapshot.QueueMember {
	return snapshot.QueueMember{Extension: ext, Dynamic: true, Position: position}
}

// testQueue is a queue with the defaults the tests share: one second to ring
// an agent, no retry delay, a long maximum wait and hang-up failovers.
func testQueue(name, number, strategy string, members ...snapshot.QueueMember) snapshot.Queue {
	hang := snapshot.QueueDest{Kind: snapshot.IVRHangup}
	return snapshot.Queue{
		ID: 1, Name: name, Number: number, Enabled: true, Strategy: strategy,
		AgentTimeout: 1, MaxWait: 60, JoinEmpty: true, Timeout: hang, Full: hang, NoAgents: hang, Members: members,
	}
}

// queuePBX starts a PBX with the queues over the shared store. The
// announcements are one object each, so a test can tell which was fetched.
func queuePBX(t *testing.T, st queue.Store, extra func(s *snapshot.Snapshot), queues ...snapshot.Queue) (*testPBX, *fakeObjects) {
	t.Helper()
	objs := &fakeObjects{}
	anns := map[string]string{}
	for _, n := range []string{"periodic", "pos", "wait", "min", "brk", "d0", "d1", "d2", "d3"} {
		key := "ann/" + n + ".wav"
		_ = objs.Put(context.Background(), key, media.EncodeWAV(make([]byte, 3200), 8000))
		anns[n] = key
	}
	_ = objs.Put(context.Background(), "ann/jazz.wav", media.EncodeWAV(make([]byte, 16000), 8000))
	feats := func(s *snapshot.Snapshot) {
		s.WithAnnouncements(anns)
		s.WithQueues(queues)
		s.WithMOHClasses([]snapshot.MOHClass{{ID: 5, Name: "jazz", Names: []string{"jazz"}, Objects: []string{"ann/jazz.wav"}}})
		s.WithFeatureCodes([]snapshot.FeatureCode{
			{Code: "*45", Action: ActionQueueLogin}, {Code: "*46", Action: ActionQueueLogout},
			{Code: "*47", Action: ActionQueuePause, Argument: "break"}, {Code: "*48", Action: ActionQueueUnpause},
		})
		if extra != nil {
			extra(s)
		}
	}
	pbx := startPBX(t, threeDevices(), shortRing(3*time.Second), withMedia(false), withFeatures(feats),
		withVoicemail(newVMStore(), objs), withQueues(st))
	return pbx, objs
}

func queueLegEvents(r cdr.Record) []string {
	var out []string
	for _, l := range r.QueueLegs {
		out = append(out, l.Event)
	}
	return out
}

// TestQueueRingsAgentAndBridges: a caller dialling the queue's number is
// answered, an agent is rung and the answered call is bridged through the
// relay. It fails if the agent is not rung with the relay's port, the CDR
// loses the queue legs (enter, agent, wait, answered) or the agent as the
// destination, or the BYE does not reach the agent.
func TestQueueRingsAgentAndBridges(t *testing.T) {
	st := queue.NewMemStore()
	pbx, _ := queuePBX(t, st, nil, testQueue("sales", "600", queue.RingAll, static("200", 0, 1)))
	a, b, _ := registerABC(t, pbx)
	b.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "600"))
	if r.err != nil {
		t.Fatalf("call to the queue: %v", r.err)
	}
	inv := waitReq(t, b.invites, "INVITE to the agent")
	if anchoredSDP(t, pbx, inv.Body()).Port == 0 {
		t.Fatal("the agent's offer is not the relay's")
	}
	eventually(t, "answered by the agent", func() bool {
		return pbx.metric(t, "hello_queue_calls_total", map[string]string{"queue": "sales", "event": "answered"}) == 1
	})
	if ws, _ := st.Waiting(t.Context(), "sales"); len(ws) != 0 {
		t.Fatalf("an answered caller still waits: %+v", ws)
	}
	hangup(t, r.dcs)
	waitReq(t, b.byes, "BYE reaches the agent")
	rec := lastCDRFor(t, pbx, "600")
	if rec.FinalStatus != 200 || rec.RewrittenDestination != "200" {
		t.Fatalf("CDR status %d rewritten %q", rec.FinalStatus, rec.RewrittenDestination)
	}
	if got := strings.Join(queueLegEvents(rec), ","); got != "enter,agent,wait,answered" {
		t.Fatalf("queue legs = %s", got)
	}
	if l := rec.QueueLegs[3]; l.Extension != "200" || l.Queue != "sales" {
		t.Fatalf("answered leg = %+v", l)
	}
	if v := pbx.metric(t, "hello_queue_calls_total", map[string]string{"queue": "sales", "event": "answered"}); v != 1 {
		t.Fatalf("answered = %v", v)
	}
	if v := pbx.metric(t, "hello_queue_waiting", map[string]string{"queue": "sales"}); v != 0 {
		t.Fatalf("hello_queue_waiting = %v after the answer", v)
	}
}

// TestQueueAbandon: a caller who hangs up while waiting is removed, the
// agent's ring is cancelled and the abandonment is counted and recorded. It
// fails if the entry stays, the gauge does not return to 0, or the CDR lacks
// the abandon leg.
func TestQueueAbandon(t *testing.T) {
	st := queue.NewMemStore()
	pbx, _ := queuePBX(t, st, nil, testQueue("sales", "600", queue.RingAll, static("200", 0, 1)))
	a, b, _ := registerABC(t, pbx)
	b.setCallee(ringForever())
	r := waitCall(t, dial(t.Context(), a, "600"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, b.invites, "INVITE to the agent")
	eventually(t, "the caller waits", func() bool {
		ws, _ := st.Waiting(t.Context(), "sales")
		return len(ws) == 1 && pbx.metric(t, "hello_queue_waiting", map[string]string{"queue": "sales"}) == 1
	})
	hangup(t, r.dcs)
	waitReq(t, b.cancels, "the agent's ring is cancelled")
	eventually(t, "the entry is gone", func() bool {
		ws, _ := st.Waiting(t.Context(), "sales")
		return len(ws) == 0 && pbx.metric(t, "hello_queue_waiting", map[string]string{"queue": "sales"}) == 0
	})
	if v := pbx.metric(t, "hello_queue_abandoned_total", map[string]string{"queue": "sales"}); v != 1 {
		t.Fatalf("abandoned = %v", v)
	}
	rec := lastCDRFor(t, pbx, "600")
	ev := queueLegEvents(rec)
	if !slices.Contains(ev, cdr.QueueAbandon) || ev[0] != cdr.QueueEnter {
		t.Fatalf("queue legs = %v", ev)
	}
}

// TestQueueLinearAndPenalty: the linear strategy rings the agents one after
// another in position order, and a lower penalty goes first whatever the
// position. It fails if the penalty-5 agent is rung first, or the second
// agent is rung before the first has timed out.
func TestQueueLinearAndPenalty(t *testing.T) {
	st := queue.NewMemStore()
	pbx, _ := queuePBX(t, st, nil, testQueue("sales", "600", queue.Linear, static("200", 5, 1), static("300", 0, 2)))
	a, b, c := registerABC(t, pbx)
	b.setCallee(ringForever())
	c.setCallee(ringForever())
	r := waitCall(t, dial(t.Context(), a, "600"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, c.invites, "the penalty-0 agent rings first")
	noReq(t, b.invites, 600*time.Millisecond, "the penalty-5 agent waits for the first to time out")
	waitReq(t, b.invites, "the second agent rings after the first timed out")
	hangup(t, r.dcs)
}

// TestQueueRingAll: ring_all rings every agent of a penalty tier at once,
// and the one that answers gets the call.
func TestQueueRingAll(t *testing.T) {
	st := queue.NewMemStore()
	pbx, _ := queuePBX(t, st, nil, testQueue("sales", "600", queue.RingAll, static("200", 0, 1), static("300", 0, 2)))
	a, b, c := registerABC(t, pbx)
	b.setCallee(ringForever())
	c.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "600"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, b.invites, "the first agent rings")
	waitReq(t, c.invites, "the second agent rings at the same time")
	eventually(t, "answered", func() bool {
		return pbx.metric(t, "hello_queue_calls_total", map[string]string{"queue": "sales", "event": "answered"}) == 1
	})
	hangup(t, r.dcs)
}

// TestQueueRoundRobinRotates: each new call starts one agent further on.
func TestQueueRoundRobinRotates(t *testing.T) {
	st := queue.NewMemStore()
	pbx, _ := queuePBX(t, st, nil, testQueue("sales", "600", queue.RoundRobin, static("200", 0, 1), static("300", 0, 2)))
	a, b, c := registerABC(t, pbx)
	b.setCallee(answerAfter(nil))
	c.setCallee(answerAfter(nil))
	var first []string
	for i := 0; i < 2; i++ {
		r := waitCall(t, dial(t.Context(), a, "600"))
		if r.err != nil {
			t.Fatal(r.err)
		}
		select {
		case <-b.invites:
			first = append(first, "200")
		case <-c.invites:
			first = append(first, "300")
		case <-time.After(5 * time.Second):
			t.Fatal("no agent rung")
		}
		hangup(t, r.dcs)
		_ = pbx.nextCDR(t)
	}
	if len(first) != 2 || first[0] == first[1] {
		t.Fatalf("agents rung = %v; a rotation must alternate", first)
	}
}

// TestQueueSkipsDNDPausedAndWrapping: agents in DND, paused or wrapping up
// are not rung; the next free one is. Wrap-up ends by itself. It fails if
// any of them gets an INVITE.
func TestQueueSkipsDNDPausedAndWrapping(t *testing.T) {
	st := queue.NewMemStore()
	q := testQueue("sales", "600", queue.Linear, static("200", 0, 1), static("210", 0, 2), static("300", 0, 3))
	q.WrapUp = 2
	pbx, _ := queuePBX(t, st, func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{{Number: "200", DND: true}})
	}, q)
	a, b, c := registerABC(t, pbx)
	d := newPhone(t, pbx, "d1", "pd")
	d.register(t)
	c.setCallee(answerAfter(nil))
	d.setCallee(ringForever())
	ctx := t.Context()
	// 200 is in DND, 210 is paused: 300 is the only free agent.
	if err := st.SetAgent(ctx, queue.AgentState{Ext: "210", LoggedIn: true, Paused: true, Reason: "lunch"}); err != nil {
		t.Fatal(err)
	}
	r := waitCall(t, dial(ctx, a, "600"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, c.invites, "the free agent rings")
	noReq(t, b.invites, 100*time.Millisecond, "an agent in DND is not rung")
	noReq(t, d.invites, 100*time.Millisecond, "a paused agent is not rung")
	hangup(t, r.dcs)
	eventually(t, "the wrap-up is recorded", func() bool {
		s, _ := st.Stats(ctx, "sales")
		return s["300"].LastEndMs != 0
	})
	// Within the wrap-up time the agent gets nothing; afterwards it does.
	if err := st.SetAgent(ctx, queue.AgentState{Ext: "210", LoggedIn: true}); err != nil {
		t.Fatal(err)
	}
	r2 := waitCall(t, dial(ctx, a, "600"))
	if r2.err != nil {
		t.Fatal(r2.err)
	}
	waitReq(t, d.invites, "with the first agent wrapping up the next one rings")
	hangup(t, r2.dcs)
}

// TestQueueTimeoutFailover: after the maximum wait the caller goes to the
// timeout destination. It fails if the destination does not ring, the queue
// entry stays, or the exit is not counted and recorded.
func TestQueueTimeoutFailover(t *testing.T) {
	st := queue.NewMemStore()
	q := testQueue("sales", "600", queue.RingAll, static("200", 0, 1))
	q.MaxWait = 1
	q.Timeout = ext("300")
	pbx, _ := queuePBX(t, st, nil, q)
	a, b, c := registerABC(t, pbx)
	b.setCallee(ringForever())
	c.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "600"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, c.invites, "the timeout destination rings")
	// Hanging up before the destination answers would end the call before
	// the CDR carries the destination (flaky on a loaded runner).
	waitReq(t, c.acks, "the timeout destination is connected")
	if ws, _ := st.Waiting(t.Context(), "sales"); len(ws) != 0 {
		t.Fatalf("the caller still waits: %+v", ws)
	}
	hangup(t, r.dcs)
	rec := lastCDRFor(t, pbx, "600")
	if rec.RewrittenDestination != "300" {
		t.Fatalf("destination = %q, want the failover's 300", rec.RewrittenDestination)
	}
	last := rec.QueueLegs[len(rec.QueueLegs)-1]
	if last.Event != cdr.QueueExit || last.Result != "timeout" {
		t.Fatalf("last queue leg = %+v", last)
	}
	if v := pbx.metric(t, "hello_queue_calls_total", map[string]string{"queue": "sales", "event": "exit_timeout"}); v != 1 {
		t.Fatalf("exit_timeout = %v", v)
	}
}

// TestQueueFullFailover: a queue at its maximum sends the next caller to the
// full destination.
func TestQueueFullFailover(t *testing.T) {
	st := queue.NewMemStore()
	q := testQueue("sales", "600", queue.RingAll, static("200", 0, 1))
	q.MaxCallers = 1
	q.Full = ext("300")
	pbx, _ := queuePBX(t, st, nil, q)
	a, b, c := registerABC(t, pbx)
	d := newPhone(t, pbx, "d1", "pd")
	d.register(t)
	b.setCallee(ringForever())
	c.setCallee(answerAfter(nil))
	r1 := waitCall(t, dial(t.Context(), a, "600"))
	if r1.err != nil {
		t.Fatal(r1.err)
	}
	eventually(t, "the first caller waits", func() bool {
		ws, _ := st.Waiting(t.Context(), "sales")
		return len(ws) == 1
	})
	r2 := waitCall(t, dial(t.Context(), d, "600"))
	if r2.err != nil {
		t.Fatal(r2.err)
	}
	waitReq(t, c.invites, "the full queue sends the second caller to its failover")
	if v := pbx.metric(t, "hello_queue_calls_total", map[string]string{"queue": "sales", "event": "exit_full"}); v != 1 {
		t.Fatalf("exit_full = %v", v)
	}
	hangup(t, r2.dcs)
	hangup(t, r1.dcs)
}

// TestQueueNoAgents: a queue that does not let callers join an empty queue
// sends them to the no-agents destination; once an agent logs in (feature
// code) the caller waits for that agent instead.
func TestQueueNoAgents(t *testing.T) {
	st := queue.NewMemStore()
	q := testQueue("sales", "600", queue.RingAll, dynamic("200", 1))
	q.JoinEmpty = false
	q.NoAgents = ext("300")
	pbx, _ := queuePBX(t, st, nil, q)
	a, b, c := registerABC(t, pbx)
	b.setCallee(answerAfter(nil))
	c.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "600"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, c.invites, "nobody is logged in: the no-agents destination rings")
	hangup(t, r.dcs)

	// The agent logs in from its phone with *45.
	lr := waitCall(t, dial(t.Context(), b, "*45"))
	if lr.err != nil {
		t.Fatalf("login code: %v", lr.err)
	}
	hangup(t, lr.dcs)
	if s, ok, _ := st.Agent(t.Context(), "200"); !ok || !s.LoggedIn {
		t.Fatalf("agent state after *45 = %+v, %v", s, ok)
	}
	r2 := waitCall(t, dial(t.Context(), a, "600"))
	if r2.err != nil {
		t.Fatal(r2.err)
	}
	waitReq(t, b.invites, "the logged-in agent rings")
	hangup(t, r2.dcs)
}

// TestQueueAgentCodes: login (optionally for one queue), pause with a reason,
// unpause and logout through the feature codes. Dialling a login code with
// an unrelated queue number is refused, and so is a non-agent.
func TestQueueAgentCodes(t *testing.T) {
	st := queue.NewMemStore()
	q := testQueue("sales", "600", queue.RingAll, dynamic("200", 1))
	q2 := testQueue("support", "601", queue.RingAll, dynamic("200", 1))
	q2.ID = 2
	pbx, _ := queuePBX(t, st, nil, q, q2)
	_, b, c := registerABC(t, pbx)
	call := func(p *phone, code string) error {
		r := waitCall(t, dial(t.Context(), p, code))
		if r.err == nil {
			hangup(t, r.dcs)
		}
		return r.err
	}
	if err := call(b, "*45601"); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := st.Agent(t.Context(), "200"); !s.LoggedIn || !slices.Equal(s.Queues, []string{"support"}) {
		t.Fatalf("login to one queue: %+v", s)
	}
	if err := call(b, "*45"); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := st.Agent(t.Context(), "200"); !s.LoggedIn || len(s.Queues) != 0 {
		t.Fatalf("login to all: %+v", s)
	}
	if err := call(b, "*47"); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := st.Agent(t.Context(), "200"); !s.Paused || s.Reason != "break" || !s.LoggedIn {
		t.Fatalf("pause: %+v", s)
	}
	if err := call(b, "*48"); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := st.Agent(t.Context(), "200"); s.Paused || s.Reason != "" {
		t.Fatalf("unpause: %+v", s)
	}
	if err := call(b, "*46"); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := st.Agent(t.Context(), "200"); s.LoggedIn {
		t.Fatalf("logout: %+v", s)
	}
	if err := call(c, "*45"); err == nil {
		t.Fatal("a phone that is no agent logged in")
	}
	if err := call(b, "*45999"); err == nil {
		t.Fatal("login to a queue the agent does not belong to was accepted")
	}
}

// TestQueueAnnouncements: at each interval the periodic prompt, the position
// and the estimated wait are played, the numbers digit by digit. It fails if
// a prompt or the digits are not fetched.
func TestQueueAnnouncements(t *testing.T) {
	st := queue.NewMemStore()
	q := testQueue("sales", "600", queue.RingAll, static("200", 0, 1))
	q.AnnounceFrequency, q.AnnouncePosition, q.AnnounceWait = 1, true, true
	q.PeriodicPrompt, q.PositionPrompt, q.WaitPrompt, q.MinutesPrompt, q.DigitsPrefix = "periodic", "pos", "wait", "min", "d"
	q.BreakoutKey, q.BreakoutBox, q.BreakoutPrompt = "1", "200", "brk"
	pbx, objs := queuePBX(t, st, nil, q)
	a, b, _ := registerABC(t, pbx)
	b.setCallee(ringForever())
	r := waitCall(t, dial(t.Context(), a, "600"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	want := []string{"ann/periodic.wav", "ann/pos.wav", "ann/d1.wav", "ann/wait.wav", "ann/min.wav", "ann/brk.wav"}
	eventually(t, "every announcement is played", func() bool {
		got := objs.snapshotGets()
		for _, w := range want {
			if !slices.Contains(got, w) {
				return false
			}
		}
		return true
	})
	hangup(t, r.dcs)
}

// TestNumberPrompts: numbers are spoken digit by digit from prefix0-9.
func TestNumberPrompts(t *testing.T) {
	for n, want := range map[int][]string{0: {"d0"}, 7: {"d7"}, 12: {"d1", "d2"}, 305: {"d3", "d0", "d5"}, -4: {"d0"}} {
		if got := numberPrompts("d", n); !slices.Equal(got, want) {
			t.Errorf("numberPrompts(%d) = %v, want %v", n, got, want)
		}
	}
}

// TestQueueBreakoutVoicemail: pressing the breakout key leaves the queue for
// the voicemail box. It fails if the caller stays queued or the exit is not
// recorded.
func TestQueueBreakoutVoicemail(t *testing.T) {
	st := queue.NewMemStore()
	q := testQueue("sales", "600", queue.RingAll, static("200", 0, 1))
	q.BreakoutKey, q.BreakoutBox = "1", "200"
	objs := &fakeObjects{}
	vm := newVMStore()
	vm.boxes["200"] = VoicemailBox{ID: 5, Extension: "200"}
	feats := func(s *snapshot.Snapshot) {
		s.WithQueues([]snapshot.Queue{q})
		s.WithExtensionData([]snapshot.Extension{{Number: "200", VoicemailEnabled: true, VoicemailBoxID: 5}})
	}
	pbx := startPBX(t, threeDevices(), withMedia(false), withFeatures(feats), withVoicemail(vm, objs), withQueues(st))
	a, b, _ := registerABC(t, pbx)
	b.setCallee(ringForever())
	r := waitCall(t, dial(t.Context(), a, "600"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, b.invites, "the agent rings")
	var seq = r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, a, r.dcs, "1", &seq)
	eventually(t, "the box is looked up", func() bool { vm.mu.Lock(); defer vm.mu.Unlock(); return vm.lookups > 0 })
	eventually(t, "the caller left the queue", func() bool {
		ws, _ := st.Waiting(t.Context(), "sales")
		return len(ws) == 0
	})
	if v := pbx.metric(t, "hello_queue_calls_total", map[string]string{"queue": "sales", "event": "exit_breakout"}); v != 1 {
		t.Fatalf("exit_breakout = %v", v)
	}
	sendDigits(t, a, r.dcs, "#", &seq)
	waitReq(t, a.byes, "the call ends after the message")
}

// TestQueueMusicOnHold: a waiting caller hears the queue's music on hold
// class (its tracks are fetched and played) and the built-in tune when neither
// the queue nor the system names a class. It fails if the class's audio is
// not fetched or no media reaches the caller.
func TestQueueMusicOnHold(t *testing.T) {
	for _, tc := range []struct {
		name  string
		class int64
		want  string
	}{
		{"built-in tune", 0, ""},
		{"class of the queue", 5, "ann/jazz.wav"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := queue.NewMemStore()
			q := testQueue("sales", "600", queue.RingAll, static("200", 0, 1))
			q.MOHClassID = tc.class
			pbx, objs := queuePBX(t, st, nil, q)
			a, b, _ := registerABC(t, pbx)
			b.setCallee(ringForever())
			frames := countPhoneRTP(t, a)
			r := waitCall(t, dial(t.Context(), a, "600"))
			if r.err != nil {
				t.Fatal(r.err)
			}
			eventually(t, "music plays to the caller", func() bool { return frames.Load() > 20 })
			if tc.want != "" && !slices.Contains(objs.snapshotGets(), tc.want) {
				t.Fatalf("the class's track %s was not fetched: %v", tc.want, objs.snapshotGets())
			}
			if tc.want == "" && slices.Contains(objs.snapshotGets(), "ann/jazz.wav") {
				t.Fatal("a track was fetched although no class is set")
			}
			hangup(t, r.dcs)
		})
	}
}

// TestQueueTwoNodesNeverRingTheAgentTwice: two nodes, each holding a waiting
// caller of the same queue, share one agent. While the agent rings for one
// caller the other node must not ring it as well (the agent's lease). It
// fails if the agent's phone gets a second INVITE while the first rings.
func TestQueueTwoNodesNeverRingTheAgentTwice(t *testing.T) {
	st := queue.NewMemStore()
	fs, pr := newFakeState(), newFakePresence()
	q := testQueue("sales", "600", queue.RingAll, static("200", 0, 1))
	q.AgentTimeout = 2
	mk := func(id string) *testPBX {
		objs := &fakeObjects{}
		feats := func(s *snapshot.Snapshot) { s.WithQueues([]snapshot.Queue{q}) }
		share := func(_ *Config, d *Deps) { d.Presence = pr }
		return startPBX(t, threeDevices(), withNodeID(id), withSharedState(fs), share, withMedia(false), withFeatures(feats),
			withVoicemail(newVMStore(), objs), withQueues(st))
	}
	n1, n2 := mk("sip-1"), mk("sip-2")
	eventually(t, "nodes see each other", func() bool { return n1.srv.peers.has(n2.addr) && n2.srv.peers.has(n1.addr) })
	a := newPhone(t, n1, "a1", "pa")
	b := newPhone(t, n1, "b1", "pb1")
	d := newPhone(t, n2, "d1", "pd")
	for _, p := range []*phone{a, b, d} {
		p.register(t)
	}
	b.setCallee(ringForever())
	r1 := waitCall(t, dial(t.Context(), a, "600"))
	if r1.err != nil {
		t.Fatal(r1.err)
	}
	r2 := waitCall(t, dial(t.Context(), d, "600"))
	if r2.err != nil {
		t.Fatal(r2.err)
	}
	waitReq(t, b.invites, "the agent rings for the first caller")
	noReq(t, b.invites, 1200*time.Millisecond, "the agent rings for one caller at a time, across nodes")
	ws, _ := st.Waiting(t.Context(), "sales")
	if len(ws) != 2 || ws[0].Node == ws[1].Node {
		t.Fatalf("waiting = %+v; want one caller on each node", ws)
	}
	hangup(t, r1.dcs)
	hangup(t, r2.dcs)
}

// TestQueueTransferBlind: a blind transfer to the queue's number (a REFER
// through the existing REFER handling) puts the transferred caller in the
// queue; the transferee's leg drops and an agent answers. It fails if the
// transferor gets no 200 NOTIFY, the transferee is not dropped, the agent is
// not rung with the original caller or the transfer is not counted.
func TestQueueTransferBlind(t *testing.T) {
	st := queue.NewMemStore()
	pbx, _ := queuePBX(t, st, nil, testQueue("sales", "600", queue.RingAll, static("300", 0, 1)))
	a, b, c := registerABC(t, pbx)
	b.setCallee(answerAfter(nil))
	c.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, b.invites, "INVITE to b")
	waitReq(t, b.acks, "ACK to b")
	dss := <-serversOf(b)
	if res := b.sendReferFromCallee(t, dss, "<sip:600@"+testDomain+">"); res.StatusCode != 202 {
		t.Fatalf("REFER = %d, want 202", res.StatusCode)
	}
	first := waitReq(t, b.notifies, "NOTIFY to the transferor")
	if !strings.Contains(string(first.Body()), "100 Trying") {
		t.Fatalf("first NOTIFY = %q", first.Body())
	}
	final := waitReq(t, b.notifies, "final NOTIFY to the transferor")
	if !strings.Contains(string(final.Body()), "200 OK") {
		t.Fatalf("final NOTIFY = %q", final.Body())
	}
	waitReq(t, b.byes, "BYE to the transferee's leg")
	inv := waitReq(t, c.invites, "the queue offers the transferred caller to its agent")
	if inv.From().Address.User != "100" {
		t.Fatalf("agent INVITE From = %s, want the original caller 100", inv.From().Address.User)
	}
	eventually(t, "the transferred call is answered", func() bool {
		return pbx.metric(t, "hello_queue_calls_total", map[string]string{"queue": "sales", "event": "answered"}) == 1
	})
	if v := pbx.metric(t, "hello_transfers_total", map[string]string{"kind": "blind", "result": "answered"}); v != 1 {
		t.Fatalf("transfers{blind,answered} = %v", v)
	}
	orig := pbx.nextCDR(t)
	if orig.Destination != "200" || orig.FailureReason != "blind transfer" {
		t.Fatalf("original CDR = %+v", orig)
	}
	hangup(t, r.dcs)
	waitReq(t, c.byes, "BYE reaches the agent")
	rec := lastCDRFor(t, pbx, "600")
	if rec.RewrittenDestination != "300" || !slices.Contains(queueLegEvents(rec), cdr.QueueAnswered) {
		t.Fatalf("queue CDR = %+v", rec)
	}
}

// TestQueueRingGroupFailover: a ring group whose members do not answer falls
// to the queue named as its failure destination.
func TestQueueRingGroupFailover(t *testing.T) {
	st := queue.NewMemStore()
	pbx, _ := queuePBX(t, st, func(s *snapshot.Snapshot) {
		s.WithRingGroups([]snapshot.RingGroup{{ID: 1, Name: "700", Strategy: "ring-all", RingTimeout: 1, MemberDelay: 1,
			FailureKind: "queue", FailureTarget: "sales"}}, [][]snapshot.RingGroupMember{{{ExtensionID: 9, Extension: "200", Position: 1}}})
	}, testQueue("sales", "", queue.RingAll, static("300", 0, 1)))
	a, b, c := registerABC(t, pbx)
	b.setCallee(ringForever())
	c.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "700"))
	if r.err != nil {
		t.Fatalf("the group's failure queue did not answer: %v", r.err)
	}
	waitReq(t, c.invites, "the queue rings its agent after the group failed")
	hangup(t, r.dcs)
}

// TestQueueIVRDestination: an IVR option can send the caller to a queue.
func TestQueueIVRDestination(t *testing.T) {
	st := queue.NewMemStore()
	m := ivrMenu("main", "800", map[string]snapshot.IVRDest{"1": {Kind: snapshot.IVRQueue, Target: "sales"}})
	pbx, _ := queuePBX(t, st, func(s *snapshot.Snapshot) {
		s.WithAnnouncements(map[string]string{"greet": "ann/periodic.wav"})
		s.WithIVRMenus([]snapshot.IVRMenu{m})
	}, testQueue("sales", "", queue.RingAll, static("300", 0, 1)))
	a, _, c := registerABC(t, pbx)
	c.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "800"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	var seq = r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, a, r.dcs, "1", &seq)
	waitReq(t, c.invites, "the queue rings its agent")
	eventually(t, "answered through the queue", func() bool {
		return pbx.metric(t, "hello_queue_calls_total", map[string]string{"queue": "sales", "event": "answered"}) == 1
	})
	hangup(t, r.dcs)
}
