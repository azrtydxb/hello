package sip

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// pageDevices: a1 (100) is the caller, b1 (200) and c1 (300) are page
// members that answer, d1 (210) is on DND, e1 (400) will be in a call, f1
// (500) never registers, g1 (600) is a second caller.
func pageDevices() []snapshot.Device {
	return []snapshot.Device{
		dev(1, "a1", "100", "pa"), dev(2, "b1", "200", "pb"), dev(3, "c1", "300", "pc"),
		dev(4, "d1", "210", "pd"), dev(5, "e1", "400", "pe"), dev(6, "f1", "500", "pf"),
		dev(7, "g1", "600", "pg"),
	}
}

func pageGroup(prio string) snapshot.PagingGroup {
	return snapshot.PagingGroup{
		ID: 1, Name: "kitchen", Code: "*60", Priority: prio, Enabled: true,
		Members: []string{"200", "300", "210", "400", "500"},
	}
}

// withPaging installs a paging group, the intercom feature code and the
// extensions' paging-relevant flags.
func withPaging(g snapshot.PagingGroup, exts ...snapshot.Extension) pbxOpt {
	return withFeatures(func(s *snapshot.Snapshot) {
		s.WithPagingGroups([]snapshot.PagingGroup{g})
		s.WithFeatureCodes([]snapshot.FeatureCode{{Code: "*80", Action: ActionIntercom}})
		s.WithExtensionData(exts)
	})
}

func pageExts() []snapshot.Extension {
	return []snapshot.Extension{
		{Number: "100", Name: "Ext 100", AllowIntercom: true},
		{Number: "200", Name: "Ext 200", AllowIntercom: true},
		{Number: "300", Name: "Ext 300", AllowIntercom: true},
		{Number: "210", Name: "Ext 210", DND: true, AllowIntercom: true},
		{Number: "400", Name: "Ext 400", AllowIntercom: true},
		{Number: "500", Name: "Ext 500", AllowIntercom: true},
		{Number: "600", Name: "Ext 600", AllowIntercom: true},
	}
}

// mediaPort is the RTP port a test phone's SDP names.
func mediaPort(t *testing.T, p *phone) int {
	t.Helper()
	sdp, err := media.ParseAudioSDP([]byte(p.sdp))
	if err != nil {
		t.Fatal(err)
	}
	return sdp.Port
}

// rtpSocket binds a free port and points the phone's SDP at it, so the test
// sees the RTP the phone would receive (set before the phone answers).
func rtpSocket(t *testing.T, p *phone) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	old := "m=audio " + strconv.Itoa(mediaPort(t, p)) + " "
	p.sdp = strings.Replace(p.sdp, old, "m=audio "+strconv.Itoa(c.LocalAddr().(*net.UDPAddr).Port)+" ", 1)
	return c
}

// expectAutoAnswer fails unless the INVITE carries the auto-answer headers.
func expectAutoAnswer(t *testing.T, who string, req *sip.Request) {
	t.Helper()
	if h := req.GetHeader("Call-Info"); h == nil || !strings.Contains(h.Value(), "answer-after=0") {
		t.Errorf("%s: Call-Info = %v, want answer-after=0", who, h)
	}
	if h := req.GetHeader("Alert-Info"); h == nil || h.Value() != "auto-answer" {
		t.Errorf("%s: Alert-Info = %v, want auto-answer", who, h)
	}
	if req.GetHeader("P-Asserted-Identity") == nil {
		t.Errorf("%s: no P-Asserted-Identity", who)
	}
}

func expectNoAutoAnswer(t *testing.T, who string, req *sip.Request) {
	t.Helper()
	for _, h := range []string{"Call-Info", "Alert-Info", "Answer-After"} {
		if v := req.GetHeader(h); v != nil {
			t.Errorf("%s: carries %s: %s", who, h, v.Value())
		}
	}
}

// registerAll starts and registers a phone per user name of pageDevices that
// is in names, returning them by user.
func registerAll(t *testing.T, pbx *testPBX, names ...string) map[string]*phone {
	t.Helper()
	pw := map[string]string{"a1": "pa", "b1": "pb", "c1": "pc", "d1": "pd", "e1": "pe", "f1": "pf", "g1": "pg"}
	out := map[string]*phone{}
	for _, n := range names {
		p := newPhone(t, pbx, n, pw[n])
		p.register(t)
		out[n] = p
	}
	return out
}

