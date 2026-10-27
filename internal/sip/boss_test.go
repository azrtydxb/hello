package sip

import (
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// bossOpts is a PBX where extension 200 (b1) is the boss and 300 (c1) the
// assistant, with the given links and extension features.
func bossOpts(links []snapshot.BossLink, exts ...snapshot.Extension) pbxOpt {
	return withFeatures(func(s *snapshot.Snapshot) {
		s.WithBossLinks(links)
		if len(exts) > 0 {
			s.WithExtensionData(exts)
		}
	})
}

// answerCounting makes the phone answer with an SDP that names a fresh media
// socket and counts the datagrams the PBX sends there. The SDP is built here
// and the phone's own is left alone, which its INVITE handler reads on
// another goroutine.
func answerCounting(t *testing.T, p *phone) *atomic.Int64 {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	sdp := strings.Replace(p.sdp, "m=audio "+strconv.Itoa(mediaPort(t, p))+" ",
		"m=audio "+strconv.Itoa(conn.LocalAddr().(*net.UDPAddr).Port)+" ", 1)
	var n atomic.Int64
	go func() {
		buf := make([]byte, 2048)
		for {
			if _, _, err := conn.ReadFromUDP(buf); err != nil {
				return
			}
			n.Add(1)
		}
	}()
	p.setCallee(func(_ *phone, _ *sip.Request, _ sip.ServerTransaction, dss *sipgo.DialogServerSession) {
		_ = dss.RespondSDP([]byte(sdp))
	})
	return &n
}

// pressKey sends the assistant's key as RFC 2833 events to the relay leg the
// offer names, again every 400 ms until the caller's call has an outcome (a
// datagram lost on a loaded runner must not fail the test), and returns it.
func pressKey(t *testing.T, offer []byte, key byte, ch <-chan callResult) callResult {
	t.Helper()
	feed := startRTPFeed(t, offer)
	for i := 0; i < 8; i++ {
		feed.digit(key)
		select {
		case r := <-ch:
			return r
		case <-time.After(400 * time.Millisecond):
		}
	}
	t.Fatal("the call had no outcome after the key press")
	return callResult{}
}

func bossLink() snapshot.BossLink {
	return snapshot.BossLink{Boss: "200", Assistant: "300", RingMode: snapshot.BossRingBoth}
}

// bossPhones registers the caller (100), the boss (200), the assistant (300)
// and a spare extension (210).
func bossPhones(t *testing.T, pbx *testPBX) (a, b, c, d *phone) {
	t.Helper()
	a = newPhone(t, pbx, "a1", "pa")
	b = newPhone(t, pbx, "b1", "pb1")
	c = newPhone(t, pbx, "c1", "pc")
	d = newPhone(t, pbx, "d1", "pd")
	for _, p := range []*phone{a, b, c, d} {
		p.register(t)
	}
	return a, b, c, d
}

// TestBossLinkAssistantAnswers: a call to the boss rings the boss's and the
// assistant's phones together; the assistant answering takes the call, the
// boss's phone is cancelled, and the CDR names the assistant. It fails if the
// assistant is not rung, the boss's leg is left ringing, or answered_by and
// the trace do not name the assistant.
func TestBossLinkAssistantAnswers(t *testing.T) {
	pbx := startPBX(t, threeDevices(), shortRing(2*time.Second), bossOpts([]snapshot.BossLink{bossLink()}))
	a, b, c, _ := bossPhones(t, pbx)
	b.setCallee(ringForever())
	c.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("call to the boss: %v", r.err)
	}
	waitReq(t, b.invites, "INVITE to the boss")
	waitReq(t, c.invites, "INVITE to the assistant")
	waitReq(t, b.cancels, "CANCEL to the boss's phone after the assistant answered")
	hangup(t, r.dcs)
	rec := pbx.nextCDR(t)
	if rec.AnsweredBy != "300" || rec.Destination != "200" || rec.FinalStatus != 200 {
		t.Fatalf("CDR answeredBy %q destination %q status %d; want 300, 200, 200", rec.AnsweredBy, rec.Destination, rec.FinalStatus)
	}
	if got := traceText(rec.Trace); !strings.Contains(got, "Answered by assistant 300 for 200") {
		t.Fatalf("trace lacks the assistant's answer:\n%s", got)
	}
}

