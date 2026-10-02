package sip

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

func callerDevices() []snapshot.Device {
	return []snapshot.Device{dev(1, "a1", "100", "pa"), dev(2, "b1", "200", "pb1")}
}

func traceText(tr routing.Trace) string {
	var b strings.Builder
	for _, s := range tr {
		b.WriteString(s.Text)
		b.WriteString("\n")
	}
	return b.String()
}

func wantTrace(t *testing.T, tr routing.Trace, lines ...string) {
	t.Helper()
	got := traceText(tr)
	for _, l := range lines {
		if !strings.Contains(got, l+"\n") {
			t.Fatalf("trace lacks %q:\n%s", l, got)
		}
	}
}

// twoCarriers sets up a PBX whose outbound route tries primary then backup.
func twoCarriers(t *testing.T, codes []int, opts ...pbxOpt) (*testPBX, *fakeTrunkState, *carrier, *carrier, *phone) {
	t.Helper()
	primary, backup := newCarrier(t, "acct1", "pw-primary"), newCarrier(t, "acct2", "pw-backup")
	r := &fakeRouter{extensions: map[string]bool{"100": true, "200": true},
		trunks: []routing.Trunk{primary.trunk(1, "carrier-primary", "ip"), backup.trunk(2, "carrier-backup", "ip")}}
	r.decide = outboundVia(r, "+971501234567", "+97140000100", false, codes, 1, 2)
	st := newFakeTrunkState()
	pbx := startPBX(t, callerDevices(), append([]pbxOpt{trunkCfg(st), withRouter(r, nil)}, opts...)...)
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	return pbx, st, primary, backup, a
}

