// In-call HA acceptance tests (incall-ha spec S-1, S-4, S-5, S-6, S-8):
// the replicated state's completeness and heartbeat, the media gap, the
// honesty flags, a double failure, the HA metrics, and the scenario
// matrix rows beyond hold and recording (TestTakeoverScenarioMatrix in
// takeover_test.go runs them).
package sip

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/valkey-io/valkey-go"
)

// --- helpers ---------------------------------------------------------------

// waitRecord waits for a replicated record owned by node that satisfies
// pred (nil: any) and returns it.
func waitRecord(t *testing.T, ha *fakeHA, what, node string, pred func(livestate.DialogState) bool) livestate.DialogState {
	t.Helper()
	var st livestate.DialogState
	eventually(t, what, func() bool {
		ha.mu.Lock()
		defer ha.mu.Unlock()
		for _, s := range ha.dialogs {
			if s.OwnerNode == node && (pred == nil || pred(s)) {
				st = s
				return true
			}
		}
		return false
	})
	return st
}

// twoLegged is a replicated call with both legs' dialogs known.
func twoLegged(s livestate.DialogState) bool {
	return s.Legs[0].LocalTag != "" && s.Legs[1].LocalTag != ""
}

// killNode stops a PBX (its node dies) and publishes the cluster view the
// survivors read: the dead nodes OFFLINE with their live calls, the rest
// READY.
func killNode(pbx *testPBX, mem *fakeMembership, dead map[string]int, ready ...string) {
	if pbx != nil {
		pbx.stop()
	}
	var ms []cluster.Member
	for id, n := range dead {
		ms = append(ms, cluster.Member{ID: id, Kind: cluster.KindSIP, State: cluster.Offline, ActiveCalls: n})
	}
	for _, id := range ready {
		ms = append(ms, cluster.Member{ID: id, Kind: cluster.KindSIP, State: cluster.Ready})
	}
	mem.set(ms...)
}

// reinviteOn waits for a re-INVITE on the dialog callID, skipping others.
func reinviteOn(t *testing.T, p *phone, callID, what string) *sip.Request {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case r := <-p.reinvites:
			if r.CallID().Value() == callID {
				return r
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
			return nil
		}
	}
}

// liveOn lists a PBX's live calls (nil while its state is unreadable).
func liveOn(pbx *testPBX) []livestate.Call { return liveCallsSkip(pbx) }

// connectedOn reports whether pbx lists a connected live call to dest
// carrying the given HA state.
func connectedOn(pbx *testPBX, dest, ha string) bool {
	for _, c := range liveOn(pbx) {
		if c.To == dest && c.State == "connected" && c.HA == ha {
			return true
		}
	}
	return false
}

// listenRTP binds a media socket for a phone and rewrites the phone's SDP
// to name it (the default SDP port is only a placeholder), so the test sees
// what the relay sends the phone. Call it before the phone's call starts.
func listenRTP(t *testing.T, p *phone) net.PacketConn {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind %s's media port: %v", p.user, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	sdp, err := media.ParseAudioSDP([]byte(p.sdp))
	if err != nil {
		t.Fatal(err)
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	p.mu.Lock()
	p.sdp = strings.Replace(p.sdp, "m=audio "+strconv.Itoa(sdp.Port)+" ", "m=audio "+strconv.Itoa(port)+" ", 1)
	p.mu.Unlock()
	return conn
}

// rtpThrough sends RTP from src to the relay port named in body until dst
// receives a packet, and reports whether one arrived before deadline.
func rtpThrough(src, dst net.PacketConn, body []byte, deadline time.Time) bool {
	sdp, err := media.ParseAudioSDP(body)
	if err != nil {
		return false
	}
	to := &net.UDPAddr{IP: net.ParseIP(sdp.Address), Port: sdp.Port}
	buf := make([]byte, 1500)
	for seq := uint16(1); time.Now().Before(deadline); seq++ {
		pkt := media.BuildRTP(0, seq, uint32(seq)*160, 4242, false, make([]byte, 160))
		_, _ = src.WriteTo(pkt, to)
		_ = dst.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
		if n, _, err := dst.ReadFrom(buf); err == nil && n > 12 {
			return true
		}
	}
	return false
}

// haPair starts an owner (sip-1) and a taker (sip-2) on one replication
// store and cluster view.
func haPair(t *testing.T, devices []snapshot.Device, opts ...pbxOpt) (*fakeHA, *fakeMembership, *testPBX, *testPBX) {
	t.Helper()
	ha, mem := newFakeHA(), &fakeMembership{}
	owner := startPBX(t, devices, append([]pbxOpt{withNodeID("sip-1"), withHA(ha, mem)}, opts...)...)
	taker := startPBX(t, devices, append([]pbxOpt{withNodeID("sip-2"), withHA(ha, mem)}, opts...)...)
	return ha, mem, owner, taker
}

// connect places a->ext on the owner and waits until the callee answered.
func connect(t *testing.T, a, b *phone, ext string) callResult {
	t.Helper()
	r := waitCall(t, dial(t.Context(), a, ext))
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, b.invites, "INVITE to "+b.user)
	waitReq(t, b.acks, "ACK to "+b.user)
	return r
}

// --- S-1 -------------------------------------------------------------------

