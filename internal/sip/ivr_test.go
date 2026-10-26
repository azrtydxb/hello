package sip

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
)

// ivrMenu is a menu with the defaults the tests share.
func ivrMenu(name, number string, opts map[string]snapshot.IVRDest) snapshot.IVRMenu {
	return snapshot.IVRMenu{
		ID: 1, Name: name, Number: number, Enabled: true, Greeting: "greet",
		DigitTimeout: 1, InterDigitTimeout: 1, Retries: 1,
		Timeout: snapshot.IVRDest{Kind: snapshot.IVRHangup}, Invalid: snapshot.IVRDest{Kind: snapshot.IVRHangup},
		Options: opts,
	}
}

func ext(n string) snapshot.IVRDest { return snapshot.IVRDest{Kind: snapshot.IVRExtension, Target: n} }

// ivrPBX starts a PBX with the menus, the announcements greet (seconds of
// audio given) and bad, and voicemail wiring.
func ivrPBX(t *testing.T, greetSeconds int, menus ...snapshot.IVRMenu) (*testPBX, *fakeObjects) {
	t.Helper()
	objs := &fakeObjects{}
	_ = objs.Put(context.Background(), "ann/greet.wav", media.EncodeWAV(make([]byte, 16000*greetSeconds), 8000))
	_ = objs.Put(context.Background(), "ann/bad.wav", media.EncodeWAV(make([]byte, 3200), 8000))
	feats := func(s *snapshot.Snapshot) {
		s.WithAnnouncements(map[string]string{"greet": "ann/greet.wav", "bad": "ann/bad.wav", "late": "ann/bad.wav"})
		s.WithIVRMenus(menus)
		s.WithFeatureCodes([]snapshot.FeatureCode{{Code: "*88", Action: ActionIVR, Argument: menus[0].Name}})
	}
	pbx := startPBX(t, threeDevices(), shortRing(2*time.Second), withMedia(false),
		withFeatures(feats), withVoicemail(newVMStore(), objs))
	return pbx, objs
}

// countPhoneRTP points the phone's media at a fresh port and counts the
// datagrams the PBX sends there (set before the phone dials).
func countPhoneRTP(t *testing.T, p *phone) *atomic.Int64 {
	t.Helper()
	conn := rtpSocket(t, p)
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
	return &n
}

func registerABC(t *testing.T, pbx *testPBX) (a, b, c *phone) {
	t.Helper()
	a = newPhone(t, pbx, "a1", "pa")
	b = newPhone(t, pbx, "b1", "pb1")
	c = newPhone(t, pbx, "c1", "pc")
	for _, p := range []*phone{a, b, c} {
		p.register(t)
	}
	return a, b, c
}

func lastCDRFor(t *testing.T, pbx *testPBX, dialled string) cdr.Record {
	t.Helper()
	for i := 0; i < 6; i++ {
		r := pbx.nextCDR(t)
		if r.Destination == dialled {
			return r
		}
	}
	t.Fatalf("no CDR for %s", dialled)
	return cdr.Record{}
}

// TestIVRDigitToExtension: the menu answers, a SIP INFO digit routes the
// call to the option's extension and the caller is bridged to it through the
// relay. It fails if the digit is not taken from the INFO, the phone is not
// rung, the call is not bridged (BYE from the caller must reach it), or the
// CDR loses the menu's number or the IVR trace.
func TestIVRDigitToExtension(t *testing.T) {
	pbx, _ := ivrPBX(t, 1, ivrMenu("main", "800", map[string]snapshot.IVRDest{"1": ext("300"), "2": ext("200")}))
	a, _, c := registerABC(t, pbx)
	c.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "800"))
	if r.err != nil {
		t.Fatalf("call to the menu: %v", r.err)
	}
	if res := r.dcs.InviteResponse; res == nil || res.StatusCode != 200 {
		t.Fatal("the menu did not answer")
	}
	noReq(t, c.invites, 300*time.Millisecond, "nothing rings before a digit")
	var seq = r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, a, r.dcs, "1", &seq)
	inv := waitReq(t, c.invites, "INVITE to the option's extension")
	sdp := anchoredSDP(t, pbx, inv.Body())
	if sdp.Port == 0 {
		t.Fatal("the leg's offer is not the relay's")
	}
	eventually(t, "bridged call published", func() bool {
		for _, lc := range liveCalls(t, pbx) {
			if lc.To == "800" && lc.State == "connected" {
				return true
			}
		}
		return false
	})
	eventually(t, "the menu connected the call", func() bool {
		return pbx.metric(t, "hello_ivr_total", map[string]string{"event": "connected"}) == 1
	})
	if err := r.dcs.Bye(t.Context()); err != nil {
		t.Fatalf("caller BYE: %v", err)
	}
	waitReq(t, c.byes, "BYE reaches the bridged phone")
	rec := lastCDRFor(t, pbx, "800")
	if rec.FinalStatus != 200 || rec.RewrittenDestination != "300" {
		t.Fatalf("CDR status %d rewritten %q", rec.FinalStatus, rec.RewrittenDestination)
	}
	if tt := traceText(rec.Trace); !strings.Contains(tt, "IVR menu main entered") || !strings.Contains(tt, "option 1") {
		t.Fatalf("trace lacks the IVR steps:\n%s", tt)
	}
	if v := pbx.metric(t, "hello_ivr_total", map[string]string{"event": "option"}); v != 1 {
		t.Fatalf("hello_ivr_total{option} = %v", v)
	}
}

