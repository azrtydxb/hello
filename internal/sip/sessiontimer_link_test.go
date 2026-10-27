package sip

import (
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// withTrunkTimers sets what a trunk whose session-timers setting is
// "default" does.
func withTrunkTimers(on bool) pbxOpt {
	return func(c *Config, _ *Deps) { c.SessionTimersTrunks = on }
}

// refreshWithin waits for a session refresh on ch, which must come within d.
func refreshWithin(t *testing.T, ch chan *sip.Request, d time.Duration, what string) *sip.Request {
	t.Helper()
	select {
	case r := <-ch:
		if got := headerOfReq(r, "Session-Expires"); got != "2;refresher=uac" {
			t.Fatalf("%s: Session-Expires = %q, want 2;refresher=uac", what, got)
		}
		return r
	case <-time.After(d):
		t.Fatalf("no session refresh for %s within %v", what, d)
		return nil
	}
}

// TestSessionTimersAfterPark fails if the party a parked call keeps is not
// refreshed (it was left alone: the timer only re-armed), if its failed
// refresh does not end the call and free the slot, or if the phone that
// retrieves the call gets no session timer of its own, is not refreshed, or
// does not end the call when its refresh fails.
func TestSessionTimersAfterPark(t *testing.T) {
	st := parkStore(t)
	pbx := startPBX(t, threeDevices(), withMedia(false), withPark(st, "sip-a", testLot), withSessions(2*time.Second, time.Second, 0))
	a, b, c := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1"), newPhone(t, pbx, "c1", "pc")
	a.register(t)
	registerAs(t, b, grandstreamUA)
	c.register(t)
	for _, p := range []*phone{a, b, c} {
		p.setCallee(answerAfter(nil))
	}
	pc := establish(t, a, b)
	parkBy(t, a, pc, "*74")
	occupied(t, st, "701", "100")
	waitReq(t, a.byes, "BYE to the parker")
	drain(b.reinvites)
	// The parked party stays on its session timer: Hello refreshes it.
	refreshWithin(t, b.reinvites, 4*time.Second, "the parked party")
	time.Sleep(200 * time.Millisecond)
	if n := len(liveCalls(t, pbx)); n != 1 {
		t.Fatalf("live calls after the refresh = %d, want the parked call", n)
	}

	// The retriever's dialog is timed too.
	r2 := waitCall(t, dial(t.Context(), c, "*75701"))
	if r2.err != nil {
		t.Fatalf("retrieve: %v", r2.err)
	}
	if got := r2.dcs.InviteResponse.GetHeader("Session-Expires"); got == nil || got.Value() != "2;refresher=uas" {
		t.Fatalf("200 to the retriever: Session-Expires = %v, want 2;refresher=uas", got)
	}
	drain(c.reinvites)
	drain(b.reinvites)
	refreshWithin(t, c.reinvites, 4*time.Second, "the retriever")
	refreshWithin(t, b.reinvites, 4*time.Second, "the held party after the retrieve")

	// The retriever's phone left the Wi-Fi: the call ends on both sides.
	c.mu.Lock()
	c.reinviteStatus = 481
	c.mu.Unlock()
	waitReq(t, c.byes, "BYE to the retriever")
	waitReq(t, b.byes, "BYE to the parked party")
	cd := pbx.nextCDR(t)
	if !strings.Contains(cd.FailureReason, "session timer") {
		t.Fatalf("CDR reason = %q, want a session timer teardown", cd.FailureReason)
	}
	pbx.nextCDR(t)
}

// TestSessionTimersParkedPartyFails fails if a parked party that stops
// answering its refresh keeps the slot (and the call) for ever.
func TestSessionTimersParkedPartyFails(t *testing.T) {
	st := parkStore(t)
	pbx := startPBX(t, threeDevices(), withMedia(false), withPark(st, "sip-a", testLot), withSessions(2*time.Second, time.Second, 0))
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	registerAs(t, b, grandstreamUA)
	a.setCallee(answerAfter(nil))
	b.setCallee(answerAfter(nil))
	pc := establish(t, a, b)
	parkBy(t, a, pc, "*74")
	occupied(t, st, "701", "100")
	waitReq(t, a.byes, "BYE to the parker")
	b.mu.Lock()
	b.reinviteStatus = 481
	b.mu.Unlock()
	waitReq(t, b.byes, "BYE to the parked party whose refresh failed")
	slotFree(t, st, "701")
	eventually(t, "no live calls", func() bool { return len(liveCalls(t, pbx)) == 0 })
}

// TestSessionTimersAfterPickup fails if the call a pickup connects has no
// session timer on the caller's or the picker's dialog.
func TestSessionTimersAfterPickup(t *testing.T) {
	st := parkStore(t)
	pbx := startPBX(t, pickupDevices(), withMedia(false), withPark(st, "sip-a", testLot, testFrontGrp), withSessions(2*time.Second, time.Second, 0))
	a, b, c := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1"), newPhone(t, pbx, "c1", "pc")
	for _, p := range []*phone{a, b, c} {
		p.register(t)
	}
	b.setCallee(ringForever())
	ch, cancel, _ := ringing(t, a, b, "200")
	defer cancel()
	rc := waitCall(t, dial(t.Context(), c, "*40"))
	if rc.err != nil {
		t.Fatalf("pickup: %v", rc.err)
	}
	ra := waitCall(t, ch)
	if ra.err != nil {
		t.Fatalf("the picked-up call was not answered: %v", ra.err)
	}
	for name, dcs := range map[string]*sipgo.DialogClientSession{"caller": ra.dcs, "picker": rc.dcs} {
		if got := dcs.InviteResponse.GetHeader("Session-Expires"); got == nil || got.Value() != "2;refresher=uas" {
			t.Fatalf("200 to the %s: Session-Expires = %v, want 2;refresher=uas", name, got)
		}
	}
	drain(a.reinvites)
	drain(c.reinvites)
	refreshWithin(t, a.reinvites, 4*time.Second, "the caller")
	refreshWithin(t, c.reinvites, 4*time.Second, "the picker")
	a.mu.Lock()
	a.reinviteStatus = 481
	a.mu.Unlock()
	waitReq(t, a.byes, "BYE to the caller")
	waitReq(t, c.byes, "BYE to the picker")
	pbx.nextCDR(t)
	pbx.nextCDR(t)
}

// TestSessionTimersAfterBlindTransfer fails if the call a blind transfer
// leaves (the caller and the target) has no session timers: it ran with none
// at all, whatever happened to the phones.
func TestSessionTimersAfterBlindTransfer(t *testing.T) {
	pbx := startPBX(t, threeDevices(), withMedia(false), withSessions(2*time.Second, time.Second, 0))
	a, b, c := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1"), newPhone(t, pbx, "c1", "pc")
	a.register(t)
	b.register(t)
	registerAs(t, c, grandstreamUA)
	c.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	waitReq(t, b.invites, "INVITE to b")
	waitReq(t, b.acks, "ACK to b")
	dss := <-serversOf(b)
	if res := b.sendReferFromCallee(t, dss, "<sip:300@"+testDomain+">"); res.StatusCode != 202 {
		t.Fatalf("REFER = %d, want 202", res.StatusCode)
	}
	for {
		n := waitReq(t, b.notifies, "NOTIFY to the transferee")
		if strings.Contains(string(n.Body()), "200 OK") {
			break
		}
	}
	waitReq(t, b.byes, "BYE to the transferee")
	waitReq(t, c.acks, "ACK to the target")
	time.Sleep(100 * time.Millisecond)
	drain(a.reinvites)
	drain(c.reinvites)
	refreshWithin(t, a.reinvites, 4*time.Second, "the transferred caller")
	refreshWithin(t, c.reinvites, 4*time.Second, "the transfer target")
	c.mu.Lock()
	c.reinviteStatus = 481
	c.mu.Unlock()
	waitReq(t, a.byes, "BYE to the caller")
	waitReq(t, c.byes, "BYE to the target")
	pbx.nextCDR(t)
	pbx.nextCDR(t)
}

// TestSessionTimersAfterAttendedTransfer fails if the two parties an
// attended transfer bridges have no session timers.
func TestSessionTimersAfterAttendedTransfer(t *testing.T) {
	pbx := startPBX(t, threeDevices(), withMedia(false), withSessions(2*time.Second, time.Second, 0))
	a, b, c := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1"), newPhone(t, pbx, "c1", "pc")
	a.register(t)
	b.register(t)
	registerAs(t, c, grandstreamUA)
	r1 := waitCall(t, dial(t.Context(), a, "200"))
	if r1.err != nil {
		t.Fatalf("call 1: %v", r1.err)
	}
	waitReq(t, b.invites, "INVITE to b")
	var dssAB *sipgo.DialogServerSession
	for d := range serversOf(b) {
		dssAB = d
	}
	r2 := waitCall(t, dial(t.Context(), b, "300"))
	if r2.err != nil {
		t.Fatalf("call 2: %v", r2.err)
	}
	waitReq(t, c.invites, "INVITE to c")
	replaces := "sip:100@" + testDomain + "?Replaces=" + dssAB.InviteRequest.CallID().Value()
	if res := b.sendRefer(t, r2.dcs, "<"+replaces+">"); res.StatusCode != 202 {
		t.Fatalf("REFER = %d, want 202", res.StatusCode)
	}
	waitReq(t, b.notifies, "progress NOTIFY")
	waitReq(t, b.notifies, "final NOTIFY")
	waitReq(t, b.byes, "BYE to the transferee's first dialog")
	waitReq(t, b.byes, "BYE to the transferee's second dialog")
	time.Sleep(100 * time.Millisecond)
	drain(a.reinvites)
	drain(c.reinvites)
	refreshWithin(t, a.reinvites, 4*time.Second, "the bridged caller")
	refreshWithin(t, c.reinvites, 4*time.Second, "the bridged second party")
	a.mu.Lock()
	a.reinviteStatus = 481
	a.mu.Unlock()
	waitReq(t, a.byes, "BYE to the caller")
	waitReq(t, c.byes, "BYE to the second party")
	pbx.nextCDR(t)
	pbx.nextCDR(t)
	pbx.nextCDR(t)
}

// outboundTrunkPBX is a PBX with one carrier trunk set to timers, a global
// default for trunks set to "default", and a phone a1 dialling out through it.
func outboundTrunkPBX(t *testing.T, timers string, global bool) (*testPBX, *carrier, *phone) {
	t.Helper()
	cr := newCarrier(t, "acct", "pw")
	tr := cr.trunk(1, "carrier-out", "ip")
	tr.SessionTimers = timers
	r := &fakeRouter{extensions: map[string]bool{"100": true, "200": true}, trunks: []routing.Trunk{tr}}
	r.decide = outboundVia(r, "+971501234567", "+97140000100", false, nil, 1)
	pbx := startPBX(t, callerDevices(), trunkCfg(newFakeTrunkState()), withRouter(r, nil),
		withSessions(2*time.Second, time.Second, 0), withTrunkTimers(global))
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	return pbx, cr, a
}

// TestTrunkSessionTimersOutbound fails if a trunk's setting does not decide
// whether Hello asks the carrier for a session timer, if a trunk set to on
// is not refreshed, or if an unanswered refresh does not end the call.
func TestTrunkSessionTimersOutbound(t *testing.T) {
	for _, tc := range []struct {
		name, setting string
		global, want  bool
	}{
		{"on", routing.SessionTimersOn, false, true},
		{"on beats a global off", "on", false, true},
		{"off beats a global on", routing.SessionTimersOff, true, false},
		{"default follows a global on", routing.SessionTimersDefault, true, true},
		{"default follows a global off", routing.SessionTimersDefault, false, false},
		{"unset is the default", "", false, false},
		{"unset follows a global on", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, cr, a := outboundTrunkPBX(t, tc.setting, tc.global)
			r := waitCall(t, dial(t.Context(), a, "0501234567"))
			if r.err != nil {
				t.Fatalf("call: %v", r.err)
			}
			inv := waitReq(t, cr.invites, "INVITE to the carrier")
			got := headerOfReq(inv, "Session-Expires")
			if tc.want {
				if got != "2;refresher=uac" || !strings.Contains(headerOfReq(inv, "Supported"), "timer") || headerOfReq(inv, "Min-SE") != "1" {
					t.Fatalf("INVITE Session-Expires = %q Supported = %q Min-SE = %q, want a session timer request",
						got, headerOfReq(inv, "Supported"), headerOfReq(inv, "Min-SE"))
				}
				waitReq(t, cr.acks, "ACK to the carrier")
				refreshWithin(t, cr.reinvites, 4*time.Second, "the carrier")
			} else {
				if got != "" || headerOfReq(inv, "Supported") != "" {
					t.Fatalf("INVITE asks for a session timer: Session-Expires = %q", got)
				}
				noReq(t, cr.reinvites, 2500*time.Millisecond, "a refresh of a trunk without session timers")
			}
			hangup(t, r.dcs)
		})
	}

	t.Run("a refresh the carrier never takes ends the call", func(t *testing.T) {
		pbx, cr, a := outboundTrunkPBX(t, routing.SessionTimersOn, false)
		r := waitCall(t, dial(t.Context(), a, "0501234567"))
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		waitReq(t, cr.invites, "INVITE to the carrier")
		cr.reinviteCode.Store(481) // the call is gone at the carrier
		waitReq(t, cr.byes, "BYE to the carrier")
		waitReq(t, a.byes, "BYE to the caller")
		cd := pbx.nextCDR(t)
		if cd.TerminationSide != "system" || !strings.Contains(cd.FailureReason, "session timer") {
			t.Fatalf("CDR = side %q reason %q, want a session timer teardown", cd.TerminationSide, cd.FailureReason)
		}
	})

	t.Run("a carrier that refuses refreshes but answers keeps the call", func(t *testing.T) {
		pbx, cr, a := outboundTrunkPBX(t, routing.SessionTimersOn, false)
		r := waitCall(t, dial(t.Context(), a, "0501234567"))
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		waitReq(t, cr.invites, "INVITE to the carrier")
		cr.reinviteCode.Store(488)
		waitReq(t, cr.reinvites, "a refresh")
		noReq(t, cr.byes, 1500*time.Millisecond, "BYE to a carrier that answered the refresh")
		if n := len(liveCalls(t, pbx)); n != 1 {
			t.Fatalf("live calls = %d, want the call kept", n)
		}
		hangup(t, r.dcs)
	})
}