// TestBossLinkBossAnswers: the boss answering first wins and the CDR carries
// no answered_by. It fails if the assistant's phone keeps ringing or the CDR
// credits the assistant.
func TestBossLinkBossAnswers(t *testing.T) {
	pbx := startPBX(t, threeDevices(), shortRing(2*time.Second), bossOpts([]snapshot.BossLink{bossLink()}))
	a, b, c, _ := bossPhones(t, pbx)
	b.setCallee(answerAfter(nil))
	c.setCallee(ringForever())
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("call to the boss: %v", r.err)
	}
	waitReq(t, c.invites, "INVITE to the assistant")
	waitReq(t, c.cancels, "CANCEL to the assistant's phone after the boss answered")
	hangup(t, r.dcs)
	if rec := pbx.nextCDR(t); rec.AnsweredBy != "" {
		t.Fatalf("CDR answeredBy = %q, want empty", rec.AnsweredBy)
	}
}

// TestBossLinkOnlyOwnBoss: a link rings only for its own boss. It fails if
// the assistant of one boss is rung for another (S-18).
func TestBossLinkOnlyOwnBoss(t *testing.T) {
	// 200 is the boss of 300; 300 is the boss of 210. A call to 200 rings 200
	// and 300 only; a call to 300 rings 300 and 210 only.
	links := []snapshot.BossLink{bossLink(), {Boss: "300", Assistant: "210", RingMode: snapshot.BossRingBoth}}
	pbx := startPBX(t, threeDevices(), shortRing(2*time.Second), bossOpts(links))
	a, b, c, d := bossPhones(t, pbx)
	b.setCallee(ringForever())
	c.setCallee(answerAfter(nil))
	d.setCallee(ringForever())
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("call to 200: %v", r.err)
	}
	waitReq(t, b.invites, "INVITE to 200")
	waitReq(t, c.invites, "INVITE to 300, the assistant of 200")
	noReq(t, d.invites, 300*time.Millisecond, "INVITE to 210, which is the assistant of 300 only")
	hangup(t, r.dcs)
}

// TestBossLinkForwardAlwaysWins: the boss's unconditional forward wins over
// the link, so the assistant does not ring. It fails if the assistant's phone
// is rung.
func TestBossLinkForwardAlwaysWins(t *testing.T) {
	pbx := startPBX(t, threeDevices(), shortRing(2*time.Second),
		bossOpts([]snapshot.BossLink{bossLink()}, snapshot.Extension{Number: "200", ForwardAlways: "210"}))
	a, _, c, d := bossPhones(t, pbx)
	c.setCallee(ringForever())
	d.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	waitReq(t, d.invites, "INVITE to the forward target")
	noReq(t, c.invites, 300*time.Millisecond, "INVITE to the assistant")
	hangup(t, r.dcs)
}

// TestBossLinkDND: DND on the boss silences the boss's phone, and the
// assistant still rings. It fails if the call is declined or the boss's phone
// rings.
func TestBossLinkDND(t *testing.T) {
	pbx := startPBX(t, threeDevices(), shortRing(2*time.Second),
		bossOpts([]snapshot.BossLink{bossLink()}, snapshot.Extension{Number: "200", DND: true}))
	a, b, c, _ := bossPhones(t, pbx)
	b.setCallee(ringForever())
	c.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	waitReq(t, c.invites, "INVITE to the assistant")
	noReq(t, b.invites, 200*time.Millisecond, "INVITE to the boss, who is DND")
	hangup(t, r.dcs)
	if rec := pbx.nextCDR(t); rec.AnsweredBy != "300" {
		t.Fatalf("answeredBy = %q, want 300", rec.AnsweredBy)
	}
}

