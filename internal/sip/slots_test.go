package sip

import (
	"context"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/routing"
	"github.com/emiago/sipgo"
)

// inboundDial places a carrier-style INVITE from p to the DID and waits for
// the final answer, ACKing a 2xx.
func inboundDial(t *testing.T, pbx *testPBX, p *phone, did string) (*sipgo.DialogClientSession, error) {
	t.Helper()
	dcs, err := p.dua.WriteInvite(t.Context(), carrierInvite(p, pbx, did))
	if err != nil {
		t.Fatal(err)
	}
	if err := dcs.WaitAnswer(t.Context(), sipgo.AnswerOptions{}); err != nil {
		return dcs, err
	}
	return dcs, dcs.Ack(t.Context())
}

// TestInboundTrunkAtCapacity fails if an inbound call does not count
// against its source trunk's max_calls, or if its slot outlives the call.
func TestInboundTrunkAtCapacity(t *testing.T) {
	in := routing.Trunk{ID: 7, Name: "carrier-in", Mode: "ip", Enabled: true, MaxCalls: 1}
	r := &fakeRouter{extensions: map[string]bool{"100": true}, trunks: []routing.Trunk{in}}
	r.decide = func(routing.Call, routing.TrunkUsability) routing.Decision {
		return routing.Decision{Kind: routing.KindInbound, Extension: "100", Route: "Main DID"}
	}
	st := newFakeTrunkState()
	pbx := startPBX(t, callerDevices(), trunkCfg(st), withRouter(r, nil))
	callee := newPhone(t, pbx, "a1", "pa")
	callee.register(t)
	from := newPhone(t, pbx, "carrier", "")
	r.setSource(mustAddr(t, from.addr), 7)

	first, err := inboundDial(t, pbx, from, "+97140000100")
	if err != nil {
		t.Fatalf("first inbound call: %v", err)
	}
	waitReq(t, callee.acks, "ACK")
	if st.activeCalls(7) != 1 {
		t.Fatalf("source trunk slots = %d, want 1", st.activeCalls(7))
	}
	if _, err := inboundDial(t, pbx, from, "+97140000100"); responseCode(err) != 503 {
		t.Fatalf("second inbound call over max_calls = %v, want 503", err)
	}
	wantTrace(t, pbx.nextCDR(t).Trace, "Trunk carrier-in is at capacity")
	hangup(t, first)
	pbx.nextCDR(t)
	eventually(t, "source slot released", func() bool { return st.activeCalls(7) == 0 })
	if _, err := inboundDial(t, pbx, from, "+97140000100"); err != nil {
		t.Fatalf("inbound call after the first ended: %v", err)
	}
}

// TestInboundToExternalHoldsBothSlots fails if a call forwarded from one
// trunk to another does not hold a slot on each, or keeps either after it
// ends.
func TestInboundToExternalHoldsBothSlots(t *testing.T) {
	out := newCarrier(t, "acct", "pw")
	in := routing.Trunk{ID: 7, Name: "carrier-in", Mode: "ip", Enabled: true}
	r := &fakeRouter{trunks: []routing.Trunk{in, out.trunk(1, "carrier-out", "ip")}}
	r.decide = outboundVia(r, "+971501234567", "+971555000111", false, nil, 1)
	st := newFakeTrunkState()
	pbx := startPBX(t, callerDevices(), trunkCfg(st), withRouter(r, nil))
	from := newPhone(t, pbx, "carrier", "")
	r.setSource(mustAddr(t, from.addr), 7)
	dcs, err := inboundDial(t, pbx, from, "+97140000199")
	if err != nil {
		t.Fatalf("forwarded call: %v", err)
	}
	waitReq(t, out.acks, "ACK at the outbound carrier")
	if st.activeCalls(7) != 1 || st.activeCalls(1) != 1 {
		t.Fatalf("slots: inbound %d outbound %d, want 1 and 1", st.activeCalls(7), st.activeCalls(1))
	}
	hangup(t, dcs)
	pbx.nextCDR(t)
	eventually(t, "both slots released", func() bool { return st.activeCalls(7) == 0 && st.activeCalls(1) == 0 })
}

// TestLostSlotOvercommitted fails if a connected call whose slot the trunk
// state lost is not counted again on the next heartbeat, or is not
// reported as overcommitted when the trunk filled meanwhile.
func TestLostSlotOvercommitted(t *testing.T) {
	cr := newCarrier(t, "acct", "pw")
	tr := cr.trunk(1, "carrier-x", "ip")
	tr.MaxCalls = 1
	r := &fakeRouter{extensions: map[string]bool{"100": true}, trunks: []routing.Trunk{tr}}
	r.decide = outboundVia(r, "+971501234567", "+97140000100", false, nil, 1)
	st := newFakeTrunkState()
	pbx := startPBX(t, callerDevices(), trunkCfg(st), withRouter(r, nil))
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	call := waitCall(t, dial(t.Context(), a, "0501234567"))
	if call.err != nil {
		t.Fatal(call.err)
	}
	// The state loses the slot and another node's call takes the only one.
	st.mu.Lock()
	st.calls[1] = map[string]time.Time{"other-node-call": time.Now().Add(time.Minute)}
	st.mu.Unlock()
	eventually(t, "lost slot counted again", func() bool { return st.activeCalls(1) == 2 })
	if v := pbx.metric(t, "hello_trunk_slot_overcommit_total", map[string]string{"trunk": "carrier-x"}); v < 1 {
		t.Fatalf("overcommit not counted: %v", v)
	}
	hangup(t, call.dcs) // the call was kept, not dropped
	pbx.nextCDR(t)
	_ = st.ReleaseTrunkCall(context.Background(), 1, "other-node-call")
	eventually(t, "released", func() bool { return st.activeCalls(1) == 0 })
}
