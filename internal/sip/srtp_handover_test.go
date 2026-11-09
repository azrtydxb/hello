package sip

import (
	"bytes"
	"net"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/secmedia"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// srtpEnd is one end of an anchored call as a phone sees it: its socket and
// the SRTP endpoint built from the keys of the exchange.
type srtpEnd struct {
	p     *srtpPhone
	proto *media.Protector
	to    *net.UDPAddr // the anchor port its audio goes to
}

// srtpEndOf builds the end from the answer the phone got (as a caller) or sent
// back to (as a callee: its own offer's key and the SDP Hello gave it).
func srtpEndOf(t *testing.T, p *srtpPhone, hello []byte) *srtpEnd {
	t.Helper()
	a := cryptoOf(t, hello)
	if len(a.Crypto) == 0 {
		t.Fatalf("Hello's SDP for %s carries no key\n%s", p.sock.LocalAddr(), hello)
	}
	proto, err := media.NewEndpoint(p.send, a.Crypto[0])
	if err != nil {
		t.Fatal(err)
	}
	return &srtpEnd{p: p, proto: proto, to: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: a.Port}}
}

// say sends one SRTP audio packet with fill from the end's socket.
func (e *srtpEnd) say(t *testing.T, seq uint16, fill byte) {
	t.Helper()
	enc, err := e.proto.Protect(rtpPacket(seq, fill))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.p.sock.WriteToUDP(enc, e.to); err != nil {
		t.Fatal(err)
	}
}

// hears reports whether an SRTP audio packet carrying fill arrives within d
// and opens with this end's key; a plain packet fails the test.
func (e *srtpEnd) hears(t *testing.T, fill byte, d time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		raw, ok := readUDP(t, e.p.sock, 100*time.Millisecond)
		if !ok {
			continue
		}
		plain, err := e.proto.Unprotect(raw)
		if err != nil {
			if pkt, ok := media.ParseRTP(raw); ok && bytes.Equal(pkt.Payload, bytes.Repeat([]byte{fill}, 160)) {
				t.Fatal("audio reached an SRTP phone in clear")
			}
			continue
		}
		if pkt, ok := media.ParseRTP(plain); ok && bytes.Equal(pkt.Payload, bytes.Repeat([]byte{fill}, 160)) {
			return true
		}
	}
	return false
}

// talk checks audio flows both ways between two ends, SRTP on each leg.
func talk(t *testing.T, x, y *srtpEnd) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var seq uint16
	var got [2]bool
	for time.Now().Before(deadline) && (!got[0] || !got[1]) {
		seq++
		x.say(t, seq, 0x11)
		y.say(t, seq, 0x22)
		got[0] = got[0] || y.hears(t, 0x11, 60*time.Millisecond)
		got[1] = got[1] || x.hears(t, 0x22, 60*time.Millisecond)
	}
	if !got[0] || !got[1] {
		t.Fatalf("audio through the anchor: x to y %v, y to x %v, want both over SRTP", got[0], got[1])
	}
}

// TestSRTPPickup fails if a call picked up by a phone drops to plain RTP, or
// if the picker is not given its own keys: the picker answers an SDES offer
// with a key (RTP/SAVP when it requires SRTP), its leg is a separate SRTP
// session from the caller's, and the ringing caller, who offered SDES, is
// answered with a key at the pickup. Mutation: handing the leg over as plain
// RTP (the previous behaviour) fails every key check.
func TestSRTPPickup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy secmedia.Policy
		savp   bool
	}{{"optional picker", secmedia.Optional, false}, {"required picker", secmedia.Required, true}} {
		t.Run(tc.name, func(t *testing.T) {
			devs := pickupDevices()
			devs[2].SRTP = string(tc.policy) // c1, extension 300
			st := parkStore(t)
			pbx := startPBX(t, devs, withMedia(false), withPark(st, "sip-a", testLot, testFrontGrp), trustLoopback)
			a := newSRTPPhone(t, pbx, "a1", "pa", true)
			c := newSRTPPhone(t, pbx, "c1", "pc", true)
			b := newPhone(t, pbx, "b1", "pb1")
			registerOver(t, a.phone, "tls")
			registerOver(t, c.phone, "tls")
			b.register(t)
			b.setCallee(ringForever())
			if tc.savp {
				c.forceSAVP()
			}

			ch := callOver(t, a, "200", "tls")
			waitReq(t, b.invites, "INVITE to the ringing phone")
			rc := waitCall(t, callOver(t, c, "*40", "tls"))
			if rc.err != nil {
				t.Fatalf("pickup: %v", rc.err)
			}
			ra := waitCall(t, ch)
			if ra.err != nil {
				t.Fatalf("the picked-up call was not answered: %v", ra.err)
			}
			ansC := cryptoOf(t, rc.dcs.InviteResponse.Body())
			if len(ansC.Crypto) != 1 || ansC.SRTPOnly != tc.savp {
				t.Fatalf("the picker was answered with %d keys, SAVP %v; want 1 and %v", len(ansC.Crypto), ansC.SRTPOnly, tc.savp)
			}
			ansA := cryptoOf(t, ra.dcs.InviteResponse.Body())
			if len(ansA.Crypto) != 1 {
				t.Fatalf("the caller was answered with %d keys at the pickup, want 1", len(ansA.Crypto))
			}
			if ansA.Crypto[0].Attribute() == ansC.Crypto[0].Attribute() {
				t.Fatal("the caller and the picker share a key")
			}
			talk(t, srtpEndOf(t, a, ra.dcs.InviteResponse.Body()), srtpEndOf(t, c, rc.dcs.InviteResponse.Body()))
			hangup(t, rc.dcs)
			waitReq(t, a.byes, "BYE to the caller")
			var legs int
			for range 2 {
				if rec := pbx.nextCDR(t); rec.SRTP != cdr.SRTPNone {
					legs++
				}
			}
			if legs == 0 {
				t.Fatal("no CDR records an encrypted leg for the picked-up call")
			}
		})
	}
}