// TestBossLinkDivert: with the divert on, only the assistant rings; an
// assistant with no phone registered falls back to the boss's own phone. It
// fails if the boss's phone rings while the divert is on and the assistant is
// there, or if the call is lost when the assistant is not.
func TestBossLinkDivert(t *testing.T) {
	link := bossLink()
	link.DivertOn = true
	pbx := startPBX(t, threeDevices(), shortRing(2*time.Second), bossOpts([]snapshot.BossLink{link}))
	a, b, c, _ := bossPhones(t, pbx)
	b.setCallee(ringForever())
	c.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	waitReq(t, c.invites, "INVITE to the assistant")
	noReq(t, b.invites, 200*time.Millisecond, "INVITE to the boss while diverted")
	hangup(t, r.dcs)

	// The assistant has no registered phone: the boss rings after all.
	pbx2 := startPBX(t, callerDevices(), shortRing(2*time.Second), bossOpts([]snapshot.BossLink{link}))
	a2 := newPhone(t, pbx2, "a1", "pa")
	b2 := newPhone(t, pbx2, "b1", "pb1")
	a2.register(t)
	b2.register(t)
	b2.setCallee(answerAfter(nil))
	r2 := waitCall(t, dial(t.Context(), a2, "200"))
	if r2.err != nil {
		t.Fatalf("call with the assistant unregistered: %v", r2.err)
	}
	waitReq(t, b2.invites, "INVITE to the boss when the assistant has no phone")
	hangup(t, r2.dcs)
}

// TestBossLinkAssistantFirst: the assistant's phone rings alone for the lead
// time, then the boss's rings too. It fails if the boss rings at once, or
// never.
func TestBossLinkAssistantFirst(t *testing.T) {
	prev := bossAssistantLead
	bossAssistantLead = 400 * time.Millisecond
	t.Cleanup(func() { bossAssistantLead = prev })
	link := bossLink()
	link.RingMode = snapshot.BossRingAssistantFirst
	pbx := startPBX(t, threeDevices(), shortRing(3*time.Second), bossOpts([]snapshot.BossLink{link}))
	a, b, c, _ := bossPhones(t, pbx)
	b.setCallee(answerAfter(nil))
	c.setCallee(ringForever())
	start := time.Now()
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	waitReq(t, c.invites, "INVITE to the assistant")
	if time.Since(start) < 350*time.Millisecond {
		t.Fatal("the call was answered by the boss before the lead time")
	}
	waitReq(t, b.invites, "INVITE to the boss after the lead time")
	hangup(t, r.dcs)
}

// TestBossLinkNoAnswerForward: CFNA of the boss runs after the boss's own
// timeout when neither the boss nor the assistant answers. It fails if the
// call ends instead of going to the forward target.
func TestBossLinkNoAnswerForward(t *testing.T) {
	pbx := startPBX(t, threeDevices(), shortRing(400*time.Millisecond),
		bossOpts([]snapshot.BossLink{bossLink()}, snapshot.Extension{Number: "200", ForwardNoAnswer: "210"}))
	a, b, c, d := bossPhones(t, pbx)
	b.setCallee(ringForever())
	c.setCallee(ringForever())
	d.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	waitReq(t, d.invites, "INVITE to the no-answer forward target")
	hangup(t, r.dcs)
}

// TestBossDivertCode: *64 dialled from the boss's own phone flips the divert
// of its link, and it does nothing for an extension that is no boss. It fails
// if the update is not queued, carries the wrong value, or the answer is not
// the phone's confirmation.
func TestBossDivertCode(t *testing.T) {
	settings := &fakeSettings{}
	link := bossLink()
	feats := withFeatures(func(s *snapshot.Snapshot) {
		s.WithBossLinks([]snapshot.BossLink{link})
		s.WithFeatureCodes([]snapshot.FeatureCode{{Code: "*64", Action: ActionBossDivert}})
	})
	pbx := startPBX(t, threeDevices(), feats, func(_ *Config, d *Deps) { d.Settings = settings })
	a, b, _, _ := bossPhones(t, pbx)
	r := waitCall(t, dial(t.Context(), b, "*64"))
	if r.err != nil {
		t.Fatalf("*64 from the boss: %v", r.err)
	}
	hangup(t, r.dcs)
	got := settings.got()
	if len(got) != 1 || got[0].Extension != "200" || got[0].BossDivert == nil || !*got[0].BossDivert {
		t.Fatalf("queued updates = %+v, want one divert-on for 200", got)
	}
	if v := pbx.metric(t, "hello_boss_divert_total", map[string]string{"result": "on"}); v != 1 {
		t.Fatalf("hello_boss_divert_total{on} = %v", v)
	}
	// An extension that is no boss is refused.
	if r := waitCall(t, dial(t.Context(), a, "*64")); r.err == nil {
		t.Fatal("*64 from an extension that is no boss was answered")
	}
	if len(settings.got()) != 1 {
		t.Fatal("*64 from a non-boss queued an update")
	}
}

