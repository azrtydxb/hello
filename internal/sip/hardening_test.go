package sip

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
)

// edgeInvite is an INVITE for target routed through node via a flow token.
func edgeInvite(from *phone, routeHost, token string, target sip.Uri) *sip.Request {
	req := sip.NewRequest(sip.INVITE, target)
	req.AppendHeader(&sip.FromHeader{Address: from.aorURI(), Params: sip.HeaderParams{{K: "tag", V: sip.GenerateTagN(8)}}})
	req.AppendHeader(&sip.ToHeader{Address: target})
	callID := sip.CallIDHeader(newID())
	req.AppendHeader(&callID)
	req.AppendHeader(sip.NewHeader("Route", "<sip:"+routeHost+";lr;hflow="+token+">"))
	req.AppendHeader(sip.HeaderClone(&from.contact))
	return req
}

func phoneURI(p *phone) sip.Uri {
	host, port, _ := sip.ParseAddr(p.addr)
	return sip.Uri{Scheme: "sip", User: p.user, Host: host, Port: port}
}

// TestEdgeRefusesNonPeer fails if a request with a valid flow token is
// relayed towards the phone when it does not come from a peer node.
func TestEdgeRefusesNonPeer(t *testing.T) {
	nodeA, _ := twoNodes(t)
	target := newPhone(t, nodeA, "b1", "pb1")
	target.setCallee(busy())
	x := newPhone(t, nodeA, "x", "x")
	tok := nodeA.srv.flowToken(target.addr, "udp", time.Now().Add(time.Hour))
	req := edgeInvite(x, nodeA.addr, tok, phoneURI(target))
	req.SetDestination(nodeA.addr)
	if res := x.do(req); res.StatusCode != 403 {
		t.Fatalf("relay from a non-peer = %d, want 403", res.StatusCode)
	}
	noReq(t, target.invites, 200*time.Millisecond, "INVITE relayed for a non-peer")
}

// TestEdgeTokenExpiry fails if an expired flow token is honoured, or a fresh
// one from a peer is not relayed.
func TestEdgeTokenExpiry(t *testing.T) {
	nodeA, _, pr := twoNodesPresence(t)
	target := newPhone(t, nodeA, "b1", "pb1")
	target.setCallee(busy())
	peer := newPhone(t, nodeA, "x", "x")
	pr.add(peer.addr) // the test phone stands in for a peer node
	eventually(t, "peer known", func() bool { return nodeA.srv.peers.has(peer.addr) })

	expired := nodeA.srv.flowToken(target.addr, "udp", time.Now().Add(-time.Second))
	req := edgeInvite(peer, nodeA.addr, expired, phoneURI(target))
	req.SetDestination(nodeA.addr)
	if res := peer.do(req); res.StatusCode != 403 {
		t.Fatalf("expired token = %d, want 403", res.StatusCode)
	}
	noReq(t, target.invites, 200*time.Millisecond, "INVITE relayed on an expired token")

	fresh := nodeA.srv.flowToken(target.addr, "udp", time.Now().Add(time.Hour))
	req = edgeInvite(peer, nodeA.addr, fresh, phoneURI(target))
	req.SetDestination(nodeA.addr)
	if res := peer.do(req); res.StatusCode != 486 {
		t.Fatalf("fresh token from a peer = %d, want the phone's 486 relayed", res.StatusCode)
	}
	inv := waitReq(t, target.invites, "INVITE relayed over the flow")
	if rr := inv.GetHeader("Record-Route"); rr == nil || !strings.Contains(rr.Value(), nodeA.addr) {
		t.Fatalf("Record-Route = %v", rr)
	}
}

// TestEdgeOnlyOwnRoute fails if a flow-token Route naming another host
// makes this node act as an edge proxy.
func TestEdgeOnlyOwnRoute(t *testing.T) {
	nodeA, _ := twoNodes(t)
	target := newPhone(t, nodeA, "b1", "pb1")
	x := newPhone(t, nodeA, "x", "x")
	tok := nodeA.srv.flowToken(target.addr, "udp", time.Now().Add(time.Hour))
	req := edgeInvite(x, "198.51.100.9:5060", tok, sip.Uri{Scheme: "sip", User: "200", Host: testDomain})
	req.SetDestination(nodeA.addr)
	// Not ours, so handled as a new call: challenged, not proxied or refused.
	if res := x.do(req); res.StatusCode != 401 {
		t.Fatalf("foreign Route with token = %d, want 401 from the B2BUA", res.StatusCode)
	}
}

// TestEdgeFromFlowNotToOutside fails if a phone can use its node's edge to
// reach a host that is not a cluster node: such a request must be handled
// by the node itself (here: challenged), never relayed.
func TestEdgeFromFlowNotToOutside(t *testing.T) {
	nodeA, _ := twoNodes(t)
	f := newPhone(t, nodeA, "b1", "pb1")
	f.register(t)
	sink := newPhone(t, nodeA, "s", "s")
	tok := nodeA.srv.flowToken(f.addr, "udp", time.Now().Add(time.Hour))
	req := edgeInvite(f, nodeA.addr, tok, phoneURI(sink))
	req.SetDestination(nodeA.addr)
	if res := f.do(req); res.StatusCode != 401 {
		t.Fatalf("phone relaying to an outside host = %d, want 401 (handled locally)", res.StatusCode)
	}
	noReq(t, sink.invites, 200*time.Millisecond, "INVITE relayed to an outside host")
}

