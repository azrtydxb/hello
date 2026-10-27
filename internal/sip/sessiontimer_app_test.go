package sip

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/queue"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

func sessionOpts() []pbxOpt { return []pbxOpt{withSessions(2*time.Second, time.Second, 0)} }

// TestSessionTimersQueue fails if a caller waiting in a queue has no session
// timer (a phone that left the Wi-Fi stays in the queue), if the failed
// refresh does not take it out, or if the bridged call to the agent has no
// timers on either dialog.
func TestSessionTimersQueue(t *testing.T) {
	t.Run("a waiting caller is refreshed and removed when the refresh fails", func(t *testing.T) {
		st := queue.NewMemStore()
		pbx, _ := queuePBXWith(t, st, nil, sessionOpts(), testQueue("sales", "600", queue.RingAll, static("300", 0, 1)))
		a, _, _ := registerABC(t, pbx)
		r := waitCall(t, dial(t.Context(), a, "600"))
		if r.err != nil {
			t.Fatalf("call to the queue: %v", r.err)
		}
		if got := r.dcs.InviteResponse.GetHeader("Session-Expires"); got == nil || got.Value() != "2;refresher=uas" {
			t.Fatalf("200 to the queued caller: Session-Expires = %v, want 2;refresher=uas", got)
		}
		refreshWithin(t, a.reinvites, 4*time.Second, "the queued caller")
		a.mu.Lock()
		a.reinviteStatus = 481
		a.mu.Unlock()
		waitReq(t, a.byes, "BYE to the queued caller")
		eventually(t, "the caller left the queue", func() bool {
			ws, _ := st.Waiting(t.Context(), "sales")
			return len(ws) == 0
		})
		cd := lastCDRFor(t, pbx, "600")
		if !strings.Contains(cd.FailureReason, "session timer") {
			t.Fatalf("CDR reason = %q", cd.FailureReason)
		}
	})

	t.Run("a caller that must refresh gets no timer in the queue", func(t *testing.T) {
		st := queue.NewMemStore()
		pbx, _ := queuePBXWith(t, st, nil, sessionOpts(), testQueue("sales", "600", queue.RingAll, static("300", 0, 1)))
		a, _, _ := registerABC(t, pbx)
		req := a.inviteReq("600")
		req.AppendHeader(sip.NewHeader("Supported", "timer"))
		req.AppendHeader(sip.NewHeader("Session-Expires", "2;refresher=uac"))
		dcs, err := a.callReq(callCtx(t), req)
		if err != nil {
			t.Fatal(err)
		}
		if got := dcs.InviteResponse.GetHeader("Session-Expires"); got != nil {
			t.Fatalf("200 carries Session-Expires %q: its refresh would be a request the queue does not handle", got.Value())
		}
		noReq(t, a.reinvites, 2500*time.Millisecond, "a refresh of a caller that refreshes itself")
		hangup(t, dcs)
	})

	t.Run("the call bridged to the agent has timers on both dialogs", func(t *testing.T) {
		st := queue.NewMemStore()
		pbx, _ := queuePBXWith(t, st, nil, sessionOpts(), testQueue("sales", "600", queue.RingAll, static("200", 0, 1)))
		a := newPhone(t, pbx, "a1", "pa")
		b := newPhone(t, pbx, "b1", "pb1")
		a.register(t)
		registerAs(t, b, grandstreamUA)
		b.setCallee(answerAfter(nil))
		r := waitCall(t, dial(t.Context(), a, "600"))
		if r.err != nil {
			t.Fatal(r.err)
		}
		waitReq(t, b.invites, "INVITE to the agent")
		eventually(t, "answered by the agent", func() bool {
			return pbx.metric(t, "hello_queue_calls_total", map[string]string{"queue": "sales", "event": "answered"}) == 1
		})
		waitReq(t, b.acks, "ACK to the agent")
		time.Sleep(100 * time.Millisecond)
		drain(a.reinvites)
		drain(b.reinvites)
		refreshWithin(t, b.reinvites, 4*time.Second, "the agent")
		refreshWithin(t, a.reinvites, 4*time.Second, "the caller")
		b.mu.Lock()
		b.reinviteStatus = 481
		b.mu.Unlock()
		waitReq(t, b.byes, "BYE to the agent")
		waitReq(t, a.byes, "BYE to the caller")
	})
}

