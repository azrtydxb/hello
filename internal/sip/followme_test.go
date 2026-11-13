package sip

import (
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
)

// fmProfile is an enabled follow-me profile with the defaults of the API.
func fmProfile(strategy string, targets ...snapshot.FollowMeTarget) snapshot.FollowMe {
	return snapshot.FollowMe{
		Enabled: true, Strategy: strategy, RingOwnDevices: true, TimeoutSeconds: 30,
		ConfirmExternal: true, DNDBlocks: true, Targets: targets,
	}
}

func fmExt(number string) snapshot.FollowMeTarget {
	return snapshot.FollowMeTarget{Kind: snapshot.FollowMeExtension, Target: number}
}

// fmPBX starts a PBX where extension 200 (phone b1) has the follow-me profile
// fm and the other extensions are 100 (a1), 300 (c1) and 210 (d1). Only the
// phones in register are registered. A ring group 700 holds 300 and 210.
func fmPBX(t *testing.T, fm snapshot.FollowMe, e200 snapshot.Extension, register string, opts ...pbxOpt) (*testPBX, map[string]*phone) {
	t.Helper()
	e200.Number = "200"
	feats := func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{{Number: "100"}, e200, {Number: "300"}, {Number: "210"}})
		g := snapshot.RingGroup{ID: 1, Name: "700", Strategy: "ring-all", RingTimeout: 30}
		s.WithRingGroups([]snapshot.RingGroup{g}, [][]snapshot.RingGroupMember{
			{{ExtensionID: 3, Extension: "300", Position: 1, Weight: 1}, {ExtensionID: 4, Extension: "210", Position: 2, Weight: 1}},
		})
		s.WithFollowMe(map[string]snapshot.FollowMe{"200": fm})
	}
	pbx := startPBX(t, threeDevices(), append([]pbxOpt{withFeatures(feats)}, opts...)...)
	ph := map[string]*phone{}
	for name, pw := range map[string]string{"a1": "pa", "b1": "pb1", "c1": "pc", "d1": "pd"} {
		ph[name] = newPhone(t, pbx, name, pw)
		if strings.Contains(register, name) {
			ph[name].register(t)
		}
	}
	return pbx, ph
}

func fmLegsOf(r cdr.Record) map[string]string {
	out := map[string]string{}
	for _, l := range r.FollowMeLegs {
		who := l.Extension
		if l.Trunk != "" {
			who += "@" + l.Trunk
		}
		out[l.Kind+" "+who] = l.Result
	}
	return out
}

func wantLegs(t *testing.T, r cdr.Record, want map[string]string) {
	t.Helper()
	got := fmLegsOf(r)
	if len(got) != len(want) {
		t.Fatalf("follow-me legs = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("follow-me legs = %v, want %v", got, want)
		}
	}
}