// TestIVRBargeIn: a key press stops the greeting at once. It fails if the
// greeting keeps playing to its end (the audio is 5 seconds, 250 frames).
func TestIVRBargeIn(t *testing.T) {
	pbx, _ := ivrPBX(t, 5, ivrMenu("main", "800", map[string]snapshot.IVRDest{"1": ext("300")}))
	a, _, c := registerABC(t, pbx)
	c.setCallee(answerAfter(nil))
	frames := countPhoneRTP(t, a)
	r := waitCall(t, dial(t.Context(), a, "800"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	eventually(t, "greeting playing", func() bool { return frames.Load() > 5 })
	var seq = r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, a, r.dcs, "1", &seq)
	waitReq(t, c.invites, "INVITE after the interrupting key press")
	time.Sleep(500 * time.Millisecond)
	if n := frames.Load(); n > 100 {
		t.Fatalf("%d frames played: the greeting was not interrupted", n)
	}
	_ = r.dcs.Bye(t.Context())
}

// TestIVRTimeoutRetriesThenDestination: silence repeats the menu with the
// timeout prompt, then the timeout destination takes the call. It fails if
// the retry count is off (1 retry: the menu plays twice), the timeout prompt
// is not fetched, or the destination does not apply.
func TestIVRTimeoutRetriesThenDestination(t *testing.T) {
	m := ivrMenu("main", "800", map[string]snapshot.IVRDest{"1": ext("300")})
	m.TimeoutPrompt = "late"
	m.Timeout = snapshot.IVRDest{Kind: snapshot.IVRAnnouncement, Target: "bad"}
	pbx, objs := ivrPBX(t, 1, m)
	a, _, _ := registerABC(t, pbx)
	r := waitCall(t, dial(t.Context(), a, "800"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, a.byes, "the timeout destination plays and hangs up")
	if v := pbx.metric(t, "hello_ivr_total", map[string]string{"event": "timeout"}); v != 2 {
		t.Fatalf("timeouts = %v, want 2 (first prompt and the retry)", v)
	}
	if !slices.Contains(objs.snapshotGets(), "ann/bad.wav") {
		t.Fatal("neither the timeout prompt nor the announcement destination was fetched")
	}
	rec := lastCDRFor(t, pbx, "800")
	if !strings.Contains(traceText(rec.Trace), "timeout after 1 retries -> announcement bad") {
		t.Fatalf("trace:\n%s", traceText(rec.Trace))
	}
}

// TestIVRInvalidChoice: a digit with no option replays the invalid prompt
// and, when the retries are used up, takes the invalid destination.
func TestIVRInvalidChoice(t *testing.T) {
	m := ivrMenu("main", "800", map[string]snapshot.IVRDest{"1": ext("300")})
	m.InvalidPrompt = "bad"
	m.Retries = 1
	m.Invalid = ext("200")
	pbx, objs := ivrPBX(t, 1, m)
	a, b, _ := registerABC(t, pbx)
	b.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "800"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	var seq = r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, a, r.dcs, "7", &seq)
	eventually(t, "invalid prompt fetched", func() bool { return slices.Contains(objs.snapshotGets(), "ann/bad.wav") })
	noReq(t, b.invites, 300*time.Millisecond, "one invalid choice is retried, not routed")
	sendDigits(t, a, r.dcs, "8", &seq)
	waitReq(t, b.invites, "the invalid destination rings")
	if v := pbx.metric(t, "hello_ivr_total", map[string]string{"event": "invalid"}); v != 2 {
		t.Fatalf("invalid = %v, want 2", v)
	}
	_ = r.dcs.Bye(t.Context())
}

// TestIVRDirectDial: with direct dialling on, digits that are not an option
// dial an extension (multi-digit, ended by the inter-digit timeout or #).
func TestIVRDirectDial(t *testing.T) {
	m := ivrMenu("main", "800", map[string]snapshot.IVRDest{"1": ext("300")})
	m.AllowDirectDial = true
	pbx, _ := ivrPBX(t, 1, m)
	a, b, _ := registerABC(t, pbx)
	b.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "800"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	var seq = r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, a, r.dcs, "200", &seq)
	waitReq(t, b.invites, "the dialled extension rings")
	_ = r.dcs.Bye(t.Context())
	rec := lastCDRFor(t, pbx, "800")
	if !strings.Contains(traceText(rec.Trace), "direct dial 200") {
		t.Fatalf("trace:\n%s", traceText(rec.Trace))
	}
}