// TestTrunkSessionTimersInbound fails if a call from a trunk is not given a
// session timer when the trunk is on (the carrier sent none), or is given one
// when the trunk is off.
func TestTrunkSessionTimersInbound(t *testing.T) {
	for _, tc := range []struct {
		name, setting string
		global, want  bool
	}{
		{"on", routing.SessionTimersOn, false, true},
		{"off", routing.SessionTimersOff, true, false},
		{"default follows a global on", routing.SessionTimersDefault, true, true},
		{"default follows a global off", routing.SessionTimersDefault, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRouter{extensions: map[string]bool{"100": true}, sources: map[netip.Addr]int64{},
				trunks: []routing.Trunk{{ID: 7, Name: "carrier-in", Mode: "ip", Enabled: true, SessionTimers: tc.setting}}}
			r.decide = func(c routing.Call, _ routing.TrunkUsability) routing.Decision {
				d := routing.Decision{Kind: routing.KindInbound, Extension: "100", Route: "Main DID", CallerID: "+971555000111"}
				d.Trace.Add(`Inbound route "Main DID" matched (exact ` + c.Number + `)`)
				return d
			}
			pbx := startPBX(t, callerDevices(), trunkCfg(newFakeTrunkState()), withRouter(r, nil),
				withSessions(2*time.Second, time.Second, 0), withTrunkTimers(tc.global))
			callee := newPhone(t, pbx, "a1", "pa")
			callee.register(t)
			carrierPhone := newPhone(t, pbx, "carrier", "")
			ap, _ := netip.ParseAddrPort(carrierPhone.addr)
			r.setSource(ap.Addr(), 7)
			dcs, err := carrierPhone.dua.WriteInvite(t.Context(), carrierInvite(carrierPhone, pbx, "+97140000100"))
			if err != nil {
				t.Fatal(err)
			}
			if err := dcs.WaitAnswer(t.Context(), sipgo.AnswerOptions{}); err != nil {
				t.Fatalf("INVITE from the trunk: %v", err)
			}
			if err := dcs.Ack(t.Context()); err != nil {
				t.Fatal(err)
			}
			got := dcs.InviteResponse.GetHeader("Session-Expires")
			if tc.want {
				if got == nil || got.Value() != "2;refresher=uas" {
					t.Fatalf("200 to the carrier: Session-Expires = %v, want 2;refresher=uas", got)
				}
				drain(carrierPhone.reinvites)
				refreshWithin(t, carrierPhone.reinvites, 4*time.Second, "the carrier")
			} else {
				if got != nil {
					t.Fatalf("200 to the carrier carries Session-Expires %q", got.Value())
				}
				noReq(t, carrierPhone.reinvites, 2500*time.Millisecond, "a refresh of a trunk without session timers")
			}
		})
	}
}