// TestFollowMe fails if a follow-me profile does not ring its own devices and
// its targets at once, in order, with the first answer cancelling the rest
// ("Call completed elsewhere"), if DND does not stop it (unless the profile
// says otherwise), if forward-always does not win over it, if a target that is
// the extension itself is rung, or if an unanswered ring skips the no-answer
// forward (S-5, S-6).
func TestFollowMe(t *testing.T) {
	t.Run("simultaneous: the first answer cancels the others", func(t *testing.T) {
		pbx, ph := fmPBX(t, fmProfile(snapshot.FollowMeSimultaneous, fmExt("300")), snapshot.Extension{}, "a1 b1 c1")
		ph["b1"].setCallee(ringForever())
		r := waitCall(t, dial(t.Context(), ph["a1"], "200"))
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		waitReq(t, ph["b1"].invites, "INVITE to the extension's own phone")
		waitReq(t, ph["c1"].invites, "INVITE to the target")
		cancel := waitReq(t, ph["b1"].cancels, "CANCEL of the own phone")
		if reason := cancel.GetHeader("Reason"); reason == nil || !strings.Contains(reason.Value(), "Call completed elsewhere") {
			t.Fatalf("CANCEL Reason = %v, want Call completed elsewhere", reason)
		}
		hangup(t, r.dcs)
		rec := pbx.nextCDR(t)
		wantLegs(t, rec, map[string]string{"own 200": "cancelled", "extension 300": "answered"})
		wantTrace(t, rec.Trace, "Extension 200 follows me (simultaneous): own devices, extension 300")
		if v := pbx.metric(t, "hello_follow_me_legs_total", map[string]string{"kind": "extension", "result": "answered"}); v != 1 {
			t.Fatalf("legs{extension,answered} = %v, want 1", v)
		}
		if v := pbx.metric(t, "hello_forwarded_calls_total", map[string]string{"kind": "follow_me"}); v != 1 {
			t.Fatalf("forwarded{follow_me} = %v, want 1", v)
		}
	})

	t.Run("sequential: the next target starts when the first one's turn is over", func(t *testing.T) {
		fm := fmProfile(snapshot.FollowMeSequential,
			snapshot.FollowMeTarget{Kind: snapshot.FollowMeExtension, Target: "300", Delay: 1}, fmExt("210"))
		pbx, ph := fmPBX(t, fm, snapshot.Extension{}, "a1 b1 c1 d1")
		ph["b1"].setCallee(ringForever())
		ph["c1"].setCallee(ringForever())
		start := time.Now()
		call := dial(t.Context(), ph["a1"], "200")
		waitReq(t, ph["b1"].invites, "INVITE to the own phone")
		waitReq(t, ph["c1"].invites, "INVITE to the first target")
		noReq(t, ph["d1"].invites, 500*time.Millisecond, "INVITE to the second target before the first one's turn is over")
		waitReq(t, ph["d1"].invites, "INVITE to the second target")
		if d := time.Since(start); d < 900*time.Millisecond {
			t.Fatalf("the second target rang after %v, want at least the first one's 1s", d)
		}
		waitReq(t, ph["c1"].cancels, "CANCEL of the first target")
		r := waitCall(t, call)
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		hangup(t, r.dcs)
		wantLegs(t, pbx.nextCDR(t), map[string]string{"own 200": "cancelled", "extension 300": "no_answer", "extension 210": "answered"})
	})

	t.Run("a ring group target rings its members", func(t *testing.T) {
		pbx, ph := fmPBX(t, fmProfile(snapshot.FollowMeSimultaneous,
			snapshot.FollowMeTarget{Kind: snapshot.FollowMeRingGroup, Target: "700"}), snapshot.Extension{}, "a1 b1 c1 d1")
		ph["b1"].setCallee(ringForever())
		ph["c1"].setCallee(ringForever())
		r := waitCall(t, dial(t.Context(), ph["a1"], "200"))
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		waitReq(t, ph["c1"].invites, "INVITE to a group member")
		waitReq(t, ph["d1"].invites, "INVITE to a group member")
		hangup(t, r.dcs)
		wantLegs(t, pbx.nextCDR(t), map[string]string{"own 200": "cancelled", "ring_group 300": "cancelled", "ring_group 210": "answered"})
	})

	t.Run("a device that is offline leaves the targets to ring alone", func(t *testing.T) {
		pbx, ph := fmPBX(t, fmProfile(snapshot.FollowMeSimultaneous, fmExt("300")), snapshot.Extension{}, "a1 c1")
		r := waitCall(t, dial(t.Context(), ph["a1"], "200"))
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		waitReq(t, ph["c1"].invites, "INVITE to the target")
		hangup(t, r.dcs)
		rec := pbx.nextCDR(t)
		wantLegs(t, rec, map[string]string{"extension 300": "answered"})
		wantTrace(t, rec.Trace, "Follow-me: no device of extension 200 is registered: the targets ring alone")
	})

	t.Run("a target that is the extension itself is not rung", func(t *testing.T) {
		pbx, ph := fmPBX(t, fmProfile(snapshot.FollowMeSimultaneous, fmExt("200"), fmExt("300")), snapshot.Extension{}, "a1 b1 c1")
		ph["b1"].setCallee(ringForever())
		r := waitCall(t, dial(t.Context(), ph["a1"], "200"))
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		waitReq(t, ph["c1"].invites, "INVITE to the other target")
		hangup(t, r.dcs)
		rec := pbx.nextCDR(t)
		wantTrace(t, rec.Trace, "Follow-me: loop back to extension 200")
		if got := fmLegsOf(rec); len(got) != 2 {
			t.Fatalf("legs = %v, want the own phone and 300 only", got)
		}
	})

	t.Run("do not disturb stops follow-me", func(t *testing.T) {
		pbx, ph := fmPBX(t, fmProfile(snapshot.FollowMeSimultaneous, fmExt("300")), snapshot.Extension{DND: true}, "a1 b1 c1")
		r := waitCall(t, dial(t.Context(), ph["a1"], "200"))
		if code := responseCode(r.err); code != 603 {
			t.Fatalf("call to a DND extension = %v, want 603", r.err)
		}
		rec := pbx.nextCDR(t)
		wantTrace(t, rec.Trace, "Follow-me of extension 200 skipped: do not disturb stops follow-me")
		noReq(t, ph["c1"].invites, 200*time.Millisecond, "INVITE to a follow-me target of a DND extension")
		noReq(t, ph["b1"].invites, 50*time.Millisecond, "INVITE to a DND extension")
	})

	t.Run("do not disturb with dnd_blocks off rings the targets only", func(t *testing.T) {
		fm := fmProfile(snapshot.FollowMeSimultaneous, fmExt("300"))
		fm.DNDBlocks = false
		pbx, ph := fmPBX(t, fm, snapshot.Extension{DND: true}, "a1 b1 c1")
		r := waitCall(t, dial(t.Context(), ph["a1"], "200"))
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		waitReq(t, ph["c1"].invites, "INVITE to the target")
		noReq(t, ph["b1"].invites, 100*time.Millisecond, "INVITE to the DND extension's own phone")
		hangup(t, r.dcs)
		wantLegs(t, pbx.nextCDR(t), map[string]string{"extension 300": "answered"})
	})

	t.Run("forward-always wins over follow-me", func(t *testing.T) {
		pbx, ph := fmPBX(t, fmProfile(snapshot.FollowMeSimultaneous, fmExt("300")), snapshot.Extension{ForwardAlways: "210"}, "a1 b1 c1 d1")
		r := waitCall(t, dial(t.Context(), ph["a1"], "200"))
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		waitReq(t, ph["d1"].invites, "INVITE to the forward-always target")
		noReq(t, ph["c1"].invites, 200*time.Millisecond, "INVITE to a follow-me target behind forward-always")
		noReq(t, ph["b1"].invites, 50*time.Millisecond, "INVITE to the forwarding extension")
		hangup(t, r.dcs)
		rec := pbx.nextCDR(t)
		if len(rec.FollowMeLegs) != 0 {
			t.Fatalf("legs = %+v, want none", rec.FollowMeLegs)
		}
	})

	t.Run("an unanswered ring goes on to the no-answer forward", func(t *testing.T) {
		fm := fmProfile(snapshot.FollowMeSimultaneous, fmExt("300"))
		fm.TimeoutSeconds = 1
		pbx, ph := fmPBX(t, fm, snapshot.Extension{ForwardNoAnswer: "210"}, "a1 b1 c1 d1")
		ph["b1"].setCallee(ringForever())
		ph["c1"].setCallee(ringForever())
		start := time.Now()
		r := waitCall(t, dial(t.Context(), ph["a1"], "200"))
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		if d := time.Since(start); d < 900*time.Millisecond {
			t.Fatalf("answered after %v, want the follow-me to ring its second first", d)
		}
		waitReq(t, ph["d1"].invites, "INVITE to the no-answer target")
		waitReq(t, ph["b1"].cancels, "CANCEL of the own phone")
		waitReq(t, ph["c1"].cancels, "CANCEL of the target")
		hangup(t, r.dcs)
		rec := pbx.nextCDR(t)
		wantTrace(t, rec.Trace, "Forwarding on no_answer to 210")
		wantLegs(t, rec, map[string]string{"own 200": "no_answer", "extension 300": "no_answer"})
	})

	t.Run("a profile with nothing to ring is the ordinary ring", func(t *testing.T) {
		pbx, ph := fmPBX(t, fmProfile(snapshot.FollowMeSimultaneous, fmExt("599")), snapshot.Extension{}, "a1 b1")
		r := waitCall(t, dial(t.Context(), ph["a1"], "200"))
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		waitReq(t, ph["b1"].invites, "INVITE to the extension")
		hangup(t, r.dcs)
		rec := pbx.nextCDR(t)
		wantLegs(t, rec, map[string]string{"own 200": "answered"})
	})
}

