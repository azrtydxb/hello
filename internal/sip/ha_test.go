package sip

import (
	"context"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/emiago/sipgo/sip"
)

type fakeLifecycle struct {
	mu     sync.Mutex
	state  cluster.State
	reason string
}

func (f *fakeLifecycle) State() (cluster.State, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, f.reason
}

func (f *fakeLifecycle) set(s cluster.State, reason string) {
	f.mu.Lock()
	f.state, f.reason = s, reason
	f.mu.Unlock()
}

func withLifecycle(lc *fakeLifecycle) pbxOpt {
	return func(_ *Config, d *Deps) { d.Lifecycle = lc }
}

// TestDrainingRefusesNewWorkKeepsDialogs fails if, while DRAINING, a new
// INVITE or REGISTER is not refused with 503 and Retry-After: 5, or if an
// established call's re-INVITE and BYE, or a ringing call's CANCEL, are not
// still handled.
func TestDrainingRefusesNewWorkKeepsDialogs(t *testing.T) {
	lc := &fakeLifecycle{state: cluster.Ready}
	pbx := startPBX(t, ringAllDevices(), withLifecycle(lc))
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, b.acks, "callee ACK")
	// A second call that is still ringing when the drain starts.
	c, cb := newPhone(t, pbx, "b2", "pb2"), newPhone(t, pbx, "b2", "pb2")
	_ = c
	cb.register(t)
	cb.setCallee(ringForever())
	b.setCallee(ringForever())
	ringCtx, cancelRing := context.WithCancel(t.Context())
	ringing := dial(ringCtx, a, "200")
	waitReq(t, cb.invites, "ringing INVITE")

	lc.set(cluster.Draining, "drain requested")
	res := a.do(a.inviteReq("200"))
	if res.StatusCode != 503 || res.GetHeader("Retry-After") == nil || res.GetHeader("Retry-After").Value() != "5" {
		t.Fatalf("new INVITE while draining = %d %v, want 503 Retry-After: 5", res.StatusCode, res.GetHeader("Retry-After"))
	}
	if res := a.do(a.registerReq(300)); res.StatusCode != 503 || res.GetHeader("Retry-After") == nil {
		t.Fatalf("REGISTER while draining = %d, want 503 with Retry-After", res.StatusCode)
	}
	// The ringing call's CANCEL still works.
	cancelRing()
	if rr := waitCall(t, ringing); rr.dcs.InviteResponse == nil || rr.dcs.InviteResponse.StatusCode != 487 {
		t.Fatalf("CANCEL while draining: caller got %v", rr.dcs.InviteResponse)
	}
	waitReq(t, cb.cancels, "CANCEL to the ringing callee")
	pbx.nextCDR(t)
	// The established call's re-INVITE and BYE still work.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	re := sip.NewRequest(sip.INVITE, r.dcs.InviteResponse.Contact().Address)
	re.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	re.SetBody([]byte(a.sdp))
	if res, err := r.dcs.Do(ctx, re); err != nil || res.StatusCode != 200 {
		t.Fatalf("re-INVITE while draining = %v, %v", res, err)
	}
	_ = r.dcs.WriteRequest(sip.NewRequest(sip.ACK, r.dcs.InviteResponse.Contact().Address))
	hangup(t, r.dcs)
	waitReq(t, b.byes, "BYE while draining")
	if cd := pbx.nextCDR(t); cd.FinalStatus != 200 || cd.TerminationSide != "caller" {
		t.Fatalf("CDR = %+v", cd)
	}
}

// TestDrainReleasesTrunkLeases fails if draining does not release the
// node's trunk leases at once (so another node registers the trunk), or if
// a drained node takes a lease again.
func TestDrainReleasesTrunkLeases(t *testing.T) {
	cr := newCarrier(t, "acct", "pw")
	r := &fakeRouter{trunks: []routing.Trunk{cr.trunk(1, "carrier-reg", "registration")}}
	shared := newFakeTrunkState()
	// A 1s refresh makes the lease live 3s: an unreleased lease would move
	// only after that, a released one at node B's next refresh.
	slow := func(c *Config, _ *Deps) { c.TrunkLeaseRefresh = time.Second }
	nodeA := startPBX(t, nil, trunkCfg(shared), withRouter(r, nil), slow, func(c *Config, _ *Deps) { c.NodeID = "sip-a" })
	eventually(t, "node A holds the lease", func() bool { return shared.holder(1) == "sip-a" })
	nodeB := startPBX(t, nil, trunkCfg(shared), withRouter(r, nil), slow, func(c *Config, _ *Deps) { c.NodeID = "sip-b" })
	time.Sleep(1200 * time.Millisecond)
	if shared.holder(1) != "sip-a" {
		t.Fatal("lease moved without a drain")
	}
	nodeA.srv.Drain()
	if shared.holder(1) != "" && shared.holder(1) != "sip-b" {
		t.Fatalf("lease still held by %s after the drain returned", shared.holder(1))
	}
	start := time.Now()
	eventually(t, "node B takes over", func() bool { return shared.holder(1) == "sip-b" })
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("takeover took %s: the lease was not released, it expired", d)
	}
	eventually(t, "node B registered", func() bool {
		for _, reg := range cr.registrations() {
			if reg.Contact().Address.HostPort() == nodeB.addr {
				return true
			}
		}
		return false
	})
	time.Sleep(2500 * time.Millisecond)
	if shared.holder(1) != "sip-b" {
		t.Fatalf("drained node took the lease back (holder %s)", shared.holder(1))
	}
}