// TestBossDivertOff: with the divert on, *64 turns it off.
func TestBossDivertOff(t *testing.T) {
	settings := &fakeSettings{}
	link := bossLink()
	link.DivertOn = true
	feats := withFeatures(func(s *snapshot.Snapshot) {
		s.WithBossLinks([]snapshot.BossLink{link})
		s.WithFeatureCodes([]snapshot.FeatureCode{{Code: "*64", Action: ActionBossDivert}})
	})
	pbx := startPBX(t, threeDevices(), feats, func(_ *Config, d *Deps) { d.Settings = settings })
	_, b, _, _ := bossPhones(t, pbx)
	r := waitCall(t, dial(t.Context(), b, "*64"))
	if r.err != nil {
		t.Fatalf("*64: %v", r.err)
	}
	hangup(t, r.dcs)
	got := settings.got()
	if len(got) != 1 || got[0].BossDivert == nil || *got[0].BossDivert {
		t.Fatalf("queued updates = %+v, want divert off", got)
	}
}

// TestBossDivertInCall: *64 dialled during a call acts like the code dialled
// as a call, and the call goes on. It fails if the digits are not taken as the
// code or the current call ends.
func TestBossDivertInCall(t *testing.T) {
	settings := &fakeSettings{}
	feats := withFeatures(func(s *snapshot.Snapshot) {
		s.WithBossLinks([]snapshot.BossLink{bossLink()})
		s.WithFeatureCodes([]snapshot.FeatureCode{{Code: "*64", Action: ActionBossDivert}})
	})
	pbx := startPBX(t, threeDevices(), feats, func(_ *Config, d *Deps) { d.Settings = settings })
	a, b, _, _ := bossPhones(t, pbx)
	a.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), b, "100"))
	if r.err != nil {
		t.Fatalf("call from the boss: %v", r.err)
	}
	waitReq(t, a.invites, "INVITE to the callee")
	for _, d := range []string{"*", "6", "4"} {
		b.sendDTMF(t, r.dcs, d)
	}
	eventually(t, "the divert update", func() bool { return len(settings.got()) == 1 })
	if u := settings.got()[0]; u.Extension != "200" || u.BossDivert == nil || !*u.BossDivert {
		t.Fatalf("update = %+v", u)
	}
	hangup(t, r.dcs) // the call was still up
	if rec := pbx.nextCDR(t); rec.FinalStatus != 200 {
		t.Fatalf("CDR status %d", rec.FinalStatus)
	}
}

// screenedBoss is a PBX with a screened link from boss 200 to assistant 300
// and a voicemail box on the boss, with the media anchor on.
func screenedBoss(t *testing.T, mode string) (*testPBX, *fakeVMStore) {
	t.Helper()
	vm := newVMStore()
	vm.boxes["200"] = VoicemailBox{ID: 5, Extension: "200"}
	link := bossLink()
	link.Screening = true
	link.RingMode = mode
	pbx := startPBX(t, threeDevices(), shortRing(5*time.Second),
		bossOpts([]snapshot.BossLink{link}, snapshot.Extension{Number: "200", Name: "Boss", VoicemailEnabled: true, VoicemailBoxID: 5}),
		withVoicemail(vm, &fakeObjects{}))
	anchored(pbx)
	return pbx, vm
}