// fmCarrierPBX starts fmPBX with a carrier trunk that outbound numbers leave
// on, and the follow-me profile fm on extension 200.
// ownPorts gives a PBX a media port range of its own, derived from the test
// process, so a test that sends RTP to the relay is not answered by another
// test binary on the same machine that holds the default range.
func ownPorts() pbxOpt {
	min := 30000 + (os.Getpid()%2000)*10
	return func(c *Config, _ *Deps) { c.RTPPortMin, c.RTPPortMax = min, min+9 }
}

func fmCarrierPBX(t *testing.T, fm snapshot.FollowMe, e200 snapshot.Extension, register string) (*testPBX, map[string]*phone, *carrier, *fakeTrunkState) {
	t.Helper()
	cr := newCarrier(t, "acct1", "pw")
	r := &fakeRouter{extensions: map[string]bool{"100": true, "200": true, "300": true, "210": true},
		trunks: []routing.Trunk{cr.trunk(1, "carrier", "ip")}}
	r.decide = outboundVia(r, "+32475123456", "+97140000100", false, nil, 1)
	st := newFakeTrunkState()
	pbx, ph := fmPBX(t, fm, e200, register, trunkCfg(st), withRouter(r, nil), ownPorts())
	return pbx, ph, cr, st
}

func fmExternal(number string) snapshot.FollowMeTarget {
	return snapshot.FollowMeTarget{Kind: snapshot.FollowMeExternal, Target: number}
}