// TestEdgeNeverRelaysRegisterOrOptions fails if REGISTER or OPTIONS with a
// flow-token Route are relayed instead of handled by this node.
func TestEdgeNeverRelaysRegisterOrOptions(t *testing.T) {
	nodeA, _, pr := twoNodesPresence(t)
	sink := newPhone(t, nodeA, "b1", "pb1")
	peer := newPhone(t, nodeA, "x", "x")
	pr.add(peer.addr)
	eventually(t, "peer known", func() bool { return nodeA.srv.peers.has(peer.addr) })
	route := sip.NewHeader("Route", "<sip:"+nodeA.addr+";lr;hflow="+nodeA.srv.flowToken(sink.addr, "udp", time.Now().Add(time.Hour))+">")

	reg := sink.registerReq(300)
	reg.AppendHeader(route)
	if res := peer.do(reg); res.StatusCode != 401 {
		t.Fatalf("REGISTER via edge = %d, want 401 from the registrar", res.StatusCode)
	}
	opt := options(peer)
	opt.AppendHeader(sip.HeaderClone(route))
	res := peer.do(opt)
	if res.StatusCode != 200 || res.Contact() == nil || res.Contact().Address.HostPort() != nodeA.addr {
		t.Fatalf("OPTIONS via edge = %d %v, want 200 from this node", res.StatusCode, res.Contact())
	}
}

// inDialog builds a request inside a dialog from the given headers.
func inDialog(m sip.RequestMethod, ruri sip.Uri, from *sip.FromHeader, to *sip.ToHeader, callID string, dest string) *sip.Request {
	req := sip.NewRequest(m, ruri)
	req.AppendHeader(sip.HeaderClone(from))
	req.AppendHeader(sip.HeaderClone(to))
	cid := sip.CallIDHeader(callID)
	req.AppendHeader(&cid)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: 9000, MethodName: m})
	req.SetDestination(dest)
	return req
}

func withTag(to *sip.ToHeader, tag string) *sip.ToHeader {
	c := sip.HeaderClone(to).(*sip.ToHeader)
	c.Params.Add("tag", tag)
	return c
}

// TestForgedInDialogRequests fails if a BYE or re-INVITE carrying a call's
// Call-ID but the wrong tags, or the right tags from the wrong source, ends
// or redirects the call.
func TestForgedInDialogRequests(t *testing.T) {
	pbx := startPBX(t, ringAllDevices())
	a, b, x := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1"), newPhone(t, pbx, "x", "x")
	a.register(t)
	b.register(t)
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	inv := waitReq(t, b.invites, "callee INVITE")
	waitReq(t, b.acks, "callee ACK")
	pbxURI := sip.Uri{Scheme: "sip", Host: "127.0.0.1"}
	pbxURI.Host, pbxURI.Port, _ = sip.ParseAddr(pbx.addr)

	aCallID, aFrom, aTo := r.dcs.InviteRequest.CallID().Value(), r.dcs.InviteRequest.From(), r.dcs.InviteResponse.To()
	// The caller's own socket, wrong To tag.
	if res := a.do(inDialog(sip.BYE, pbxURI, aFrom, withTag(aTo, "bogus"), aCallID, pbx.addr)); res.StatusCode != 481 {
		t.Fatalf("BYE with wrong tag = %d, want 481", res.StatusCode)
	}
	// Right Call-ID and tags, wrong source.
	if res := x.do(inDialog(sip.BYE, pbxURI, aFrom, aTo, aCallID, pbx.addr)); res.StatusCode != 481 {
		t.Fatalf("BYE from another source = %d, want 481", res.StatusCode)
	}
	re := inDialog(sip.INVITE, pbxURI, aFrom, aTo, aCallID, pbx.addr)
	re.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	re.SetBody([]byte("v=0\r\no=evil 1 1 IN IP4 203.0.113.66\r\ns=-\r\nc=IN IP4 203.0.113.66\r\nt=0 0\r\nm=audio 9 RTP/AVP 0\r\n"))
	re.AppendHeader(sip.HeaderClone(&x.contact))
	if res := x.do(re); res.StatusCode != 481 {
		t.Fatalf("re-INVITE from another source = %d, want 481", res.StatusCode)
	}
	// The callee's leg: its tags, sent from elsewhere.
	b.mu.Lock()
	dss := b.servers[inv.CallID().Value()]
	b.mu.Unlock()
	bFrom := dss.InviteResponse.To().AsFrom()
	bTo := inv.From().AsTo()
	if res := x.do(inDialog(sip.BYE, pbxURI, &bFrom, &bTo, inv.CallID().Value(), pbx.addr)); res.StatusCode != 481 {
		t.Fatalf("BYE on the callee's leg from another source = %d, want 481", res.StatusCode)
	}
	noReq(t, b.byes, 100*time.Millisecond, "BYE to the callee")
	noReq(t, b.reinvites, 50*time.Millisecond, "re-INVITE to the callee")
	noReq(t, a.byes, 50*time.Millisecond, "BYE to the caller")

	// The call is still up and ends normally.
	hangup(t, r.dcs)
	waitReq(t, b.byes, "real BYE")
	if cd := pbx.nextCDR(t); cd.TerminationSide != "caller" || cd.FinalStatus != 200 {
		t.Fatalf("CDR = %+v", cd)
	}
}