// TestOutboundFailover503ToBackup fails if a 503 from the primary does not
// fail over to the backup, if the backup does not get the rewritten number
// and the decision's caller ID in its From domain, if SDP changes, if the
// trace lacks either attempt, or if a trunk slot outlives the call.
func TestOutboundFailover503ToBackup(t *testing.T) {
	pbx, st, primary, backup, a := twoCarriers(t, nil)
	primary.inviteCode.Store(503)
	r := waitCall(t, dial(t.Context(), a, "0501234567"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	if string(r.dcs.InviteResponse.Body()) != backup.sdp {
		t.Fatalf("answer SDP = %q", r.dcs.InviteResponse.Body())
	}
	waitReq(t, primary.invites, "INVITE to the primary")
	inv := waitReq(t, backup.invites, "INVITE to the backup")
	if inv.Recipient.User != "+971501234567" || inv.From().Address.User != "+97140000100" || inv.From().Address.Host != "carrier-backup.example" {
		t.Fatalf("backup INVITE: R-URI %s From %s", inv.Recipient.String(), inv.From().Address.String())
	}
	if string(inv.Body()) != a.sdp {
		t.Fatalf("offer SDP changed: %q", inv.Body())
	}
	waitReq(t, backup.acks, "ACK to the backup")
	if st.activeCalls(2) != 1 || st.activeCalls(1) != 0 {
		t.Fatalf("slots during the call: primary %d backup %d; want 0 and 1", st.activeCalls(1), st.activeCalls(2))
	}
	hangup(t, r.dcs)
	waitReq(t, backup.byes, "BYE to the backup")
	cd := pbx.nextCDR(t)
	if cd.Direction != "outbound" || cd.Trunk != "carrier-backup" || cd.RewrittenDestination != "+971501234567" ||
		cd.OriginalDestination != "0501234567" || cd.Route != "Test route" || cd.FinalStatus != 200 {
		t.Fatalf("CDR = %+v", cd)
	}
	wantTrace(t, cd.Trace,
		"carrier-primary ("+primary.addr+") -> 503 Service Unavailable",
		"Failover permitted for 503",
		"carrier-backup -> 200 OK",
		"Call established")
	eventually(t, "slots released", func() bool { return st.activeCalls(1) == 0 && st.activeCalls(2) == 0 })
}

// TestOutbound486NotFailedOver fails if a 486 from the primary is failed
// over instead of being returned to the caller.
func TestOutbound486NotFailedOver(t *testing.T) {
	pbx, st, primary, backup, a := twoCarriers(t, nil)
	primary.inviteCode.Store(486)
	r := waitCall(t, dial(t.Context(), a, "0501234567"))
	if code := responseCode(r.err); code != 486 {
		t.Fatalf("call = %v, want 486", r.err)
	}
	noReq(t, backup.invites, 200*time.Millisecond, "INVITE to the backup after a 486")
	cd := pbx.nextCDR(t)
	wantTrace(t, cd.Trace, "carrier-primary ("+primary.addr+") -> 486 Busy Here", "No failover for 486")
	if cd.FinalStatus != 486 || cd.Trunk != "carrier-primary" {
		t.Fatalf("CDR = %+v", cd)
	}
	eventually(t, "slot released", func() bool { return st.activeCalls(1) == 0 })
}

// TestOutboundFailoverOnTimeout fails if a destination that never answers
// beyond 100 Trying does not fail over within the attempt timeout.
func TestOutboundFailoverOnTimeout(t *testing.T) {
	pbx, _, primary, backup, a := twoCarriers(t, nil)
	primary.inviteCode.Store(-1)
	start := time.Now()
	r := waitCall(t, dial(t.Context(), a, "0501234567"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("failover took %s", d)
	}
	waitReq(t, backup.invites, "INVITE to the backup")
	hangup(t, r.dcs)
	wantTrace(t, pbx.nextCDR(t).Trace, "carrier-primary ("+primary.addr+") -> no answer", "Failover permitted for no answer", "carrier-backup -> 200 OK")
}

// TestOutboundProxyChallenge fails if a carrier's 407 on INVITE is not
// answered with the trunk credentials, or if the trace shows them.
func TestOutboundProxyChallenge(t *testing.T) {
	pbx, _, primary, _, a := twoCarriers(t, nil)
	primary.challenge.Store(true)
	r := waitCall(t, dial(t.Context(), a, "0501234567"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	inv := waitReq(t, primary.invites, "authorized INVITE at the primary")
	if inv.GetHeader("Proxy-Authorization") == nil || primary.rejected.Load() != 0 {
		t.Fatal("INVITE not answered with valid trunk credentials")
	}
	hangup(t, r.dcs)
	cd := pbx.nextCDR(t)
	wantTrace(t, cd.Trace, "carrier-primary ("+primary.addr+") -> 407 Proxy Authentication Required; answered with the trunk credentials (credentials: configured)",
		"carrier-primary -> 200 OK")
	if strings.Contains(traceText(cd.Trace), "pw-primary") {
		t.Fatal("trace contains the trunk password")
	}
}

// TestTrunkConcurrencyLimit fails if a trunk at max_calls takes another
// call instead of being skipped as full, or if a slot is held after its
// call ends.
func TestTrunkConcurrencyLimit(t *testing.T) {
	primary, backup := newCarrier(t, "acct1", "pw1"), newCarrier(t, "acct2", "pw2")
	tp := primary.trunk(1, "carrier-primary", "ip")
	tp.MaxCalls = 1
	r := &fakeRouter{extensions: map[string]bool{"100": true}, trunks: []routing.Trunk{tp, backup.trunk(2, "carrier-backup", "ip")}}
	r.decide = outboundVia(r, "+971501234567", "+97140000100", false, nil, 1, 2)
	st := newFakeTrunkState()
	pbx := startPBX(t, callerDevices(), trunkCfg(st), withRouter(r, nil))
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)

	first := waitCall(t, dial(t.Context(), a, "0501234567"))
	if first.err != nil {
		t.Fatal(first.err)
	}
	waitReq(t, primary.invites, "first call on the primary")
	second := waitCall(t, dial(t.Context(), a, "0501234568"))
	if second.err != nil {
		t.Fatal(second.err)
	}
	waitReq(t, backup.invites, "second call on the backup")
	noReq(t, primary.invites, 100*time.Millisecond, "second INVITE on the full primary")
	if st.activeCalls(1) != 1 || st.activeCalls(2) != 1 {
		t.Fatalf("slots = %d, %d", st.activeCalls(1), st.activeCalls(2))
	}
	hangup(t, second.dcs)
	cd := pbx.nextCDR(t)
	if !strings.Contains(traceText(cd.Trace), "Trunk carrier-primary skipped: full") {
		t.Fatalf("trace:\n%s", traceText(cd.Trace))
	}
	hangup(t, first.dcs)
	pbx.nextCDR(t)
	eventually(t, "slots released after both calls", func() bool { return st.activeCalls(1) == 0 && st.activeCalls(2) == 0 })
	if v := pbx.metric(t, "hello_trunk_calls_total", map[string]string{"trunk": "carrier-primary", "result": TrunkAnswered}); v != 1 {
		t.Fatalf("primary answered = %v", v)
	}
}

// TestCancelDuringFailover fails if the caller's CANCEL does not cancel the
// in-flight attempt, or if further trunks are tried after it.
func TestCancelDuringFailover(t *testing.T) {
	primary, backup, third := newCarrier(t, "a", "p"), newCarrier(t, "b", "p"), newCarrier(t, "c", "p")
	r := &fakeRouter{extensions: map[string]bool{"100": true},
		trunks: []routing.Trunk{primary.trunk(1, "carrier-primary", "ip"), backup.trunk(2, "carrier-backup", "ip"), third.trunk(3, "carrier-third", "ip")}}
	r.decide = outboundVia(r, "+971501234567", "+97140000100", false, nil, 1, 2, 3)
	st := newFakeTrunkState()
	pbx := startPBX(t, callerDevices(), trunkCfg(st), withRouter(r, nil))
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	primary.inviteCode.Store(503)
	backup.inviteCode.Store(0) // rings
	ctx, cancel := context.WithCancel(t.Context())
	res := dial(ctx, a, "0501234567")
	waitReq(t, backup.invites, "INVITE to the backup after failover")
	waitRing(t, a)
	cancel()
	cr := waitCall(t, res)
	if cr.dcs.InviteResponse == nil || cr.dcs.InviteResponse.StatusCode != 487 {
		t.Fatalf("caller final = %v, want 487", cr.dcs.InviteResponse)
	}
	waitReq(t, backup.cancels, "CANCEL to the backup")
	noReq(t, third.invites, 300*time.Millisecond, "INVITE to a third trunk after the caller cancelled")
	cd := pbx.nextCDR(t)
	if cd.FinalStatus != 487 || cd.TerminationSide != "caller" {
		t.Fatalf("CDR = %+v", cd)
	}
	wantTrace(t, cd.Trace, "Failover permitted for 503", "carrier-backup ("+backup.addr+") -> cancelled by the caller; no further trunks tried")
	eventually(t, "slots released", func() bool { return st.activeCalls(1)+st.activeCalls(2)+st.activeCalls(3) == 0 })
}

// TestInboundSourceValidation fails if an INVITE from an address that is
// neither a trunk nor a phone is not refused with 403 and counted by the
// throttle, or if one from a trunk's source is not routed to the extension
// the decision names.
func TestInboundSourceValidation(t *testing.T) {
	r := &fakeRouter{extensions: map[string]bool{"100": true}, sources: map[netip.Addr]int64{},
		trunks: []routing.Trunk{{ID: 7, Name: "carrier-in", Mode: "ip", Enabled: true}}}
	r.decide = func(c routing.Call, _ routing.TrunkUsability) routing.Decision {
		d := routing.Decision{Kind: routing.KindInbound, Extension: "100", Route: "Main DID", CallerID: "+971555000111"}
		d.Trace.Add(`Inbound route "Main DID" matched (exact ` + c.Number + `)`)
		return d
	}
	st := newFakeTrunkState()
	pbx := startPBX(t, callerDevices(), trunkCfg(st), withRouter(r, nil))
	callee := newPhone(t, pbx, "a1", "pa")
	callee.register(t)

	stranger := newPhone(t, pbx, "x", "x")
	inv := carrierInvite(stranger, pbx, "+97140000100")
	if res := stranger.do(inv); res.StatusCode != 403 || res.GetHeader("WWW-Authenticate") != nil {
		t.Fatalf("INVITE from an unknown source = %d, want 403 without a challenge", res.StatusCode)
	}
	if n, _ := pbx.throttle.Failures(context.Background(), "127.0.0.1"); n != 1 {
		t.Fatalf("throttle count = %d, want 1", n)
	}
	noReq(t, callee.invites, 100*time.Millisecond, "INVITE to the extension from an unknown source")

	// The same request from the trunk's address is an inbound call.
	carrierPhone := newPhone(t, pbx, "carrier", "")
	ap, _ := netip.ParseAddrPort(carrierPhone.addr)
	r.setSource(ap.Addr(), 7)
	inv = carrierInvite(carrierPhone, pbx, "+97140000100")
	dcs, err := carrierPhone.dua.WriteInvite(t.Context(), inv)
	if err != nil {
		t.Fatal(err)
	}
	if err := dcs.WaitAnswer(t.Context(), sipgo.AnswerOptions{}); err != nil {
		t.Fatalf("INVITE from the trunk: %v, want answered by the extension", err)
	}
	if err := dcs.Ack(t.Context()); err != nil {
		t.Fatal(err)
	}
	in := waitReq(t, callee.invites, "INVITE to the extension")
	if in.From().Address.User != "+971555000111" {
		t.Fatalf("callee sees caller %s, want the route's caller ID", in.From().Address.User)
	}
	waitReq(t, callee.acks, "ACK to the extension")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := dcs.Bye(ctx); err != nil {
		t.Fatal(err)
	}
	waitReq(t, callee.byes, "BYE to the extension")
	cd := pbx.nextCDR(t)
	if cd.Direction != "inbound" || cd.Trunk != "carrier-in" || cd.Route != "Main DID" || cd.Source != "+971555000111" || cd.FinalStatus != 200 {
		t.Fatalf("CDR = %+v", cd)
	}
	wantTrace(t, cd.Trace, "Source 127.0.0.1 identifies trunk carrier-in", `Inbound route "Main DID" matched (exact +97140000100)`, "Call established")
	if cd.Trace[0].N != 1 || cd.Trace[1].N != 2 {
		t.Fatalf("trace numbering: %+v", cd.Trace)
	}
}

// carrierInvite is an INVITE as a carrier sends it: no credentials, From in
// the carrier's domain, to the DID at the node.
func carrierInvite(from *phone, pbx *testPBX, did string) *sip.Request {
	host, port, _ := sip.ParseAddr(pbx.addr)
	req := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: did, Host: host, Port: port})
	req.AppendHeader(&sip.FromHeader{Address: sip.Uri{Scheme: "sip", User: "+971555000111", Host: "carrier.example"},
		Params: sip.HeaderParams{{K: "tag", V: sip.GenerateTagN(8)}}})
	req.AppendHeader(&sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: did, Host: host}})
	req.AppendHeader(sip.HeaderClone(&from.contact))
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.SetBody([]byte(from.sdp))
	req.SetDestination(pbx.addr)
	return req
}