// TestSRTPParkRetrieve fails if a retrieved parked call restarts as plain RTP:
// the retriever answers an SDES offer with its own key (RTP/SAVP when it
// requires SRTP), the parked party keeps the SRTP session it had, the two are
// separate sessions and audio flows both ways under them. Mutation: the
// previous behaviour (retriever answered in plain RTP) fails the key checks.
func TestSRTPParkRetrieve(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy secmedia.Policy
		savp   bool
	}{{"optional retriever", secmedia.Optional, false}, {"required retriever", secmedia.Required, true}} {
		t.Run(tc.name, func(t *testing.T) {
			devs := threeDevices()
			devs[2].SRTP = string(tc.policy) // c1, extension 300
			st := parkStore(t)
			pbx := startPBX(t, devs, withMedia(false), withPark(st, "sip-a", testLot), trustLoopback)
			a := newPhone(t, pbx, "a1", "pa")
			b := newSRTPPhone(t, pbx, "b1", "pb1", true)
			c := newSRTPPhone(t, pbx, "c1", "pc", true)
			a.register(t)
			registerOver(t, b.phone, "tls")
			registerOver(t, c.phone, "tls")
			b.setCallee(answerAfter(nil))
			if tc.savp {
				c.forceSAVP()
			}

			r := waitCall(t, dial(t.Context(), a, "200"))
			if r.err != nil {
				t.Fatalf("call: %v", r.err)
			}
			invB := waitReq(t, b.invites, "INVITE to the party that will be parked")
			waitReq(t, b.acks, "ACK")
			feedA := startRTPFeed(t, r.dcs.InviteResponse.Body())
			feedA.silence(0.1)
			seq := r.dcs.InviteRequest.CSeq().SeqNo + 10
			sendDigits(t, a, r.dcs, "*74", &seq)
			occupied(t, st, "701", "100")
			waitReq(t, a.byes, "BYE to the parker")

			rc := waitCall(t, callOver(t, c, "*75701", "tls"))
			if rc.err != nil {
				t.Fatalf("retrieve: %v", rc.err)
			}
			ansC := cryptoOf(t, rc.dcs.InviteResponse.Body())
			if len(ansC.Crypto) != 1 || ansC.SRTPOnly != tc.savp {
				t.Fatalf("the retriever was answered with %d keys, SAVP %v; want 1 and %v", len(ansC.Crypto), ansC.SRTPOnly, tc.savp)
			}
			bEnd := srtpEndOf(t, b, invB.Body())
			cEnd := srtpEndOf(t, c, rc.dcs.InviteResponse.Body())
			if bytes.Equal([]byte(ansC.Crypto[0].Attribute()), []byte(cryptoOf(t, invB.Body()).Crypto[0].Attribute())) {
				t.Fatal("the retriever and the parked party share a key")
			}
			talk(t, bEnd, cEnd)
			hangup(t, rc.dcs)
			waitReq(t, b.byes, "BYE to the parked party")
			pbx.nextCDR(t)
			pbx.nextCDR(t)
		})
	}
}

