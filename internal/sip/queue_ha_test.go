package sip

import (
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/queue"
	"github.com/azrtydxb/hello/internal/snapshot"
)

// TestQueueSurvivesNodeFailover follows .procoder/specs/incall-ha.md for
// callers waiting in a queue: the node holding two waiting callers dies, a
// survivor takes both over (re-INVITE onto its own relay) and each caller
// keeps its place, with the original join time. It fails if the survivor does
// not re-INVITE the callers, the queue order or the join times change, an
// agent who logs in afterwards is not offered the caller that waited longest,
// or the taken-over call is not answered through the survivor.
func TestQueueSurvivesNodeFailover(t *testing.T) {
	st := queue.NewMemStore()
	fs := newFakeState()
	q := testQueue("sales", "600", queue.RingAll, dynamic("200", 1))
	feats := func(s *snapshot.Snapshot) { s.WithQueues([]snapshot.Queue{q}) }
	ha, mem, owner, taker := haPair(t, threeDevices(), withSharedState(fs), withMedia(false), withFeatures(feats),
		withVoicemail(newVMStore(), &fakeObjects{}), withQueues(st))
	a := newPhone(t, owner, "a1", "pa")
	b := newPhone(t, taker, "b1", "pb1") // registered through the survivor: the dead node was no edge for it
	d := newPhone(t, owner, "d1", "pd")
	for _, p := range []*phone{a, b, d} {
		p.register(t)
	}
	b.setCallee(answerAfter(nil))
	ra := waitCall(t, dial(t.Context(), a, "600"))
	if ra.err != nil {
		t.Fatal(ra.err)
	}
	time.Sleep(20 * time.Millisecond)
	rd := waitCall(t, dial(t.Context(), d, "600"))
	if rd.err != nil {
		t.Fatal(rd.err)
	}
	eventually(t, "both callers wait", func() bool {
		ws, _ := st.Waiting(t.Context(), "sales")
		return len(ws) == 2
	})
	before, _ := st.Waiting(t.Context(), "sales")
	if len(before) != 2 || before[0].Caller != "100" || before[1].Caller != "210" {
		t.Fatalf("waiting before the failover = %+v", before)
	}
	// The queued call is replicated with its place in the queue.
	waitRecord(t, ha, "the queued call is replicated", "sip-1", func(s livestate.DialogState) bool {
		return s.State == haPhaseQueue && s.Legs[1].CallID == "" && strings.HasPrefix(s.StateDetail, "queue:sales:") &&
			!strings.HasSuffix(s.StateDetail, ":0")
	})
	haReinviteTimeout = time.Second
	killNode(owner, mem, map[string]int{"sip-1": 2}, "sip-2")
	taker.srv.takeoverPass(t.Context())
	raFirst := waitReq(t, a.reinvites, "takeover re-INVITE to the first caller")
	anchoredSDP(t, taker, raFirst.Body())
	anchoredSDP(t, taker, waitReq(t, d.reinvites, "takeover re-INVITE to the second caller").Body())

	eventually(t, "the callers wait on the survivor in the same order", func() bool {
		ws, _ := st.Waiting(t.Context(), "sales")
		return len(ws) == 2 && ws[0].Caller == "100" && ws[1].Caller == "210" && ws[0].Node == "sip-2" && ws[1].Node == "sip-2"
	})
	after, _ := st.Waiting(t.Context(), "sales")
	if after[0].EnteredMs != before[0].EnteredMs || after[1].EnteredMs != before[1].EnteredMs {
		t.Fatalf("join times changed: %d,%d -> %d,%d", before[0].EnteredMs, before[1].EnteredMs, after[0].EnteredMs, after[1].EnteredMs)
	}
	// An agent logs in: the caller who waited longest is offered first, by
	// the survivor.
	if err := st.SetAgent(t.Context(), queue.AgentState{Ext: "200", LoggedIn: true}); err != nil {
		t.Fatal(err)
	}
	inv := waitReq(t, b.invites, "the survivor offers the longest-waiting caller to the agent")
	if inv.From().Address.User != "100" {
		t.Fatalf("agent INVITE From = %s, want the first caller 100", inv.From().Address.User)
	}
	eventually(t, "answered through the survivor", func() bool {
		return taker.metric(t, "hello_queue_calls_total", map[string]string{"queue": "sales", "event": "answered"}) >= 1
	})
	if ws, _ := st.Waiting(t.Context(), "sales"); len(ws) != 1 || ws[0].Caller != "210" {
		t.Fatalf("waiting after the first answer = %+v", ws)
	}
	// The answered call is an ordinary taken-over call: it replicates with
	// both legs and the caller's BYE reaches the agent.
	rec := waitRecord(t, ha, "the bridged call replicates from the survivor", "sip-2", func(s livestate.DialogState) bool {
		return s.State == "talking" && s.Legs[0].CallID == raFirst.CallID().Value() && s.Legs[1].LocalTag != ""
	})
	waitReq(t, b.acks, "the agent's leg is confirmed")
	hangupHomed(t, a, taker, raFirst, rec.Legs[0])
	waitReq(t, b.byes, "the caller's BYE reaches the agent")
}