// TestTrunkSessionTimersCarrierRequest: a carrier that sends Session-Expires
// itself is honoured on a trunk left at the default, as before.
func TestTrunkSessionTimersCarrierRequest(t *testing.T) {
	r := &fakeRouter{extensions: map[string]bool{"100": true}, sources: map[netip.Addr]int64{},
		trunks: []routing.Trunk{{ID: 7, Name: "carrier-in", Mode: "ip", Enabled: true}}}
	r.decide = func(c routing.Call, _ routing.TrunkUsability) routing.Decision {
		return routing.Decision{Kind: routing.KindInbound, Extension: "100", Route: "Main DID", CallerID: "+971555000111"}
	}
	pbx := startPBX(t, callerDevices(), trunkCfg(newFakeTrunkState()), withRouter(r, nil), withSessions(2*time.Second, time.Second, 0))
	callee := newPhone(t, pbx, "a1", "pa")
	callee.register(t)
	carrierPhone := newPhone(t, pbx, "carrier", "")
	ap, _ := netip.ParseAddrPort(carrierPhone.addr)
	r.setSource(ap.Addr(), 7)
	req := carrierInvite(carrierPhone, pbx, "+97140000100")
	req.AppendHeader(sip.NewHeader("Supported", "timer"))
	req.AppendHeader(sip.NewHeader("Session-Expires", "2;refresher=uac"))
	dcs, err := carrierPhone.dua.WriteInvite(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err := dcs.WaitAnswer(t.Context(), sipgo.AnswerOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := dcs.InviteResponse.GetHeader("Session-Expires"); got == nil || got.Value() != "2;refresher=uac" {
		t.Fatalf("200 Session-Expires = %v, want the carrier's refresher=uac kept", got)
	}
	_ = dcs.Ack(t.Context())
	_ = pbx
}

// TestTrunkTimersPolicy fails if a trunk's setting and Hello's own are
// combined wrongly.
func TestTrunkTimersPolicy(t *testing.T) {
	for _, tc := range []struct {
		expires time.Duration
		global  bool
		setting string
		want    timerPolicy
	}{
		{time.Minute, false, "", timersPassive},
		{time.Minute, false, "default", timersPassive},
		{time.Minute, true, "default", timersForced},
		{time.Minute, false, "on", timersForced},
		{time.Minute, true, "off", timersOff},
		{0, true, "on", timersOff}, // timers off globally are off for every trunk
		{0, false, "default", timersOff},
	} {
		s := &Server{cfg: Config{SessionExpires: tc.expires, SessionTimersTrunks: tc.global}}
		if got := s.trunkTimers(&routing.Trunk{SessionTimers: tc.setting}); got != tc.want {
			t.Errorf("expires %v global %v setting %q = %v, want %v", tc.expires, tc.global, tc.setting, got, tc.want)
		}
	}
}

// parkTakeoverWithTimer parks a call whose callee b answers with the given
// Session-Expires, kills the owner and returns what the survivor sees.
func parkTakeoverWithTimer(t *testing.T, sessionExpires string) (rec livestate.DialogState, taker *testPBX, b *phone, takenAt time.Time) {
	t.Helper()
	st := parkStore(t)
	ha := newFakeHA()
	mem := &fakeMembership{}
	owner := startPBX(t, threeDevices(), withMedia(false), withNodeID("sip-1"), withHA(ha, mem), withPark(st, "sip-1", testLot),
		withSessions(2*time.Second, time.Second, 0))
	taker = startPBX(t, threeDevices(), withMedia(false), withNodeID("sip-2"), withHA(ha, mem), withPark(st, "sip-2", testLot),
		withSessions(2*time.Second, time.Second, 0))
	a := newPhone(t, owner, "a1", "pa")
	b = newPhone(t, owner, "b1", "pb1")
	a.register(t)
	registerAs(t, b, grandstreamUA)
	a.setCallee(answerAfter(nil))
	b.setCallee(answerWithTimer(sessionExpires))
	pc := establish(t, a, b)
	parkBy(t, a, pc, "*74")
	occupied(t, st, "701", "100")
	waitReq(t, a.byes, "BYE to the parker")
	rec = waitRecord(t, ha, "the parked call is replicated with its session timer", "sip-1", func(s livestate.DialogState) bool {
		return s.State == haPhasePark && s.Session != nil && s.Session.Legs[sessCallee] != nil
	})
	haReinviteTimeout = time.Second
	killNode(owner, mem, map[string]int{"sip-1": 1}, "sip-2")
	taker.srv.takeoverPass(t.Context())
	waitReq(t, b.reinvites, "takeover re-INVITE to the parked party")
	waitReq(t, b.acks, "ACK of the parked party's 200")
	return rec, taker, b, time.Now()
}

// TestSessionTimersParkTakeover fails if a parked call's negotiated session
// timer is not carried in the replicated state, or a surviving node does not
// resume it with the same interval and refresher: a Hello-refreshed leg
// refreshed at the negotiated interval (not the node's default), a leg the
// phone must refresh ended when it does not.
func TestSessionTimersParkTakeover(t *testing.T) {
	t.Run("Hello refreshes at the negotiated interval", func(t *testing.T) {
		rec, taker, b, takenAt := parkTakeoverWithTimer(t, "6;refresher=uac")
		leg := rec.Session.Legs[sessCallee]
		if leg.IntervalSecs != 6 || !leg.Local {
			t.Fatalf("replicated timer = %+v, want 6 s refreshed by Hello", leg)
		}
		_, takerPort, _ := net.SplitHostPort(taker.addr)
		// The survivor's refreshes only: the dead owner's contact differs.
		var got *sip.Request
		deadline := time.After(8 * time.Second)
		for got == nil {
			select {
			case r := <-b.reinvites:
				if strings.Contains(headerOfReq(r, "Contact"), ":"+takerPort) {
					got = r
				}
			case <-deadline:
				t.Fatal("the survivor never refreshed the parked party")
			}
		}
		if d := time.Since(takenAt); d < 2*time.Second {
			t.Fatalf("first refresh after %v: the node's own 2 s default, not the negotiated 6 s (half = 3 s)", d)
		}
		if h := headerOfReq(got, "Session-Expires"); h != "6;refresher=uac" {
			t.Fatalf("refresh Session-Expires = %q, want 6;refresher=uac", h)
		}
	})

	t.Run("a phone that must refresh is ended when it does not", func(t *testing.T) {
		rec, taker, b, takenAt := parkTakeoverWithTimer(t, "6;refresher=uas")
		leg := rec.Session.Legs[sessCallee]
		if leg.IntervalSecs != 6 || leg.Local {
			t.Fatalf("replicated timer = %+v, want 6 s refreshed by the phone", leg)
		}
		for _, r := range drain(b.reinvites) {
			t.Fatalf("the survivor refreshed a leg whose phone is the refresher: %s", r.StartLine())
		}
		// The survivor's BYE: the dead owner's timer may fire too.
		_, takerPort, _ := net.SplitHostPort(taker.addr)
		for {
			if bye := waitReq(t, b.byes, "BYE from the survivor to the phone that never refreshed"); strings.Contains(headerOfReq(bye, "Via"), ":"+takerPort) {
				break
			}
		}
		if d := time.Since(takenAt); d < 3*time.Second {
			t.Fatalf("BYE after %v, want near the 6 s interval, not the node's 2 s default", d)
		}
	})
}