// TestDrainTimeoutHangsUp fails if the drain timeout does not BYE both
// legs of a connected call and end a ringing one, with CDR side system and
// reason "drain timeout".
func TestDrainTimeoutHangsUp(t *testing.T) {
	pbx := startPBX(t, ringAllDevices())
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, b.acks, "callee ACK")
	pbx.srv.HangupAll("drain timeout")
	waitReq(t, a.byes, "BYE to the caller")
	waitReq(t, b.byes, "BYE to the callee")
	cd := pbx.nextCDR(t)
	if cd.TerminationSide != "system" || cd.FailureReason != "drain timeout" || cd.AnswerTime.IsZero() {
		t.Fatalf("CDR = %+v", cd)
	}
	eventually(t, "no calls left", func() bool { return pbx.srv.ActiveCalls() == 0 })

	b.setCallee(ringForever())
	res := dial(t.Context(), a, "200")
	waitReq(t, b.invites, "ringing INVITE")
	waitRing(t, a)
	pbx.srv.HangupAll("drain timeout")
	if rr := waitCall(t, res); responseCode(rr.err) != 503 {
		t.Fatalf("ringing caller got %v, want 503", rr.err)
	}
	waitReq(t, b.cancels, "CANCEL to the ringing callee")
	if cd := pbx.nextCDR(t); cd.TerminationSide != "system" || cd.FailureReason != "drain timeout" {
		t.Fatalf("CDR = %+v", cd)
	}
}

// TestOptionsSelfProbe fails if an OPTIONS to this node is not 200 only in
// READY (503 with Retry-After otherwise), or if an OPTIONS to the domain
// stops being answered 200.
func TestOptionsSelfProbe(t *testing.T) {
	lc := &fakeLifecycle{state: cluster.Joining, reason: "snapshot: not loaded"}
	pbx := startPBX(t, nil, withLifecycle(lc))
	p := newPhone(t, pbx, "x", "x")
	host, port, _ := sip.ParseAddr(pbx.addr)
	probe := func() *sip.Response {
		req := sip.NewRequest(sip.OPTIONS, sip.Uri{Scheme: "sip", Host: host, Port: port})
		req.SetDestination(pbx.addr)
		return p.do(req)
	}
	for _, tc := range []struct {
		state cluster.State
		code  int
	}{{cluster.Joining, 503}, {cluster.Ready, 200}, {cluster.Unhealthy, 503}, {cluster.Draining, 503}, {cluster.Ready, 200}} {
		lc.set(tc.state, "reason")
		res := probe()
		if res.StatusCode != tc.code {
			t.Fatalf("probe in %s = %d, want %d", tc.state, res.StatusCode, tc.code)
		}
		if tc.code == 503 && (res.GetHeader("Retry-After") == nil || res.GetHeader("Retry-After").Value() != "5") {
			t.Fatalf("probe in %s: no Retry-After: 5", tc.state)
		}
		lc.set(cluster.Draining, "reason")
		if res := p.do(options(p)); res.StatusCode != 200 {
			t.Fatalf("OPTIONS to the domain in %s = %d, want 200", tc.state, res.StatusCode)
		}
	}
}

// proxied adds what a trusted edge proxy adds to a request.
func proxied(req *sip.Request, client, path string) *sip.Request {
	req.AppendHeader(sip.NewHeader("X-Hello-Client", client))
	if path != "" {
		req.AppendHeader(sip.NewHeader("Path", path))
	}
	return req
}