// TestBossScreeningMessage: the assistant answers a screened call, hears the
// prompt and presses 2: the caller goes to the boss's voicemail and the
// boss's phone stops ringing. It fails if the caller is bridged to the
// assistant, the prompt is not played to the assistant, the boss's voicemail
// is not opened, or the boss's phone is left ringing.
func TestBossScreeningMessage(t *testing.T) {
	pbx, vm := screenedBoss(t, snapshot.BossRingBoth)
	a, b, c, _ := bossPhones(t, pbx)
	b.setCallee(ringForever())
	heard := answerCounting(t, c)
	ch := dial(t.Context(), a, "200")
	invC := waitReq(t, c.invites, "INVITE to the assistant")
	waitReq(t, c.acks, "ACK to the assistant")
	eventually(t, "the screening prompt reaches the assistant", func() bool { return heard.Load() > 0 })
	r := pressKey(t, invC.Body(), 2, ch)
	if r.err != nil {
		t.Fatalf("screened call: %v", r.err)
	}
	waitReq(t, c.byes, "BYE to the assistant after the choice")
	waitReq(t, b.cancels, "CANCEL to the boss's phone")
	eventually(t, "the boss's voicemail is opened", func() bool {
		vm.mu.Lock()
		defer vm.mu.Unlock()
		for _, e := range vm.looked {
			if e == "200" {
				return true
			}
		}
		return false
	})
	hangup(t, r.dcs)
	rec := pbx.nextCDR(t)
	if rec.AnsweredBy != "" {
		t.Fatalf("answeredBy = %q: the assistant never took the call", rec.AnsweredBy)
	}
	if got := traceText(rec.Trace); !strings.Contains(got, "assistant 300 sends the call to the voicemail of 200") {
		t.Fatalf("trace lacks the screening choice:\n%s", got)
	}
}

// TestBossScreeningThrough: the assistant presses 1: the boss's phone rings
// at once, even before the assistant-first lead has passed, and the boss
// answering takes the call. It fails if the boss is not rung or the assistant
// is left connected.
func TestBossScreeningThrough(t *testing.T) {
	pbx, _ := screenedBoss(t, snapshot.BossRingAssistantFirst)
	a, b, c, _ := bossPhones(t, pbx)
	b.setCallee(answerAfter(nil))
	heard := answerCounting(t, c)
	ch := dial(t.Context(), a, "200")
	invC := waitReq(t, c.invites, "INVITE to the assistant")
	waitReq(t, c.acks, "ACK to the assistant")
	noReq(t, b.invites, 300*time.Millisecond, "INVITE to the boss before the assistant chose")
	eventually(t, "the screening prompt reaches the assistant", func() bool { return heard.Load() > 0 })
	r := pressKey(t, invC.Body(), 1, ch)
	waitReq(t, b.invites, "INVITE to the boss after the assistant pressed 1")
	if r.err != nil {
		t.Fatalf("screened call: %v", r.err)
	}
	waitReq(t, c.byes, "BYE to the assistant")
	hangup(t, r.dcs)
	rec := pbx.nextCDR(t)
	if rec.AnsweredBy != "" || rec.FinalStatus != 200 {
		t.Fatalf("answeredBy %q status %d; want the boss to have answered", rec.AnsweredBy, rec.FinalStatus)
	}
	if got := traceText(rec.Trace); !strings.Contains(got, "assistant 300 puts the call through to 200") {
		t.Fatalf("trace lacks the screening choice:\n%s", got)
	}
}

// TestBossScreeningBossAnswersFirst: the boss answering while the assistant
// hears the prompt takes the call and the assistant is dropped. It fails if
// the assistant stays on the line.
func TestBossScreeningBossAnswersFirst(t *testing.T) {
	pbx, _ := screenedBoss(t, snapshot.BossRingBoth)
	a, b, c, _ := bossPhones(t, pbx)
	release := make(chan struct{})
	b.setCallee(answerAfter(release))
	heard := answerCounting(t, c)
	ch := dial(t.Context(), a, "200")
	waitReq(t, c.invites, "INVITE to the assistant")
	eventually(t, "the screening prompt reaches the assistant", func() bool { return heard.Load() > 0 })
	close(release)
	r := waitCall(t, ch)
	if r.err != nil {
		t.Fatalf("screened call: %v", r.err)
	}
	waitReq(t, c.byes, "BYE to the assistant who was being screened")
	hangup(t, r.dcs)
	if rec := pbx.nextCDR(t); rec.AnsweredBy != "" {
		t.Fatalf("answeredBy = %q, want the boss's own answer", rec.AnsweredBy)
	}
}