// TestTrunkRegistrationDigest fails if a registration trunk is not
// registered with the carrier using its credentials, re-registered before
// the granted expiry, or its state not shared.
func TestTrunkRegistrationDigest(t *testing.T) {
	cr := newCarrier(t, "acct", "reg-pass")
	cr.grant.Store(1) // re-register at 80%: within a second
	r := &fakeRouter{trunks: []routing.Trunk{cr.trunk(1, "carrier-reg", "registration")}}
	st := newFakeTrunkState()
	pbx := startPBX(t, nil, trunkCfg(st), withRouter(r, nil))
	eventually(t, "registered", func() bool { return len(cr.registrations()) >= 1 })
	reg := cr.registrations()[0]
	if c := reg.Contact(); c == nil || c.Address.HostPort() != pbx.addr || c.Address.User != "acct" {
		t.Fatalf("registered contact = %v, want acct@%s", reg.Contact(), pbx.addr)
	}
	if reg.To().Address.User != "acct" || reg.To().Address.Host != "carrier-reg.example" {
		t.Fatalf("AOR = %s", reg.To().Address.String())
	}
	eventually(t, "state registered", func() bool {
		r, ok := st.registration(1)
		return ok && r.State == "registered" && r.Node == "sip-test" && r.LastCode == 200
	})
	eventually(t, "re-registered before expiry", func() bool { return len(cr.registrations()) >= 2 })
	if cr.rejected.Load() != 0 {
		t.Fatal("carrier rejected our credentials")
	}

	// A wrong password: failed with the code, and retried with backoff.
	bad := newCarrier(t, "acct", "the-real-pass")
	tb := bad.trunk(2, "carrier-bad", "registration")
	tb.Password = "wrong"
	r2 := &fakeRouter{trunks: []routing.Trunk{tb}}
	st2 := newFakeTrunkState()
	startPBX(t, nil, trunkCfg(st2), withRouter(r2, nil))
	eventually(t, "registration failed", func() bool {
		r, ok := st2.registration(2)
		return ok && r.State == "failed" && r.LastCode == 403
	})
	eventually(t, "retried", func() bool { return bad.rejected.Load() >= 2 })
}