// TestIVRNestedMenu: an option can lead to another menu; a menu that sends
// its timeout to the menu that sent it there is refused at run time rather
// than looping.
func TestIVRNestedMenu(t *testing.T) {
	top := ivrMenu("main", "800", map[string]snapshot.IVRDest{"2": {Kind: snapshot.IVRMenuKind, Target: "sub"}})
	sub := ivrMenu("sub", "", map[string]snapshot.IVRDest{"1": ext("300")})
	sub.ID = 2
	pbx, _ := ivrPBX(t, 1, top, sub)
	a, _, c := registerABC(t, pbx)
	c.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "800"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	var seq = r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, a, r.dcs, "2", &seq)
	eventually(t, "sub menu entered", func() bool {
		return pbx.metric(t, "hello_ivr_total", map[string]string{"event": "entered"}) == 2
	})
	sendDigits(t, a, r.dcs, "1", &seq)
	waitReq(t, c.invites, "the nested option rings")
	_ = r.dcs.Bye(t.Context())
}

func TestIVRAutomaticLoopStopped(t *testing.T) {
	x := ivrMenu("x", "800", nil)
	x.Retries = 0
	x.Timeout = snapshot.IVRDest{Kind: snapshot.IVRMenuKind, Target: "y"}
	y := ivrMenu("y", "", nil)
	y.ID, y.Retries = 2, 0
	y.Timeout = snapshot.IVRDest{Kind: snapshot.IVRMenuKind, Target: "x"}
	pbx, _ := ivrPBX(t, 1, x, y)
	a, _, _ := registerABC(t, pbx)
	r := waitCall(t, dial(t.Context(), a, "800"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, a.byes, "the loop through automatic exits ends the call")
	rec := lastCDRFor(t, pbx, "800")
	if !strings.Contains(traceText(rec.Trace), "loop through automatic exits") {
		t.Fatalf("trace:\n%s", traceText(rec.Trace))
	}
}

// TestIVRFailedDestinationReplays: a destination nobody answers returns the
// caller to the menu instead of ending the call.
func TestIVRFailedDestinationReplays(t *testing.T) {
	pbx, _ := ivrPBX(t, 1, ivrMenu("main", "800", map[string]snapshot.IVRDest{"1": ext("599")}))
	a, _, _ := registerABC(t, pbx)
	r := waitCall(t, dial(t.Context(), a, "800"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	var seq = r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, a, r.dcs, "1", &seq)
	eventually(t, "menu entered again", func() bool {
		return pbx.metric(t, "hello_ivr_total", map[string]string{"event": "entered"}) == 2
	})
	_ = r.dcs.Bye(t.Context())
}

// TestIVRFeatureCode: *88 runs the menu named by the code's argument.
func TestIVRFeatureCode(t *testing.T) {
	pbx, _ := ivrPBX(t, 1, ivrMenu("main", "", map[string]snapshot.IVRDest{"1": ext("300")}))
	a, _, c := registerABC(t, pbx)
	c.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "*88"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	var seq = r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, a, r.dcs, "1", &seq)
	waitReq(t, c.invites, "the feature code's menu routes the digit")
	_ = r.dcs.Bye(t.Context())
}

// TestIVRRingGroupFailure: a ring group whose members do not answer falls
// to the IVR menu named as its failure destination.
func TestIVRRingGroupFailure(t *testing.T) {
	pbx, _ := ivrPBX(t, 1, ivrMenu("main", "", map[string]snapshot.IVRDest{"1": ext("300")}))
	cur := pbx.snaps.p.Load()
	cur.WithRingGroups([]snapshot.RingGroup{{ID: 1, Name: "700", Strategy: "ring-all", RingTimeout: 1, MemberDelay: 1,
		FailureKind: "ivr", FailureTarget: "main"}}, [][]snapshot.RingGroupMember{{{ExtensionID: 9, Extension: "200", Position: 1}}})
	a, b, c := registerABC(t, pbx)
	b.setCallee(ringForever())
	c.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "700"))
	if r.err != nil {
		t.Fatalf("the group's failure menu did not answer: %v", r.err)
	}
	var seq = r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, a, r.dcs, "1", &seq)
	waitReq(t, c.invites, "the menu routes after the group failed")
	eventually(t, "bridged after the group failed", func() bool {
		return pbx.metric(t, "hello_ivr_total", map[string]string{"event": "connected"}) == 1
	})
	_ = r.dcs.Bye(t.Context())
	waitReq(t, c.byes, "BYE reaches the phone the menu connected")
	rec := lastCDRFor(t, pbx, "700")
	if !strings.Contains(traceText(rec.Trace), "caller connected to 300") {
		t.Fatalf("trace:\n%s", traceText(rec.Trace))
	}
}