// TestTrustedProxyHonoured fails if, from a trusted proxy, X-Hello-Client
// is not the binding source and the throttle key, the proxy's Path is not
// stored, or a call to that binding does not go through the Path.
func TestTrustedProxyHonoured(t *testing.T) {
	pbx := startPBX(t, ringAllDevices(), func(c *Config, _ *Deps) {
		c.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	})
	kam := newPhone(t, pbx, "b1", "pb1") // stands in for Kamailio: calls to b1 must reach it
	kamPath := "<sip:" + kam.addr + ";lr;received=sip:198.51.100.7:40000>"
	p := newPhone(t, pbx, "b1", "pb1")
	contact := "<sip:b1@10.1.1.7:5060>"
	res := p.authDo(proxied(p.registerReq(300, contact), "198.51.100.7:40000", kamPath), AlgSHA256, "pb1")
	if res.StatusCode != 200 {
		t.Fatalf("REGISTER through the proxy = %d", res.StatusCode)
	}
	b := bindings(t, pbx, "sip:b1@"+testDomain)["sip:b1@10.1.1.7:5060"]
	if b.Source != "198.51.100.7:40000" || len(b.Path) != 1 || b.Path[0] != kamPath {
		t.Fatalf("binding = %+v; want the client's source and the proxy's Path", b)
	}
	// Failed auth is keyed on the client, not the proxy.
	if res := p.authDo(proxied(p.registerReq(300), "198.51.100.99:5060", ""), AlgSHA256, "wrong"); res.StatusCode != 401 {
		t.Fatalf("bad password = %d", res.StatusCode)
	}
	th := pbx.throttle.(*fakeThrottle)
	if n, _ := th.Failures(context.Background(), "198.51.100.99"); n != 1 {
		t.Fatalf("failures for the client IP = %d, want 1", n)
	}
	if n, _ := th.Failures(context.Background(), "127.0.0.1"); n != 0 {
		t.Fatalf("failures for the proxy IP = %d, want 0", n)
	}
	// A call to the binding goes to the Path, with it as Route.
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	inv := waitReq(t, kam.invites, "INVITE via the stored Path")
	if rt := inv.GetHeader("Route"); rt == nil || !strings.Contains(rt.Value(), kam.addr) || inv.Recipient.Host != "10.1.1.7" {
		t.Fatalf("INVITE R-URI %s Route %v; want the contact via the Path", inv.Recipient.String(), inv.GetHeader("Route"))
	}
	hangup(t, r.dcs)
	pbx.nextCDR(t)
}

// TestUntrustedProxyIgnored fails if X-Hello-Client or a client Path from a
// source that is not a trusted proxy is believed.
func TestUntrustedProxyIgnored(t *testing.T) {
	pbx := startPBX(t, ringAllDevices()) // no trusted proxies
	p := newPhone(t, pbx, "b1", "pb1")
	contact := "<sip:b1@10.1.1.7:5060>"
	res := p.authDo(proxied(p.registerReq(300, contact), "198.51.100.7:40000", "<sip:evil.example;lr>"), AlgSHA256, "pb1")
	if res.StatusCode != 200 {
		t.Fatalf("REGISTER = %d", res.StatusCode)
	}
	b := bindings(t, pbx, "sip:b1@"+testDomain)["sip:b1@10.1.1.7:5060"]
	if b.Source != p.addr || len(b.Path) != 1 || strings.Contains(b.Path[0], "evil") || !strings.Contains(b.Path[0], "hflow=") {
		t.Fatalf("binding = %+v; want the packet source and this node's own Path", b)
	}
	if res := p.authDo(proxied(p.registerReq(300), "198.51.100.99:5060", ""), AlgSHA256, "wrong"); res.StatusCode != 401 {
		t.Fatalf("bad password = %d", res.StatusCode)
	}
	th := pbx.throttle.(*fakeThrottle)
	if n, _ := th.Failures(context.Background(), "198.51.100.99"); n != 0 {
		t.Fatalf("untrusted X-Hello-Client used as the throttle key (%d)", n)
	}
	if n, _ := th.Failures(context.Background(), "127.0.0.1"); n != 1 {
		t.Fatalf("failures for the real source = %d, want 1", n)
	}
}

// TestTrustedProxyTrunkSource fails if trunk source validation does not use
// the trusted proxy's X-Hello-Client, or uses an untrusted one.
func TestTrustedProxyTrunkSource(t *testing.T) {
	r := &fakeRouter{extensions: map[string]bool{"100": true}, trunks: []routing.Trunk{{ID: 7, Name: "carrier-in", Mode: "ip", Enabled: true}}}
	r.setSource(netip.MustParseAddr("203.0.113.9"), 7)
	r.decide = func(routing.Call, routing.TrunkUsability) routing.Decision {
		return routing.Decision{Kind: routing.KindReject, RejectCode: 486, Reason: "test"}
	}
	for _, trusted := range []bool{true, false} {
		opts := []pbxOpt{trunkCfg(newFakeTrunkState()), withRouter(r, nil)}
		if trusted {
			opts = append(opts, func(c *Config, _ *Deps) { c.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")} })
		}
		pbx := startPBX(t, callerDevices(), opts...)
		from := newPhone(t, pbx, "carrier", "")
		res := from.do(proxied(carrierInvite(from, pbx, "+97140000100"), "203.0.113.9:5060", ""))
		switch {
		case trusted && res.StatusCode != 486:
			t.Fatalf("trusted X-Hello-Client of a trunk = %d, want the inbound route's 486", res.StatusCode)
		case !trusted && res.StatusCode != 403:
			t.Fatalf("untrusted X-Hello-Client = %d, want 403 (not a trunk source)", res.StatusCode)
		}
	}
}