// TestMaxCallDuration fails if a connected call that nobody hangs up is not
// ended at MaxCallDuration with BYE to both legs and a system-side CDR.
func TestMaxCallDuration(t *testing.T) {
	pbx := startPBX(t, ringAllDevices(), func(c *Config, _ *Deps) { c.MaxCallDuration = 300 * time.Millisecond })
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, a.byes, "BYE to the caller at max duration")
	waitReq(t, b.byes, "BYE to the callee at max duration")
	cd := pbx.nextCDR(t)
	if cd.TerminationSide != "system" || cd.FailureReason != "max duration" || cd.AnswerTime.IsZero() {
		t.Fatalf("CDR = %+v", cd)
	}
	eventually(t, "call released", func() bool {
		return pbx.srv.ActiveCalls() == 0 && pbx.metric(t, "hello_active_calls", nil) == 0 && len(liveCalls(t, pbx)) == 0
	})
}

// TestLegReportAfterSetup fails if a fork's report blocks once setup has
// stopped reading (the fork's goroutine would leak).
func TestLegReportAfterSetup(t *testing.T) {
	c := &call{events: make(chan legEvent), setupDone: make(chan struct{})}
	close(c.setupDone)
	done := make(chan struct{})
	go func() {
		c.report(legEvent{kind: evFailed, code: 487})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("report blocked after setup returned")
	}
}

// TestRegisterContactCapAndPath fails if an AOR can hold more than ten
// bindings, or if a client-supplied Path is stored.
func TestRegisterContactCapAndPath(t *testing.T) {
	pbx := startPBX(t, ringAllDevices())
	p := newPhone(t, pbx, "a1", "pa")
	var ten []string
	for i := range 10 {
		ten = append(ten, "<sip:a1@127.0.0.1:"+strconv.Itoa(7000+i)+">")
	}
	req := p.registerReq(300, ten...)
	req.AppendHeader(sip.NewHeader("Path", "<sip:evil.example.com;lr>"))
	if res := p.authDo(req, AlgSHA256, "pa"); res.StatusCode != 200 {
		t.Fatalf("ten contacts = %d", res.StatusCode)
	}
	for _, b := range bindings(t, pbx, "sip:a1@"+testDomain) {
		if len(b.Path) != 1 || strings.Contains(b.Path[0], "evil") || !strings.Contains(b.Path[0], pbx.addr) {
			t.Fatalf("Path = %v, want only this node's", b.Path)
		}
	}
	if res := p.authDo(p.registerReq(300, "<sip:a1@127.0.0.1:7999>"), AlgSHA256, "pa"); res.StatusCode != 403 {
		t.Fatalf("eleventh contact = %d, want 403", res.StatusCode)
	}
	if res := p.authDo(p.registerReq(300, ten[0]), AlgSHA256, "pa"); res.StatusCode != 200 {
		t.Fatalf("refresh at the cap = %d, want 200", res.StatusCode)
	}
	if n := len(bindings(t, pbx, "sip:a1@"+testDomain)); n != 10 {
		t.Fatalf("bindings = %d, want 10", n)
	}
}

// TestOneSnapshotPerInvite fails if a call attempt reads the configuration
// snapshot more than once per INVITE (authentication and routing must see
// the same revision).
func TestOneSnapshotPerInvite(t *testing.T) {
	pbx := startPBX(t, ringAllDevices())
	a := newPhone(t, pbx, "a1", "pa")
	before := pbx.snaps.reads.Load()
	r := waitCall(t, dial(t.Context(), a, "999")) // challenged once, then 404
	if responseCode(r.err) != 404 {
		t.Fatalf("call = %v", r.err)
	}
	if n := pbx.snaps.reads.Load() - before; n != 2 {
		t.Fatalf("snapshot reads for two INVITEs = %d, want 2", n)
	}
}

// TestServingReportsDeadListener fails if Serving stays true after the SIP
// socket dies.
func TestServingReportsDeadListener(t *testing.T) {
	pbx := startPBX(t, nil)
	eventually(t, "serving", pbx.srv.Serving)
	_ = pbx.conn.Close()
	eventually(t, "not serving after the socket died", func() bool { return !pbx.srv.Serving() })
}