// TestTrunkLeaseTakeover fails if two nodes register the same trunk at
// once, or if after the holder stops another node does not take over and
// register within one lease period.
func TestTrunkLeaseTakeover(t *testing.T) {
	cr := newCarrier(t, "acct", "pw")
	cr.grant.Store(1)
	r := &fakeRouter{trunks: []routing.Trunk{cr.trunk(1, "carrier-reg", "registration")}}
	shared := newFakeTrunkState()
	va, vb := &nodeView{fakeTrunkState: shared}, &nodeView{fakeTrunkState: shared}
	nodeA := startPBX(t, nil, trunkCfg(va), withRouter(r, nil), func(c *Config, _ *Deps) { c.NodeID = "sip-a" })
	nodeB := startPBX(t, nil, trunkCfg(vb), withRouter(r, nil), func(c *Config, _ *Deps) { c.NodeID = "sip-b" })
	eventually(t, "someone registered", func() bool { return len(cr.registrations()) >= 1 })
	time.Sleep(time.Second) // several renewals and re-registrations
	holder := shared.holder(1)
	seen := map[string]bool{}
	for _, reg := range cr.registrations() {
		seen[reg.Contact().Address.HostPort()] = true
	}
	if len(seen) != 1 {
		t.Fatalf("contacts registered by more than one node: %v", seen)
	}
	holderNode, other := nodeA, nodeB
	if holder == "sip-b" {
		holderNode, other = nodeB, nodeA
	}
	if !seen[holderNode.addr] {
		t.Fatalf("registered contact %v is not the lease holder %s", seen, holder)
	}
	// The holder dies without releasing anything.
	if holder == "sip-a" {
		va.cut.Store(true)
	} else {
		vb.cut.Store(true)
	}
	died := time.Now()
	eventually(t, "the other node registers", func() bool {
		for _, reg := range cr.registrations() {
			if reg.Contact().Address.HostPort() == other.addr {
				return true
			}
		}
		return false
	})
	if d := time.Since(died); d > 3*100*time.Millisecond+time.Second {
		t.Fatalf("takeover took %s, more than one lease period", d)
	}
}