// TestSessionTimersIVRConnect fails if a call an IVR menu connects to an
// extension has no session timers, or one in the menu has none.
func TestSessionTimersIVRConnect(t *testing.T) {
	objs := &fakeObjects{}
	_ = objs.Put(context.Background(), "ann/greet.wav", media.EncodeWAV(make([]byte, 16000), 8000))
	m := ivrMenu("main", "800", map[string]snapshot.IVRDest{"1": ext("300")})
	m.DigitTimeout = 30
	feats := func(s *snapshot.Snapshot) {
		s.WithAnnouncements(map[string]string{"greet": "ann/greet.wav"})
		s.WithIVRMenus([]snapshot.IVRMenu{m})
	}
	pbx := startPBX(t, threeDevices(), withMedia(false), withFeatures(feats), withVoicemail(newVMStore(), objs), withSessions(2*time.Second, time.Second, 0))
	a := newPhone(t, pbx, "a1", "pa")
	c := newPhone(t, pbx, "c1", "pc")
	a.register(t)
	registerAs(t, c, grandstreamUA)
	c.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "800"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := r.dcs.InviteResponse.GetHeader("Session-Expires"); got == nil || got.Value() != "2;refresher=uas" {
		t.Fatalf("200 from the menu: Session-Expires = %v, want 2;refresher=uas", got)
	}
	refreshWithin(t, a.reinvites, 4*time.Second, "the caller in the menu")
	seq := r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, a, r.dcs, "1", &seq)
	waitReq(t, c.invites, "INVITE to the option's extension")
	waitReq(t, c.acks, "ACK to the extension")
	time.Sleep(100 * time.Millisecond)
	drain(a.reinvites)
	drain(c.reinvites)
	refreshWithin(t, c.reinvites, 4*time.Second, "the connected extension")
	refreshWithin(t, a.reinvites, 4*time.Second, "the caller after the connect")
	c.mu.Lock()
	c.reinviteStatus = 481
	c.mu.Unlock()
	waitReq(t, c.byes, "BYE to the extension")
	waitReq(t, a.byes, "BYE to the caller")
}

// TestSessionTimersQueueTransfer fails if a caller a blind transfer puts in a
// queue loses the session timer it had.
func TestSessionTimersQueueTransfer(t *testing.T) {
	st := queue.NewMemStore()
	pbx, _ := queuePBXWith(t, st, nil, sessionOpts(), testQueue("sales", "600", queue.RingAll, static("300", 0, 1)))
	a, b, _ := registerABC(t, pbx)
	b.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, b.invites, "INVITE to b")
	waitReq(t, b.acks, "ACK to b")
	dss := <-serversOf(b)
	if res := b.sendReferFromCallee(t, dss, "<sip:600@"+testDomain+">"); res.StatusCode != 202 {
		t.Fatalf("REFER = %d", res.StatusCode)
	}
	for {
		n := waitReq(t, b.notifies, "NOTIFY to the transferor")
		if strings.Contains(string(n.Body()), "200 OK") {
			break
		}
	}
	waitReq(t, b.byes, "BYE to the transferee's leg")
	eventually(t, "the caller waits in the queue", func() bool {
		ws, _ := st.Waiting(t.Context(), "sales")
		return len(ws) == 1
	})
	time.Sleep(100 * time.Millisecond)
	drain(a.reinvites)
	refreshWithin(t, a.reinvites, 4*time.Second, "the queued, transferred caller")
}

// TestSessionTimersIVRTakeover fails if an IVR menu's caller, taken over by a
// survivor, is not refreshed with the timer the owner negotiated, or the
// one-legged call is ended for a callee dialog it never had.
func TestSessionTimersIVRTakeover(t *testing.T) {
	objs := &fakeObjects{}
	_ = objs.Put(context.Background(), "ann/greet.wav", media.EncodeWAV(make([]byte, 16000*4), 8000))
	m := ivrMenu("main", "800", map[string]snapshot.IVRDest{"1": ext("200")})
	m.DigitTimeout = 30
	feats := func(s *snapshot.Snapshot) {
		s.WithAnnouncements(map[string]string{"greet": "ann/greet.wav"})
		s.WithIVRMenus([]snapshot.IVRMenu{m})
	}
	ha, mem, owner, taker := haPair(t, callerDevices(), withMedia(false), withFeatures(feats), withVoicemail(newVMStore(), objs),
		withSessions(2*time.Second, time.Second, 0))
	a := newPhone(t, owner, "a1", "pa")
	a.register(t)
	r := waitCall(t, dial(t.Context(), a, "800"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	rec := waitRecord(t, ha, "the menu call is replicated with its timer", "sip-1", func(s livestate.DialogState) bool {
		return s.State == haPhaseIVR && s.Session != nil && s.Session.Legs[sessCaller] != nil && s.Legs[0].RemoteSDP != ""
	})
	if l := rec.Session.Legs[sessCaller]; l.IntervalSecs != 2 || !l.Local || rec.Session.Legs[sessCallee] != nil {
		t.Fatalf("replicated session = %+v", rec.Session)
	}
	haReinviteTimeout = time.Second
	killNode(owner, mem, map[string]int{"sip-1": 1}, "sip-2")
	taker.srv.takeoverPass(t.Context())
	waitReq(t, a.reinvites, "takeover re-INVITE to the caller")
	waitReq(t, a.acks, "ACK to the caller's 200")
	drain(a.reinvites)
	refreshWithin(t, a.reinvites, 4*time.Second, "the caller on the survivor")
	noReq(t, a.byes, 1500*time.Millisecond, "BYE to a caller whose refresh was answered")
	if n := len(liveCalls(t, taker)); n != 1 {
		t.Fatalf("live calls on the survivor = %d, want the menu call", n)
	}
}