// TestDialogReplication fails if the replicated state of a connected call
// is missing any field a taker needs to re-create a leg — Call-ID, both
// tags, both CSeqs, the route set, contact, remote target, both sides' SDP,
// the relay ports, the correlation id — or if the heartbeat does not keep
// re-writing it with the dialog TTL (spec S-1).
func TestDialogReplication(t *testing.T) {
	t.Run("fields", func(t *testing.T) {
		ha, mem := newFakeHA(), &fakeMembership{}
		owner := startPBX(t, ringAllDevices(), withNodeID("sip-1"), withHA(ha, mem))
		a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1")
		a.register(t)
		b.register(t)
		// The callee's 200 carries a Record-Route (its own address, as a
		// proxy next to it would), so leg b has a route set to replicate.
		rr := "<sip:" + b.addr + ";lr>"
		b.setCallee(func(p *phone, _ *sip.Request, _ sip.ServerTransaction, dss *sipgo.DialogServerSession) {
			_ = dss.Respond(200, "OK", []byte(p.sdp), sip.NewHeader("Content-Type", "application/sdp"),
				sip.NewHeader("Record-Route", rr))
		})
		r := connect(t, a, b, "200")
		var dss *sipgo.DialogServerSession
		for d := range serversOf(b) {
			dss = d
		}
		st := waitRecord(t, ha, "the call is replicated", "sip-1", twoLegged)
		c := ownedCall(t, owner.srv)
		relay := c.anchorRelay()
		ainv, bres := r.dcs.InviteRequest, dss.InviteResponse
		la, lb := st.Legs[0], st.Legs[1]
		check := func(what string, got, want any) {
			t.Helper()
			if got != want {
				t.Errorf("%s = %v, want %v", what, got, want)
			}
		}
		check("CallID", st.CallID, ainv.CallID().Value())
		check("OwnerNode", st.OwnerNode, "sip-1")
		check("Correlation", st.Correlation, c.id)
		check("State", st.State, "talking")
		check("Caller", st.Caller, "100")
		check("Destination", st.Destination, "200")
		check("RelayPorts[0]", st.RelayPorts[0], relay.LegPort(legCaller))
		check("RelayPorts[1]", st.RelayPorts[1], relay.LegPort(legCallee))
		// Leg a: Hello is the UAS of the caller's dialog.
		check("a.CallID", la.CallID, ainv.CallID().Value())
		check("a.LocalTag", la.LocalTag, toTagOf(r.dcs))
		check("a.RemoteTag", la.RemoteTag, tagOfTest(ainv.From()))
		check("a.LocalCSeq", la.LocalCSeq, ainv.CSeq().SeqNo+1)
		check("a.RemoteCSeq", la.RemoteCSeq, ainv.CSeq().SeqNo)
		check("a.RemoteTarget", la.RemoteTarget, uriString(a.contact.Address))
		check("a.Source", la.Source, a.addr)
		check("a.RemoteSDP", la.RemoteSDP, a.sdp)
		check("a.Endpoint", la.Endpoint, "sip:100@"+testDomain)
		// Leg b: Hello is the UAC of the callee's dialog.
		check("b.CallID", lb.CallID, bres.CallID().Value())
		check("b.LocalTag", lb.LocalTag, tagOfTest(bres.From()))
		check("b.RemoteTag", lb.RemoteTag, tagOfTest(bres.To()))
		check("b.LocalCSeq", lb.LocalCSeq, bres.CSeq().SeqNo+1)
		check("b.RemoteCSeq", lb.RemoteCSeq, bres.CSeq().SeqNo)
		check("b.RemoteTarget", lb.RemoteTarget, uriString(b.contact.Address))
		check("b.Source", lb.Source, b.addr)
		check("b.RemoteSDP", lb.RemoteSDP, b.sdp)
		if len(lb.RouteSet) != 1 || lb.RouteSet[0] != rr {
			t.Errorf("b.RouteSet = %v, want [%s]", lb.RouteSet, rr)
		}
		for i, l := range st.Legs {
			if l.Contact == "" || l.LocalIdentity == "" || l.RemoteIdentity == "" {
				t.Errorf("leg %d lacks contact or identities: %+v", i, l)
			}
			sdp, err := media.ParseAudioSDP([]byte(l.SDP))
			if err != nil || sdp.Port != st.RelayPorts[i] {
				t.Errorf("leg %d SDP = %q (%v), want the relay port %d", i, l.SDP, err, st.RelayPorts[i])
			}
		}
		_ = mem
	})

	t.Run("heartbeat", func(t *testing.T) {
		ha, mem := newFakeHA(), &fakeMembership{}
		owner := startPBX(t, ringAllDevices(), withNodeID("sip-1"), withHA(ha, mem))
		a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1")
		a.register(t)
		b.register(t)
		connect(t, a, b, "200")
		st := waitRecord(t, ha, "the call is replicated", "sip-1", twoLegged)
		ha.mu.Lock()
		saves, at := ha.saves[st.CallID], ha.dialogs[st.CallID].UpdatedAt
		ha.mu.Unlock()
		// Nothing about the call changes; the heartbeat (20ms here) alone
		// must keep re-writing the record, each write carrying the TTL.
		eventually(t, "three more heartbeat writes", func() bool {
			ha.mu.Lock()
			defer ha.mu.Unlock()
			return ha.saves[st.CallID] >= saves+3 && ha.dialogs[st.CallID].UpdatedAt.After(at)
		})
		ha.mu.Lock()
		ttl := ha.ttls[st.CallID]
		ha.mu.Unlock()
		if ttl != livestate.DialogTTL {
			t.Fatalf("replication TTL = %s, want %s", ttl, livestate.DialogTTL)
		}
		_ = mem
	})

	t.Run("valkey-ttl", func(t *testing.T) {
		addr := os.Getenv("HELLO_TEST_VALKEY_ADDR")
		if addr == "" {
			t.Skip("HELLO_TEST_VALKEY_ADDR not set")
		}
		vc, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, ForceSingleClient: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(vc.Close)
		live := livestate.New(vc)
		owner := startPBX(t, ringAllDevices(), withNodeID("sip-ttl"), withHA(live, &fakeMembership{}))
		a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1")
		a.register(t)
		b.register(t)
		r := connect(t, a, b, "200")
		key := "hello:dialog:" + r.dcs.InviteRequest.CallID().Value()
		pttl := func() int64 {
			n, err := vc.Do(context.Background(), vc.B().Pttl().Key(key).Build()).AsInt64()
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
		eventually(t, "the record is in Valkey", func() bool { return pttl() > 0 })
		// Half a second later the TTL is still fresh: the 20ms heartbeat
		// re-wrote the record with the full TTL. Unrefreshed it would have
		// decayed below DialogTTL-500ms.
		time.Sleep(500 * time.Millisecond)
		if got, floor := pttl(), (livestate.DialogTTL - 250*time.Millisecond).Milliseconds(); got < floor {
			t.Fatalf("PTTL = %dms after 500ms, want >= %dms (the heartbeat refreshes it)", got, floor)
		}
		hangup(t, r.dcs)
		eventually(t, "the ended call's record is deleted", func() bool { return pttl() == -2 })
	})
}

// --- S-4 -------------------------------------------------------------------

// TestTakeoverMediaGap fails if the media gap of a takeover exceeds 3s:
// from the claim, both endpoints' re-INVITEs must be answered and RTP must
// flow through the taker's own relay (its ports, both directions) within
// 3s (spec S-4). The clock starts at the claim: membership's OFFLINE bound
// before it is Phase 3's, measured by the lab suite.
func TestTakeoverMediaGap(t *testing.T) {
	ha, mem, owner, taker := haPair(t, ringAllDevices())
	a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1")
	a.register(t)
	b.register(t)
	for _, p := range []*phone{a, b} {
		p.mu.Lock()
		p.reinviteOwnSDP = true // the phones answer with their own media address
		p.mu.Unlock()
	}
	aRTP, bRTP := listenRTP(t, a), listenRTP(t, b)
	connect(t, a, b, "200")
	waitRecord(t, ha, "the call is replicated", "sip-1", twoLegged)

	haReinviteTimeout = time.Second
	killNode(owner, mem, map[string]int{"sip-1": 1}, "sip-2")
	claimed := time.Now()
	taker.srv.takeoverPass(t.Context())
	ra := waitReq(t, a.reinvites, "takeover re-INVITE to the caller")
	rb := waitReq(t, b.reinvites, "takeover re-INVITE to the callee")
	waitReq(t, a.acks, "ACK to the caller's 200")
	waitReq(t, b.acks, "ACK to the callee's 200")
	answered := time.Since(claimed)

	// The re-INVITEs name the taker's relay: its ports, nobody else's.
	sa, sb := anchoredSDP(t, taker, ra.Body()), anchoredSDP(t, taker, rb.Body())
	var relay *media.Relay
	eventually(t, "the call homed on the taker", func() bool {
		taker.srv.mu.Lock()
		defer taker.srv.mu.Unlock()
		for c := range taker.srv.calls {
			if c.homed() != nil {
				relay = c.anchorRelay()
			}
		}
		return relay != nil
	})
	if sa.Port != relay.LegPort(legCaller) || sb.Port != relay.LegPort(legCallee) {
		t.Fatalf("re-INVITE ports %d/%d, taker relay %d/%d", sa.Port, sb.Port, relay.LegPort(legCaller), relay.LegPort(legCallee))
	}
	// Media flows through the taker's relay both ways, inside the 3s gap.
	deadline := claimed.Add(haMediaGap)
	if !rtpThrough(aRTP, bRTP, ra.Body(), deadline) {
		t.Fatalf("caller->callee RTP did not cross the taker's relay within %s of the claim", haMediaGap)
	}
	if !rtpThrough(bRTP, aRTP, rb.Body(), deadline) {
		t.Fatalf("callee->caller RTP did not cross the taker's relay within %s of the claim", haMediaGap)
	}
	gap := time.Since(claimed)
	t.Logf("re-INVITEs answered %s after the claim; media restored at %s", answered.Round(time.Millisecond), gap.Round(time.Millisecond))
	if gap > haMediaGap {
		t.Fatalf("media gap %s > %s", gap, haMediaGap)
	}
}

// --- S-6 -------------------------------------------------------------------

// TestHonestyFlags fails if a taken-over call is not marked in the live
// view (ha "taken-over" on the taker, "owned" before) and its CDR trace, or
// if a zombie is not counted in hello_zombie_calls_total — exactly once,
// and never for a call a survivor took over (spec S-6).
func TestHonestyFlags(t *testing.T) {
	defer func(d time.Duration) { haReapDelay = d }(haReapDelay)
	t.Run("taken-over", func(t *testing.T) {
		ha, mem, owner, taker := haPair(t, ringAllDevices())
		a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1")
		a.register(t)
		b.register(t)
		connect(t, a, b, "200")
		st := waitRecord(t, ha, "the call is replicated", "sip-1", twoLegged)
		eventually(t, "the owner lists the call owned", func() bool { return connectedOn(owner, "200", livestate.HAOwned) })
		haReinviteTimeout = time.Second
		killNode(owner, mem, map[string]int{"sip-1": 1}, "sip-2")
		taker.srv.takeoverPass(t.Context())
		ra := waitReq(t, a.reinvites, "takeover re-INVITE to the caller")
		waitReq(t, b.reinvites, "takeover re-INVITE to the callee")
		eventually(t, "the taker lists the call taken over", func() bool { return connectedOn(taker, "200", livestate.HATakenOver) })
		hangupHomed(t, a, taker, ra, st.Legs[0])
		cd := taker.nextCDR(t)
		if !strings.Contains(traceText(cd.Trace), "ha: taken over from sip-1") {
			t.Fatalf("CDR trace lacks the takeover mark: %s", traceText(cd.Trace))
		}
		if z := taker.metric(t, "hello_zombie_calls_total", nil); z != 0 {
			t.Fatalf("zombies = %v, want 0", z)
		}
	})
	t.Run("zombie-counted-once", func(t *testing.T) {
		// The owner's replication is failing when it dies: nothing can be
		// taken over, and the reaper counts the call exactly once.
		ha, mem, owner, taker := haPair(t, ringAllDevices())
		ha.mu.Lock()
		ha.failSave = true
		ha.mu.Unlock()
		a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1")
		a.register(t)
		b.register(t)
		connect(t, a, b, "200")
		haReapDelay = 50 * time.Millisecond
		// Seen alive with its call; once OFFLINE, membership lists it with
		// no load (as the real store does), and the reaper must still
		// count the call it last had.
		mem.set(
			cluster.Member{ID: "sip-1", Kind: cluster.KindSIP, State: cluster.Ready, ActiveCalls: 1},
			cluster.Member{ID: "sip-2", Kind: cluster.KindSIP, State: cluster.Ready},
		)
		taker.srv.takeoverPass(t.Context())
		killNode(owner, mem, map[string]int{"sip-1": 0}, "sip-2")
		taker.srv.takeoverPass(t.Context()) // first sight: the reaper waits
		time.Sleep(100 * time.Millisecond)
		for i := 0; i < 3; i++ {
			taker.srv.takeoverPass(t.Context())
		}
		if z := taker.metric(t, "hello_zombie_calls_total", nil); z != 1 {
			t.Fatalf("zombies = %v, want exactly 1", z)
		}
		if v := taker.metric(t, "hello_dialog_takeovers_total", nil); v != 0 {
			t.Fatalf("takeovers = %v, want 0", v)
		}
	})
	t.Run("reaper-spares-taken-over", func(t *testing.T) {
		// The dead node had two calls: one replicated (taken over), one
		// that never replicated. Only the second is a zombie.
		ha, mem, owner, taker := haPair(t, ringAllDevices())
		a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1")
		a.register(t)
		b.register(t)
		connect(t, a, b, "200")
		waitRecord(t, ha, "the call is replicated", "sip-1", twoLegged)
		haReapDelay = 300 * time.Millisecond
		haReinviteTimeout = time.Second
		killNode(owner, mem, map[string]int{"sip-1": 2}, "sip-2")
		taker.srv.takeoverPass(t.Context())
		waitReq(t, a.reinvites, "takeover re-INVITE to the caller")
		waitReq(t, b.reinvites, "takeover re-INVITE to the callee")
		eventually(t, "taken over", func() bool { return taker.metric(t, "hello_dialog_takeovers_total", nil) == 1 })
		time.Sleep(haReapDelay)
		taker.srv.takeoverPass(t.Context())
		taker.srv.takeoverPass(t.Context())
		if z := taker.metric(t, "hello_zombie_calls_total", nil); z != 1 {
			t.Fatalf("zombies = %v, want 1 (the unreplicated call only)", z)
		}
	})
}

// --- S-8 -------------------------------------------------------------------

// TestDoubleFailure fails if a node dying while another node is mid-takeover
// of a different call corrupts or zombies either call (spec S-8): sip-1
// dies under call X; sip-2 claims X and is still waiting on X's re-INVITE
// when sip-3 dies under call Y. sip-2 must take Y over meanwhile, then
// finish X, each exactly once, both hangup-able, no zombie.
func TestDoubleFailure(t *testing.T) {
	ha, mem := newFakeHA(), &fakeMembership{}
	devs := threeDevices()
	n1 := startPBX(t, devs, withNodeID("sip-1"), withHA(ha, mem))
	n2 := startPBX(t, devs, withNodeID("sip-2"), withHA(ha, mem))
	n3 := startPBX(t, devs, withNodeID("sip-3"), withHA(ha, mem))
	a, b := newPhone(t, n1, "a1", "pa"), newPhone(t, n1, "b1", "pb1")
	c, d := newPhone(t, n3, "c1", "pc"), newPhone(t, n3, "d1", "pd")
	for _, p := range []*phone{a, b, c, d} {
		p.register(t)
	}
	connect(t, a, b, "200") // X on sip-1
	connect(t, c, d, "210") // Y on sip-3
	recX := waitRecord(t, ha, "X replicated", "sip-1", twoLegged)
	recY := waitRecord(t, ha, "Y replicated", "sip-3", twoLegged)

	haReinviteTimeout = 5 * time.Second
	hold := make(chan struct{})
	a.mu.Lock()
	a.reinviteHold = hold // X's takeover stalls on the caller's answer
	a.mu.Unlock()
	killNode(n1, mem, map[string]int{"sip-1": 1}, "sip-2", "sip-3")
	n2.srv.takeoverPass(t.Context())
	ra := waitReq(t, a.reinvites, "X's takeover re-INVITE (held)")

	// sip-3 dies while X's takeover is in flight.
	killNode(n3, mem, map[string]int{"sip-1": 1, "sip-3": 1}, "sip-2")
	n2.srv.takeoverPass(t.Context())
	rc := waitReq(t, c.reinvites, "Y's takeover re-INVITE to its caller")
	waitReq(t, d.reinvites, "Y's takeover re-INVITE to its callee")
	eventually(t, "Y re-homed while X is still mid-takeover", func() bool {
		return connectedOn(n2, "210", livestate.HATakenOver)
	})
	if connectedOn(n2, "200", livestate.HATakenOver) {
		t.Fatal("X finished before its caller answered")
	}
	n2.srv.takeoverPass(t.Context()) // nothing new to claim
	close(hold)
	waitReq(t, b.reinvites, "X's takeover re-INVITE to its callee")
	eventually(t, "X re-homed", func() bool { return connectedOn(n2, "200", livestate.HATakenOver) })
	eventually(t, "both claims released", func() bool {
		return ha.claimOf(recX.CallID) == "" && ha.claimOf(recY.CallID) == ""
	})
	// Scans after both takeovers find nothing: each call taken exactly once.
	for i := 0; i < 3; i++ {
		n2.srv.takeoverPass(t.Context())
	}
	time.Sleep(100 * time.Millisecond)
	if v := n2.metric(t, "hello_dialog_takeovers_total", nil); v != 2 {
		t.Fatalf("takeovers = %v, want 2", v)
	}
	if z := n2.metric(t, "hello_zombie_calls_total", nil); z != 0 {
		t.Fatalf("zombies = %v, want 0", z)
	}
	ha.mu.Lock()
	stale := ha.staleReleases
	ha.mu.Unlock()
	if stale != 0 {
		t.Fatalf("%d claims released before the record named the taker: a survivor could take the call again", stale)
	}
	if ha.ownerOf(recX.CallID) != "sip-2" || ha.ownerOf(recY.CallID) != "sip-2" {
		t.Fatalf("records owned by %q/%q, want sip-2", ha.ownerOf(recX.CallID), ha.ownerOf(recY.CallID))
	}
	if n := len(liveOn(n2)); n != 2 {
		t.Fatalf("live calls on sip-2 = %d, want 2", n)
	}
	hangupHomed(t, a, n2, ra, recX.Legs[0])
	waitReq(t, b.byes, "X's callee BYE")
	hangupHomed(t, c, n2, rc, recY.Legs[0])
	waitReq(t, d.byes, "Y's callee BYE")
	for i := 0; i < 2; i++ {
		if cd := n2.nextCDR(t); cd.FinalStatus != 200 || !strings.Contains(traceText(cd.Trace), "ha: taken over") {
			t.Fatalf("CDR = %+v", cd)
		}
	}
}

// --- metrics / UI ------------------------------------------------------------

// TestHAMetrics fails if hello_dialog_replicated_total (ok and failed),
// hello_dialog_takeovers_total, hello_zombie_calls_total or the per-call ha
// flag in the live view do not move with what happened (incall-ha
// metrics/UI criterion).
func TestHAMetrics(t *testing.T) {
	ha, mem, owner, taker := haPair(t, ringAllDevices())
	a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1")
	a.register(t)
	b.register(t)
	connect(t, a, b, "200")
	st := waitRecord(t, ha, "the call is replicated", "sip-1", twoLegged)
	if v := owner.metric(t, "hello_dialog_replicated_total", map[string]string{"result": "ok"}); v < 1 {
		t.Fatalf("replicated{ok} = %v", v)
	}
	if liveOn(owner)[0].HA != livestate.HAOwned {
		t.Fatalf("live ha = %q before the takeover, want owned", liveOn(owner)[0].HA)
	}
	// A replication write failing is counted, never fatal.
	ha.mu.Lock()
	ha.failSave = true
	ha.mu.Unlock()
	eventually(t, "replicated{failed} moves", func() bool {
		return owner.metric(t, "hello_dialog_replicated_total", map[string]string{"result": "failed"}) >= 1
	})
	ha.mu.Lock()
	ha.failSave = false
	ha.mu.Unlock()
	// A takeover moves the takeover counter and flips the live flag.
	haReinviteTimeout = time.Second
	killNode(owner, mem, map[string]int{"sip-1": 1}, "sip-2")
	taker.srv.takeoverPass(t.Context())
	ra := waitReq(t, a.reinvites, "takeover re-INVITE")
	eventually(t, "takeovers moves", func() bool { return taker.metric(t, "hello_dialog_takeovers_total", nil) == 1 })
	eventually(t, "live ha flag is taken-over", func() bool { return connectedOn(taker, "200", livestate.HATakenOver) })
	// A dialog whose endpoint is gone moves the zombie counter.
	ha.put(orphanState("orphan-m", "sip-9", "sip:x@127.0.0.1:1"))
	haReinviteTimeout = 300 * time.Millisecond
	killNode(nil, mem, map[string]int{"sip-1": 1, "sip-9": 1}, "sip-2")
	taker.srv.takeoverPass(t.Context())
	eventually(t, "zombies moves", func() bool { return taker.metric(t, "hello_zombie_calls_total", nil) == 1 })
	hangupHomed(t, a, taker, ra, st.Legs[0])
}

// --- S-5 scenario rows --------------------------------------------------------

// matrixRinging: a call still ringing has no dialog to replicate; its node
// dying leaves nothing to take over (the edge proxy answers the caller,
// Phase 3 behaviour, proven by the lab's TestKillSIPNodeDuringRinging).
func matrixRinging(t *testing.T) {
	ha, mem, owner, taker := haPair(t, ringAllDevices())
	a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1")
	a.register(t)
	b.register(t)
	b.setCallee(ringForever())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	dial(ctx, a, "200")
	waitReq(t, b.invites, "INVITE to the callee")
	time.Sleep(100 * time.Millisecond) // five replication heartbeats
	ha.mu.Lock()
	n := len(ha.dialogs)
	ha.mu.Unlock()
	if n != 0 {
		t.Fatalf("a ringing call was replicated (%d records)", n)
	}
	killNode(owner, mem, map[string]int{"sip-1": 1}, "sip-2")
	taker.srv.takeoverPass(t.Context())
	noReq(t, a.reinvites, 200*time.Millisecond, "takeover re-INVITE for a ringing call")
	if v := taker.metric(t, "hello_dialog_takeovers_total", nil); v != 0 {
		t.Fatalf("takeovers = %v", v)
	}
}

// matrixBlindTransfer: the node dies while a blind transfer's target is
// still ringing. The original call survives untransferred on the taker;
// the transferor's dialog continues past the NOTIFY it already got (CSeq),
// and it is told the transfer failed (final NOTIFY, 503).
func matrixBlindTransfer(t *testing.T) {
	ha, mem, owner, taker := haPair(t, threeDevices())
	a, b, c := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1"), newPhone(t, owner, "c1", "pc")
	for _, p := range []*phone{a, b, c} {
		p.register(t)
	}
	c.setCallee(ringForever())
	connect(t, a, b, "200")
	dss := <-serversOf(b)
	if res := b.sendReferFromCallee(t, dss, "<sip:300@"+testDomain+">"); res.StatusCode != 202 {
		t.Fatalf("REFER = %d", res.StatusCode)
	}
	trying := waitReq(t, b.notifies, "the transfer's 100 Trying NOTIFY")
	waitReq(t, c.invites, "the transfer target rings")
	st := waitRecord(t, ha, "the transfer is replicated", "sip-1", func(s livestate.DialogState) bool {
		return s.State == haPhaseTransferring && s.StateDetail == "refer:callee" && s.Legs[1].LocalCSeq > trying.CSeq().SeqNo
	})
	haReinviteTimeout = time.Second
	killNode(owner, mem, map[string]int{"sip-1": 1}, "sip-2")
	taker.srv.takeoverPass(t.Context())
	ra := waitReq(t, a.reinvites, "takeover re-INVITE to the transferee's peer")
	rb := waitReq(t, b.reinvites, "takeover re-INVITE to the transferor")
	if rb.CSeq().SeqNo <= trying.CSeq().SeqNo {
		t.Fatalf("transferor re-INVITE CSeq %d does not pass the NOTIFY's %d", rb.CSeq().SeqNo, trying.CSeq().SeqNo)
	}
	// The in-process owner's shutdown may still flush a NOTIFY of its
	// own (a dead node cannot); the taker's is the 503.
	var final *sip.Request
	for final == nil {
		if n := waitReq(t, b.notifies, "the transferor's final NOTIFY"); strings.Contains(string(n.Body()), "503") {
			final = n
		}
	}
	if !strings.Contains(string(final.Body()), "503") ||
		!strings.Contains(final.GetHeader("Subscription-State").Value(), "terminated") {
		t.Fatalf("final NOTIFY = %q (%s)", final.Body(), final.GetHeader("Subscription-State").Value())
	}
	if final.CSeq().SeqNo <= rb.CSeq().SeqNo {
		t.Fatalf("final NOTIFY CSeq %d does not pass the re-INVITE's %d", final.CSeq().SeqNo, rb.CSeq().SeqNo)
	}
	eventually(t, "the original call talks on the taker", func() bool { return connectedOn(taker, "200", livestate.HATakenOver) })
	hangupHomed(t, a, taker, ra, st.Legs[0])
	waitReq(t, b.byes, "BYE to the transferor")
	cd := taker.nextCDR(t)
	if !strings.Contains(traceText(cd.Trace), "Transfer abandoned by the takeover") {
		t.Fatalf("CDR trace: %s", traceText(cd.Trace))
	}
}

// matrixBlindTransferAnswered: the node dies after a blind transfer
// completed. The transferred call (caller and target) is anchored and
// replicated like any call, and survives on the taker under its target.
func matrixBlindTransferAnswered(t *testing.T) {
	ha, mem, owner, taker := haPair(t, threeDevices())
	a, b, c := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1"), newPhone(t, owner, "c1", "pc")
	for _, p := range []*phone{a, b, c} {
		p.register(t)
	}
	r := connect(t, a, b, "200")
	dss := <-serversOf(b)
	if res := b.sendReferFromCallee(t, dss, "<sip:300@"+testDomain+">"); res.StatusCode != 202 {
		t.Fatalf("REFER = %d", res.StatusCode)
	}
	cInv := waitReq(t, c.invites, "the transfer target rings")
	waitReq(t, c.acks, "the target's ACK")
	waitReq(t, b.byes, "the transferor released")
	handC := waitReq(t, c.reinvites, "the target moved onto the anchor")
	handA := waitReq(t, a.reinvites, "the caller refreshed on the anchor")
	anchoredSDP(t, owner, handC.Body())
	if cd := owner.nextCDR(t); cd.FailureReason != "blind transfer" {
		t.Fatalf("original CDR = %+v", cd)
	}
	aCallID := r.dcs.InviteRequest.CallID().Value()
	st := waitRecord(t, ha, "the transferred call is replicated", "sip-1", func(s livestate.DialogState) bool {
		return s.Legs[0].CallID == aCallID && s.Legs[1].CallID == cInv.CallID().Value() && s.Destination == "300"
	})
	haReinviteTimeout = time.Second
	killNode(owner, mem, map[string]int{"sip-1": 1}, "sip-2")
	taker.srv.takeoverPass(t.Context())
	ra := waitReq(t, a.reinvites, "takeover re-INVITE to the caller")
	rc := waitReq(t, c.reinvites, "takeover re-INVITE to the target")
	anchoredSDP(t, taker, ra.Body())
	anchoredSDP(t, taker, rc.Body())
	if ra.CSeq().SeqNo <= handA.CSeq().SeqNo || rc.CSeq().SeqNo <= handC.CSeq().SeqNo {
		t.Fatalf("takeover CSeqs %d/%d do not pass the hand-over's %d/%d",
			ra.CSeq().SeqNo, rc.CSeq().SeqNo, handA.CSeq().SeqNo, handC.CSeq().SeqNo)
	}
	eventually(t, "the transferred call talks on the taker", func() bool { return connectedOn(taker, "300", livestate.HATakenOver) })
	hangupHomed(t, a, taker, ra, st.Legs[0])
	waitReq(t, c.byes, "BYE to the target")
	if cd := taker.nextCDR(t); cd.Destination != "300" || cd.FinalStatus != 200 {
		t.Fatalf("transferred call's CDR on the taker = %+v", cd)
	}
}

// matrixAttendedTransferHalf: the node dies during an attended transfer's
// consultation (the transfer half): the held original call and the
// consultation call are each taken over and survive as two calls.
func matrixAttendedTransferHalf(t *testing.T) {
	ha, mem, owner, taker := haPair(t, threeDevices())
	a, b, c := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1"), newPhone(t, owner, "c1", "pc")
	for _, p := range []*phone{a, b, c} {
		p.register(t)
	}
	r1 := connect(t, a, b, "200")
	r2 := connect(t, b, c, "300")
	id1, id2 := r1.dcs.InviteRequest.CallID().Value(), r2.dcs.InviteRequest.CallID().Value()
	rec1 := waitRecord(t, ha, "the original call replicated", "sip-1", func(s livestate.DialogState) bool {
		return s.CallID == id1 && twoLegged(s)
	})
	rec2 := waitRecord(t, ha, "the consultation replicated", "sip-1", func(s livestate.DialogState) bool {
		return s.CallID == id2 && twoLegged(s)
	})
	haReinviteTimeout = time.Second
	killNode(owner, mem, map[string]int{"sip-1": 2}, "sip-2")
	taker.srv.takeoverPass(t.Context())
	ra := reinviteOn(t, a, id1, "takeover re-INVITE to the original caller")
	rb2 := reinviteOn(t, b, id2, "takeover re-INVITE to the transferor's consultation dialog")
	reinviteOn(t, c, rec2.Legs[1].CallID, "takeover re-INVITE to the consultation target")
	eventually(t, "both calls talk on the taker", func() bool {
		return connectedOn(taker, "200", livestate.HATakenOver) && connectedOn(taker, "300", livestate.HATakenOver)
	})
	if v := taker.metric(t, "hello_dialog_takeovers_total", nil); v != 2 {
		t.Fatalf("takeovers = %v, want 2", v)
	}
	hangupHomed(t, a, taker, ra, rec1.Legs[0])
	hangupHomed(t, b, taker, rb2, rec2.Legs[0])
	waitReq(t, c.byes, "BYE to the consultation target")
}

// matrixAttendedBridged: the node dies after an attended transfer bridged
// the two outer parties (the bridged half): the bridged call is anchored,
// replicated, and survives on the taker.
func matrixAttendedBridged(t *testing.T) {
	ha, mem, owner, taker := haPair(t, threeDevices())
	a, b, c := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1"), newPhone(t, owner, "c1", "pc")
	for _, p := range []*phone{a, b, c} {
		p.register(t)
	}
	r1 := connect(t, a, b, "200")
	var dssAB *sipgo.DialogServerSession
	for d := range serversOf(b) {
		dssAB = d
	}
	r2 := connect(t, b, c, "300")
	consult := waitRecord(t, ha, "consultation replicated", "sip-1", func(s livestate.DialogState) bool {
		return s.CallID == r2.dcs.InviteRequest.CallID().Value() && twoLegged(s)
	})
	cLeg := consult.Legs[1].CallID
	replaces := "sip:100@" + testDomain + "?Replaces=" + dssAB.InviteRequest.CallID().Value()
	if res := b.sendRefer(t, r2.dcs, "<"+replaces+">"); res.StatusCode != 202 {
		t.Fatalf("REFER = %d", res.StatusCode)
	}
	waitReq(t, b.notifies, "progress NOTIFY")
	if final := waitReq(t, b.notifies, "final NOTIFY"); !strings.Contains(string(final.Body()), "200 OK") {
		t.Fatalf("final NOTIFY = %q", final.Body())
	}
	waitReq(t, a.reinvites, "the caller refreshed on the anchor")
	handC := waitReq(t, c.reinvites, "the target moved onto the anchor")
	anchoredSDP(t, owner, handC.Body())
	waitReq(t, b.byes, "BYE to the transferee (1)")
	waitReq(t, b.byes, "BYE to the transferee (2)")
	owner.nextCDR(t)
	owner.nextCDR(t)
	aCallID := r1.dcs.InviteRequest.CallID().Value()
	st := waitRecord(t, ha, "the bridged call is replicated", "sip-1", func(s livestate.DialogState) bool {
		return s.Legs[0].CallID == aCallID && s.Legs[1].CallID == cLeg && s.Destination == "300"
	})
	haReinviteTimeout = time.Second
	killNode(owner, mem, map[string]int{"sip-1": 1}, "sip-2")
	taker.srv.takeoverPass(t.Context())
	ra := waitReq(t, a.reinvites, "takeover re-INVITE to the caller")
	rc := waitReq(t, c.reinvites, "takeover re-INVITE to the bridged target")
	anchoredSDP(t, taker, ra.Body())
	if rc.CSeq().SeqNo <= handC.CSeq().SeqNo {
		t.Fatalf("takeover CSeq %d does not pass the bridge's %d", rc.CSeq().SeqNo, handC.CSeq().SeqNo)
	}
	eventually(t, "the bridged call talks on the taker", func() bool { return connectedOn(taker, "300", livestate.HATakenOver) })
	hangupHomed(t, a, taker, ra, st.Legs[0])
	waitReq(t, c.byes, "BYE to the bridged target")
	if cd := taker.nextCDR(t); cd.FinalStatus != 200 || cd.Source != "100" || cd.Destination != "300" {
		t.Fatalf("bridged CDR on the taker = %+v", cd)
	}
}

// matrixAnnouncement: the node dies while an announcement plays into a
// connected call: the taker plays it again from its beginning (spec S-4).
func matrixAnnouncement(t *testing.T) {
	objs := &fakeObjects{}
	const obj = "ann/long.wav"
	_ = objs.Put(context.Background(), obj, media.EncodeWAV(make([]byte, 2*8000*5), 8000)) // 5s
	ha, mem, owner, taker := haPair(t, ringAllDevices(), withVoicemail(newVMStore(), objs))
	a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1")
	a.register(t)
	b.register(t)
	connect(t, a, b, "200")
	waitRecord(t, ha, "the call is replicated", "sip-1", twoLegged)
	go ownedCall(t, owner.srv).playAnnouncementObject(obj)
	st := waitRecord(t, ha, "the announcement is replicated", "sip-1", func(s livestate.DialogState) bool {
		return s.State == haPhaseAnnouncement && s.StateDetail == haRecordDetailPrefix+obj
	})
	haReinviteTimeout = time.Second
	killNode(owner, mem, map[string]int{"sip-1": 1}, "sip-2")
	taker.srv.takeoverPass(t.Context())
	ra := waitReq(t, a.reinvites, "takeover re-INVITE to the caller")
	waitReq(t, b.reinvites, "takeover re-INVITE to the callee")
	eventually(t, "the taker fetches the announcement again", func() bool {
		n := 0
		for _, k := range objs.snapshotGets() {
			if k == obj {
				n++
			}
		}
		return n == 2
	})
	hangupHomed(t, a, taker, ra, st.Legs[0])
	waitReq(t, b.byes, "BYE to the callee")
	if cd := taker.nextCDR(t); !strings.Contains(traceText(cd.Trace), "Announcement restarts from its beginning") {
		t.Fatalf("CDR trace: %s", traceText(cd.Trace))
	}
}

// matrixAnnouncementDestination: the node dies while an announcement
// destination plays (a one-legged call Hello answered itself, here the
// announcement feature code): the taker re-INVITEs the caller onto its own
// relay, plays the announcement from the top, then hangs up as the
// destination would have.
func matrixAnnouncementDestination(t *testing.T) {
	objs := &fakeObjects{}
	const obj = "ann/closed.wav"
	_ = objs.Put(context.Background(), obj, media.EncodeWAV(make([]byte, 2*8000*2), 8000)) // 2s
	feats := func(s *snapshot.Snapshot) {
		s.WithFeatureCodes([]snapshot.FeatureCode{{Code: "*89", Action: ActionAnnouncement, Argument: "closed"}})
		s.WithAnnouncements(map[string]string{"closed": obj})
	}
	ha, mem, owner, taker := haPair(t, ringAllDevices(), withFeatures(feats), withVoicemail(newVMStore(), objs))
	a := newPhone(t, owner, "a1", "pa")
	a.register(t)
	if r := waitCall(t, dial(t.Context(), a, "*89")); r.err != nil {
		t.Fatalf("announcement call: %v", r.err)
	}
	st := waitRecord(t, ha, "the announcement destination is replicated", "sip-1", func(s livestate.DialogState) bool {
		return s.State == haPhaseAnnouncement && s.Legs[1].CallID == "" && s.StateDetail == haRecordDetailPrefix+obj
	})
	haReinviteTimeout = time.Second
	killNode(owner, mem, map[string]int{"sip-1": 1}, "sip-2")
	taker.srv.takeoverPass(t.Context())
	ra := waitReq(t, a.reinvites, "takeover re-INVITE to the caller")
	anchoredSDP(t, taker, ra.Body())
	eventually(t, "the taker plays the announcement again", func() bool {
		n := 0
		for _, k := range objs.snapshotGets() {
			if k == obj {
				n++
			}
		}
		return n == 2
	})
	bye := waitReq(t, a.byes, "the destination hangs up from the taker")
	if bye.CallID().Value() != st.Legs[0].CallID || tagOfTest(bye.From()) != st.Legs[0].LocalTag {
		t.Fatalf("BYE broke the dialog: %s", bye.StartLine())
	}
	cd := taker.nextCDR(t)
	if tr := traceText(cd.Trace); !strings.Contains(tr, "Announcement restarts from its beginning") || cd.FinalStatus != 200 {
		t.Fatalf("CDR = %+v, trace: %s", cd, tr)
	}
}

// matrixVoicemail: the node dies while the caller is in voicemail: the
// taker re-INVITEs the caller onto its own media anchor and restarts the
// application from the greeting (spec edge case "mid-voicemail-prompt: the
// prompt restarts"); the message left afterwards is stored, and the
// application hangs up from the taker.
func matrixVoicemail(t *testing.T) {
	vm := newVMStore()
	vm.boxes["200"] = VoicemailBox{ID: 5, Extension: "200"}
	objs := &fakeObjects{}
	feats := func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{{Number: "200", VoicemailEnabled: true, VoicemailBoxID: 5}})
	}
	ha, mem, owner, taker := haPair(t, callerDevices(), shortRing(200*time.Millisecond),
		withFeatures(feats), withVoicemail(vm, objs))
	anchored(owner)
	anchored(taker)
	a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1")
	a.register(t)
	b.register(t)
	b.setCallee(ringForever())
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("voicemail call: %v", r.err)
	}
	st := waitRecord(t, ha, "the voicemail call is replicated", "sip-1", func(s livestate.DialogState) bool {
		return s.State == haPhaseVoicemail && s.Legs[1].CallID == "" && s.Legs[0].RemoteSDP != "" &&
			strings.HasPrefix(s.StateDetail, "vm:leave:") && strings.HasSuffix(s.StateDetail, ":200")
	})
	ownerAns, _ := media.ParseAudioSDP(r.dcs.InviteResponse.Body())
	haReinviteTimeout = time.Second
	killNode(owner, mem, map[string]int{"sip-1": 1}, "sip-2")
	taker.srv.takeoverPass(t.Context())
	ra := waitReq(t, a.reinvites, "takeover re-INVITE to the voicemail caller")
	waitReq(t, a.acks, "ACK to the caller's 200")
	nsdp, err := media.ParseAudioSDP(ra.Body())
	if err != nil || nsdp.Port == ownerAns.Port {
		t.Fatalf("re-INVITE SDP %q (%v) does not name the taker's own anchor", ra.Body(), err)
	}
	eventually(t, "the voicemail call lives on the taker", func() bool { return connectedOn(taker, "200", livestate.HATakenOver) })
	// The caller leaves the message on the taker: the record loop runs
	// there, past the restarted greeting and beep. The taker's application
	// starts with its box lookup (the owner's was the first); its recording
	// clock starts right after, so the caller speaks only from then on.
	eventually(t, "the taker restarts the voicemail application", func() bool {
		vm.mu.Lock()
		defer vm.mu.Unlock()
		return vm.lookups >= 2
	})
	feed := startRTPFeed(t, ra.Body())
	feed.silence(2.5)
	feed.digit('#')
	eventually(t, "the message is stored by the taker", func() bool {
		for _, m := range vm.snapshotMsgs() {
			if m.BoxID == 5 && m.Caller == "100" && m.DurationMs >= 1000 {
				return true
			}
		}
		return false
	})
	bye := waitReq(t, a.byes, "the voicemail application hangs up from the taker")
	if bye.CallID().Value() != st.Legs[0].CallID || tagOfTest(bye.From()) != st.Legs[0].LocalTag {
		t.Fatalf("BYE broke the dialog: %s", bye.StartLine())
	}
	cd := taker.nextCDR(t)
	if tr := traceText(cd.Trace); !strings.Contains(tr, "Voicemail restarts from its greeting") || !strings.Contains(tr, "ha: taken over from sip-1") {
		t.Fatalf("CDR trace: %s", tr)
	}
	if z := taker.metric(t, "hello_zombie_calls_total", nil); z != 0 {
		t.Fatalf("zombies = %v, want 0", z)
	}
}