// TestTrunkOptionsHealth fails if a destination is not marked up after an
// answered OPTIONS (with latency), down after a timeout, and up again.
func TestTrunkOptionsHealth(t *testing.T) {
	cr := newCarrier(t, "acct", "pw")
	r := &fakeRouter{trunks: []routing.Trunk{cr.trunk(1, "carrier-ip", "ip")}}
	st := newFakeTrunkState()
	startPBX(t, nil, trunkCfg(st), withRouter(r, nil))
	up := func(want bool) func() bool {
		return func() bool { h, ok := st.destHealth(1, cr.destKey()); return ok && h.Up == want }
	}
	eventually(t, "destination up", up(true))
	if h, _ := st.destHealth(1, cr.destKey()); h.Latency <= 0 || h.LastCode != 200 {
		t.Fatalf("health = %+v", h)
	}
	cr.optionsDown.Store(true)
	eventually(t, "destination down after a timeout", up(false))
	cr.optionsDown.Store(false)
	eventually(t, "destination up again", up(true))
}

// TestTrunkMetrics fails if registration, OPTIONS and a completed trunk
// call do not move the trunk metrics.
func TestTrunkMetrics(t *testing.T) {
	cr := newCarrier(t, "acct", "pw")
	tr := cr.trunk(1, "carrier-m", "registration")
	r := &fakeRouter{extensions: map[string]bool{"100": true}, trunks: []routing.Trunk{tr}}
	r.decide = outboundVia(r, "+971501234567", "+97140000100", false, nil, 1)
	st := newFakeTrunkState()
	pbx := startPBX(t, callerDevices(), trunkCfg(st), withRouter(r, nil))
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	labels := map[string]string{"trunk": "carrier-m"}
	eventually(t, "hello_trunk_registered = 1", func() bool { return pbx.metric(t, "hello_trunk_registered", labels) == 1 })
	dl := map[string]string{"trunk": "carrier-m", "destination": cr.destKey()}
	eventually(t, "hello_trunk_status = 1", func() bool { return pbx.metric(t, "hello_trunk_status", dl) == 1 })
	eventually(t, "latency measured", func() bool { return pbx.metric(t, "hello_trunk_options_latency_seconds", dl) > 0 })

	r1 := waitCall(t, dial(t.Context(), a, "0501234567"))
	if r1.err != nil {
		t.Fatal(r1.err)
	}
	eventually(t, "hello_trunk_active_calls = 1", func() bool { return pbx.metric(t, "hello_trunk_active_calls", labels) == 1 })
	hangup(t, r1.dcs)
	pbx.nextCDR(t)
	if v := pbx.metric(t, "hello_trunk_calls_total", map[string]string{"trunk": "carrier-m", "result": TrunkAnswered}); v != 1 {
		t.Fatalf("hello_trunk_calls_total{answered} = %v", v)
	}
	eventually(t, "hello_trunk_active_calls back to 0", func() bool { return pbx.metric(t, "hello_trunk_active_calls", labels) == 0 })
	if n := histogramCount(t, pbx, "hello_route_decision_seconds"); n < 1 {
		t.Fatalf("route decisions observed = %d", n)
	}
	cr.optionsDown.Store(true)
	eventually(t, "hello_trunk_status = 0", func() bool { return pbx.metric(t, "hello_trunk_status", dl) == 0 })
}