// TestPagingGroup fails if a page does not INVITE every registered contact
// of every non-DND, non-busy member with auto-answer headers and a sendonly
// PCMU-only offer, if a busy or DND member or the caller itself is rung, if
// the caller is answered before any member answers, if the caller's audio
// does not reach every member, if a member's audio reaches the caller, if a
// caller hang-up leaves a member leg up, or if the CDR lacks kind page and
// a result per member.
func TestPagingGroup(t *testing.T) {
	pbx := startPBX(t, pageDevices(), withPaging(pageGroup("normal"), pageExts()...))
	ph := registerAll(t, pbx, "a1", "b1", "c1", "d1", "e1")
	a, b, c, d, e := ph["a1"], ph["b1"], ph["c1"], ph["d1"], ph["e1"]
	if err := pbx.fake.PutCall(t.Context(), livestate.Call{ID: "busy", From: "400", To: "900", State: "connected"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	aRTP, bRTP, cRTP := rtpSocket(t, a), rtpSocket(t, b), rtpSocket(t, c)

	release := make(chan struct{})
	b.setCallee(answerAfter(release))
	c.setCallee(answerAfter(release))
	res := dial(t.Context(), a, "*60")
	bi := waitReq(t, b.invites, "page INVITE to 200")
	ci := waitReq(t, c.invites, "page INVITE to 300")
	expectAutoAnswer(t, "200", bi)
	expectAutoAnswer(t, "300", ci)
	for who, req := range map[string]*sip.Request{"200": bi, "300": ci} {
		body := string(req.Body())
		if media.SDPDirection(req.Body()) != "sendonly" || !strings.Contains(body, "RTP/AVP 0\r\n") || strings.Contains(body, "101") {
			t.Errorf("%s: offer %q, want sendonly with PCMU only", who, body)
		}
	}
	noReq(t, d.invites, 100*time.Millisecond, "INVITE to the DND member")
	noReq(t, e.invites, 100*time.Millisecond, "INVITE to the member in a call")
	noReq(t, a.invites, 100*time.Millisecond, "INVITE to the caller itself")

	select {
	case <-res:
		t.Fatal("the caller was answered before any member answered")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	r := waitCall(t, res)
	if r.err != nil {
		t.Fatalf("page: %v", r.err)
	}
	ans := r.dcs.InviteResponse.Body()
	if media.SDPDirection(ans) != "recvonly" {
		t.Fatalf("the caller's answer is %q, want recvonly", ans)
	}
	callerSDP, err := media.ParseAudioSDP(ans)
	if err != nil {
		t.Fatal(err)
	}
	memberSDP, err := media.ParseAudioSDP(bi.Body())
	if err != nil {
		t.Fatal(err)
	}

	// The caller's audio reaches both members (retrying while the second
	// member's answer is still being processed).
	payload := []byte("speak-to-all")
	to := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: callerSDP.Port}
	got := map[string]bool{}
	deadline := time.Now().Add(5 * time.Second)
	for seq := uint16(1); len(got) < 2 && time.Now().Before(deadline); seq++ {
		_, _ = aRTP.WriteToUDP(media.BuildRTP(0, seq, uint32(seq)*160, 99, false, payload), to) //nolint:gosec // test
		for name, conn := range map[string]*net.UDPConn{"200": bRTP, "300": cRTP} {
			_ = conn.SetReadDeadline(time.Now().Add(40 * time.Millisecond))
			buf := make([]byte, 2048)
			if n, _, err := conn.ReadFromUDP(buf); err == nil {
				if pkt, ok := media.ParseRTP(buf[:n]); ok && string(pkt.Payload) == string(payload) {
					got[name] = true
				}
			}
		}
	}
	if len(got) != 2 {
		t.Fatalf("members that heard the caller: %v, want both", got)
	}

	// One-way: whatever a member sends back never reaches the caller.
	back := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: memberSDP.Port}
	_, _ = bRTP.WriteToUDP(media.BuildRTP(0, 1, 160, 7, false, []byte("from-member")), back)
	_ = aRTP.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, _, err := aRTP.ReadFromUDP(make([]byte, 2048)); err == nil {
		t.Fatalf("the caller received %d bytes of a member's audio", n)
	}

	// The caller hangs up: every member leg ends.
	hangup(t, r.dcs)
	waitReq(t, b.byes, "BYE to 200")
	waitReq(t, c.byes, "BYE to 300")
	cd := pbx.nextCDR(t)
	if cd.Kind != cdr.KindPage || cd.FinalStatus != 200 || cd.TerminationSide != cdr.SideCaller {
		t.Fatalf("CDR = kind %q status %d side %q, want page 200 caller", cd.Kind, cd.FinalStatus, cd.TerminationSide)
	}
	tr := traceText(cd.Trace)
	for _, want := range []string{
		"Page leg 200 (b1): answered", "Page leg 300 (c1): answered",
		"Page member 210 skipped: dnd", "Page member 400 skipped: busy", "Page member 500 skipped: no_registration",
	} {
		if !strings.Contains(tr, want) {
			t.Errorf("trace lacks %q:\n%s", want, tr)
		}
	}
	if v := pbx.metric(t, "hello_paging_total", map[string]string{"result": "answered"}); v != 1 {
		t.Errorf("hello_paging_total{answered} = %v", v)
	}
	if v := pbx.metric(t, "hello_paging_legs_total", map[string]string{"result": "dnd"}); v != 1 {
		t.Errorf("hello_paging_legs_total{dnd} = %v", v)
	}
}

// TestPagingGroupRules fails if a disabled group or a caller outside the
// allowed list pages, if a second caller gets through on a busy normal
// page, if an emergency page does not preempt it, if a page outlives its
// maximum, if a page nobody answers is not busy for the caller, if a DND
// member is spared by an emergency page or woken by a normal one, if a page
// beyond the leg limit is placed, or if a trunk caller can trigger one.
func TestPagingGroupRules(t *testing.T) {
	denied := func(t *testing.T, pbx *testPBX, who *phone, want int) {
		t.Helper()
		r := waitCall(t, dial(t.Context(), who, "*60"))
		if r.err == nil || !strings.Contains(r.err.Error(), strconv.Itoa(want)) {
			t.Fatalf("page = %v, want %d", r.err, want)
		}
		cd := pbx.nextCDR(t)
		if cd.Kind != cdr.KindPage || cd.FinalStatus != want {
			t.Fatalf("CDR = kind %q status %d, want page %d", cd.Kind, cd.FinalStatus, want)
		}
	}

	t.Run("disabled", func(t *testing.T) {
		g := pageGroup("normal")
		g.Enabled = false
		pbx := startPBX(t, pageDevices(), withPaging(g, pageExts()...))
		ph := registerAll(t, pbx, "a1", "b1")
		denied(t, pbx, ph["a1"], 403)
		noReq(t, ph["b1"].invites, 100*time.Millisecond, "INVITE for a disabled group")
	})

	t.Run("allowed callers", func(t *testing.T) {
		g := pageGroup("normal")
		g.Callers = []string{"600"}
		pbx := startPBX(t, pageDevices(), withPaging(g, pageExts()...))
		ph := registerAll(t, pbx, "a1", "b1", "g1")
		denied(t, pbx, ph["a1"], 403)
		r := waitCall(t, dial(t.Context(), ph["g1"], "*60"))
		if r.err != nil {
			t.Fatalf("allowed caller: %v", r.err)
		}
		hangup(t, r.dcs)
		pbx.nextCDR(t)
	})

	t.Run("busy and preemption", func(t *testing.T) {
		pbx := startPBX(t, pageDevices(), withPaging(pageGroup("normal"), pageExts()...))
		ph := registerAll(t, pbx, "a1", "b1", "g1")
		first := waitCall(t, dial(t.Context(), ph["a1"], "*60"))
		if first.err != nil {
			t.Fatalf("first page: %v", first.err)
		}
		waitReq(t, ph["b1"].invites, "first page INVITE")
		denied(t, pbx, ph["g1"], 486) // a second caller on a normal page
		// The group becomes an emergency one: its page preempts the normal one.
		snap := snapshot.New(2, testDomain, pageDevices()).WithExtensions(bareExtension).
			WithExtensionData(pageExts()).WithPagingGroups([]snapshot.PagingGroup{pageGroup("emergency")}).
			WithFeatureCodes([]snapshot.FeatureCode{{Code: "*80", Action: ActionIntercom}})
		pbx.snaps.p.Store(snap)
		second := waitCall(t, dial(t.Context(), ph["g1"], "*60"))
		if second.err != nil {
			t.Fatalf("emergency page: %v", second.err)
		}
		waitReq(t, ph["b1"].byes, "BYE to the member of the preempted page")
		waitReq(t, ph["a1"].byes, "BYE to the preempted caller")
		first1 := pbx.nextCDR(t)
		if first1.FinalStatus != 200 || !strings.Contains(first1.FailureReason, "preempted") {
			t.Fatalf("preempted CDR = %d %q", first1.FinalStatus, first1.FailureReason)
		}
		hangup(t, second.dcs)
		pbx.nextCDR(t)
	})

	t.Run("page maximum", func(t *testing.T) {
		pbx := startPBX(t, pageDevices(), withPaging(pageGroup("normal"), pageExts()...),
			func(c *Config, _ *Deps) { c.PageMaxDuration = 400 * time.Millisecond })
		ph := registerAll(t, pbx, "a1", "b1")
		r := waitCall(t, dial(t.Context(), ph["a1"], "*60"))
		if r.err != nil {
			t.Fatalf("page: %v", r.err)
		}
		waitReq(t, ph["b1"].byes, "BYE to the member at the page maximum")
		waitReq(t, ph["a1"].byes, "BYE to the caller at the page maximum")
		cd := pbx.nextCDR(t)
		if cd.TerminationSide != cdr.SideSystem || cd.FailureReason != "max duration" {
			t.Fatalf("CDR = side %q reason %q", cd.TerminationSide, cd.FailureReason)
		}
	})

	t.Run("nobody answers", func(t *testing.T) {
		pbx := startPBX(t, pageDevices(), withPaging(pageGroup("normal"), pageExts()...),
			func(c *Config, _ *Deps) { c.PageAnswerTimeout = 300 * time.Millisecond })
		ph := registerAll(t, pbx, "a1", "b1")
		ph["b1"].setCallee(ringForever())
		denied(t, pbx, ph["a1"], 486)
		waitReq(t, ph["b1"].cancels, "CANCEL to the ringing member")
	})

	t.Run("DND", func(t *testing.T) {
		pbx := startPBX(t, pageDevices(), withPaging(pageGroup("normal"), pageExts()...))
		ph := registerAll(t, pbx, "a1", "d1")
		denied(t, pbx, ph["a1"], 486) // the only registered member is on DND
		noReq(t, ph["d1"].invites, 100*time.Millisecond, "INVITE to a DND member of a normal page")

		pbx = startPBX(t, pageDevices(), withPaging(pageGroup("emergency"), pageExts()...))
		ph = registerAll(t, pbx, "a1", "d1")
		r := waitCall(t, dial(t.Context(), ph["a1"], "*60"))
		if r.err != nil {
			t.Fatalf("emergency page: %v", r.err)
		}
		waitReq(t, ph["d1"].invites, "emergency page INVITE to a DND member")
		hangup(t, r.dcs)
		pbx.nextCDR(t)
	})

	t.Run("leg limit", func(t *testing.T) {
		pbx := startPBX(t, pageDevices(), withPaging(pageGroup("normal"), pageExts()...),
			func(c *Config, _ *Deps) { c.PageFanout = 1 })
		ph := registerAll(t, pbx, "a1", "b1", "c1")
		denied(t, pbx, ph["a1"], 486)
		noReq(t, ph["b1"].invites, 100*time.Millisecond, "INVITE beyond the leg limit")
		if v := pbx.metric(t, "hello_paging_total", map[string]string{"result": "capacity"}); v != 1 {
			t.Fatalf("hello_paging_total{capacity} = %v", v)
		}
	})

	t.Run("trunk caller", func(t *testing.T) {
		st := newFakeTrunkState()
		pbx := startPBX(t, pageDevices(), trunkCfg(st), withEngine(t, internalPeer(t, "")), withPaging(pageGroup("normal"), pageExts()...))
		ph := registerAll(t, pbx, "b1")
		carrier := newPhone(t, pbx, "carrier", "")
		for _, code := range []string{"*60", "*80200"} {
			dcs, err := carrier.dua.WriteInvite(t.Context(), trunkInvite(carrier, pbx, code, "+971555000111", ""))
			if err != nil {
				t.Fatal(err)
			}
			if err := dcs.WaitAnswer(t.Context(), sipgo.AnswerOptions{}); err == nil || !strings.Contains(err.Error(), "403") {
				t.Fatalf("trunk call to %s = %v, want 403", code, err)
			}
			if cd := pbx.nextCDR(t); cd.FinalStatus != 403 {
				t.Fatalf("CDR for %s = %d", code, cd.FinalStatus)
			}
		}
		noReq(t, ph["b1"].invites, 100*time.Millisecond, "INVITE from a trunk's page")
	})
}

// TestIntercom fails if a call to an idle extension lacks the auto-answer
// headers or offers one-way media, if a busy extension is auto-answered, if
// allow_intercom=false still auto-answers, if DND is overridden, if an
// unknown extension connects, or if the CDR is not kind intercom.
func TestIntercom(t *testing.T) {
	exts := pageExts()
	for i := range exts {
		if exts[i].Number == "300" {
			exts[i].AllowIntercom = false
		}
	}
	pbx := startPBX(t, pageDevices(), withPaging(pageGroup("normal"), exts...))
	ph := registerAll(t, pbx, "a1", "b1", "c1", "d1", "e1")
	a, b, c, d, e := ph["a1"], ph["b1"], ph["c1"], ph["d1"], ph["e1"]

	// Idle extension: auto-answer headers and an ordinary two-way offer.
	r := waitCall(t, dial(t.Context(), a, "*80200"))
	if r.err != nil {
		t.Fatalf("intercom: %v", r.err)
	}
	in := waitReq(t, b.invites, "intercom INVITE")
	expectAutoAnswer(t, "200", in)
	if dir := media.SDPDirection(in.Body()); dir != "sendrecv" {
		t.Errorf("intercom offer is %s, want two-way", dir)
	}
	hangup(t, r.dcs)
	waitReq(t, b.byes, "BYE to the intercom callee")
	if cd := pbx.nextCDR(t); cd.Kind != cdr.KindIntercom || cd.FinalStatus != 200 || cd.RewrittenDestination != "200" {
		t.Fatalf("CDR = kind %q status %d rewritten %q", cd.Kind, cd.FinalStatus, cd.RewrittenDestination)
	}

	// allow_intercom=false: it rings like an ordinary call.
	r = waitCall(t, dial(t.Context(), a, "*80300"))
	if r.err != nil {
		t.Fatalf("intercom to a non-intercom extension: %v", r.err)
	}
	expectNoAutoAnswer(t, "300 (allow_intercom=false)", waitReq(t, c.invites, "INVITE to 300"))
	hangup(t, r.dcs)
	if cd := pbx.nextCDR(t); cd.Kind != cdr.KindIntercom {
		t.Fatalf("CDR kind %q", cd.Kind)
	}

	// A callee in a call is never cut into: normal ring, no headers.
	if err := pbx.fake.PutCall(t.Context(), livestate.Call{ID: "busy", From: "400", To: "900", State: "connected"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	r = waitCall(t, dial(t.Context(), a, "*80400"))
	if r.err != nil {
		t.Fatalf("intercom to a busy extension: %v", r.err)
	}
	expectNoAutoAnswer(t, "400 (busy)", waitReq(t, e.invites, "INVITE to 400"))
	hangup(t, r.dcs)
	pbx.nextCDR(t)

	// DND is respected, an unknown or own extension is refused.
	for dialled, want := range map[string]int{"*80210": 603, "*80777": 404, "*80100": 486, "*80": 404} {
		r = waitCall(t, dial(t.Context(), a, dialled))
		if r.err == nil || !strings.Contains(r.err.Error(), strconv.Itoa(want)) {
			t.Fatalf("%s = %v, want %d", dialled, r.err, want)
		}
		if cd := pbx.nextCDR(t); cd.Kind != cdr.KindIntercom || cd.FinalStatus != want {
			t.Fatalf("%s CDR = kind %q status %d", dialled, cd.Kind, cd.FinalStatus)
		}
	}
	noReq(t, d.invites, 100*time.Millisecond, "INVITE to a DND extension")
}

// TestAutoAnswerHardening fails if an Alert-Info, Call-Info or Answer-After
// header sent by a phone reaches another phone, if a forwarded call carries
// auto-answer, or if an ordinary call carries it.
func TestAutoAnswerHardening(t *testing.T) {
	exts := pageExts()
	for i := range exts {
		if exts[i].Number == "200" {
			exts[i].ForwardAlways = "300"
		}
	}
	pbx := startPBX(t, pageDevices(), withPaging(pageGroup("normal"), exts...))
	ph := registerAll(t, pbx, "a1", "b1", "c1", "e1")
	a, b, c, e := ph["a1"], ph["b1"], ph["c1"], ph["e1"]

	// A phone asks for auto-answer on an ordinary call to 400: the headers
	// never reach the callee.
	req := a.inviteReq("400")
	req.AppendHeader(sip.NewHeader("Alert-Info", "<sip:evil>;info=alert-autoanswer"))
	req.AppendHeader(sip.NewHeader("Call-Info", "<sip:evil>;answer-after=0"))
	req.AppendHeader(sip.NewHeader("Answer-After", "0"))
	dcs, err := a.dua.WriteInvite(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err := dcs.WaitAnswer(t.Context(), sipgo.AnswerOptions{Username: a.user, Password: a.pass}); err != nil {
		t.Fatalf("call: %v", err)
	}
	_ = dcs.Ack(context.Background())
	expectNoAutoAnswer(t, "400", waitReq(t, e.invites, "INVITE to 400"))
	hangup(t, dcs)
	pbx.nextCDR(t)

	// A forwarded call (200 forwards to 300) carries none either, even when
	// the caller asked for it.
	req = a.inviteReq("200")
	req.AppendHeader(sip.NewHeader("Alert-Info", "auto-answer"))
	dcs, err = a.dua.WriteInvite(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err := dcs.WaitAnswer(t.Context(), sipgo.AnswerOptions{Username: a.user, Password: a.pass}); err != nil {
		t.Fatalf("forwarded call: %v", err)
	}
	_ = dcs.Ack(context.Background())
	expectNoAutoAnswer(t, "300 (forwarded)", waitReq(t, c.invites, "forwarded INVITE to 300"))
	noReq(t, b.invites, 100*time.Millisecond, "INVITE to the forwarding extension")
	hangup(t, dcs)
	pbx.nextCDR(t)
}

// TestAutoAnswerHeaders fails if a vendor's User-Agent selects the wrong
// Alert-Info value, or any vendor's headers lack Call-Info answer-after=0.
func TestAutoAnswerHeaders(t *testing.T) {
	for ua, want := range map[string]string{
		"Grandstream WP822 1.0.11.78": "auto-answer",
		"":                            "auto-answer",
		"Yealink SIP-T54W 96.86":      "Ring Answer",
		"PolycomVVX-VVX_450-UA/6.4":   "Auto Answer",
		"snom D785-10.1.150":          "info=alert-autoanswer",
		"Fanvil X6U 2.4":              "info=Auto Answer",
	} {
		hs := autoAnswerHeaders("pbx.example", ua)
		got := map[string]string{}
		for _, h := range hs {
			got[h.Name()] = h.Value()
		}
		if got["Alert-Info"] != want {
			t.Errorf("%q: Alert-Info = %q, want %q", ua, got["Alert-Info"], want)
		}
		if got["Call-Info"] != "<sip:pbx.example>;answer-after=0" {
			t.Errorf("%q: Call-Info = %q", ua, got["Call-Info"])
		}
	}
}
