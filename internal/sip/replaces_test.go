package sip

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// replacesDevices: extension 100 and extension 200 have two phones each (a
// user's desk phone and handset), 300 is a colleague in 200's pickup group.
func replacesDevices() []snapshot.Device {
	return []snapshot.Device{
		dev(1, "a1", "100", "pa"), dev(2, "a2", "100", "pa2"),
		dev(3, "b1", "200", "pb1"), dev(4, "b2", "200", "pb2"),
		dev(5, "c1", "300", "pc"),
	}
}

// dialReplacing sends an INVITE to ext with a Replaces header from p.
func dialReplacing(ctx context.Context, p *phone, ext, replaces string) <-chan callResult {
	req := p.inviteReq(ext)
	req.AppendHeader(sip.NewHeader("Replaces", replaces))
	out := make(chan callResult, 1)
	go func() {
		dcs, err := p.callReq(ctx, req)
		out <- callResult{dcs, err}
	}()
	return out
}

// callerDialog is the Replaces value naming a caller's dialog with Hello.
func callerDialog(dcs *sipgo.DialogClientSession) string {
	return dcs.InviteRequest.CallID().Value() + ";to-tag=" + toTagOf(dcs) + ";from-tag=" + tagOfTest(dcs.InviteRequest.From())
}

// calleeDialog is the Replaces value naming a called phone's dialog.
func calleeDialog(dss *sipgo.DialogServerSession) string {
	return dss.InviteRequest.CallID().Value() + ";to-tag=" + tagOfTest(dss.InviteResponse.To()) + ";from-tag=" + tagOfTest(dss.InviteRequest.From())
}

// talks fails the test unless media flows both ways between the two
// phones' RTP feeds through the anchor.
func talks(t *testing.T, x, y *rtpFeed, what string) {
	t.Helper()
	x.packet(0, make([]byte, 160), false)
	y.packet(0, make([]byte, 160), false)
	time.Sleep(100 * time.Millisecond)
	x.packet(0, []byte("x-to-y......................"), false)
	if !hears(y.conn, "x-to-y......................", 3*time.Second) {
		t.Fatalf("%s: no media one way", what)
	}
	y.packet(0, []byte("y-to-x......................"), false)
	if !hears(x.conn, "y-to-x......................", 3*time.Second) {
		t.Fatalf("%s: no media the other way", what)
	}
}

func traced(r cdr.Record, text string) bool {
	for _, s := range r.Trace {
		if strings.Contains(s.Text, text) {
			return true
		}
	}
	return false
}

