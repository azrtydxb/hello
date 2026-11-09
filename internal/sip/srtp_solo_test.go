package sip

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/secmedia"
	"github.com/azrtydxb/hello/internal/snapshot"
)

// TestSRTPVoicemail fails if a phone on TLS cannot leave a voicemail over
// SRTP: the answer must carry Hello's key (in the profile the phone offered),
// the greeting must be SRTP under the phone's key, the phone's SRTP audio and
// digit must be recorded, the CDR must say the caller leg was encrypted, and a
// phone that requires SRTP must not be answered in clear. Mutation: answering
// voicemail with plain RTP (the previous behaviour) fails every case.
func TestSRTPVoicemail(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy secmedia.Policy
		savp   bool
	}{
		{"optional, RTP/AVP with crypto", secmedia.Optional, false},
		{"required, RTP/SAVP", secmedia.Required, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vm := newVMStore()
			vm.boxes["200"] = VoicemailBox{ID: 5, Extension: "200"}
			objs := &fakeObjects{}
			feats := func(s *snapshot.Snapshot) {
				s.WithExtensionData([]snapshot.Extension{{Number: "200", VoicemailEnabled: true, VoicemailBoxID: 5}})
			}
			pbx := startPBX(t, srtpDevices(tc.policy, secmedia.Optional), shortRing(200*time.Millisecond),
				withFeatures(feats), withVoicemail(vm, objs), withMedia(false), trustLoopback)
			anchored(pbx)
			a, b := newSRTPPhone(t, pbx, "a1", "pa", true), newSRTPPhone(t, pbx, "b1", "pb1", true)
			registerOver(t, a.phone, "tls")
			registerOver(t, b.phone, "tls")
			b.setCallee(ringForever())
			if tc.savp {
				a.forceSAVP()
			}
			r := waitCall(t, callOver(t, a, "200", "tls"))
			if r.err != nil {
				t.Fatalf("voicemail call: %v", r.err)
			}
			ans := cryptoOf(t, r.dcs.InviteResponse.Body())
			if len(ans.Crypto) != 1 || ans.SRTPOnly != tc.savp {
				t.Fatalf("voicemail answered with %d crypto lines, SAVP %v; want 1 and %v", len(ans.Crypto), ans.SRTPOnly, tc.savp)
			}
			if strings.Contains(string(r.dcs.InviteResponse.Body()), a.send.Attribute()) {
				t.Fatal("the phone's own key was echoed back")
			}
			proto, err := media.NewEndpoint(a.send, ans.Crypto[0])
			if err != nil {
				t.Fatal(err)
			}
			// The greeting is SRTP that only the phone's key opens.
			raw, ok := readUDP(t, a.sock, 5*time.Second)
			if !ok {
				t.Fatal("no greeting audio")
			}
			if _, err := proto.Unprotect(raw); err != nil {
				t.Fatalf("the greeting is not SRTP for the phone's key: %v", err)
			}
			// The phone records over SRTP.
			feed := startRTPFeed(t, r.dcs.InviteResponse.Body())
			feed.protect = func(p []byte) []byte {
				enc, err := proto.Protect(p)
				if err != nil {
					t.Error(err)
				}
				return enc
			}
			// protect encrypts with the phone's send key; the protector built
			// above sends with a.send, as the phone does.
			feed.silence(2.5)
			feed.digit('#')
			deadline := time.Now().Add(5 * time.Second)
			for len(vm.snapshotMsgs()) != 1 {
				if time.Now().After(deadline) {
					t.Fatal("the SRTP message was not stored")
				}
				time.Sleep(20 * time.Millisecond)
			}
			if msg := vm.snapshotMsgs()[0]; msg.DurationMs < 1000 {
				t.Fatalf("message of %d ms, want the recorded second and more", msg.DurationMs)
			}
			if v := pbx.metric(t, "hello_srtp_negotiations_total", map[string]string{"result": "srtp"}); v < 1 {
				t.Fatalf("srtp negotiations = %v, want the voicemail leg counted", v)
			}
			if rec := pbx.nextCDR(t); rec.SRTP != cdr.SRTPCaller {
				t.Fatalf("CDR srtp = %q, want caller", rec.SRTP)
			}
		})
	}

	t.Run("a required phone is never answered in clear", func(t *testing.T) {
		vm := newVMStore()
		vm.boxes["200"] = VoicemailBox{ID: 5, Extension: "200"}
		feats := func(s *snapshot.Snapshot) {
			s.WithExtensionData([]snapshot.Extension{{Number: "200", VoicemailEnabled: true, VoicemailBoxID: 5}})
		}
		pbx := startPBX(t, srtpDevices(secmedia.Required, secmedia.Optional), shortRing(200*time.Millisecond),
			withFeatures(feats), withVoicemail(vm, &fakeObjects{}), withMedia(false), trustLoopback)
		anchored(pbx)
		a, b := newSRTPPhone(t, pbx, "a1", "pa", true), newSRTPPhone(t, pbx, "b1", "pb1", true)
		registerOver(t, a.phone, "tls")
		registerOver(t, b.phone, "tls")
		b.setCallee(ringForever())
		a.forceSAVP()
		r := waitCall(t, callOver(t, a, "200", "tls"))
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		if ans := cryptoOf(t, r.dcs.InviteResponse.Body()); len(ans.Crypto) == 0 || !ans.SRTPOnly {
			t.Fatal("a phone that requires SRTP was answered without it")
		}
	})
}

