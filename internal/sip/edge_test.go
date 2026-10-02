package sip

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

func edgeDevices() []snapshot.Device {
	return []snapshot.Device{dev(1, "a1", "100", "pa"), dev(2, "b1", "200", "pb1"), dev(3, "b2", "200", "pb2")}
}

// twoNodes starts two SIP nodes sharing one live state, as a cluster does.
func twoNodes(t *testing.T, opts ...pbxOpt) (nodeA, nodeB *testPBX) {
	t.Helper()
	st := newFakeState()
	with := func(id string) []pbxOpt {
		return append([]pbxOpt{func(c *Config, d *Deps) { c.NodeID, d.State = id, st }}, opts...)
	}
	return startPBX(t, edgeDevices(), with("sip-a")...), startPBX(t, edgeDevices(), with("sip-b")...)
}

func onlyBinding(t *testing.T, pbx *testPBX, user string) livestate.Binding {
	t.Helper()
	bs, err := pbx.state.Bindings(context.Background(), "sip:"+user+"@"+testDomain)
	if err != nil || len(bs) != 1 {
		t.Fatalf("bindings of %s = %v, %v", user, bs, err)
	}
	return bs[0]
}

// TestEdgeCallAcrossNodes fails if a phone registered through node A, which
// only accepts packets from node A, cannot be called from node B; if the
// call is not record-routed through A; if SDP changes; or if BYE from either
// side or a re-INVITE does not cross the edge.
func TestEdgeCallAcrossNodes(t *testing.T) {
	nodeA, nodeB := twoNodes(t)
	callee, calleeNAT := newNATPhone(t, nodeA, "b1", "pb1")
	caller, _ := newNATPhone(t, nodeB, "a1", "pa")
	callee.register(t)
	caller.register(t)

	b := onlyBinding(t, nodeB, "b1")
	if b.ReceivedNode != "sip-a" || len(b.Path) != 1 || !strings.Contains(b.Path[0], "sip:"+nodeA.addr+";lr;hflow=") {
		t.Fatalf("binding = %+v; want node A's Path with a flow token", b)
	}

	// Call 1: the caller hangs up, after a re-INVITE.
	answer := make(chan struct{})
	callee.setCallee(answerAfter(answer))
	res := dial(t.Context(), caller, "200")
	inv := waitReq(t, callee.invites, "INVITE at the NAT'd callee")
	if rr := inv.GetHeader("Record-Route"); rr == nil || !strings.Contains(rr.Value(), nodeA.addr) || !strings.Contains(rr.Value(), "hflow=") {
		t.Fatalf("Record-Route = %v, want node A with its flow token", rr)
	}
	if string(inv.Body()) != caller.sdp {
		t.Fatalf("SDP changed across the edge: %q", inv.Body())
	}
	waitRing(t, caller)
	close(answer)
	r := waitCall(t, res)
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	if string(r.dcs.InviteResponse.Body()) != callee.sdp {
		t.Fatalf("answer SDP changed: %q", r.dcs.InviteResponse.Body())
	}
	waitReq(t, callee.acks, "ACK through the edge")

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	hold := caller.sdp + "a=sendonly\r\n"
	re := sip.NewRequest(sip.INVITE, r.dcs.InviteResponse.Contact().Address)
	re.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	re.SetBody([]byte(hold))
	if res, err := r.dcs.Do(ctx, re); err != nil || res.StatusCode != 200 {
		t.Fatalf("re-INVITE = %v, %v", res, err)
	}
	if got := waitReq(t, callee.reinvites, "re-INVITE through the edge"); string(got.Body()) != hold {
		t.Fatalf("re-INVITE body = %q", got.Body())
	}
	if err := r.dcs.WriteRequest(sip.NewRequest(sip.ACK, r.dcs.InviteResponse.Contact().Address)); err != nil {
		t.Fatal(err)
	}
	waitReq(t, callee.acks, "re-INVITE ACK through the edge")

	hangup(t, r.dcs)
	waitReq(t, callee.byes, "caller's BYE through the edge")
	if cd := nodeB.nextCDR(t); cd.FinalStatus != 200 || cd.TerminationSide != "caller" || cd.SIPNode != "sip-b" {
		t.Fatalf("CDR = %+v", cd)
	}

	// Call 2: the callee hangs up; its BYE follows the route set via A.
	callee.setCallee(answerAfter(nil))
	r = waitCall(t, dial(t.Context(), caller, "200"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	inv = waitReq(t, callee.invites, "second INVITE")
	waitReq(t, callee.acks, "second ACK")
	callee.mu.Lock()
	dss := callee.servers[inv.CallID().Value()]
	callee.mu.Unlock()
	if err := dss.Bye(ctx); err != nil {
		t.Fatalf("callee BYE: %v", err)
	}
	waitReq(t, caller.byes, "callee's BYE reaching the caller")
	if cd := nodeB.nextCDR(t); cd.FinalStatus != 200 || cd.TerminationSide != "callee" {
		t.Fatalf("CDR = %+v", cd)
	}
	t.Logf("datagrams the callee's NAT dropped: %d", calleeNAT.dropped.Load())
}

// TestEdgeNATBlocksDirect is the control: without the Path, node B sends
// straight to the flow node A saw, the NAT drops it, and the call fails —
// the bug edge routing fixes.
func TestEdgeNATBlocksDirect(t *testing.T) {
	nodeA, nodeB := twoNodes(t, func(c *Config, _ *Deps) { c.RingTimeout = time.Second })
	callee, calleeNAT := newNATPhone(t, nodeA, "b1", "pb1")
	caller, _ := newNATPhone(t, nodeB, "a1", "pa")
	callee.register(t)
	caller.register(t)
	b := onlyBinding(t, nodeB, "b1")
	b.Path = nil
	if err := nodeB.state.PutBinding(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	r := waitCall(t, dial(t.Context(), caller, "200"))
	if code := responseCode(r.err); code != 408 {
		t.Fatalf("direct call to NAT'd phone = %v, want 408", r.err)
	}
	if calleeNAT.dropped.Load() == 0 {
		t.Fatal("the NAT filter dropped nothing")
	}
	noReq(t, callee.invites, 50*time.Millisecond, "INVITE through the NAT")
}

// TestEdgeCancelAcrossNodes fails if the caller's CANCEL does not reach a
// ringing callee behind another node's edge.
func TestEdgeCancelAcrossNodes(t *testing.T) {
	nodeA, nodeB := twoNodes(t)
	callee, _ := newNATPhone(t, nodeA, "b1", "pb1")
	caller, _ := newNATPhone(t, nodeB, "a1", "pa")
	callee.register(t)
	caller.register(t)
	callee.setCallee(ringForever())
	ctx, cancel := context.WithCancel(t.Context())
	res := dial(ctx, caller, "200")
	waitReq(t, callee.invites, "INVITE")
	waitRing(t, caller)
	cancel()
	r := waitCall(t, res)
	if r.dcs.InviteResponse == nil || r.dcs.InviteResponse.StatusCode != 487 {
		t.Fatalf("caller final = %v, want 487", r.dcs.InviteResponse)
	}
	waitReq(t, callee.cancels, "CANCEL through the edge")
	if cd := nodeB.nextCDR(t); cd.FinalStatus != 487 || cd.TerminationSide != "caller" {
		t.Fatalf("CDR = %+v", cd)
	}
}

// TestEdgeForgedToken fails if a Route with a flow token that is forged,
// tampered with or garbage gets anything but 403.
func TestEdgeForgedToken(t *testing.T) {
	nodeA, _ := twoNodes(t)
	other, err := New(Config{Domain: testDomain, AdvertisedAddr: "127.0.0.1:1", NonceSecret: []byte(strings.Repeat("z", 40))},
		Deps{Snapshots: nodeA.snaps, State: newFakeState(), Throttle: &fakeThrottle{}, CDRs: &fakeCDRs{}, Metrics: nodeA.m})
	if err != nil {
		t.Fatal(err)
	}
	p := newPhone(t, nodeA, "x", "x")
	valid := nodeA.srv.flowToken(p.addr, "udp")
	tampered := []byte(valid)
	tampered[2] ^= 1
	for name, tok := range map[string]string{
		"other secret": other.flowToken(p.addr, "udp"),
		"tampered":     string(tampered),
		"garbage":      "bm90LWEtdG9rZW4",
	} {
		req := p.inviteReq("200")
		req.Recipient = sip.Uri{Scheme: "sip", User: "b1", Host: "127.0.0.1", Port: 9}
		req.RemoveHeader("Route")
		req.AppendHeader(sip.NewHeader("Route", "<sip:"+nodeA.addr+";lr;hflow="+tok+">"))
		req.SetDestination(nodeA.addr)
		if res := p.do(req); res.StatusCode != 403 {
			t.Errorf("%s token: %d, want 403", name, res.StatusCode)
		}
	}
	if src, tr, ok := nodeA.srv.parseFlowToken(valid); !ok || src != p.addr || tr != "udp" {
		t.Fatalf("valid token parsed as %q %q %v", src, tr, ok)
	}
}

// TestEdgeDeadNode fails if a fork through an unreachable node hangs the
// call instead of ending at the ring timeout, or blocks a live fork.
func TestEdgeDeadNode(t *testing.T) {
	nodeA, nodeB := twoNodes(t, func(c *Config, _ *Deps) { c.RingTimeout = time.Second })
	caller, _ := newNATPhone(t, nodeB, "a1", "pa")
	caller.register(t)
	dead, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := dead.LocalAddr().String()
	_ = dead.Close()
	ghost := livestate.Binding{
		AOR: "sip:b1@" + testDomain, Extension: "200", Device: "b1", ContactURI: "sip:b1@10.255.255.1:5060",
		Source: "10.255.255.1:5060", Transport: "udp", ReceivedNode: "sip-dead",
		Path:    []string{"<sip:" + deadAddr + ";lr;hflow=" + nodeB.srv.flowToken("10.255.255.1:5060", "udp") + ">"},
		Expires: time.Now().Add(time.Hour), UpdatedAt: time.Now(),
	}
	if err := nodeB.state.PutBinding(context.Background(), ghost); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	r := waitCall(t, dial(t.Context(), caller, "200"))
	if code := responseCode(r.err); code != 408 || time.Since(start) > 5*time.Second {
		t.Fatalf("call via dead node = %v after %s, want 408 at the ring timeout", r.err, time.Since(start))
	}
	if cd := nodeB.nextCDR(t); cd.FinalStatus != 408 {
		t.Fatalf("CDR = %+v", cd)
	}

	// A live fork through node A still answers alongside the dead one.
	live, _ := newNATPhone(t, nodeA, "b2", "pb2")
	live.register(t)
	r = waitCall(t, dial(t.Context(), caller, "200"))
	if r.err != nil {
		t.Fatalf("call with a dead and a live fork: %v", r.err)
	}
	hangup(t, r.dcs)
	waitReq(t, live.byes, "BYE to the live fork")
}
