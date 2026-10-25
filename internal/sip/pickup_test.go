package sip

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

func pickupDevices() []snapshot.Device {
	return append(threeDevices(), dev(5, "e1", "400", "pe"), dev(6, "g1", "600", "pg"))
}

// ringing starts a call from caller to ext that nobody answers and returns
// the channel its outcome arrives on and the cancel function.
func ringing(t *testing.T, caller *phone, callee *phone, ext string) (<-chan callResult, context.CancelFunc, *sip.Request) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	ch := dial(ctx, caller, ext)
	inv := waitReq(t, callee.invites, "INVITE to "+ext)
	return ch, cancel, inv
}

// TestCallPickup: group and directed pickup, scope, Replaces and the race.
// It fails if
//   - the group code does not answer the oldest call ringing a member of the
//     dialler's pickup group,
//   - the directed code answers a call outside the dialler's scope,
//   - an INVITE with Replaces naming an early dialog in scope does not take
//     the call,
//   - the picked-up call's other ringing legs are not cancelled,
//   - the caller and the picker are not connected both ways,
//   - hanging up on either side does not end the other,
//   - two simultaneous pickups both connect.
func TestCallPickup(t *testing.T) {
	st := parkStore(t)
	pbx := startPBX(t, pickupDevices(), withMedia(false), withPark(st, "sip-a", testLot, testFrontGrp))
	a, b, c, d := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1"), newPhone(t, pbx, "c1", "pc"), newPhone(t, pbx, "d1", "pd")
	e, g := newPhone(t, pbx, "e1", "pe"), newPhone(t, pbx, "g1", "pg")
	for _, p := range []*phone{a, b, c, d, e, g} {
		p.register(t)
	}
	b.setCallee(ringForever())
	d.setCallee(ringForever())
	e.setCallee(ringForever())
	c.setCallee(answerAfter(nil))

	// 1. Group pickup answers a call ringing 200; 200's phone stops ringing,
	// the caller and c talk both ways.
	ch, cancel, _ := ringing(t, a, b, "200")
	defer cancel()
	rc := waitCall(t, dial(t.Context(), c, "*40"))
	if rc.err != nil {
		t.Fatalf("group pickup: %v", rc.err)
	}
	ra := waitCall(t, ch)
	if ra.err != nil {
		t.Fatalf("the picked-up call was not answered: %v", ra.err)
	}
	cancelElsewhere(t, waitReq(t, b.cancels, "CANCEL to the phone that was ringing"))
	feedA := startRTPFeed(t, ra.dcs.InviteResponse.Body())
	feedC := startRTPFeed(t, rc.dcs.InviteResponse.Body())
	feedA.packet(0, make([]byte, 160), false)
	feedC.packet(0, make([]byte, 160), false)
	time.Sleep(100 * time.Millisecond)
	feedA.packet(0, []byte("caller-to-the-picker........"), false)
	if !hears(feedC.conn, "caller-to-the-picker........", 3*time.Second) {
		t.Fatalf("the picker did not hear the caller")
	}
	feedC.packet(0, []byte("picker-to-the-caller........"), false)
	if !hears(feedA.conn, "picker-to-the-caller........", 3*time.Second) {
		t.Fatalf("the caller did not hear the picker")
	}
	// The picker hangs up: the caller hears BYE, and the CDR names the pickup.
	hangup(t, rc.dcs)
	waitReq(t, a.byes, "BYE to the caller")
	var traces []string
	for range 2 {
		for _, s := range pbx.nextCDR(t).Trace {
			traces = append(traces, s.Text)
		}
	}
	if joined := strings.Join(traces, "\n"); !strings.Contains(joined, "Call picked up by 300") {
		t.Errorf("CDR traces do not record the pickup:\n%s", joined)
	}

	// 2. Nothing in scope is ringing: the picker is refused.
	if r := waitCall(t, dial(t.Context(), c, "*40")); r.err == nil {
		t.Fatal("group pickup with nothing ringing connected")
	}
	pbx.nextCDR(t)

	// 3. A caller outside the picker's scope: e (400) is in no pickup group,
	// so c can neither group-pick nor directed-pick its call.
	chE, cancelE, _ := ringing(t, g, e, "400")
	defer cancelE()
	if r := waitCall(t, dial(t.Context(), c, "*40")); r.err == nil {
		t.Fatal("group pickup answered a call outside the scope")
	}
	pbx.nextCDR(t)
	if r := waitCall(t, dial(t.Context(), c, "*41400")); r.err == nil {
		t.Fatal("directed pickup answered a call outside the scope")
	}
	pbx.nextCDR(t)
	noReq(t, e.cancels, 300*time.Millisecond, "CANCEL: the out-of-scope call must keep ringing")
	cancelE()
	waitCall(t, chE)
	pbx.nextCDR(t)

	// 4. Directed pickup answers the named extension's call.
	ch, cancel, _ = ringing(t, a, b, "200")
	defer cancel()
	rc = waitCall(t, dial(t.Context(), c, "*41200"))
	if rc.err != nil {
		t.Fatalf("directed pickup: %v", rc.err)
	}
	if ra = waitCall(t, ch); ra.err != nil {
		t.Fatalf("directed pickup: the call was not answered: %v", ra.err)
	}
	cancelElsewhere(t, waitReq(t, b.cancels, "CANCEL to the phone that was ringing"))
	hangup(t, ra.dcs)
	waitReq(t, c.byes, "BYE to the picker")
	pbx.nextCDR(t)
	pbx.nextCDR(t)

	// 5. The oldest ringing call goes first.
	chOld, cancelOld, _ := ringing(t, a, b, "200")
	defer cancelOld()
	time.Sleep(200 * time.Millisecond)
	chNew, cancelNew, _ := ringing(t, g, d, "210")
	defer cancelNew()
	rc = waitCall(t, dial(t.Context(), c, "*40"))
	if rc.err != nil {
		t.Fatalf("group pickup of the oldest: %v", rc.err)
	}
	if r := waitCall(t, chOld); r.err != nil {
		t.Fatalf("the oldest call was not the one picked up: %v", r.err)
	}
	noReq(t, d.cancels, 300*time.Millisecond, "CANCEL: the newer call must keep ringing")
	hangup(t, rc.dcs)
	waitReq(t, a.byes, "BYE to the caller")
	cancelNew()
	waitCall(t, chNew)
	for range 3 {
		pbx.nextCDR(t)
	}

	// 6. An INVITE with Replaces naming the early dialog of the ringing leg.
	ch, cancel, inv := ringing(t, a, b, "200")
	req := c.inviteReq("200")
	req.AppendHeader(sip.NewHeader("Replaces", inv.CallID().Value()+";to-tag=x;from-tag=y;early-only"))
	rc = waitCall(t, func() <-chan callResult {
		out := make(chan callResult, 1)
		go func() { dcs, err := c.callReq(t.Context(), req); out <- callResult{dcs, err} }()
		return out
	}())
	if rc.err != nil {
		t.Fatalf("Replaces pickup: %v", rc.err)
	}
	if ra = waitCall(t, ch); ra.err != nil {
		t.Fatalf("Replaces pickup: the call was not answered: %v", ra.err)
	}
	hangup(t, ra.dcs)
	waitReq(t, c.byes, "BYE to the picker")
	pbx.nextCDR(t)
	pbx.nextCDR(t)
	cancel()

	// 7. Two pickers race for one ringing call: exactly one gets it.
	g.setCallee(ringForever())
	ch, cancel, _ = ringing(t, a, b, "200")
	defer cancel()
	d.setCallee(answerAfter(nil))
	r1, r2 := dial(t.Context(), c, "*40"), dial(t.Context(), d, "*40")
	res := []callResult{waitCall(t, r1), waitCall(t, r2)}
	won := 0
	for _, r := range res {
		if r.err == nil {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("%d of two simultaneous pickups connected, want exactly 1", won)
	}
	if ra = waitCall(t, ch); ra.err != nil {
		t.Fatalf("the contested call was not answered: %v", ra.err)
	}
}

// cancelElsewhere fails if a picked-up call's CANCEL does not say it was
// completed elsewhere (RFC 3326), which keeps it off the missed-call list.
func cancelElsewhere(t *testing.T, req *sip.Request) {
	t.Helper()
	h := req.GetHeader("Reason")
	if h == nil || h.Value() != `SIP;cause=200;text="Call completed elsewhere"` {
		t.Fatalf("pickup CANCEL Reason = %v, want cause=200 completed elsewhere", h)
	}
}