// TestBossScreeningAssistantHangsUp: the assistant hanging up during the
// prompt is no choice: the boss's phone goes on ringing and can still take
// the call. It fails if the caller is cut off or the boss's phone is
// cancelled.
func TestBossScreeningAssistantHangsUp(t *testing.T) {
	pbx, _ := screenedBoss(t, snapshot.BossRingBoth)
	a, b, c, _ := bossPhones(t, pbx)
	release := make(chan struct{})
	b.setCallee(answerAfter(release))
	heard := answerCounting(t, c)
	ch := dial(t.Context(), a, "200")
	invC := waitReq(t, c.invites, "INVITE to the assistant")
	waitReq(t, c.acks, "ACK to the assistant")
	eventually(t, "the screening prompt reaches the assistant", func() bool { return heard.Load() > 0 })
	byeFrom(t, c.serverFor(invC))
	noReq(t, b.cancels, 300*time.Millisecond, "CANCEL to the boss's phone: it is still ringing")
	close(release)
	r := waitCall(t, ch)
	if r.err != nil {
		t.Fatalf("screened call: %v", r.err)
	}
	hangup(t, r.dcs)
	if rec := pbx.nextCDR(t); rec.AnsweredBy != "" || rec.FinalStatus != 200 {
		t.Fatalf("answeredBy %q status %d; want the boss's own answer", rec.AnsweredBy, rec.FinalStatus)
	}
}

// TestBossOutboundAsBoss: with the link's flag on, the assistant's outbound
// call is decided with the boss's extension for its caller ID, and without
// the flag it is not. It fails if the routing call does not carry the boss
// (the default is off) or carries it when the flag is off.
func TestBossOutboundAsBoss(t *testing.T) {
	for _, on := range []bool{false, true} {
		link := snapshot.BossLink{Boss: "200", Assistant: "100", RingMode: snapshot.BossRingBoth, OutboundAsBoss: on}
		primary, backup := newCarrier(t, "acct1", "pw-primary"), newCarrier(t, "acct2", "pw-backup")
		r := &fakeRouter{extensions: map[string]bool{"100": true, "200": true},
			trunks: []routing.Trunk{primary.trunk(1, "carrier-primary", "ip"), backup.trunk(2, "carrier-backup", "ip")}}
		var mu sync.Mutex
		var seen routing.Call
		via := outboundVia(r, "+971501234567", "+97140000100", false, nil, 1)
		r.decide = func(c routing.Call, u routing.TrunkUsability) routing.Decision {
			mu.Lock()
			seen = c
			mu.Unlock()
			return via(c, u)
		}
		st := newFakeTrunkState()
		pbx := startPBX(t, callerDevices(), trunkCfg(st), withRouter(r, nil), bossOpts([]snapshot.BossLink{link}))
		a := newPhone(t, pbx, "a1", "pa")
		a.register(t)
		res := waitCall(t, dial(t.Context(), a, "0501234567"))
		if res.err != nil {
			t.Fatalf("on=%v: call: %v", on, res.err)
		}
		waitReq(t, primary.invites, "INVITE to the carrier")
		hangup(t, res.dcs)
		mu.Lock()
		got := seen
		mu.Unlock()
		want := ""
		if on {
			want = "200"
		}
		if got.FromExtension != "100" || got.CallerIDExtension != want {
			t.Fatalf("on=%v: routing call from %q caller ID of %q; want from 100, caller ID of %q",
				on, got.FromExtension, got.CallerIDExtension, want)
		}
		pbx.nextCDR(t)
	}
}

// TestBossLinkFromIVR: a menu option that dials the boss rings the assistant
// too, and the assistant answering is recorded. It fails if only the boss's
// phone rings or the CDR does not name the assistant.
func TestBossLinkFromIVR(t *testing.T) {
	pbx, _ := ivrPBX(t, 1, ivrMenu("main", "800", map[string]snapshot.IVRDest{"1": ext("200")}))
	pbx.snaps.p.Load().WithBossLinks([]snapshot.BossLink{bossLink()})
	a, b, c := registerABC(t, pbx)
	b.setCallee(ringForever())
	c.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "800"))
	if r.err != nil {
		t.Fatalf("call to the menu: %v", r.err)
	}
	seq := r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, a, r.dcs, "1", &seq)
	waitReq(t, b.invites, "INVITE to the boss")
	waitReq(t, c.invites, "INVITE to the assistant")
	waitReq(t, b.cancels, "CANCEL to the boss's phone after the assistant answered")
	hangup(t, r.dcs)
	rec := lastCDRFor(t, pbx, "800")
	if rec.AnsweredBy != "300" {
		t.Fatalf("answeredBy = %q, want 300", rec.AnsweredBy)
	}
}