// TestFollowMeExternal fails if an external target is not dialled through
// outbound routing with the decision's caller ID, if its answer does not
// connect the caller on the carrier's trunk, or if the trunk slot outlives
// the call (S-6).
func TestFollowMeExternal(t *testing.T) {
	fm := fmProfile(snapshot.FollowMeSimultaneous, fmExternal("0475123456"))
	fm.ConfirmExternal = false
	pbx, ph, cr, st := fmCarrierPBX(t, fm, snapshot.Extension{}, "a1 b1")
	ph["b1"].setCallee(ringForever())
	r := waitCall(t, dial(t.Context(), ph["a1"], "200"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	inv := waitReq(t, cr.invites, "INVITE to the carrier")
	if inv.Recipient.User != "+32475123456" || inv.From().Address.User != "+97140000100" || inv.From().Address.Host != "carrier.example" {
		t.Fatalf("carrier INVITE: R-URI %s From %s", inv.Recipient.String(), inv.From().Address.String())
	}
	anchoredSDP(t, pbx, inv.Body())
	waitReq(t, ph["b1"].invites, "INVITE to the own phone")
	cancel := waitReq(t, ph["b1"].cancels, "CANCEL of the own phone")
	if reason := cancel.GetHeader("Reason"); reason == nil || !strings.Contains(reason.Value(), "Call completed elsewhere") {
		t.Fatalf("CANCEL Reason = %v", reason)
	}
	waitReq(t, cr.acks, "ACK to the carrier")
	hangup(t, r.dcs)
	waitReq(t, cr.byes, "BYE to the carrier")
	rec := pbx.nextCDR(t)
	if rec.Trunk != "carrier" || rec.Direction != "outbound" || rec.FinalStatus != 200 {
		t.Fatalf("CDR = %+v", rec)
	}
	wantLegs(t, rec, map[string]string{"own 200": "cancelled", "external 0475123456@carrier": "answered"})
	eventually(t, "slot released", func() bool { return st.activeCalls(1) == 0 })
	if v := pbx.metric(t, "hello_follow_me_legs_total", map[string]string{"kind": "external", "result": "answered"}); v != 1 {
		t.Fatalf("legs{external,answered} = %v, want 1", v)
	}
}

// TestFollowMeExternalUnreachable fails if an external target the carrier
// rejects, or a number with no route, stops the other legs from ringing or
// leaves a trunk slot held.
func TestFollowMeExternalUnreachable(t *testing.T) {
	fm := fmProfile(snapshot.FollowMeSimultaneous, fmExternal("0475123456"))
	fm.ConfirmExternal = false
	pbx, ph, cr, st := fmCarrierPBX(t, fm, snapshot.Extension{}, "a1 b1")
	cr.inviteCode.Store(486)
	answer := make(chan struct{})
	ph["b1"].setCallee(answerAfter(answer))
	call := dial(t.Context(), ph["a1"], "200")
	waitReq(t, cr.invites, "INVITE to the carrier")
	waitReq(t, ph["b1"].invites, "INVITE to the own phone")
	// The carrier's busy is recorded while the own phone is still ringing.
	eventually(t, "the external leg ends busy", func() bool {
		return pbx.metric(t, "hello_follow_me_legs_total", map[string]string{"kind": "external", "result": "busy"}) == 1
	})
	close(answer)
	r := waitCall(t, call)
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	hangup(t, r.dcs)
	wantLegs(t, pbx.nextCDR(t), map[string]string{"own 200": "answered", "external 0475123456@carrier": "busy"})
	eventually(t, "slot released", func() bool { return st.activeCalls(1) == 0 })
}

// rtpPeer is a carrier's media socket: it receives what Hello plays and sends
// RFC 2833 digits.
type rtpPeer struct {
	conn *net.UDPConn
	pkts chan *media.RTPPacket
}

func newRTPPeer(t *testing.T) *rtpPeer {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	p := &rtpPeer{conn: conn, pkts: make(chan *media.RTPPacket, 512)}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, _, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if pkt, ok := media.ParseRTP(buf[:n]); ok {
				cp := *pkt
				cp.Payload = append([]byte(nil), pkt.Payload...)
				select {
				case p.pkts <- &cp:
				default:
				}
			}
		}
	}()
	return p
}