// srtpSeatJoin dials a conference room from a TLS phone with SDES and returns
// the seat whose media is SRTP under the keys of the exchange.
func srtpSeatJoin(t *testing.T, a *srtpPhone, room string) *confSeat {
	t.Helper()
	r := waitCall(t, callOver(t, a, room, "tls"))
	if r.err != nil {
		t.Fatalf("join: %v", r.err)
	}
	ans := cryptoOf(t, r.dcs.InviteResponse.Body())
	if len(ans.Crypto) != 1 {
		t.Fatalf("the room answered with %d crypto lines, want 1", len(ans.Crypto))
	}
	proto, err := media.NewEndpoint(a.send, ans.Crypto[0])
	if err != nil {
		t.Fatal(err)
	}
	feed := startRTPFeed(t, r.dcs.InviteResponse.Body())
	feed.protect = func(p []byte) []byte {
		enc, err := proto.Protect(p)
		if err != nil {
			t.Error(err)
		}
		return enc
	}
	feed.unprotect = proto.Unprotect
	s := &confSeat{t: t, p: a.phone, dcs: r.dcs, feed: feed}
	feed.packet(feed.audio, make([]byte, 160), true) // latches the participant
	return s
}

// TestSRTPConferenceRoom fails if a TLS phone with SDES cannot be in a
// conference: the room must answer with a key, hear the phone's SRTP, and send
// it the mix as SRTP, while a plain phone in the same room keeps hearing and
// being heard in plain RTP (each participant has its own SRTP session). The
// CDR records the encrypted caller leg. Mutation: a mixer that answers plain
// RTP fails the answer check.
func TestSRTPConferenceRoom(t *testing.T) {
	pbx := startPBX(t, confDevices(), withRooms(room800()), withMedia(false), trustLoopback)
	a := newSRTPPhone(t, pbx, "a1", "pa", true)
	registerOver(t, a.phone, "tls")
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)

	sa := srtpSeatJoin(t, a, "800")
	sb, err := seatJoin(t, b, "800")
	if err != nil {
		t.Fatalf("b joins: %v", err)
	}
	sa.drain()
	sb.drain()
	time.Sleep(300 * time.Millisecond)
	speaksUntilHeard(t, "the plain phone hears the SRTP phone", sa, sb)
	speaksUntilHeard(t, "the SRTP phone hears the plain phone", sb, sa)
	sa.leave()
	if rec := pbx.nextCDR(t); rec.SRTP != cdr.SRTPCaller {
		t.Fatalf("CDR srtp = %q, want caller", rec.SRTP)
	}
	sb.leave()
	pbx.nextCDR(t)
}