// TestQueueTransferAttended: the transferor consults the queue (its second
// call waits there) and completes an attended transfer with Replaces. The
// transferee takes the consultation call's place in the queue and the
// consultation call ends. It fails if the transferee is not rung to an agent
// as the original caller, the consultation dialog stays up, or the transfer
// is not counted as an attended one.
func TestQueueTransferAttended(t *testing.T) {
	st := queue.NewMemStore()
	pbx, _ := queuePBX(t, st, nil, testQueue("sales", "600", queue.RingAll, static("300", 0, 1)))
	a, b, c := registerABC(t, pbx)
	b.setCallee(answerAfter(nil))
	answer := make(chan struct{})
	c.setCallee(answerAfter(answer))
	r1 := waitCall(t, dial(t.Context(), a, "200"))
	if r1.err != nil {
		t.Fatal(r1.err)
	}
	waitReq(t, b.invites, "INVITE to b")
	waitReq(t, b.acks, "ACK to b")
	dssAB := <-serversOf(b)
	// The consultation call: b waits in the queue.
	r2 := waitCall(t, dial(t.Context(), b, "600"))
	if r2.err != nil {
		t.Fatal(r2.err)
	}
	waitReq(t, c.invites, "the agent rings for the consultation call")
	refer := "<sip:600@" + testDomain + "?Replaces=" + r2.dcs.InviteRequest.CallID().Value() + ">"
	if res := b.sendReferFromCallee(t, dssAB, refer); res.StatusCode != 202 {
		t.Fatalf("REFER = %d, want 202", res.StatusCode)
	}
	waitReq(t, b.notifies, "progress NOTIFY")
	final := waitReq(t, b.notifies, "final NOTIFY")
	if !strings.Contains(string(final.Body()), "200 OK") {
		t.Fatalf("final NOTIFY = %q", final.Body())
	}
	waitReq(t, b.byes, "the consultation call ends")
	inv := waitReq(t, c.invites, "the agent rings for the transferee")
	if inv.From().Address.User != "100" {
		t.Fatalf("agent INVITE From = %s, want the transferee 100", inv.From().Address.User)
	}
	close(answer)
	eventually(t, "the transferee is answered by the agent", func() bool {
		return pbx.metric(t, "hello_queue_calls_total", map[string]string{"queue": "sales", "event": "answered"}) == 1
	})
	if v := pbx.metric(t, "hello_transfers_total", map[string]string{"kind": "attended", "result": "answered"}); v != 1 {
		t.Fatalf("transfers{attended,answered} = %v", v)
	}
	hangup(t, r1.dcs)
}