func (p *rtpPeer) sdp() string {
	port := p.conn.LocalAddr().(*net.UDPAddr).Port
	return "v=0\r\no=carrier 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
		"m=audio " + strconv.Itoa(port) + " RTP/AVP 0 101\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:101 telephone-event/8000\r\na=fmtp:101 0-16\r\n"
}

// heardAudio waits for a G.711 packet: the prompt Hello plays to the target.
func (p *rtpPeer) heardAudio(t *testing.T) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case pkt := <-p.pkts:
			if pkt.PayloadType == 0 {
				return
			}
		case <-deadline:
			t.Fatal("the target heard no prompt")
		}
	}
}

// press sends the digit 1 as an RFC 2833 event to the relay port the INVITE offered.
func (p *rtpPeer) press(t *testing.T, to media.AudioSDP, code byte) {
	t.Helper()
	dst := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: to.Port}
	for i := 0; i < 3; i++ {
		payload := []byte{code, 0x80 | 10, 0x00, 0xa0}
		wire := media.BuildRTP(101, uint16(100+i), 8000, 0x0a0b0c0d, i == 0, payload)
		_, err := p.conn.WriteToUDP(wire, dst)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestFollowMeConfirm fails if an external target is connected without
// pressing 1 (the carrier's voicemail would take the call), if the digit does
// not connect it, if the prompt is not played before the bridge, or if a
// target that never presses 1 is not dropped with its result recorded (S-6).
func TestFollowMeConfirm(t *testing.T) {
	old := fmAcceptWindow
	fmAcceptWindow = 400 * time.Millisecond
	t.Cleanup(func() { fmAcceptWindow = old })

	t.Run("pressing 1 connects the call", func(t *testing.T) {
		fm := fmProfile(snapshot.FollowMeSimultaneous, fmExternal("0475123456"))
		fm.RingOwnDevices = false
		pbx, ph, cr, st := fmCarrierPBX(t, fm, snapshot.Extension{}, "a1 b1")
		peer := newRTPPeer(t)
		cr.setSDP(peer.sdp())
		call := dial(t.Context(), ph["a1"], "200")
		inv := waitReq(t, cr.invites, "INVITE to the carrier")
		offer := anchoredSDP(t, pbx, inv.Body())
		peer.heardAudio(t) // the prompt, before the caller has an answer
		select {
		case r := <-call:
			t.Fatalf("the caller was answered before the digit: %v", r.err)
		case <-time.After(100 * time.Millisecond):
		}
		peer.press(t, offer, 1)
		r := waitCall(t, call)
		if r.err != nil {
			t.Fatalf("call: %v\n%s", r.err, traceText(pbx.nextCDR(t).Trace))
		}
		hangup(t, r.dcs)
		waitReq(t, cr.byes, "BYE to the carrier")
		rec := pbx.nextCDR(t)
		wantLegs(t, rec, map[string]string{"external 0475123456@carrier": "answered"})
		eventually(t, "slot released", func() bool { return st.activeCalls(1) == 0 })
	})

	t.Run("no digit drops the target and the ring goes on", func(t *testing.T) {
		fm := fmProfile(snapshot.FollowMeSimultaneous, fmExternal("0475123456"))
		fm.TimeoutSeconds = 2
		pbx, ph, cr, _ := fmCarrierPBX(t, fm, snapshot.Extension{ForwardNoAnswer: "210"}, "a1 b1 d1")
		ph["b1"].setCallee(ringForever())
		peer := newRTPPeer(t)
		cr.setSDP(peer.sdp())
		r := waitCall(t, dial(t.Context(), ph["a1"], "200"))
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		waitReq(t, cr.invites, "INVITE to the carrier")
		waitReq(t, cr.byes, "BYE to the carrier that did not press 1")
		waitReq(t, ph["d1"].invites, "INVITE to the no-answer target")
		hangup(t, r.dcs)
		rec := pbx.nextCDR(t)
		got := fmLegsOf(rec)
		if got["external 0475123456@carrier"] != "declined" {
			t.Fatalf("legs = %v, want the external target declined", got)
		}
		if rec.Trunk == "carrier" {
			t.Fatalf("the call was connected to the carrier that did not press 1: %+v", rec)
		}
		if v := pbx.metric(t, "hello_follow_me_legs_total", map[string]string{"kind": "external", "result": "declined"}); v != 1 {
			t.Fatalf("legs{external,declined} = %v, want 1", v)
		}
	})
}

// TestFollowMeCodes fails if *76 and *77 do not turn the dialling extension's
// own profile on and off, or if a code dialled by an extension with no
// profile target is answered as if it had worked (S-5).
func TestFollowMeCodes(t *testing.T) {
	settings := &fakeSettings{}
	feats := func(s *snapshot.Snapshot) {
		s.WithFeatureCodes([]snapshot.FeatureCode{
			{Code: "*76", Action: ActionFollowMeOn}, {Code: "*77", Action: ActionFollowMeOff},
		})
		s.WithExtensionData([]snapshot.Extension{{Number: "100"}, {Number: "200"}, {Number: "300"}, {Number: "210"}})
		s.WithFollowMe(map[string]snapshot.FollowMe{
			"100": {Strategy: snapshot.FollowMeSimultaneous, Targets: []snapshot.FollowMeTarget{fmExt("300")}},
		})
	}
	pbx := startPBX(t, threeDevices(), withFeatures(feats), func(_ *Config, d *Deps) { d.Settings = settings })
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)

	r := waitCall(t, dial(t.Context(), a, "*76"))
	if r.err != nil {
		t.Fatalf("*76: %v", r.err)
	}
	hangup(t, r.dcs)
	pbx.nextCDR(t)
	ups := settings.got()
	if len(ups) != 1 || ups[0].FollowMe == nil || !*ups[0].FollowMe || ups[0].Extension != "100" {
		t.Fatalf("*76 update = %+v", ups)
	}
	r = waitCall(t, dial(t.Context(), a, "*77"))
	if r.err != nil {
		t.Fatalf("*77: %v", r.err)
	}
	hangup(t, r.dcs)
	pbx.nextCDR(t)
	ups = settings.got()
	if len(ups) != 2 || ups[1].FollowMe == nil || *ups[1].FollowMe {
		t.Fatalf("*77 update = %+v", ups)
	}

	// Extension 200 has no profile: the code is refused and changes nothing.
	r = waitCall(t, dial(t.Context(), b, "*76"))
	if code := responseCode(r.err); code != 404 {
		t.Fatalf("*76 without a profile = %v, want 404", r.err)
	}
	pbx.nextCDR(t)
	if got := len(settings.got()); got != 2 {
		t.Fatalf("%d updates after a refused code, want 2", got)
	}

	// A user may toggle only their own extension: a code carries no target.
	if upd, ok := featureUpdate(snapshot.FeatureCode{Code: "*76", Action: ActionFollowMeOn}, "200", "100"); !ok || upd.Extension != "100" {
		t.Fatalf("update = %+v ok %v, want the dialling extension", upd, ok)
	}
}