// TestSRTPConferenceRequired fails if a phone that requires SRTP is let into
// a room in clear media: its SAVP offer is answered with SAVP and a key.
func TestSRTPConferenceRequired(t *testing.T) {
	devs := confDevices()
	devs[0].SRTP = string(secmedia.Required)
	pbx := startPBX(t, devs, withRooms(room800()), withMedia(false), trustLoopback)
	a := newSRTPPhone(t, pbx, "a1", "pa", true)
	registerOver(t, a.phone, "tls")
	a.forceSAVP()
	r := waitCall(t, callOver(t, a, "800", "tls"))
	if r.err != nil {
		t.Fatalf("join: %v", r.err)
	}
	ans := cryptoOf(t, r.dcs.InviteResponse.Body())
	if !ans.SRTPOnly || len(ans.Crypto) != 1 {
		t.Fatalf("a required phone was answered with SAVP %v and %d keys", ans.SRTPOnly, len(ans.Crypto))
	}
	hangup(t, r.dcs)
	pbx.nextCDR(t)
}

// TestSRTPPage fails if a page is sent in clear to a member whose policy
// forbids it, or if one member's SRTP session is shared with another: the
// caller's SDES leg is answered with a key (early media and final answer) and
// decrypted on the way in; every member that can have SRTP is offered its own
// keys (RTP/SAVP when it requires them) and hears the page as SRTP under the
// key it answered with; a plain member keeps hearing plain RTP; a member that
// requires SRTP and is not on TLS is skipped and never gets a plain offer.
// Mutation: a page that offers members plain RTP fails the offer checks.
func TestSRTPPage(t *testing.T) {
	devs := pageDevices()
	devs[1].SRTP = string(secmedia.Required) // b1, extension 200, on TLS
	devs[4].SRTP = string(secmedia.Required) // e1, extension 400, on UDP
	pbx := startPBX(t, devs, withPaging(pageGroup("normal"), pageExts()...), withMedia(false), trustLoopback)
	a := newSRTPPhone(t, pbx, "a1", "pa", true)
	b := newSRTPPhone(t, pbx, "b1", "pb", true)
	registerOver(t, a.phone, "tls")
	registerOver(t, b.phone, "tls")
	ph := registerAll(t, pbx, "c1", "e1")
	c, e := ph["c1"], ph["e1"]
	cRTP := rtpSocket(t, c)

	release := make(chan struct{})
	b.setCallee(answerAfter(release))
	c.setCallee(answerAfter(release))
	res := callOver(t, a, "*60", "tls")
	bi := waitReq(t, b.invites, "page INVITE to the SRTP member")
	ci := waitReq(t, c.invites, "page INVITE to the plain member")
	noReq(t, e.invites, 200*time.Millisecond, "INVITE to a member that requires SRTP on a UDP leg")

	offB := cryptoOf(t, bi.Body())
	if !offB.SRTPOnly || len(offB.Crypto) != 2 {
		t.Fatalf("the SRTP member's offer: SAVP %v with %d keys, want RTP/SAVP with 2", offB.SRTPOnly, len(offB.Crypto))
	}
	if offC := cryptoOf(t, ci.Body()); offC.SRTPOnly || len(offC.Crypto) != 0 {
		t.Fatalf("the plain member was offered crypto: %s", ci.Body())
	}

	// Until a member answers the caller hears the page tone as SRTP.
	if raw, ok := readUDP(t, a.sock, 3*time.Second); !ok {
		t.Fatal("no page tone for the caller")
	} else if _, ok := media.ParseRTP(raw); !ok {
		t.Fatal("page tone is not RTP")
	}
	close(release)
	r := waitCall(t, res)
	if r.err != nil {
		t.Fatalf("page: %v", r.err)
	}
	ansA := cryptoOf(t, r.dcs.InviteResponse.Body())
	if len(ansA.Crypto) != 1 {
		t.Fatalf("the caller was answered with %d keys, want 1", len(ansA.Crypto))
	}
	callerProto, err := media.NewEndpoint(a.send, ansA.Crypto[0])
	if err != nil {
		t.Fatal(err)
	}
	memberProto, err := media.NewEndpoint(b.send, offB.Crypto[0])
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("speak-to-all")
	to := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: ansA.Port}
	heardB, heardC := false, false
	deadline := time.Now().Add(6 * time.Second)
	for seq := uint16(1); (!heardB || !heardC) && time.Now().Before(deadline); seq++ {
		enc, err := callerProto.Protect(media.BuildRTP(0, seq, uint32(seq)*160, 99, false, payload))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = a.sock.WriteToUDP(enc, to)
		if raw, ok := readUDP(t, b.sock, 40*time.Millisecond); ok {
			if plain, err := memberProto.Unprotect(raw); err == nil {
				if pkt, ok := media.ParseRTP(plain); ok && string(pkt.Payload) == string(payload) {
					heardB = true
				}
			} else if pkt, ok := media.ParseRTP(raw); ok && string(pkt.Payload) == string(payload) {
				t.Fatal("the SRTP member heard the page in clear")
			}
		}
		if raw, ok := readUDP(t, cRTP, 40*time.Millisecond); ok {
			if pkt, ok := media.ParseRTP(raw); ok && string(pkt.Payload) == string(payload) {
				heardC = true
			}
		}
	}
	if !heardB || !heardC {
		t.Fatalf("the page was heard by the SRTP member %v and the plain member %v, want both", heardB, heardC)
	}
	hangup(t, r.dcs)
	waitReq(t, b.byes, "BYE to the SRTP member")
	cd := pbx.nextCDR(t)
	if cd.SRTP != cdr.SRTPCaller {
		t.Fatalf("CDR srtp = %q, want caller", cd.SRTP)
	}
	skipped := false
	for _, l := range cd.PageLegs {
		if l.Extension == "400" && l.Result == PageLegSRTP {
			skipped = true
		}
	}
	if !skipped {
		t.Fatalf("the CDR page legs %+v lack extension 400 skipped for SRTP", cd.PageLegs)
	}
}

