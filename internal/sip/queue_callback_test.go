package sip

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/queue"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/azrtydxb/hello/internal/voiceprompt"
)

// testClock is the callback store's clock: tests move it instead of waiting
// for the 2-minute retry or the 60-second lease.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock { return &testClock{t: time.Now()} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func withCallbacks(cs queue.CallbackStore) pbxOpt {
	return func(_ *Config, d *Deps) { d.Callbacks = cs }
}

func withSpeech(sp Speaker) pbxOpt {
	return func(_ *Config, d *Deps) { d.Speech = sp }
}

// withCallbackSweep sets how often the node sweeps the callback requests.
func withCallbackSweep(every time.Duration) pbxOpt {
	return func(c *Config, _ *Deps) { c.CallbackInterval = every }
}

func withPromptLanguage(l string) pbxOpt {
	return func(c *Config, _ *Deps) { c.PromptLanguage = l }
}

// callbackQueue is the "sales" queue with the callback on. The offer
// threshold is an hour, so only the key (not the estimated wait) offers it,
// unless a test lowers it.
func callbackQueue(members ...snapshot.QueueMember) snapshot.Queue {
	if len(members) == 0 {
		members = []snapshot.QueueMember{dynamic("200", 1)}
	}
	q := testQueue("sales", "600", queue.RingAll, members...)
	q.CallbackEnabled, q.CallbackOfferAfter, q.CallbackDigit = true, 3600, "2"
	return q
}

// callbackPBX starts a PBX with the queue, the callback store and the
// speech stand-in, sweeping every 50 ms.
func callbackPBX(t *testing.T, q snapshot.Queue, st queue.Store, cbs queue.CallbackStore, sp Speaker, opts ...pbxOpt) *testPBX {
	t.Helper()
	oldOffer := callbackOfferCheck
	callbackOfferCheck = 50 * time.Millisecond
	t.Cleanup(func() { callbackOfferCheck = oldOffer })
	feats := func(s *snapshot.Snapshot) { s.WithQueues([]snapshot.Queue{q}) }
	base := []pbxOpt{shortRing(3 * time.Second), withMedia(false), withFeatures(feats),
		withVoicemail(newVMStore(), &fakeObjects{}), withQueues(st), withCallbacks(cbs), withSpeech(sp), withCallbackSweep(50 * time.Millisecond)}
	return startPBX(t, threeDevices(), append(base, opts...)...)
}

// text is the prompt as the queue speaks it.
func text(lang voiceprompt.Lang, k voiceprompt.Key) string { return voiceprompt.Text(lang, k, nil) }

// cdrOfKind returns the next CDR of that kind, dropping the ones before it.
func cdrOfKind(t *testing.T, pbx *testPBX, kind string) cdr.Record {
	t.Helper()
	for i := 0; i < 12; i++ {
		r := pbx.nextCDR(t)
		if k := orDefault(r.Kind, cdr.KindCall); k == kind { // the writer stores an empty kind as call
			return r
		}
	}
	t.Fatalf("no CDR of kind %q", kind)
	return cdr.Record{}
}