// TestPrackAndReplaces (spec S-17). It fails if
//   - a Require: 100rel offer is refused, or a PRACK is not answered on the
//     caller's side and sent on the phone's;
//   - an INVITE with Replaces naming an early dialog in scope does not take
//     the call;
//   - an attended transfer before answer leaves both of the transferor's
//     calls up, or rings the target twice;
//   - an INVITE with Replaces naming a confirmed dialog is not served: the
//     phone of the same extension does not take the call over with media
//     both ways through the anchor, the replaced phone is not sent BYE, the
//     CDRs do not name each other, the call is not replicated under its new
//     dialog, a second takeover of the same party fails, or a phone of
//     another extension, wrong tags or early-only are not refused.
func TestPrackAndReplaces(t *testing.T) {
	t.Run("100rel and PRACK", func(t *testing.T) {
		pbx := startPBX(t, callerDevices())
		a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
		a.register(t)
		b.register(t)
		seen := make(chan *sip.Request, 1)
		b.setCallee(reliableRinger(seen))
		heard, _, _, err := reliableCaller(t, a, "200", 100*time.Millisecond)
		if err != nil {
			t.Fatalf("a Require: 100rel call was refused: %v", err)
		}
		reliable := false
		for _, h := range heard {
			if h.code == 180 && h.rseq != "" {
				reliable = true
			}
		}
		if !reliable {
			t.Fatalf("the caller that requires 100rel heard no reliable 180: %+v", heard)
		}
		select {
		case <-seen:
		case <-time.After(5 * time.Second):
			t.Fatal("the phone's reliable 180 was not PRACKed")
		}
		pbx.nextCDR(t)
	})

	t.Run("early dialog", func(t *testing.T) {
		st := parkStore(t)
		pbx := startPBX(t, replacesDevices(), withMedia(false), withPark(st, "sip-a", testLot, testFrontGrp))
		ph := map[string]*phone{}
		for _, d := range replacesDevices() {
			ph[d.Username] = newPhone(t, pbx, d.Username, map[string]string{"a1": "pa", "a2": "pa2", "b1": "pb1", "b2": "pb2", "c1": "pc"}[d.Username])
			ph[d.Username].register(t)
		}
		ph["b1"].setCallee(ringForever())
		ph["b2"].setCallee(ringForever())
		ch := dial(t.Context(), ph["a1"], "200")
		inv := waitReq(t, ph["b1"].invites, "INVITE to b1")
		waitReq(t, ph["b2"].invites, "INVITE to b2")
		rc := waitCall(t, dialReplacing(t.Context(), ph["c1"], "200", inv.CallID().Value()+";to-tag=x;from-tag="+tagOfTest(inv.From())+";early-only"))
		if rc.err != nil {
			t.Fatalf("Replaces of an early dialog in scope: %v", rc.err)
		}
		ra := waitCall(t, ch)
		if ra.err != nil {
			t.Fatalf("the ringing call was not answered by the Replaces: %v", ra.err)
		}
		cancelElsewhere(t, waitReq(t, ph["b1"].cancels, "CANCEL to b1"))
		waitReq(t, ph["b2"].cancels, "CANCEL to b2")
		hangup(t, ra.dcs)
		waitReq(t, ph["c1"].byes, "BYE to the picker")
		pbx.nextCDR(t)
		pbx.nextCDR(t)
		if v := pbx.metric(t, "hello_replaces_total", map[string]string{"dialog": "early", "result": "ok"}); v != 1 {
			t.Errorf("hello_replaces_total{early,ok} = %v", v)
		}
	})

	// The target's phone decides per INVITE: the consultation INVITE rings,
	// the transferred call's INVITE is answered (at once, or only once the
	// consultation's CANCEL reached the phone). The answer must not depend on
	// when the test reads its events: the transferred call's INVITE can reach
	// the target before the test has seen the consultation end, and a
	// callee swapped in by the test after that would never answer it.
	for _, order := range []struct {
		name        string
		afterCancel bool // the target answers only after the CANCEL arrived
	}{
		{"attended transfer before answer, answered at once", false},
		{"attended transfer before answer, answered after the cancel", true},
	} {
		t.Run(order.name, func(t *testing.T) {
			pbx := startPBX(t, threeDevices())
			a, b, c := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1"), newPhone(t, pbx, "c1", "pc")
			for _, p := range []*phone{a, b, c} {
				p.register(t)
			}
			// Call 1: a -> b, answered.
			r1 := waitCall(t, dial(t.Context(), a, "200"))
			if r1.err != nil {
				t.Fatalf("call 1: %v", r1.err)
			}
			inv1 := waitReq(t, b.invites, "INVITE to b")
			dssAB := b.serverFor(inv1)
			// Call 2: b -> c, which rings; c answers the next INVITE.
			var gate chan struct{}
			if order.afterCancel {
				gate = make(chan struct{})
			}
			c.setCallee(consultThenAnswer(gate))
			ch2 := dial(t.Context(), b, "300")
			inv2 := waitReq(t, c.invites, "consultation INVITE to c")
			// b transfers a to c before c answers.
			cs2 := b.clientCallID(t)
			referTo := "<sip:300@" + testDomain + "?Replaces=" + cs2 + ">"
			if res := b.sendReferFromCallee(t, dssAB, referTo); res.StatusCode != 202 {
				t.Fatalf("REFER = %d, want 202", res.StatusCode)
			}
			// The consultation call is cancelled: c stops ringing it.
			if got := waitReq(t, c.cancels, "CANCEL of the consultation call"); got.CallID().Value() != inv2.CallID().Value() {
				t.Fatalf("CANCEL for %s, want the consultation call %s", got.CallID().Value(), inv2.CallID().Value())
			}
			if gate != nil {
				close(gate)
			}
			// b's consultation INVITE ends without an answer.
			if r2 := waitCall(t, ch2); r2.err == nil {
				t.Fatal("the transferor's consultation call stayed up after the transfer")
			}
			// c rings once more, for a, and answers: b's first call goes.
			inv3 := waitReq(t, c.invites, "the transferred call's INVITE to c")
			if inv3.CallID().Value() == inv2.CallID().Value() {
				t.Fatal("the transferred call reused the consultation call")
			}
			waitReq(t, b.byes, "BYE to the transferor's first call")
			noReq(t, c.invites, 500*time.Millisecond, "a second ring of the target")
			pbx.nextCDR(t) // the consultation call, cancelled
			hangup(t, r1.dcs)
			waitReq(t, c.byes, "BYE to the target")
		})
	}

	t.Run("confirmed dialog", func(t *testing.T) {
		ha := newFakeHA()
		pbx := startPBX(t, replacesDevices(), withMedia(false), withNodeID("sip-1"), withHA(ha, &fakeMembership{}))
		pass := map[string]string{"a1": "pa", "a2": "pa2", "b1": "pb1", "b2": "pb2", "c1": "pc"}
		ph := map[string]*phone{}
		for _, d := range replacesDevices() {
			ph[d.Username] = newPhone(t, pbx, d.Username, pass[d.Username])
			ph[d.Username].register(t)
		}
		a1, a2, b1, b2, c1 := ph["a1"], ph["a2"], ph["b1"], ph["b2"], ph["c1"]
		b2.setCallee(ringForever())

		// a1 calls 200; b1 answers, b2 stops ringing.
		r := waitCall(t, dial(t.Context(), a1, "200"))
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		invB := waitReq(t, b1.invites, "INVITE to b1")
		waitReq(t, b1.acks, "ACK to b1")
		waitReq(t, b2.cancels, "CANCEL to b2")
		aID := r.dcs.InviteRequest.CallID().Value()
		eventually(t, "the call is replicated", func() bool { return ha.ownerOf(aID) == "sip-1" })

		// Refused: another extension, wrong tags, early-only.
		if rc := waitCall(t, dialReplacing(t.Context(), c1, "200", callerDialog(r.dcs))); rc.err == nil {
			t.Fatal("a phone of another extension took over the call")
		}
		pbx.nextCDR(t)
		if rc := waitCall(t, dialReplacing(t.Context(), a2, "200", aID+";to-tag=nope;from-tag=nope")); rc.err == nil {
			t.Fatal("Replaces with the wrong tags took over the call")
		}
		pbx.nextCDR(t)
		if rc := waitCall(t, dialReplacing(t.Context(), a2, "200", callerDialog(r.dcs)+";early-only")); rc.err == nil {
			t.Fatal("Replaces with early-only took over an answered call")
		}
		pbx.nextCDR(t)
		if v := pbx.metric(t, "hello_replaces_total", map[string]string{"dialog": "confirmed", "result": "forbidden"}); v != 1 {
			t.Errorf("hello_replaces_total{confirmed,forbidden} = %v", v)
		}

		// 1. The caller's dialog: a2 takes a1's call over.
		ra2 := waitCall(t, dialReplacing(t.Context(), a2, "200", callerDialog(r.dcs)))
		if ra2.err != nil {
			t.Fatalf("a2 taking over a1's call: %v", ra2.err)
		}
		waitReq(t, a1.byes, "BYE to the replaced caller")
		feedA2 := startRTPFeed(t, ra2.dcs.InviteResponse.Body())
		feedB1 := startRTPFeed(t, invB.Body())
		talks(t, feedA2, feedB1, "a2 and b1 after the takeover")
		old := pbx.nextCDR(t)
		if old.SIPCallID != aID || !traced(old, "replaced by 100") {
			t.Fatalf("the replaced call's CDR %s does not record the takeover: %+v", old.SIPCallID, old.Trace)
		}
		a2ID := ra2.dcs.InviteRequest.CallID().Value()
		eventually(t, "the call replicates under its new caller dialog", func() bool {
			ha.mu.Lock()
			defer ha.mu.Unlock()
			st, ok := ha.dialogs[a2ID]
			_, stale := ha.dialogs[aID]
			return ok && !stale && st.Legs[1].CallID == invB.CallID().Value()
		})

		// 2. The callee's dialog: b2 takes b1's side over.
		dssB1 := b1.serverFor(invB)
		rb2 := waitCall(t, dialReplacing(t.Context(), b2, "100", calleeDialog(dssB1)))
		if rb2.err != nil {
			t.Fatalf("b2 taking over b1's call: %v", rb2.err)
		}
		waitReq(t, b1.byes, "BYE to the replaced callee")
		feedB2 := startRTPFeed(t, rb2.dcs.InviteResponse.Body())
		talks(t, feedA2, feedB2, "a2 and b2 after the second takeover")
		eventually(t, "the linked call's record is gone", func() bool {
			ha.mu.Lock()
			defer ha.mu.Unlock()
			_, ok := ha.dialogs[a2ID]
			return !ok
		})

		// 3. Back again: b1 takes the call over from b2 (a linked call's own
		// party).
		b1.setCallee(answerAfter(nil))
		rb1 := waitCall(t, dialReplacing(t.Context(), b1, "100", callerDialog(rb2.dcs)))
		if rb1.err != nil {
			t.Fatalf("b1 taking the call back from b2: %v", rb1.err)
		}
		waitReq(t, b2.byes, "BYE to b2")
		feedB1b := startRTPFeed(t, rb1.dcs.InviteResponse.Body())
		talks(t, feedA2, feedB1b, "a2 and b1 after taking the call back")
		b2cdr := pbx.nextCDR(t)
		if !traced(b2cdr, "Took over the answering dialog") || !traced(b2cdr, "replaced by 200") {
			t.Errorf("b2's call CDR does not record both takeovers: %+v", b2cdr.Trace)
		}

		// The caller hangs up: the phone that has the call now hears BYE.
		hangup(t, ra2.dcs)
		waitReq(t, b1.byes, "BYE to b1 when a2 hangs up")
		got := []cdr.Record{pbx.nextCDR(t), pbx.nextCDR(t)}
		var sawCaller bool
		for _, rec := range got {
			if rec.SIPCallID == a2ID && traced(rec, "Took over the caller's dialog of call "+aID) {
				sawCaller = true
			}
		}
		if !sawCaller {
			t.Errorf("the replacing caller's CDR does not name the call it replaced: %+v", got)
		}
		if v := pbx.metric(t, "hello_replaces_total", map[string]string{"dialog": "confirmed", "result": "ok"}); v != 3 {
			t.Errorf("hello_replaces_total{confirmed,ok} = %v, want 3", v)
		}
	})
}