// TestSRTPBlindTransfer fails if the target of a blind transfer is moved onto
// the anchor in plain RTP: it must be re-INVITEd with its own keys under its
// own policy (RTP/SAVP when it requires SRTP) and the call must stay SRTP on
// both legs. A target that requires SRTP and answers the re-INVITE in clear
// ends the call (never silently downgraded). Mutation: the previous
// PlainOffer hand-over fails the offer checks.
func TestSRTPBlindTransfer(t *testing.T) {
	setup := func(t *testing.T, policy secmedia.Policy, targetCrypto bool) (*testPBX, *srtpPhone, *phone, *srtpPhone, callResult) {
		devs := threeDevices()
		devs[2].SRTP = string(policy) // c1, extension 300
		pbx := startPBX(t, devs, withMedia(false), trustLoopback)
		a := newSRTPPhone(t, pbx, "a1", "pa", true)
		b := newPhone(t, pbx, "b1", "pb1")
		c := newSRTPPhone(t, pbx, "c1", "pc", targetCrypto)
		registerOver(t, a.phone, "tls")
		b.register(t)
		registerOver(t, c.phone, "tls")
		a.reinviteOwnSDP, c.reinviteOwnSDP = true, true // answer with their own media and key
		b.setCallee(answerAfter(nil))
		r := waitCall(t, callOver(t, a, "200", "tls"))
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		waitReq(t, b.invites, "INVITE to the transferee")
		waitReq(t, b.acks, "ACK to the transferee")
		return pbx, a, b, c, r
	}
	refer := func(t *testing.T, b *phone, c *srtpPhone) {
		t.Helper()
		dss := <-serversOf(b)
		c.setCallee(answerAfter(nil))
		if res := b.sendReferFromCallee(t, dss, "<sip:300@"+testDomain+">"); res.StatusCode != 202 {
			t.Fatalf("REFER = %d, want 202", res.StatusCode)
		}
	}

	for _, tc := range []struct {
		name   string
		policy secmedia.Policy
		savp   bool
	}{{"optional target", secmedia.Optional, false}, {"required target", secmedia.Required, true}} {
		t.Run(tc.name+" keeps SRTP", func(t *testing.T) {
			pbx, a, b, c, r := setup(t, tc.policy, true)
			refer(t, b, c)
			waitReq(t, c.invites, "INVITE to the target")
			waitReq(t, c.acks, "ACK to the target")
			re := waitReq(t, c.reinvites, "re-INVITE moving the target onto the anchor")
			off := cryptoOf(t, re.Body())
			if len(off.Crypto) == 0 || off.SRTPOnly != tc.savp {
				t.Fatalf("the target's re-INVITE carries %d keys, SAVP %v; want keys and %v\n%s", len(off.Crypto), off.SRTPOnly, tc.savp, re.Body())
			}
			waitReq(t, a.reinvites, "re-INVITE refreshing the caller")
			talk(t, srtpEndOf(t, a, r.dcs.InviteResponse.Body()), srtpEndOf(t, c, re.Body()))
			hangup(t, r.dcs)
			pbx.nextCDR(t)
			if rec := pbx.nextCDR(t); rec.SRTP == cdr.SRTPNone {
				t.Fatalf("the transferred call's CDR says no leg was encrypted")
			}
		})
	}

	t.Run("a required target that answers in clear ends the call", func(t *testing.T) {
		pbx, a, b, c, r := setup(t, secmedia.Required, false)
		refer(t, b, c)
		waitReq(t, c.invites, "INVITE to the target")
		waitReq(t, c.acks, "ACK to the target")
		re := waitReq(t, c.reinvites, "re-INVITE to the target")
		if off := cryptoOf(t, re.Body()); !off.SRTPOnly || len(off.Crypto) == 0 {
			t.Fatalf("a required target was re-INVITEd without SAVP and keys:\n%s", re.Body())
		}
		waitReq(t, c.byes, "BYE to the target that would not take SRTP")
		waitReq(t, a.byes, "BYE to the caller of a call that cannot be encrypted")
		_ = r
		pbx.nextCDR(t)
	})
}