// TestFollowMeIVR fails if an IVR option that dials an extension skips the
// extension's follow-me profile: the own phone and the target must both ring
// and the target's answer must connect the menu's caller (S-6).
func TestFollowMeIVR(t *testing.T) {
	menu := ivrMenu("main", "800", map[string]snapshot.IVRDest{"1": ext("200")})
	pbx, _ := ivrPBXWith(t, 1, func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{{Number: "100"}, {Number: "200"}, {Number: "300"}})
		s.WithFollowMe(map[string]snapshot.FollowMe{"200": fmProfile(snapshot.FollowMeSimultaneous, fmExt("300"))})
	}, menu)
	a, b, c := registerABC(t, pbx)
	b.setCallee(ringForever())
	c.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "800"))
	if r.err != nil {
		t.Fatalf("call to the menu: %v", r.err)
	}
	seq := r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, a, r.dcs, "1", &seq)
	waitReq(t, b.invites, "INVITE to the extension's own phone")
	waitReq(t, c.invites, "INVITE to the follow-me target")
	eventually(t, "the menu connected the call", func() bool {
		return pbx.metric(t, "hello_ivr_total", map[string]string{"event": "connected"}) == 1
	})
	waitReq(t, b.cancels, "CANCEL of the own phone")
	if err := r.dcs.Bye(t.Context()); err != nil {
		t.Fatalf("caller BYE: %v", err)
	}
	rec := lastCDRFor(t, pbx, "800")
	wantLegs(t, rec, map[string]string{"own 200": "cancelled", "extension 300": "answered"})
	wantTrace(t, rec.Trace, "Extension 200 follows me (simultaneous): own devices, extension 300")
}