// TestIVRCallerHangsUpInMenu: a BYE during the greeting ends the call and
// records it; nothing keeps running.
func TestIVRCallerHangsUpInMenu(t *testing.T) {
	pbx, _ := ivrPBX(t, 5, ivrMenu("main", "800", nil))
	a, _, _ := registerABC(t, pbx)
	r := waitCall(t, dial(t.Context(), a, "800"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	if err := r.dcs.Bye(t.Context()); err != nil {
		t.Fatal(err)
	}
	rec := lastCDRFor(t, pbx, "800")
	if rec.TerminationSide != cdr.SideCaller {
		t.Fatalf("side %q status %d trace:\n%s", rec.TerminationSide, rec.FinalStatus, traceText(rec.Trace))
	}
	eventually(t, "no live calls", func() bool { return len(liveCalls(t, pbx)) == 0 })
}

// TestIVRClassify covers the digit logic: options fire at once unless an
// extension starts with the digit and direct dialling is on.
func TestIVRClassify(t *testing.T) {
	snap := snapshot.New(1, testDomain, threeDevices())
	m := ivrMenu("main", "800", map[string]snapshot.IVRDest{"1": ext("100"), "2": ext("200"), "#": ext("300")})
	direct := m
	direct.AllowDirectDial = true
	cases := []struct {
		m          snapshot.IVRMenu
		digits     string
		terminated bool
		want       int
	}{
		{m, "1", false, ivrVerdictOption},
		{m, "#", false, ivrVerdictOption},
		{m, "5", false, ivrVerdictInvalid},
		{m, "200", false, ivrVerdictInvalid},
		{direct, "2", false, ivrMore}, // 200 and 210 start with 2, and 2 is an option
		{direct, "2", true, ivrVerdictOption},
		{direct, "20", false, ivrMore},
		{direct, "200", false, ivrVerdictDirect},
		{direct, "21", false, ivrMore},
		{direct, "21", true, ivrVerdictInvalid},
		{direct, "5", false, ivrVerdictInvalid},
		{direct, "3", false, ivrMore}, // 300 starts with 3
		{direct, "2*", false, ivrVerdictInvalid},
	}
	for _, tc := range cases {
		got, _ := ivrClassify(snap, tc.m, tc.digits, tc.terminated)
		if got != tc.want {
			t.Errorf("classify(%q, terminated=%v, direct=%v) = %d, want %d", tc.digits, tc.terminated, tc.m.AllowDirectDial, got, tc.want)
		}
	}
}

// TestIVRPagingDestination: an option can page a group. The caller's audio
// goes through the relay into the page's fan-out and reaches the member that
// answered; the call is a page in the CDR. It fails if the member is not
// auto-answer paged, hears nothing, or the CDR kind is not page.
func TestIVRPagingDestination(t *testing.T) {
	objs := &fakeObjects{}
	_ = objs.Put(context.Background(), "ann/greet.wav", media.EncodeWAV(make([]byte, 16000), 8000))
	m := ivrMenu("main", "800", map[string]snapshot.IVRDest{"5": {Kind: snapshot.IVRPaging, Target: "*60"}})
	feats := func(s *snapshot.Snapshot) {
		s.WithAnnouncements(map[string]string{"greet": "ann/greet.wav"})
		s.WithIVRMenus([]snapshot.IVRMenu{m})
		s.WithPagingGroups([]snapshot.PagingGroup{{ID: 1, Name: "kitchen", Code: "*60", Priority: "normal", Enabled: true, Members: []string{"200"}}})
		s.WithExtensionData(pageExts())
	}
	pbx := startPBX(t, pageDevices(), withMedia(false), withFeatures(feats), withVoicemail(newVMStore(), objs))
	ph := registerAll(t, pbx, "a1", "b1")
	a, b := ph["a1"], ph["b1"]
	aRTP, bRTP := rtpSocket(t, a), rtpSocket(t, b)
	b.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "800"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	callerSDP, err := media.ParseAudioSDP(r.dcs.InviteResponse.Body())
	if err != nil {
		t.Fatal(err)
	}
	var seq = r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, a, r.dcs, "5", &seq)
	bi := waitReq(t, b.invites, "page INVITE to 200")
	expectAutoAnswer(t, "200", bi)
	payload := []byte("speak-to-all")
	to := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: callerSDP.Port}
	heard := false
	for i, deadline := uint16(1), time.Now().Add(5*time.Second); !heard && time.Now().Before(deadline); i++ {
		_, _ = aRTP.WriteToUDP(media.BuildRTP(0, i, uint32(i)*160, 99, false, payload), to) //nolint:gosec // test
		_ = bRTP.SetReadDeadline(time.Now().Add(40 * time.Millisecond))
		buf := make([]byte, 2048)
		if n, _, err := bRTP.ReadFromUDP(buf); err == nil {
			if pkt, ok := media.ParseRTP(buf[:n]); ok && string(pkt.Payload) == string(payload) {
				heard = true
			}
		}
	}
	if !heard {
		t.Fatal("the paged member did not hear the caller")
	}
	if err := r.dcs.Bye(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitReq(t, b.byes, "the page member leg ends with the caller")
	rec := lastCDRFor(t, pbx, "800")
	if rec.Kind != cdr.KindPage || len(rec.PageLegs) != 1 {
		t.Fatalf("CDR kind %q, page legs %v", rec.Kind, rec.PageLegs)
	}
}

// TestIVRVoicemailDestination: an option can leave a message in a box. It
// fails if the box is not looked up or the call is not ended after the take.
func TestIVRVoicemailDestination(t *testing.T) {
	m := ivrMenu("main", "800", map[string]snapshot.IVRDest{"3": {Kind: snapshot.IVRVoicemail, Target: "200"}})
	objs := &fakeObjects{}
	_ = objs.Put(context.Background(), "ann/greet.wav", media.EncodeWAV(make([]byte, 16000), 8000))
	vm := newVMStore()
	vm.boxes["200"] = VoicemailBox{ID: 5, Extension: "200"}
	feats := func(s *snapshot.Snapshot) {
		s.WithAnnouncements(map[string]string{"greet": "ann/greet.wav"})
		s.WithIVRMenus([]snapshot.IVRMenu{m})
		s.WithExtensionData([]snapshot.Extension{{Number: "200", VoicemailEnabled: true, VoicemailBoxID: 5}})
	}
	pbx := startPBX(t, threeDevices(), withMedia(false), withFeatures(feats), withVoicemail(vm, objs))
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	r := waitCall(t, dial(t.Context(), a, "800"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	var seq = r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, a, r.dcs, "3", &seq)
	eventually(t, "the box is looked up", func() bool { vm.mu.Lock(); defer vm.mu.Unlock(); return vm.lookups > 0 })
	sendDigits(t, a, r.dcs, "#", &seq) // ends the take (nothing recorded: discarded)
	waitReq(t, a.byes, "the call ends after the message")
}

// TestIVRInboundRoute: an inbound route naming an IVR menu answers the
// carrier's call with the menu, and a digit routes it to an extension. It
// fails if the engine's IVR decision is not dispatched to the menu.
func TestIVRInboundRoute(t *testing.T) {
	cr := newCarrier(t, "acct", "pw")
	tr := cr.trunk(1, "carrier-in", "ip")
	tr.SourceCIDRs = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	cfg := routing.Config{
		Trunks: []routing.Trunk{tr},
		Inbound: []routing.InboundRoute{{ID: 1, Position: 1, Name: "Main DID", DIDKind: "exact", DID: "+97140000100",
			DestinationKind: "ivr", Destination: "main", Enabled: true}},
		Extensions: map[string]string{"100": "", "200": ""},
		IVRMenus:   map[string]bool{"main": true},
	}
	objs := &fakeObjects{}
	_ = objs.Put(context.Background(), "ann/greet.wav", media.EncodeWAV(make([]byte, 16000), 8000))
	m := ivrMenu("main", "", map[string]snapshot.IVRDest{"1": ext("100")})
	pbx := startPBX(t, callerDevices(), trunkCfg(newFakeTrunkState()), withEngine(t, cfg), withMedia(false),
		withVoicemail(newVMStore(), objs), withFeatures(func(s *snapshot.Snapshot) {
			s.WithAnnouncements(map[string]string{"greet": "ann/greet.wav"})
			s.WithIVRMenus([]snapshot.IVRMenu{m})
		}))
	callee := newPhone(t, pbx, "a1", "pa")
	callee.register(t)
	callee.setCallee(answerAfter(nil))
	from := newPhone(t, pbx, "carrier", "")
	dcs, err := from.dua.WriteInvite(t.Context(), carrierInvite(from, pbx, "+97140000100"))
	if err != nil {
		t.Fatal(err)
	}
	if err := dcs.WaitAnswer(t.Context(), sipgo.AnswerOptions{}); err != nil {
		t.Fatalf("inbound call to the menu: %v", err)
	}
	_ = dcs.Ack(t.Context())
	noReq(t, callee.invites, 300*time.Millisecond, "nothing rings before a digit")
	var seq = dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, from, dcs, "1", &seq)
	waitReq(t, callee.invites, "INVITE to the option's extension")
	eventually(t, "the menu connected the call", func() bool {
		return pbx.metric(t, "hello_ivr_total", map[string]string{"event": "connected"}) == 1
	})
	hangup(t, dcs)
	cd := pbx.nextCDR(t)
	if cd.Direction != "inbound" || cd.Route != "Main DID" || cd.RewrittenDestination != "100" {
		t.Fatalf("CDR = %+v", cd)
	}
}

// TestInboundRingGroupDestination: an inbound route of kind ring_group (the
// unified destinations, migration 00041) rings the named group, whatever its
// name. It fails if the engine's decision is not carried out by the shared
// destination dispatcher.
func TestInboundRingGroupDestination(t *testing.T) {
	cr := newCarrier(t, "acct", "pw")
	tr := cr.trunk(1, "carrier-in", "ip")
	tr.SourceCIDRs = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	cfg := routing.Config{
		Trunks: []routing.Trunk{tr},
		Inbound: []routing.InboundRoute{{ID: 1, Position: 1, Name: "Sales DID", DIDKind: "exact", DID: "+97140000100",
			DestinationKind: "ring_group", Destination: "sales", Enabled: true}},
		Extensions: map[string]string{"100": ""},
		Named:      map[string]bool{"ring_group:sales": true},
	}
	pbx := startPBX(t, callerDevices(), trunkCfg(newFakeTrunkState()), withEngine(t, cfg), withMedia(false),
		withFeatures(func(s *snapshot.Snapshot) {
			s.WithRingGroups([]snapshot.RingGroup{{ID: 1, Name: "sales", Strategy: "ring-all", RingTimeout: 5, MemberDelay: 1}},
				[][]snapshot.RingGroupMember{{{ExtensionID: 1, Extension: "100", Position: 1}}})
		}))
	callee := newPhone(t, pbx, "a1", "pa")
	callee.register(t)
	callee.setCallee(answerAfter(nil))
	from := newPhone(t, pbx, "carrier", "")
	dcs, err := from.dua.WriteInvite(t.Context(), carrierInvite(from, pbx, "+97140000100"))
	if err != nil {
		t.Fatal(err)
	}
	if err := dcs.WaitAnswer(t.Context(), sipgo.AnswerOptions{}); err != nil {
		t.Fatalf("inbound call to the group: %v", err)
	}
	_ = dcs.Ack(t.Context())
	waitReq(t, callee.invites, "INVITE to the group's member")
	hangup(t, dcs)
	cd := pbx.nextCDR(t)
	if cd.Direction != "inbound" || cd.Route != "Sales DID" {
		t.Fatalf("CDR = %+v", cd)
	}
}