func callbackRows(t *testing.T, cbs queue.CallbackStore, queueID int64) []queue.Callback {
	t.Helper()
	rows, err := cbs.List(t.Context(), queueID)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// requestCallback has the phone dial the queue and press the callback key,
// and returns the entry's original join time.
func requestCallback(t *testing.T, st queue.Store, p *phone, hang bool) int64 {
	t.Helper()
	r := waitCall(t, dial(t.Context(), p, "600"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	var entered int64
	eventually(t, "the caller waits", func() bool {
		ws, _ := st.Waiting(t.Context(), "sales")
		for _, e := range ws {
			if e.Caller == callerOf(p) {
				entered = e.EnteredMs
				return true
			}
		}
		return false
	})
	seq := r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, p, r.dcs, "2", &seq)
	if hang {
		waitReq(t, p.byes, "the caller is hung up after the confirmation")
	}
	return entered
}

func callerOf(p *phone) string {
	switch p.user {
	case "a1":
		return "100"
	case "b1":
		return "200"
	case "d1":
		return "210"
	}
	return p.user
}

// TestQueueCallback follows pbx-gaps S-9 and S-10: a caller who presses the
// callback key is hung up and keeps its place by position_key; a second
// request from the number is coalesced; the sweep rings the caller before
// the agent and serves it ahead of a caller who waited less; three failed
// attempts end the request; and a claim whose lease lapsed is taken again.
// It fails if the row is not written with the original enqueue time, a
// request adds a second row, the agent is rung before the caller answered or
// a later caller is served first, an attempt past the third is made, a
// caller who hangs up on the callback is counted as an abandon, or a lapsed
// lease is not reclaimed.
func TestQueueCallback(t *testing.T) {
	en := voiceprompt.English
	t.Run("offered, keeps its place, the caller is rung before the agent", func(t *testing.T) {
		st, cbs, sp := queue.NewMemStore(), queue.NewMemCallbacks(), &fakeSpeaker{}
		clk := newTestClock()
		cbs.Now = clk.Now
		q := callbackQueue()
		pbx := callbackPBX(t, q, st, cbs, sp)
		a, b, d := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1"), newPhone(t, pbx, "d1", "pd")
		for _, p := range []*phone{a, b, d} {
			p.register(t)
		}
		chA := make(chan struct{})
		a.setCallee(answerAfter(chA)) // the customer's phone rings until the test answers
		b.setCallee(answerAfter(nil))

		entered := requestCallback(t, st, a, true)
		eventually(t, "the request is recorded", func() bool { return len(callbackRows(t, cbs, q.ID)) == 1 })
		row := callbackRows(t, cbs, q.ID)[0]
		if row.Number != "100" || row.Status != queue.CallbackWaiting || row.Attempts != 0 || row.PositionKey != entered {
			t.Fatalf("request = %+v, want number 100, waiting, no attempt, position key %d", row, entered)
		}
		if got := row.ExpiresAt.Sub(row.CreatedAt); got != queue.CallbackTTL {
			t.Fatalf("the request expires after %s, want %s", got, queue.CallbackTTL)
		}
		if sp.n(text(en, voiceprompt.QueueCallbackConfirm)) != 1 {
			t.Fatalf("the confirmation was not spoken: %v", sp.texts())
		}
		if ws, _ := st.Waiting(t.Context(), "sales"); len(ws) != 0 {
			t.Fatalf("a caller who asked for a callback still waits: %+v", ws)
		}
		first := cdrOfKind(t, pbx, cdr.KindCall)
		if got := strings.Join(queueLegEvents(first), ","); got != "enter,callback_requested,wait,exit" {
			t.Fatalf("the original call's queue legs = %s", got)
		}
		if l := first.QueueLegs[1]; l.Result != "accepted" || l.Queue != "sales" {
			t.Fatalf("callback_requested leg = %+v", l)
		}
		// No agent is logged in: nothing is dialled.
		noReq(t, a.invites, 300*time.Millisecond, "a callback with no agent staffed")

		// A caller who joins after the request, then an agent logs in.
		rd := waitCall(t, dial(t.Context(), d, "600"))
		if rd.err != nil {
			t.Fatal(rd.err)
		}
		eventually(t, "the later caller waits", func() bool {
			ws, _ := st.Waiting(t.Context(), "sales")
			return len(ws) == 1 && ws[0].EnteredMs > entered
		})
		if err := st.SetAgent(t.Context(), queue.AgentState{Ext: "200", LoggedIn: true}); err != nil {
			t.Fatal(err)
		}
		inv := waitReq(t, a.invites, "the callback rings the caller")
		if from := inv.From().Address.User; from != "600" {
			t.Fatalf("the callback comes from %q, want the queue number 600", from)
		}
		if to := inv.To().Address.User; to != "100" {
			t.Fatalf("the callback is to %q, want the caller's number 100", to)
		}
		eventually(t, "the request is claimed", func() bool {
			rows := callbackRows(t, cbs, q.ID)
			return rows[0].Status == queue.CallbackDialling && rows[0].Attempts == 1 && rows[0].ClaimedBy == "sip-test"
		})
		// The caller is rung first: the free agent is not rung while the
		// caller's phone is still ringing, and the later caller does not
		// take it either.
		noReq(t, b.invites, 900*time.Millisecond, "an agent rung before the caller answered")
		close(chA)
		inv = waitReq(t, b.invites, "the agent rings once the caller answered")
		if from := inv.From().Address.User; from != "100" {
			t.Fatalf("the agent sees %q, want the called-back caller 100 (not the later caller 210)", from)
		}
		eventually(t, "the request is connected", func() bool {
			rows := callbackRows(t, cbs, q.ID)
			return rows[0].Status == queue.CallbackConnected && rows[0].Agent == "200"
		})
		if ws, _ := st.Waiting(t.Context(), "sales"); len(ws) != 1 || ws[0].Caller != "210" {
			t.Fatalf("the later caller must still wait: %+v", ws)
		}
		if v := pbx.metric(t, "hello_callback_total", map[string]string{"result": "connected"}); v != 1 {
			t.Fatalf("hello_callback_total{connected} = %v", v)
		}
		dss := <-serversOf(a)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if err := dss.Bye(ctx); err != nil {
			t.Fatal(err)
		}
		waitReq(t, b.byes, "the caller's hang-up reaches the agent")
		rec := cdrOfKind(t, pbx, cdr.KindCallback)
		if got := strings.Join(queueLegEvents(rec), ","); got != "callback_dialled,enter,agent,wait,answered,callback_connected" {
			t.Fatalf("callback CDR queue legs = %s", got)
		}
		if rec.Destination != "600" || rec.Source != "100" || rec.FinalStatus != 200 {
			t.Fatalf("callback CDR = %s -> %s (%d)", rec.Source, rec.Destination, rec.FinalStatus)
		}
		hangup(t, rd.dcs)
	})

	t.Run("a second request from the number is coalesced", func(t *testing.T) {
		st, cbs, sp := queue.NewMemStore(), queue.NewMemCallbacks(), &fakeSpeaker{}
		q := callbackQueue()
		pbx := callbackPBX(t, q, st, cbs, sp)
		a := newPhone(t, pbx, "a1", "pa")
		a.register(t)
		first := requestCallback(t, st, a, true)
		second := requestCallback(t, st, a, true)
		if second <= first {
			t.Fatalf("the second call joined at %d, not after %d", second, first)
		}
		rows := callbackRows(t, cbs, q.ID)
		if len(rows) != 1 || rows[0].PositionKey != first {
			t.Fatalf("rows = %+v, want one request that keeps the first place %d", rows, first)
		}
		for _, label := range []string{"requested", "coalesced"} {
			if v := pbx.metric(t, "hello_callback_total", map[string]string{"result": label}); v != 1 {
				t.Fatalf("hello_callback_total{%s} = %v, want 1", label, v)
			}
		}
		cdrOfKind(t, pbx, cdr.KindCall)
		second2 := cdrOfKind(t, pbx, cdr.KindCall)
		if l := second2.QueueLegs[1]; l.Event != cdr.QueueCallbackRequested || l.Result != "coalesced" {
			t.Fatalf("second request's leg = %+v", l)
		}
	})

	t.Run("three failed attempts end the request", func(t *testing.T) {
		st, cbs, sp := queue.NewMemStore(), queue.NewMemCallbacks(), &fakeSpeaker{}
		clk := newTestClock()
		cbs.Now = clk.Now
		q := callbackQueue()
		pbx := callbackPBX(t, q, st, cbs, sp)
		a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
		a.register(t)
		b.register(t)
		a.setCallee(busy())
		b.setCallee(answerAfter(nil))
		requestCallback(t, st, a, true)
		if err := st.SetAgent(t.Context(), queue.AgentState{Ext: "200", LoggedIn: true}); err != nil {
			t.Fatal(err)
		}
		for attempt := 1; attempt <= queue.CallbackMaxAttempts; attempt++ {
			waitReq(t, a.invites, "a callback attempt")
			attempt := attempt
			eventually(t, "the attempt is recorded", func() bool {
				r := callbackRows(t, cbs, q.ID)[0]
				if attempt < queue.CallbackMaxAttempts {
					return r.Status == queue.CallbackWaiting && r.Attempts == attempt && r.Result == "busy"
				}
				return r.Status == queue.CallbackFailed && r.Attempts == attempt && r.Result == "busy"
			})
			if attempt < queue.CallbackMaxAttempts {
				noReq(t, a.invites, 300*time.Millisecond, "an attempt before the retry delay")
				clk.Advance(queue.CallbackRetryAfter + time.Second)
			}
		}
		clk.Advance(queue.CallbackRetryAfter + time.Second)
		noReq(t, a.invites, 400*time.Millisecond, "a fourth attempt")
		noReq(t, b.invites, 10*time.Millisecond, "the agent rung for a caller who never answered")
		if v := pbx.metric(t, "hello_callback_total", map[string]string{"result": "busy"}); v != 3 {
			t.Fatalf("hello_callback_total{busy} = %v, want 3", v)
		}
		cdrOfKind(t, pbx, cdr.KindCall) // the original call
		for i := 1; i <= 3; i++ {
			rec := cdrOfKind(t, pbx, cdr.KindCallback)
			if got := strings.Join(queueLegEvents(rec), ","); got != "callback_dialled,callback_failed" {
				t.Fatalf("attempt %d queue legs = %s", i, got)
			}
			if rec.FinalStatus != 486 || rec.QueueLegs[1].Result != "busy" {
				t.Fatalf("attempt %d: status %d, leg %+v", i, rec.FinalStatus, rec.QueueLegs[1])
			}
		}
	})

	t.Run("a caller who hangs up on the callback is a failed attempt, not an abandon", func(t *testing.T) {
		st, cbs, sp := queue.NewMemStore(), queue.NewMemCallbacks(), &fakeSpeaker{}
		q := callbackQueue()
		pbx := callbackPBX(t, q, st, cbs, sp)
		a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
		a.register(t)
		b.register(t)
		a.setCallee(answerAfter(nil))
		b.setCallee(ringForever())
		requestCallback(t, st, a, true)
		if err := st.SetAgent(t.Context(), queue.AgentState{Ext: "200", LoggedIn: true}); err != nil {
			t.Fatal(err)
		}
		waitReq(t, a.invites, "the callback rings the caller")
		waitReq(t, b.invites, "the agent is rung once the caller answered")
		dss := <-serversOf(a)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if err := dss.Bye(ctx); err != nil {
			t.Fatal(err)
		}
		eventually(t, "the attempt failed and waits for the next one", func() bool {
			r := callbackRows(t, cbs, q.ID)[0]
			return r.Status == queue.CallbackWaiting && r.Attempts == 1 && r.Result == "abandoned"
		})
		cdrOfKind(t, pbx, cdr.KindCall)
		rec := cdrOfKind(t, pbx, cdr.KindCallback)
		events := queueLegEvents(rec)
		if slices.Contains(events, "abandon") || !slices.Contains(events, "callback_failed") {
			t.Fatalf("queue legs = %v, want a callback_failed and no abandon", events)
		}
		if v := pbx.metric(t, "hello_queue_abandoned_total", map[string]string{"queue": "sales"}); v != 0 {
			t.Fatalf("hello_queue_abandoned_total = %v for a callback", v)
		}
	})

	t.Run("a claim whose lease lapsed is taken again", func(t *testing.T) {
		st, cbs, sp := queue.NewMemStore(), queue.NewMemCallbacks(), &fakeSpeaker{}
		clk := newTestClock()
		cbs.Now = clk.Now
		q := callbackQueue()
		pbx := callbackPBX(t, q, st, cbs, sp)
		a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
		a.register(t)
		b.register(t)
		a.setCallee(answerAfter(nil))
		b.setCallee(answerAfter(nil))
		if err := st.SetAgent(t.Context(), queue.AgentState{Ext: "200", LoggedIn: true}); err != nil {
			t.Fatal(err)
		}
		// Another node claimed the request and died before the caller
		// answered: the row is dialling under its lease.
		if _, _, err := cbs.Request(t.Context(), q.ID, "100", "", clk.Now().Add(-time.Minute).UnixMilli()); err != nil {
			t.Fatal(err)
		}
		cb, ok, err := cbs.Claim(t.Context(), q.ID, "sip-dead")
		if err != nil || !ok || cb.Attempts != 1 {
			t.Fatalf("claim = %+v %v %v", cb, ok, err)
		}
		noReq(t, a.invites, 400*time.Millisecond, "a callback while the other node's lease holds")
		clk.Advance(queue.CallbackLease + time.Second)
		waitReq(t, a.invites, "the lapsed claim is taken again")
		eventually(t, "the request is connected by this node", func() bool {
			r := callbackRows(t, cbs, q.ID)[0]
			return r.Status == queue.CallbackConnected && r.Attempts == 2
		})
		waitReq(t, b.invites, "the agent rings once the caller answered")
	})

	t.Run("the queue's language is spoken", func(t *testing.T) {
		for _, tc := range []struct {
			name, queueLang, global string
			want                    voiceprompt.Lang
		}{
			{"the queue's own language", "nl", "", voiceprompt.Dutch},
			{"inherited from the deployment", "", "nl", voiceprompt.Dutch},
			{"the queue overrides the deployment", "en", "nl", voiceprompt.English},
			{"English by default", "", "", voiceprompt.English},
		} {
			t.Run(tc.name, func(t *testing.T) {
				st, cbs, sp := queue.NewMemStore(), queue.NewMemCallbacks(), &fakeSpeaker{}
				q := callbackQueue()
				q.PromptLanguage = tc.queueLang
				pbx := callbackPBX(t, q, st, cbs, sp, withPromptLanguage(tc.global))
				a := newPhone(t, pbx, "a1", "pa")
				a.register(t)
				requestCallback(t, st, a, true)
				if sp.n(text(tc.want, voiceprompt.QueueCallbackConfirm)) != 1 {
					t.Fatalf("the confirmation was not spoken in %s: %v", tc.want, sp.texts())
				}
				if tc.want == voiceprompt.Dutch && sp.n(text(voiceprompt.English, voiceprompt.QueueCallbackConfirm)) != 0 {
					t.Fatal("the English confirmation was spoken too")
				}
			})
		}
	})

	t.Run("the estimated wait offers it", func(t *testing.T) {
		st, cbs, sp := queue.NewMemStore(), queue.NewMemCallbacks(), &fakeSpeaker{}
		q := callbackQueue()
		q.CallbackOfferAfter = 1 // nobody is staffed: the time waited stands in for the estimate
		pbx := callbackPBX(t, q, st, cbs, sp)
		a := newPhone(t, pbx, "a1", "pa")
		a.register(t)
		r := waitCall(t, dial(t.Context(), a, "600"))
		if r.err != nil {
			t.Fatal(r.err)
		}
		offer := voiceprompt.CallbackOffer(voiceprompt.English, "2")
		eventually(t, "the offer is spoken", func() bool { return sp.n(offer) == 1 })
		time.Sleep(300 * time.Millisecond)
		if n := sp.n(offer); n != 1 {
			t.Fatalf("the offer was spoken %d times, want once", n)
		}
		seq := r.dcs.InviteRequest.CSeq().SeqNo + 10
		sendDigits(t, a, r.dcs, "2", &seq) // accepting the spoken offer
		waitReq(t, a.byes, "the caller is hung up after the confirmation")
		if len(callbackRows(t, cbs, q.ID)) != 1 {
			t.Fatal("the offer was not accepted")
		}
	})

	t.Run("the callback is not offered when it is off", func(t *testing.T) {
		st, cbs, sp := queue.NewMemStore(), queue.NewMemCallbacks(), &fakeSpeaker{}
		q := callbackQueue()
		q.CallbackEnabled = false
		pbx := callbackPBX(t, q, st, cbs, sp)
		a := newPhone(t, pbx, "a1", "pa")
		a.register(t)
		r := waitCall(t, dial(t.Context(), a, "600"))
		if r.err != nil {
			t.Fatal(r.err)
		}
		eventually(t, "the caller waits", func() bool {
			ws, _ := st.Waiting(t.Context(), "sales")
			return len(ws) == 1
		})
		seq := r.dcs.InviteRequest.CSeq().SeqNo + 10
		sendDigits(t, a, r.dcs, "2", &seq)
		time.Sleep(300 * time.Millisecond)
		if rows := callbackRows(t, cbs, q.ID); len(rows) != 0 {
			t.Fatalf("a request was taken with the callback off: %+v", rows)
		}
		if ws, _ := st.Waiting(t.Context(), "sales"); len(ws) != 1 {
			t.Fatalf("the caller left: %+v", ws)
		}
	})
}

// TestQueueCallbackSurvivesNodeFailover: the node that called the customer
// back dies while the customer waits on the line for an agent. A survivor
// takes the call over (the customer is re-INVITEd onto its relay), adopts
// the request, keeps the customer at its original place and connects it to
// the agent that becomes free. It fails if the request is not replicated
// with the call, the survivor does not adopt it (ClaimedBy stays the dead
// node), the customer is rung again, its place changes, or the agent is not
// connected through the survivor.
func TestQueueCallbackSurvivesNodeFailover(t *testing.T) {
	st, cbs, sp := queue.NewMemStore(), queue.NewMemCallbacks(), &fakeSpeaker{}
	clk := newTestClock()
	cbs.Now = clk.Now
	q := callbackQueue()
	feats := func(s *snapshot.Snapshot) { s.WithQueues([]snapshot.Queue{q}) }
	// Only the first node sweeps, so it is the one that rings the customer.
	sweep := func(c *Config, _ *Deps) {
		c.CallbackInterval = time.Hour
		if c.NodeID == "sip-1" {
			c.CallbackInterval = 50 * time.Millisecond
		}
	}
	ha, mem, owner, taker := haPair(t, threeDevices(), withSharedState(newFakeState()), withMedia(false), withFeatures(feats),
		withVoicemail(newVMStore(), &fakeObjects{}), withQueues(st), withCallbacks(cbs), withSpeech(sp), sweep)
	a := newPhone(t, owner, "a1", "pa")
	b := newPhone(t, taker, "b1", "pb1")
	a.register(t)
	b.register(t)
	chA := make(chan struct{})
	a.setCallee(answerAfter(chA))
	b.setCallee(answerAfter(nil))

	positionKey := clk.Now().Add(-time.Minute).UnixMilli()
	if _, _, err := cbs.Request(t.Context(), q.ID, "100", "", positionKey); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAgent(t.Context(), queue.AgentState{Ext: "200", LoggedIn: true}); err != nil {
		t.Fatal(err)
	}
	inv := waitReq(t, a.invites, "the owner rings the customer")
	// The agent pauses while the customer answers, so the customer waits on
	// the line for one.
	if err := st.SetAgent(t.Context(), queue.AgentState{Ext: "200", LoggedIn: true, Paused: true}); err != nil {
		t.Fatal(err)
	}
	close(chA)
	waitReq(t, a.acks, "the customer's answer is acknowledged")
	eventually(t, "the customer waits at its original place", func() bool {
		ws, _ := st.Waiting(t.Context(), "sales")
		return len(ws) == 1 && ws[0].Caller == "100" && ws[0].EnteredMs == positionKey && ws[0].Node == "sip-1"
	})
	rec := waitRecord(t, ha, "the callback call is replicated with its request", "sip-1", func(s livestate.DialogState) bool {
		return s.State == haPhaseQueue && strings.HasPrefix(s.StateDetail, "queue:sales:") && strings.Contains(s.StateDetail, ":cb")
	})
	if _, _, id := parseQueueDetail(rec.StateDetail); id != 1 {
		t.Fatalf("replicated detail %q names request %d, want 1", rec.StateDetail, id)
	}
	if rec.Legs[0].CallID != inv.CallID().Value() {
		t.Fatalf("the replicated dialog is %s, want the customer's %s", rec.Legs[0].CallID, inv.CallID().Value())
	}

	haReinviteTimeout = time.Second
	killNode(owner, mem, map[string]int{"sip-1": 1}, "sip-2")
	taker.srv.takeoverPass(t.Context())
	re := reinviteOn(t, a, inv.CallID().Value(), "the survivor re-INVITEs the customer")
	anchoredSDP(t, taker, re.Body())
	eventually(t, "the customer waits on the survivor at the same place", func() bool {
		ws, _ := st.Waiting(t.Context(), "sales")
		return len(ws) == 1 && ws[0].EnteredMs == positionKey && ws[0].Node == "sip-2"
	})
	eventually(t, "the survivor adopts the request", func() bool {
		r := callbackRows(t, cbs, q.ID)[0]
		return r.Status == queue.CallbackDialling && r.ClaimedBy == "sip-2"
	})
	noReq(t, a.invites, 300*time.Millisecond, "the customer rung again after the takeover")

	// The agent becomes free: the survivor connects the waiting customer.
	if err := st.SetAgent(t.Context(), queue.AgentState{Ext: "200", LoggedIn: true}); err != nil {
		t.Fatal(err)
	}
	ag := waitReq(t, b.invites, "the survivor offers the customer to the agent")
	if ag.From().Address.User != "100" {
		t.Fatalf("agent INVITE From = %s, want the customer 100", ag.From().Address.User)
	}
	eventually(t, "the request is connected through the survivor", func() bool {
		r := callbackRows(t, cbs, q.ID)[0]
		return r.Status == queue.CallbackConnected && r.Agent == "200"
	})
	if v := taker.metric(t, "hello_callback_total", map[string]string{"result": "connected"}); v != 1 {
		t.Fatalf("hello_callback_total{connected} on the survivor = %v", v)
	}
}

func TestCallbackNumber(t *testing.T) {
	for in, want := range map[string]string{
		"100": "100", "+32471234567": "+32471234567", "0471 23 45 67": "0471234567", "+32 (0) 471-23": "+32047123",
		"anonymous": "", "": "", "1": "", "sip:x": "", "+": "", "12345678901234567890123": "",
	} {
		got, ok := callbackNumber(in)
		if ok != (want != "") || (ok && got != want) {
			t.Errorf("callbackNumber(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestQueueDetailRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entered int64
		cb      int64
		want    string
	}{
		{"sales", 0, 0, "queue:sales:0"},
		{"sales", 1700000000123, 0, "queue:sales:1700000000123"},
		{"sales", 1700000000123, 42, "queue:sales:1700000000123:cb42"},
		{"a.b-c_1", 5, 7, "queue:a.b-c_1:5:cb7"},
	} {
		d := queueDetail(tc.name, tc.entered, tc.cb)
		if d != tc.want {
			t.Errorf("queueDetail = %q, want %q", d, tc.want)
		}
		n, e, c := parseQueueDetail(d)
		if n != tc.name || e != tc.entered || c != tc.cb {
			t.Errorf("parseQueueDetail(%q) = %q %d %d", d, n, e, c)
		}
	}
}