func histogramCount(t *testing.T, pbx *testPBX, name string) uint64 {
	t.Helper()
	mfs, err := pbx.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name && len(mf.GetMetric()) > 0 {
			return mf.GetMetric()[0].GetHistogram().GetSampleCount()
		}
	}
	return 0
}

// TestMisconfiguredTrunk fails if a trunk whose password did not open is
// registered, not published as misconfigured, or used for calls.
func TestMisconfiguredTrunk(t *testing.T) {
	cr := newCarrier(t, "acct", "pw")
	tr := cr.trunk(1, "carrier-x", "registration")
	tr.Password = ""
	r := &fakeRouter{extensions: map[string]bool{"100": true}, trunks: []routing.Trunk{tr}}
	r.decide = outboundVia(r, "+971501234567", "+97140000100", false, nil, 1)
	st := newFakeTrunkState()
	pbx := startPBX(t, callerDevices(), trunkCfg(st), withRouter(r, map[int64]string{1: "trunk password does not open with HELLO_SECRET_KEY"}))
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	eventually(t, "published misconfigured", func() bool { r, ok := st.registration(1); return ok && r.State == "misconfigured" })
	time.Sleep(300 * time.Millisecond)
	cr.mu.Lock()
	n := len(cr.regs)
	cr.mu.Unlock()
	if n != 0 {
		t.Fatalf("misconfigured trunk registered %d times", n)
	}
	res := waitCall(t, dial(t.Context(), a, "0501234567"))
	if code := responseCode(res.err); code != 503 {
		t.Fatalf("call over a misconfigured trunk = %v, want 503", res.err)
	}
	noReq(t, cr.invites, 100*time.Millisecond, "INVITE over a misconfigured trunk")
	wantTrace(t, pbx.nextCDR(t).Trace, "Trunk carrier-x skipped: misconfigured")
}

