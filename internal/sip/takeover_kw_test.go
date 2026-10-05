// In-call HA defects found live on kw (2026-10-05), reproduced in-process:
// the takeover's hop when the edge double-record-routes with distinct
// phone-facing and Hello-facing addresses, symmetric responses to the
// edge's probes, and handing calls over on drain instead of cutting them
// at the drain timeout.
package sip

import (
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// withTrusted sets the trusted proxy ranges (kw trusts its whole node CIDR,
// which also holds the edge's phone-facing NodePort address).
func withTrusted(prefixes ...string) pbxOpt {
	return func(c *Config, _ *Deps) {
		for _, p := range prefixes {
			c.TrustedProxies = append(c.TrustedProxies, netip.MustParsePrefix(p))
		}
	}
}

// TestTakeoverEdgeRouteSet fails if a taker sends a callee leg's in-dialog
// request to the edge's phone-facing address instead of its Hello-facing
// one (kw: the leg-b re-INVITE went to the public NodePort and got 403).
// The callee's 200 carries the edge's double Record-Route as Kamailio
// writes it towards a phone: the phone-facing entry on top (an address in
// a trusted range that does not route), the Hello-facing one below (here
// the phone itself, standing in for the edge). The replicated route set
// must be the UAC's - reversed - and the takeover must use its first hop.
func TestTakeoverEdgeRouteSet(t *testing.T) {
	trusted := withTrusted("127.0.0.0/8", "192.0.2.0/24")
	ha, mem, owner, taker := haPair(t, ringAllDevices(), trusted)
	a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1")
	a.register(t)
	b.register(t)
	phoneFacing := "<sip:192.0.2.99:30508;r2=on;lr>"
	helloFacing := "<sip:" + b.addr + ";r2=on;lr>"
	b.setCallee(func(p *phone, _ *sip.Request, _ sip.ServerTransaction, dss *sipgo.DialogServerSession) {
		_ = dss.Respond(200, "OK", []byte(p.sdp), sip.NewHeader("Content-Type", "application/sdp"),
			sip.NewHeader("Record-Route", phoneFacing), sip.NewHeader("Record-Route", helloFacing))
	})
	connect(t, a, b, "200")
	st := waitRecord(t, ha, "the call is replicated", "sip-1", twoLegged)
	if rs := st.Legs[1].RouteSet; len(rs) != 2 || rs[0] != helloFacing || rs[1] != phoneFacing {
		t.Fatalf("callee leg route set = %v, want [%s %s] (the 200's Record-Route reversed)", rs, helloFacing, phoneFacing)
	}
	haReinviteTimeout = time.Second
	killNode(owner, mem, map[string]int{"sip-1": 1}, "sip-2")
	taker.srv.takeoverPass(t.Context())
	rb := waitReq(t, b.reinvites, "takeover re-INVITE through the Hello-facing hop")
	routes := headerValues(rb, "Route")
	if len(routes) != 2 || routes[0] != helloFacing {
		t.Fatalf("re-INVITE Route = %v, want the Hello-facing hop first", routes)
	}
	waitReq(t, a.reinvites, "takeover re-INVITE to the caller")
	eventually(t, "the call talks on the taker", func() bool { return connectedOn(taker, "200", livestate.HATakenOver) })
	if z := taker.metric(t, "hello_zombie_calls_total", nil); z != 0 {
		t.Fatalf("zombies = %v (the callee leg failed)", z)
	}
}

// TestSymmetricResponse fails if Hello answers a request at its Via port
// rather than at the address it came from (kw: the edge's dispatcher
// OPTIONS carry no rport; answered at Via port 5070, a replaced Kamailio
// pod's stale conntrack kept both nodes probed DOWN).
func TestSymmetricResponse(t *testing.T) {
	pbx := startPBX(t, nil)
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	src := conn.LocalAddr().(*net.UDPAddr)
	// Via names a port nobody listens on; the datagram leaves from src.
	msg := "OPTIONS sip:" + pbx.addr + " SIP/2.0\r\n" +
		"Via: SIP/2.0/UDP 127.0.0.1:9;branch=z9hG4bK-sym1\r\n" +
		"From: <sip:kamailio@hello.edge>;tag=probe\r\n" +
		"To: <sip:" + pbx.addr + ">\r\n" +
		"Call-ID: sym-probe-1\r\n" +
		"CSeq: 1 OPTIONS\r\n" +
		"Max-Forwards: 70\r\n" +
		"Content-Length: 0\r\n\r\n"
	dst, _ := net.ResolveUDPAddr("udp", pbx.addr)
	if _, err := conn.WriteTo([]byte(msg), dst); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 4096)
	n, _, err := conn.ReadFrom(buf)
	if err != nil {
		t.Fatalf("no answer at the probe's source %s (answered at the Via port?): %v", src, err)
	}
	if !strings.HasPrefix(string(buf[:n]), "SIP/2.0 200") {
		t.Fatalf("answer = %q", buf[:n])
	}
}