// TestSRTPRoamKeepsKeys fails if a phone that moves its media address in a
// re-INVITE (a Wi-Fi roam) loses its SRTP session: the same phone, the same
// keys. The relay leg must be re-aimed without dropping the leg's keys, or
// the call would continue in clear to the new address. Mutation: resetting
// the leg's crypto on a media-address change (the hand-over path) sends plain
// RTP to the new address.
func TestSRTPRoamKeepsKeys(t *testing.T) {
	pbx := startPBX(t, srtpDevices(secmedia.Optional, secmedia.Optional), withMedia(false), trustLoopback)
	a, b := newSRTPPhone(t, pbx, "a1", "pa", true), newSRTPPhone(t, pbx, "b1", "pb1", true)
	registerOver(t, a.phone, "tls")
	registerOver(t, b.phone, "tls")
	b.reinviteOwnSDP = true // the roam is relayed to b, which answers with its own media and key
	b.setCallee(answerAfter(nil))
	ch := callOver(t, a, "200", "tls")
	inv := waitReq(t, b.invites, "INVITE to b")
	r := waitCall(t, ch)
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	waitReq(t, b.acks, "ACK to b")
	aEnd := srtpEndOf(t, a, r.dcs.InviteResponse.Body())
	bEnd := srtpEndOf(t, b, inv.Body())
	talk(t, aEnd, bEnd)

	// a roams: same key, a new socket.
	sock, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sock.Close() })
	old := "m=audio " + strconv.Itoa(a.sock.LocalAddr().(*net.UDPAddr).Port) + " "
	moved := strings.Replace(a.sdp, old, "m=audio "+strconv.Itoa(sock.LocalAddr().(*net.UDPAddr).Port)+" ", 1)
	res := a.reinviteAsCaller(t, r.dcs, moved)
	if res.StatusCode != 200 {
		t.Fatalf("roam re-INVITE = %d", res.StatusCode)
	}
	roamed := &srtpEnd{p: &srtpPhone{phone: a.phone, sock: sock, send: a.send}, to: srtpEndOf(t, a, res.Body()).to}
	roamed.proto = srtpEndOf(t, a, res.Body()).proto
	talk(t, roamed, bEnd)
	hangup(t, r.dcs)
	if rec := pbx.nextCDR(t); rec.SRTP != cdr.SRTPBoth {
		t.Fatalf("CDR srtp = %q after the roam, want both", rec.SRTP)
	}
}

// TestSRTPParkReturn fails if a parked call that rings its parker again is
// offered to the parker's phone in plain RTP: the returned call carries the
// device's own keys (RTP/SAVP when it requires SRTP), the parked party keeps
// its session and audio flows over SRTP on both legs once the parker answers.
// A parker that requires SRTP and answers without it is dropped, never bridged
// in the clear. Mutation: the previous behaviour (the parker's old leg keys
// reused, then reset on answer) fails the key checks.
func TestSRTPParkReturn(t *testing.T) {
	setup := func(t *testing.T, parkerCrypto bool) (*testPBX, *srtpPhone, *srtpPhone, callResult, *sip.Request) {
		devs := threeDevices()
		devs[0].SRTP = string(secmedia.Required) // a1, the parker
		st := parkStore(t)
		lot := snapshot.ParkLot{ID: 1, Name: "default", Start: 701, End: 703, ReturnSecs: 1, VoicemailSecs: 5}
		vm := newVMStore()
		vm.boxes["100"] = VoicemailBox{ID: 1, Extension: "100"}
		pbx := startPBX(t, devs, withMedia(false), withPark(st, "sip-a", lot), withVoicemail(vm, &fakeObjects{}), trustLoopback,
			withFeatures(func(s *snapshot.Snapshot) {
				s.WithExtensionData([]snapshot.Extension{{Number: "100", Name: "Ext 100", VoicemailEnabled: true, VoicemailBoxID: 1}})
			}))
		a := newSRTPPhone(t, pbx, "a1", "pa", true)
		b := newSRTPPhone(t, pbx, "b1", "pb1", true)
		registerOver(t, a.phone, "tls")
		registerOver(t, b.phone, "tls")
		b.reinviteOwnSDP = true
		b.setCallee(answerAfter(nil))
		a.setCallee(answerAfter(nil))
		a.forceSAVP()
		r := waitCall(t, callOver(t, a, "200", "tls"))
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		invB := waitReq(t, b.invites, "INVITE to the party that will be parked")
		waitReq(t, b.acks, "ACK")
		if !parkerCrypto {
			// The parker's phone no longer sends a key when it is rung again.
			a.dropCrypto()
		}
		seq := r.dcs.InviteRequest.CSeq().SeqNo + 10
		sendDigits(t, a.phone, r.dcs, "*74", &seq)
		occupied(t, st, "701", "100")
		waitReq(t, a.byes, "BYE to the parker")
		return pbx, a, b, r, invB
	}

	t.Run("the parker answers over SRTP", func(t *testing.T) {
		_, a, b, _, invB := setup(t, true)
		back := waitReq(t, a.invites, "the returned call")
		off := cryptoOf(t, back.Body())
		if !off.SRTPOnly || len(off.Crypto) == 0 {
			t.Fatalf("the returned call to a parker that requires SRTP carries %d keys (SAVP %v)\n%s", len(off.Crypto), off.SRTPOnly, back.Body())
		}
		waitReq(t, a.acks, "ACK of the returned call")
		talk(t, srtpEndOf(t, b, invB.Body()), srtpEndOf(t, a, back.Body()))
	})

	t.Run("a parker that answers without SRTP is dropped", func(t *testing.T) {
		_, a, b, _, _ := setup(t, false)
		waitReq(t, a.invites, "the returned call")
		waitReq(t, a.byes, "BYE to the parker's device that would not take SRTP")
		_ = b
	})
}