// TestTrunkStateUnavailable fails if, with the trunk state unreachable, a
// normal call is not refused with "state unavailable" or an emergency call
// is blocked.
func TestTrunkStateUnavailable(t *testing.T) {
	cr := newCarrier(t, "acct", "pw")
	tr := cr.trunk(1, "carrier-e", "ip")
	r := &fakeRouter{extensions: map[string]bool{"100": true}, trunks: []routing.Trunk{tr}}
	st := newFakeTrunkState()
	pbx := startPBX(t, callerDevices(), trunkCfg(st), withRouter(r, nil))
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	st.down.Store(true)
	time.Sleep(200 * time.Millisecond) // the status cache goes stale
	r.setDecide(outboundVia(r, "+971501234567", "+97140000100", false, nil, 1))
	res := waitCall(t, dial(t.Context(), a, "0501234567"))
	if code := responseCode(res.err); code != 503 {
		t.Fatalf("normal call with state down = %v, want 503", res.err)
	}
	wantTrace(t, pbx.nextCDR(t).Trace, "Trunk carrier-e skipped: state unavailable")

	r.setDecide(outboundVia(r, "112", "+97140000100", true, nil, 1))
	em := waitCall(t, dial(t.Context(), a, "112"))
	if em.err != nil {
		t.Fatalf("emergency call with state down: %v\n%s", em.err, traceText(pbx.nextCDR(t).Trace))
	}
	waitReq(t, cr.invites, "emergency INVITE")
	hangup(t, em.dcs)
	pbx.nextCDR(t)
}

// TestTrunkFreedSlotUsedAtOnce fails if a trunk whose only slot was just
// freed is skipped as full because a cached call count is stale.
func TestTrunkFreedSlotUsedAtOnce(t *testing.T) {
	primary, backup := newCarrier(t, "acct1", "pw1"), newCarrier(t, "acct2", "pw2")
	tp := primary.trunk(1, "carrier-primary", "ip")
	tp.MaxCalls = 1
	r := &fakeRouter{extensions: map[string]bool{"100": true}, trunks: []routing.Trunk{tp, backup.trunk(2, "carrier-backup", "ip")}}
	r.decide = outboundVia(r, "+971501234567", "+97140000100", false, nil, 1, 2)
	st := newFakeTrunkState()
	pbx := startPBX(t, callerDevices(), trunkCfg(st), withRouter(r, nil),
		func(c *Config, _ *Deps) { c.TrunkStatusPoll = 400 * time.Millisecond })
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	first := waitCall(t, dial(t.Context(), a, "0501234567"))
	if first.err != nil {
		t.Fatal(first.err)
	}
	waitReq(t, primary.invites, "first call on the primary")
	eventually(t, "the status cache sees the call", func() bool {
		return pbx.metric(t, "hello_trunk_active_calls", map[string]string{"trunk": "carrier-primary"}) == 1
	})
	hangup(t, first.dcs)
	pbx.nextCDR(t)
	eventually(t, "slot freed", func() bool { return st.activeCalls(1) == 0 })
	second := waitCall(t, dial(t.Context(), a, "0501234568")) // before the next poll
	if second.err != nil {
		t.Fatal(second.err)
	}
	waitReq(t, primary.invites, "second call on the primary")
	noReq(t, backup.invites, 100*time.Millisecond, "INVITE to the backup")
	hangup(t, second.dcs)
	pbx.nextCDR(t)
}