// TestHandoffOnDrain fails if a draining node cuts its live call at the
// drain timeout instead of handing it to a surviving node (kw: CDR "drain
// timeout"): the call's record must be marked for handoff, a survivor must
// claim it while the owner is merely DRAINING (never before the drain),
// re-INVITE both endpoints and carry the call; the drainer must yield its
// copy (no BYE to the endpoints), answer 503 to stray in-dialog requests so
// the edge retries them on the taker, and end up with no live call so its
// drain completes.
func TestHandoffOnDrain(t *testing.T) {
	defer func(d time.Duration) { handoffPoll = d }(handoffPoll)
	handoffPoll = 20 * time.Millisecond
	ha, mem, owner, taker := haPair(t, ringAllDevices())
	a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1")
	a.register(t)
	b.register(t)
	connect(t, a, b, "200")
	st := waitRecord(t, ha, "the call is replicated", "sip-1", twoLegged)

	// The owner is DRAINING but has not handed anything off: a fresh
	// dialog of a live node is never taken.
	mem.set(
		cluster.Member{ID: "sip-1", Kind: cluster.KindSIP, State: cluster.Draining, ActiveCalls: 1},
		cluster.Member{ID: "sip-2", Kind: cluster.KindSIP, State: cluster.Ready},
	)
	taker.srv.takeoverPass(t.Context())
	noReq(t, a.reinvites, 150*time.Millisecond, "a takeover of a call nobody handed off")

	haReinviteTimeout = time.Second
	owner.srv.Drain()
	waitRecord(t, ha, "the call is marked for handoff", "sip-1", func(s livestate.DialogState) bool { return s.Handoff })
	taker.srv.takeoverPass(t.Context())
	ra := waitReq(t, a.reinvites, "handoff re-INVITE to the caller")
	waitReq(t, b.reinvites, "handoff re-INVITE to the callee")
	eventually(t, "the call talks on the survivor", func() bool { return connectedOn(taker, "200", livestate.HATakenOver) })

	// The drainer yields: its CDR says so, it holds no call, and the
	// endpoints got no BYE from it.
	cd := owner.nextCDR(t)
	if !strings.Contains(cd.FailureReason, "taken over by sip-2") {
		t.Fatalf("drainer CDR = %+v", cd)
	}
	eventually(t, "the drainer holds no call", func() bool { return owner.srv.ActiveCalls() == 0 })
	noReq(t, b.byes, 100*time.Millisecond, "BYE from the drainer")

	// A BYE that still reaches the drainer gets 503 (the edge retries it
	// on a survivor); sent to the survivor it ends the call.
	stray := sip.NewRequest(sip.BYE, sip.Uri{Scheme: "sip", Host: hostOf(owner.addr), Port: portOf(owner.addr)})
	stray.AppendHeader(&sip.FromHeader{Address: *ra.To().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: st.Legs[0].RemoteTag}}})
	stray.AppendHeader(&sip.ToHeader{Address: *ra.From().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: st.Legs[0].LocalTag}}})
	callID := sip.CallIDHeader(st.Legs[0].CallID)
	stray.AppendHeader(&callID)
	stray.AppendHeader(&sip.CSeqHeader{SeqNo: ra.CSeq().SeqNo + 1, MethodName: sip.BYE})
	stray.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	stray.SetTransport("UDP")
	if res := a.do(stray); res.StatusCode != 503 {
		t.Fatalf("BYE to the drainer = %d, want 503 so the edge retries", res.StatusCode)
	}
	hangupHomed(t, a, taker, ra, st.Legs[0])
	waitReq(t, b.byes, "BYE to the callee from the survivor")
	if z := taker.metric(t, "hello_zombie_calls_total", nil); z != 0 {
		t.Fatalf("zombies = %v", z)
	}
	ha.mu.Lock()
	stale := ha.staleReleases
	ha.mu.Unlock()
	if stale != 0 {
		t.Fatalf("%d claims released before the record named the taker", stale)
	}
}

// TestHandoffCancelledDrain fails if a cancelled drain does not take back
// a call no survivor has claimed yet: its record must lose the handoff
// mark, so a later scan of a DRAINING node never takes it.
func TestHandoffCancelledDrain(t *testing.T) {
	ha, _, owner, _ := haPair(t, ringAllDevices())
	a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1")
	a.register(t)
	b.register(t)
	connect(t, a, b, "200")
	waitRecord(t, ha, "the call is replicated", "sip-1", twoLegged)
	owner.srv.Drain()
	waitRecord(t, ha, "marked for handoff", "sip-1", func(s livestate.DialogState) bool { return s.Handoff })
	owner.srv.Undrain()
	waitRecord(t, ha, "the handoff mark withdrawn", "sip-1", func(s livestate.DialogState) bool { return !s.Handoff })
	if owner.srv.ActiveCalls() != 1 {
		t.Fatal("the cancelled drain lost the call")
	}
}
