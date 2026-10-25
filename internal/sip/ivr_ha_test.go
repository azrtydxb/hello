package sip

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
)

// TestIVRTakeoverDialsOut: the node dies while a caller is in an IVR menu.
// The survivor re-homes the caller, restarts the menu from its greeting,
// reads the caller's key (RFC 4733), rings the option's extension with the
// caller's identity and offer taken from the replicated dialog, and carries
// the bridged call as an ordinary taken-over two-leg call: the caller's BYE
// reaches the callee. It fails if the taker cannot build the leg, the caller
// is not bridged, or the hang-up does not reach the callee.
func TestIVRTakeoverDialsOut(t *testing.T) {
	objs := &fakeObjects{}
	_ = objs.Put(context.Background(), "ann/greet.wav", media.EncodeWAV(make([]byte, 16000*4), 8000))
	m := ivrMenu("main", "800", map[string]snapshot.IVRDest{"1": ext("200")})
	m.DigitTimeout = 20
	feats := func(s *snapshot.Snapshot) {
		s.WithAnnouncements(map[string]string{"greet": "ann/greet.wav"})
		s.WithIVRMenus([]snapshot.IVRMenu{m})
	}
	shared := newFakeState() // the survivor finds the callee's registration
	ha, mem, owner, taker := haPair(t, callerDevices(), withMedia(false), withFeatures(feats), withVoicemail(newVMStore(), objs),
		func(_ *Config, d *Deps) { d.State = shared })
	// The callee registered through the survivor: a registration made through
	// the dead node is only reachable through that node's flow.
	a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, taker, "b1", "pb1")
	a.register(t)
	b.register(t)
	b.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "800"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	st := waitRecord(t, ha, "the menu call is replicated", "sip-1", func(s livestate.DialogState) bool {
		return s.State == haPhaseIVR && s.Legs[1].CallID == "" && s.StateDetail == haIVRDetailPrefix+"main" && s.Legs[0].RemoteSDP != ""
	})
	haReinviteTimeout = time.Second
	killNode(owner, mem, map[string]int{"sip-1": 1}, "sip-2")
	taker.srv.takeoverPass(t.Context())
	ra := waitReq(t, a.reinvites, "takeover re-INVITE to the caller")
	waitReq(t, a.acks, "ACK to the caller's 200")
	eventually(t, "the taker enters the menu", func() bool {
		return taker.metric(t, "hello_ivr_total", map[string]string{"event": "entered"}) >= 1
	})
	noReq(t, b.invites, 300*time.Millisecond, "nothing rings before a key")

	feed := startRTPFeed(t, ra.Body())
	feed.digit('1')
	inv := waitReq(t, b.invites, "the taker rings the option's extension")
	if inv.From().Address.User != "100" {
		t.Fatalf("the callee sees caller %q, want 100", inv.From().Address.User)
	}
	anchoredSDP(t, taker, inv.Body())
	waitReq(t, b.acks, "ACK to the callee")
	eventually(t, "the call is connected on the taker", func() bool {
		return taker.metric(t, "hello_ivr_total", map[string]string{"event": "connected"}) == 1
	})
	hangupHomed(t, a, taker, ra, st.Legs[0])
	waitReq(t, b.byes, "the caller's BYE reaches the callee through the taker")
	cd := taker.nextCDR(t)
	tr := traceText(cd.Trace)
	if !strings.Contains(tr, "ha: taken over from sip-1") || !strings.Contains(tr, "caller connected to 200") {
		t.Fatalf("CDR trace: %s", tr)
	}
	if z := taker.metric(t, "hello_zombie_calls_total", nil); z != 0 {
		t.Fatalf("zombies = %v", z)
	}
}

// TestIVRTakeoverUnrecoverableFallsBack: when the replicated state lacks the
// caller's media offer the survivor cannot build a leg; the key's destination
// is then not treated as a no-answer, the menu's invalid-choice exit applies.
func TestIVRTakeoverUnrecoverableFallsBack(t *testing.T) {
	objs := &fakeObjects{}
	_ = objs.Put(context.Background(), "ann/greet.wav", media.EncodeWAV(make([]byte, 16000*4), 8000))
	_ = objs.Put(context.Background(), "ann/bad.wav", media.EncodeWAV(make([]byte, 3200), 8000))
	m := ivrMenu("main", "800", map[string]snapshot.IVRDest{"1": ext("200")})
	m.DigitTimeout = 20
	m.Invalid = snapshot.IVRDest{Kind: snapshot.IVRAnnouncement, Target: "bad"}
	feats := func(s *snapshot.Snapshot) {
		s.WithAnnouncements(map[string]string{"greet": "ann/greet.wav", "bad": "ann/bad.wav"})
		s.WithIVRMenus([]snapshot.IVRMenu{m})
	}
	ha, mem, owner, taker := haPair(t, callerDevices(), withMedia(false), withFeatures(feats), withVoicemail(newVMStore(), objs))
	a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1")
	a.register(t)
	b.register(t)
	r := waitCall(t, dial(t.Context(), a, "800"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitRecord(t, ha, "the menu call is replicated", "sip-1", func(s livestate.DialogState) bool {
		return s.State == haPhaseIVR && s.Legs[0].RemoteSDP != ""
	})
	haReinviteTimeout = time.Second
	killNode(owner, mem, map[string]int{"sip-1": 1}, "sip-2")
	taker.srv.takeoverPass(t.Context())
	ra := waitReq(t, a.reinvites, "takeover re-INVITE to the caller")
	eventually(t, "the taker enters the menu", func() bool {
		return taker.metric(t, "hello_ivr_total", map[string]string{"event": "entered"}) >= 1
	})
	// What the taker rebuilt from the state loses its offer.
	taker.srv.mu.Lock()
	for c := range taker.srv.calls {
		if hom := c.homed(); hom != nil {
			hom.legs[0].mu.Lock()
			hom.legs[0].remoteSDP = ""
			hom.legs[0].mu.Unlock()
		}
	}
	taker.srv.mu.Unlock()
	feed := startRTPFeed(t, ra.Body())
	feed.digit('1')
	waitReq(t, a.byes, "the invalid-choice exit plays and hangs up")
	noReq(t, b.invites, 200*time.Millisecond, "nobody is rung")
	if v := taker.metric(t, "hello_ivr_total", map[string]string{"event": "exit_announcement"}); v != 1 {
		t.Fatalf("exit_announcement = %v, want 1", v)
	}
}