// TestFollowMeReplicates fails if a call a follow-me external target answered
// is not replicated like any other answered call (its owner, the caller's
// dialog and the carrier's dialog as the target a second node re-INVITEs), and
// if the profile is not in the snapshot every node reads: the second node of a
// pair takes the call over from this record and needs nothing of the ring
// (S-6, HA).
func TestFollowMeReplicates(t *testing.T) {
	ha := newFakeHA()
	fm := fmProfile(snapshot.FollowMeSimultaneous, fmExternal("0475123456"))
	fm.ConfirmExternal = false
	cr := newCarrier(t, "acct1", "pw")
	r := &fakeRouter{extensions: map[string]bool{"100": true, "200": true}, trunks: []routing.Trunk{cr.trunk(1, "carrier", "ip")}}
	r.decide = outboundVia(r, "+32475123456", "+97140000100", false, nil, 1)
	st := newFakeTrunkState()
	pbx, ph := fmPBX(t, fm, snapshot.Extension{}, "a1 b1", trunkCfg(st), withRouter(r, nil), ownPorts(),
		withNodeID("sip-1"), withHA(ha, &fakeMembership{}))
	ph["b1"].setCallee(ringForever())
	call := waitCall(t, dial(t.Context(), ph["a1"], "200"))
	if call.err != nil {
		t.Fatalf("call: %v", call.err)
	}
	id := call.dcs.InviteRequest.CallID().Value()
	eventually(t, "the answered call is replicated", func() bool { return ha.ownerOf(id) == "sip-1" })
	ha.mu.Lock()
	state := ha.dialogs[id]
	ha.mu.Unlock()
	if !strings.Contains(state.Legs[1].RemoteTarget, strconv.Itoa(cr.port)) {
		t.Fatalf("the replicated callee leg targets %q, want the carrier on port %d", state.Legs[1].RemoteTarget, cr.port)
	}
	if state.Destination != "200" {
		t.Fatalf("replicated destination = %q, want the dialled 200", state.Destination)
	}
	hangup(t, call.dcs)
	pbx.nextCDR(t)

	// The profile rides the snapshot, so a second node loading the same
	// revision rings the same legs.
	got, ok := pbx.snaps.Current().FollowMe("200")
	if !ok || !got.Active() || got.Targets[0].Target != "0475123456" {
		t.Fatalf("snapshot profile = %+v ok %v", got, ok)
	}
}