// TestSRTPPageMemberWithoutCrypto fails if a member that requires SRTP and
// answers a page in the clear stays in the page.
func TestSRTPPageMemberWithoutCrypto(t *testing.T) {
	devs := pageDevices()
	devs[1].SRTP = string(secmedia.Required)
	pbx := startPBX(t, devs, withPaging(pageGroup("normal"), pageExts()...), withMedia(false), trustLoopback)
	a := newSRTPPhone(t, pbx, "a1", "pa", true)
	b := newSRTPPhone(t, pbx, "b1", "pb", false) // answers without a key
	registerOver(t, a.phone, "tls")
	registerOver(t, b.phone, "tls")
	b.setCallee(answerAfter(nil))
	res := callOver(t, a, "*60", "tls")
	waitReq(t, b.invites, "page INVITE")
	waitReq(t, b.byes, "BYE to the member that answered without SRTP")
	r := waitCall(t, res)
	if responseCode(r.err) != 486 && responseCode(r.err) != 600 && r.err == nil {
		t.Fatalf("the page connected although its only member answered in clear")
	}
}

// TestSRTPVoicemailTakeover fails if a voicemail call that was SRTP restarts
// on the node that takes it over in plain RTP: the replicated state names the
// caller's SRTP mode but no key, the taker re-INVITEs the caller with its own
// fresh keys, a caller that answers with crypto leaves its message over SRTP,
// and a caller that answers in the clear ends the call instead of carrying on.
func TestSRTPVoicemailTakeover(t *testing.T) {
	run := func(t *testing.T, answerWithCrypto bool) {
		vm := newVMStore()
		vm.boxes["200"] = VoicemailBox{ID: 5, Extension: "200"}
		objs := &fakeObjects{}
		feats := func(s *snapshot.Snapshot) {
			s.WithExtensionData([]snapshot.Extension{{Number: "200", VoicemailEnabled: true, VoicemailBoxID: 5}})
		}
		ha, mem, owner, taker := haPair(t, srtpDevices(secmedia.Optional, secmedia.Optional), shortRing(200*time.Millisecond),
			withFeatures(feats), withVoicemail(vm, objs), withMedia(false), trustLoopback)
		anchored(owner)
		anchored(taker)
		a, b := newSRTPPhone(t, owner, "a1", "pa", true), newSRTPPhone(t, owner, "b1", "pb1", true)
		registerOver(t, a.phone, "tls")
		registerOver(t, b.phone, "tls")
		b.setCallee(ringForever())
		a.reinviteOwnSDP = true
		r := waitCall(t, callOver(t, a, "200", "tls"))
		if r.err != nil {
			t.Fatalf("voicemail call: %v", r.err)
		}
		st := waitRecord(t, ha, "the SRTP voicemail call is replicated", "sip-1", func(s livestate.DialogState) bool {
			return s.State == haPhaseVoicemail && s.Legs[0].RemoteSDP != "" && s.Legs[0].SRTP != ""
		})
		if st.Legs[0].SRTP != "avp" {
			t.Fatalf("replicated SRTP mode = %q, want avp", st.Legs[0].SRTP)
		}
		if strings.Contains(st.Legs[0].RemoteSDP, "a=crypto") || strings.Contains(st.Legs[0].SDP, "a=crypto") {
			t.Fatal("a key was replicated")
		}
		if !answerWithCrypto {
			a.dropCrypto()
		}
		haReinviteTimeout = time.Second
		killNode(owner, mem, map[string]int{"sip-1": 1}, "sip-2")
		taker.srv.takeoverPass(t.Context())
		ra := waitReq(t, a.reinvites, "takeover re-INVITE to the voicemail caller")
		offer := cryptoOf(t, ra.Body())
		if len(offer.Crypto) == 0 || offer.SRTPOnly {
			t.Fatalf("the taker's re-INVITE carries %d keys (SAVP %v), want fresh keys on RTP/AVP:\n%s", len(offer.Crypto), offer.SRTPOnly, ra.Body())
		}
		if !answerWithCrypto {
			eventually(t, "the taker ends the call that would not keep SRTP", func() bool {
				return taker.metric(t, "hello_zombie_calls_total", nil) == 1
			})
			return
		}
		waitReq(t, a.acks, "ACK to the caller's 200")
		eventually(t, "the voicemail call lives on the taker", func() bool { return connectedOn(taker, "200", livestate.HATakenOver) })
		eventually(t, "the taker restarts the voicemail application", func() bool {
			vm.mu.Lock()
			defer vm.mu.Unlock()
			return vm.lookups >= 2
		})
		proto, err := media.NewEndpoint(a.send, offer.Crypto[0])
		if err != nil {
			t.Fatal(err)
		}
		feed := startRTPFeed(t, ra.Body())
		feed.protect = func(p []byte) []byte {
			enc, err := proto.Protect(p)
			if err != nil {
				t.Error(err)
			}
			return enc
		}
		feed.silence(2.5)
		feed.digit('#')
		eventually(t, "the SRTP message is stored by the taker", func() bool {
			for _, m := range vm.snapshotMsgs() {
				if m.BoxID == 5 && m.DurationMs >= 1000 {
					return true
				}
			}
			return false
		})
	}
	t.Run("the caller keeps SRTP", func(t *testing.T) { run(t, true) })
	t.Run("a caller that answers in clear ends the call", func(t *testing.T) { run(t, false) })
}