// TestSRTPAttendedTransfer fails if the second party of an attended transfer
// is bridged onto the original call's anchor in plain RTP: it is re-INVITEd
// with keys of its own under its policy (RTP/SAVP when it requires SRTP) and
// the bridged call stays SRTP on both legs; a second party that requires
// SRTP and answers in clear ends the call.
func TestSRTPAttendedTransfer(t *testing.T) {
	run := func(t *testing.T, targetCrypto bool) {
		devs := threeDevices()
		devs[2].SRTP = string(secmedia.Required) // c1, extension 300
		pbx := startPBX(t, devs, withMedia(false), trustLoopback)
		a := newSRTPPhone(t, pbx, "a1", "pa", true)
		b := newPhone(t, pbx, "b1", "pb1")
		c := newSRTPPhone(t, pbx, "c1", "pc", true)
		registerOver(t, a.phone, "tls")
		b.register(t)
		registerOver(t, c.phone, "tls")
		a.reinviteOwnSDP, c.reinviteOwnSDP = true, true
		b.setCallee(answerAfter(nil))
		c.setCallee(answerAfter(nil))
		c.forceSAVP()
		r1 := waitCall(t, callOver(t, a, "200", "tls"))
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
		waitReq(t, c.acks, "ACK to c")
		if !targetCrypto {
			// The second party answers the bridge re-INVITE without a key.
			c.dropCrypto()
		}
		replaces := "sip:100@" + testDomain + "?Replaces=" + dssAB.InviteRequest.CallID().Value()
		if res := b.sendRefer(t, r2.dcs, "<"+replaces+">"); res.StatusCode != 202 {
			t.Fatalf("REFER = %d, want 202", res.StatusCode)
		}
		re := waitReq(t, c.reinvites, "re-INVITE bridging c onto the anchor")
		off := cryptoOf(t, re.Body())
		if !off.SRTPOnly || len(off.Crypto) == 0 {
			t.Fatalf("the second party was re-INVITEd with %d keys (SAVP %v), want RTP/SAVP and keys\n%s", len(off.Crypto), off.SRTPOnly, re.Body())
		}
		if !targetCrypto {
			waitReq(t, c.byes, "BYE to the second party that would not take SRTP")
			waitReq(t, a.byes, "BYE to the caller of a call that cannot be encrypted")
			return
		}
		waitReq(t, a.reinvites, "re-INVITE refreshing the caller")
		talk(t, srtpEndOf(t, a, r1.dcs.InviteResponse.Body()), srtpEndOf(t, c, re.Body()))
	}
	t.Run("the bridged call stays SRTP", func(t *testing.T) { run(t, true) })
	t.Run("a second party that answers in clear ends the call", func(t *testing.T) { run(t, false) })
}

// dropCrypto makes the phone answer from now on without a key or SAVP, while
// its handlers may be running.
func (p *srtpPhone) dropCrypto() {
	p.mu.Lock()
	defer p.mu.Unlock()
	lines := slices.DeleteFunc(strings.Split(p.sdp, "\r\n"), func(l string) bool { return strings.HasPrefix(l, "a=crypto") })
	p.sdp = strings.Replace(strings.Join(lines, "\r\n"), "RTP/SAVP", "RTP/AVP", 1)
}

// forceSAVP makes the phone offer RTP/SAVP (a phone that requires SRTP). The
// edit is made under the phone's lock so its handlers see it.
func (p *srtpPhone) forceSAVP() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sdp = strings.Replace(p.sdp, "RTP/AVP", "RTP/SAVP", 1)
}