// consultThenAnswer rings on a phone's first INVITE until it is cancelled
// and answers every later one: at once when gate is nil, else once gate is
// closed.
func consultThenAnswer(gate <-chan struct{}) calleeFn {
	var n atomic.Int32
	return func(p *phone, req *sip.Request, tx sip.ServerTransaction, dss *sipgo.DialogServerSession) {
		if n.Add(1) == 1 {
			ringForever()(p, req, tx, dss)
			return
		}
		answerAfter(gate)(p, req, tx, dss)
	}
}

// clientCallID is the Call-ID of the one call p dialled that is still being
// set up (its consultation call).
func (p *phone) clientCallID(t *testing.T) string {
	t.Helper()
	var id string
	eventually(t, "the phone's outgoing call", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		for cid := range p.pending {
			id = cid
		}
		return id != ""
	})
	return id
}

// TestReplacesAcrossNodes: an INVITE with Replaces that lands on another
// node than the call's reaches the call through the command mailbox. It
// fails if the phone of the same extension does not take over the answered
// call held on the other node, media does not flow, the replaced phone is
// not sent BYE, or a hang-up on either node does not end the other's call.
func TestReplacesAcrossNodes(t *testing.T) {
	st := parkStore(t)
	shared := newFakeState()
	withShared := func(_ *Config, d *Deps) { d.State = shared }
	ports := func(lo, hi int) pbxOpt { return func(c *Config, _ *Deps) { c.RTPPortMin, c.RTPPortMax = lo, hi } }
	pbx1 := startPBX(t, replacesDevices(), ports(30000, 30499), withPark(st, "sip-a", testLot), withShared)
	pbx2 := startPBX(t, replacesDevices(), ports(30500, 30999), withPark(st, "sip-b", testLot), withShared)
	a1, b1 := newPhone(t, pbx1, "a1", "pa"), newPhone(t, pbx1, "b1", "pb1")
	b2 := newPhone(t, pbx2, "b2", "pb2")
	for _, p := range []*phone{a1, b1, b2} {
		p.register(t)
	}
	b2.setCallee(busy())
	r := waitCall(t, dial(t.Context(), a1, "200"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	invB := waitReq(t, b1.invites, "INVITE to b1")
	waitReq(t, b1.acks, "ACK to b1")
	feedA := startRTPFeed(t, r.dcs.InviteResponse.Body())
	rb2 := waitCall(t, dialReplacing(t.Context(), b2, "100", calleeDialog(b1.serverFor(invB))))
	if rb2.err != nil {
		t.Fatalf("b2 on the other node taking over b1's call: %v", rb2.err)
	}
	waitReq(t, b1.byes, "BYE to the replaced callee")
	feedB2 := startRTPFeed(t, rb2.dcs.InviteResponse.Body())
	talks(t, feedA, feedB2, "a1 and b2 across nodes")
	hangup(t, r.dcs)
	waitReq(t, b2.byes, "BYE to b2 on the other node")
}

func TestParseReplaces(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want replacesHdr
		ok   bool
	}{
		{"abc@h;to-tag=1;from-tag=2", replacesHdr{callID: "abc@h", toTag: "1", fromTag: "2"}, true},
		{"abc%40h ; from-tag=2 ; to-tag=1 ; early-only", replacesHdr{callID: "abc@h", toTag: "1", fromTag: "2", earlyOnly: true}, true},
		{"", replacesHdr{}, false},
	} {
		got, ok := parseReplaces(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parseReplaces(%q) = %+v %v, want %+v %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	r := replacesHdr{toTag: "L", fromTag: "R"}
	if !r.tagsMatch("L", "R") || !r.tagsMatch("R", "L") || r.tagsMatch("L", "X") || (replacesHdr{}).tagsMatch("", "") {
		t.Error("tagsMatch does not match exactly the dialog's own tag pair")
	}
}

// TestDialogInfoNamesTheDialog fails if a BLF NOTIFY for an extension on a
// call does not carry the dialog's call-id and tags (RFC 4235), which a phone
// needs to build a Replaces header, or if an attribute is not escaped.
func TestDialogInfoNamesTheDialog(t *testing.T) {
	body := string(dialogXML("200", testDomain, 3, StateOnCall, &dialogIdent{callID: `x"<y`, localTag: "l", remoteTag: "r", direction: "recipient"}))
	for _, want := range []string{`call-id="x&#34;&lt;y"`, `local-tag="l"`, `remote-tag="r"`, `direction="recipient"`, "<state>confirmed</state>"} {
		if !strings.Contains(body, want) {
			t.Errorf("dialog-info lacks %s:\n%s", want, body)
		}
	}
	if strings.Contains(string(dialogXML("200", testDomain, 3, StateIdle, nil)), "call-id") {
		t.Error("an idle extension's dialog-info names a dialog")
	}

	pbx := startPBX(t, callerDevices())
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	if res := a.subscribe(t, "200", 300); res.StatusCode != 200 {
		t.Fatalf("SUBSCRIBE = %d", res.StatusCode)
	}
	waitNotifyState(t, a, "terminated")
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	inv := waitReq(t, b.invites, "INVITE to b")
	deadline := time.After(5 * time.Second)
	for {
		select {
		case n := <-a.notifies:
			if strings.Contains(string(n.Body()), `call-id="`+inv.CallID().Value()+`"`) && strings.Contains(string(n.Body()), "confirmed") {
				hangup(t, r.dcs)
				pbx.nextCDR(t)
				return
			}
		case <-deadline:
			t.Fatal("no dialog-info NOTIFY named the answered call's dialog")
		}
	}
}
