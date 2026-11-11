package sip

import (
	"bytes"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/secmedia"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

// srtpPhone is a test phone with a media socket and SDES keys: its SDP names
// the socket and (when crypto is set) one a=crypto line with the key the
// phone sends with.
type srtpPhone struct {
	*phone
	sock *net.UDPConn
	send media.Crypto // the key this phone sends with
}

func newSRTPPhone(t *testing.T, pbx *testPBX, user, pass string, crypto bool) *srtpPhone {
	t.Helper()
	p := newPhone(t, pbx, user, pass)
	sock, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sock.Close() })
	sp := &srtpPhone{phone: p, sock: sock}
	sdp := "v=0\r\no=" + user + " 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
		"m=audio " + strconv.Itoa(sock.LocalAddr().(*net.UDPAddr).Port) + " RTP/AVP 0 101\r\na=rtpmap:101 telephone-event/8000\r\n"
	if crypto {
		k, err := media.NewCrypto(1, media.SuiteAES128SHA1_80)
		if err != nil {
			t.Fatal(err)
		}
		sp.send = k
		sdp += "a=" + k.Attribute() + "\r\n"
	}
	p.sdp = sdp
	return sp
}

// cryptoOf returns the crypto lines of an SDP body.
func cryptoOf(t *testing.T, body []byte) media.AudioSDP {
	t.Helper()
	a, err := media.ParseAudioSDP(body)
	if err != nil {
		t.Fatalf("SDP: %v\n%s", err, body)
	}
	return a
}

func srtpDevices(a, b secmedia.Policy) []snapshot.Device {
	da, db := dev(1, "a1", "100", "pa"), dev(2, "b1", "200", "pb1")
	da.SRTP, db.SRTP = string(a), string(b)
	return []snapshot.Device{da, db}
}

// callOver places a call from a over transport and returns the result channel.
func callOver(t *testing.T, a *srtpPhone, ext, transport string) <-chan callResult {
	t.Helper()
	req := viaEdge(a.inviteReq(ext), transport)
	ch := make(chan callResult, 1)
	go func() {
		dcs, err := a.callReq(t.Context(), req)
		ch <- callResult{dcs, err}
	}()
	return ch
}

// rtpPacket is a PCMU packet of the given payload byte.
func rtpPacket(seq uint16, fill byte) []byte {
	return media.BuildRTP(0, seq, uint32(seq)*160, 4242, false, bytes.Repeat([]byte{fill}, 160))
}

func readUDPWithin(t *testing.T, c *net.UDPConn, within time.Duration) ([]byte, bool) {
	t.Helper()
	buf := make([]byte, 2048)
	_ = c.SetReadDeadline(time.Now().Add(within))
	n, _, err := c.ReadFromUDP(buf)
	if err != nil {
		return nil, false
	}
	return append([]byte(nil), buf[:n]...), true
}

// TestSRTPCallThroughTheAnchor fails if an SDES call between two TLS phones is
// not encrypted on both legs with a key per leg, if audio does not survive the
// anchor (decrypt, rewrite, encrypt), if a leg that is not TLS is offered
// crypto, if the CDR and the metrics do not say which legs were encrypted, or
// if a key shows up in the CDR trace. Mutation: offering crypto regardless of
// the leg's transport fails the UDP cases; sending leg b in clear fails the
// decrypt.
func TestSRTPCallThroughTheAnchor(t *testing.T) {
	t.Run("both legs TLS", func(t *testing.T) {
		pbx := startPBX(t, srtpDevices(secmedia.Optional, secmedia.Optional), withMedia(false), trustLoopback)
		a, b := newSRTPPhone(t, pbx, "a1", "pa", true), newSRTPPhone(t, pbx, "b1", "pb1", true)
		registerOver(t, a.phone, "tls")
		registerOver(t, b.phone, "tls")
		b.setCallee(answerAfter(nil))
		ch := callOver(t, a, "200", "tls")
		inv := waitReq(t, b.invites, "INVITE to b")
		r := waitCall(t, ch)
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		offB := cryptoOf(t, inv.Body())
		ansA := cryptoOf(t, r.dcs.InviteResponse.Body())
		if len(offB.Crypto) != 2 || offB.SRTPOnly {
			t.Fatalf("the callee's offer has %d crypto lines (SAVP only: %v), want 2 on RTP/AVP", len(offB.Crypto), offB.SRTPOnly)
		}
		if offB.Crypto[0].Suite != media.SuiteAES128SHA1_80 || offB.Crypto[1].Suite != media.SuiteAES128SHA1_32 {
			t.Fatalf("offered suites = %s, %s", offB.Crypto[0].Suite, offB.Crypto[1].Suite)
		}
		if len(ansA.Crypto) != 1 || ansA.Crypto[0].Tag != 1 {
			t.Fatalf("the caller's answer has %d crypto lines", len(ansA.Crypto))
		}
		if strings.Contains(string(inv.Body()), a.send.Attribute()) || strings.Contains(string(r.dcs.InviteResponse.Body()), b.send.Attribute()) {
			t.Fatal("one phone's key was passed through to the other leg")
		}
		if offB.Crypto[0].Attribute() == ansA.Crypto[0].Attribute() {
			t.Fatal("both legs use the same key")
		}
		if v := pbx.metric(t, "hello_srtp_legs", nil); v != 2 {
			t.Fatalf("hello_srtp_legs = %v, want 2", v)
		}
		// Audio: a protects with its key; the anchor decrypts, rewrites and
		// encrypts for b with leg b's key; b decrypts with it.
		aSend, err := media.NewEndpoint(a.send, ansA.Crypto[0])
		if err != nil {
			t.Fatal(err)
		}
		bRecv, err := media.NewEndpoint(b.send, offB.Crypto[0])
		if err != nil {
			t.Fatal(err)
		}
		toA := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: ansA.Port}
		var got []byte
		for i := 0; i < 20 && got == nil; i++ {
			wire, err := aSend.Protect(rtpPacket(uint16(i+1), 0x5a))
			if err != nil {
				t.Fatal(err)
			}
			_, _ = a.sock.WriteToUDP(wire, toA)
			if raw, ok := readUDPWithin(t, b.sock, 200*time.Millisecond); ok {
				got = raw
			}
		}
		if got == nil {
			t.Fatal("no audio reached the callee")
		}
		if bytes.Contains(got, bytes.Repeat([]byte{0x5a}, 32)) {
			t.Fatal("the callee received clear audio on an SRTP leg")
		}
		plain, err := bRecv.Unprotect(got)
		if err != nil {
			t.Fatalf("the callee cannot decrypt with leg b's key: %v", err)
		}
		if pkt, ok := media.ParseRTP(plain); !ok || !bytes.Equal(pkt.Payload, bytes.Repeat([]byte{0x5a}, 160)) {
			t.Fatal("the audio changed on the way")
		}
		hangup(t, r.dcs)
		rec := pbx.nextCDR(t)
		if rec.SRTP != cdr.SRTPBoth {
			t.Fatalf("CDR srtp = %q, want both", rec.SRTP)
		}
		trace := traceText(rec.Trace)
		for _, c := range []media.Crypto{a.send, b.send, offB.Crypto[0], offB.Crypto[1], ansA.Crypto[0]} {
			if line := c.Attribute(); strings.Contains(trace, line[strings.Index(line, "inline:")+7:]) {
				t.Fatal("a key appears in the CDR trace")
			}
		}
		if !strings.Contains(trace, "SRTP on the caller leg") || !strings.Contains(trace, "SRTP on the callee leg") {
			t.Fatalf("the trace does not say which legs were encrypted: %s", trace)
		}
		eventually(t, "the SRTP legs gauge to drop", func() bool { return pbx.metric(t, "hello_srtp_legs", nil) == 0 })
	})

	t.Run("a UDP callee is not offered crypto, a TLS caller is still encrypted", func(t *testing.T) {
		pbx := startPBX(t, srtpDevices(secmedia.Optional, secmedia.Optional), withMedia(false), trustLoopback)
		a, b := newSRTPPhone(t, pbx, "a1", "pa", true), newSRTPPhone(t, pbx, "b1", "pb1", true)
		registerOver(t, a.phone, "tls")
		registerOver(t, b.phone, "udp")
		b.setCallee(answerAfter(nil))
		ch := callOver(t, a, "200", "tls")
		inv := waitReq(t, b.invites, "INVITE to b")
		r := waitCall(t, ch)
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		if strings.Contains(string(inv.Body()), "a=crypto") || strings.Contains(string(inv.Body()), "SAVP") {
			t.Fatalf("crypto was offered on a UDP leg:\n%s", inv.Body())
		}
		ansA := cryptoOf(t, r.dcs.InviteResponse.Body())
		if len(ansA.Crypto) != 1 {
			t.Fatal("the TLS caller was not answered with crypto")
		}
		// a -> b: SRTP in, plain RTP out.
		aSend, _ := media.NewEndpoint(a.send, ansA.Crypto[0])
		offB := cryptoOf(t, inv.Body())
		_ = offB
		toA := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: ansA.Port}
		var got []byte
		for i := 0; i < 20 && got == nil; i++ {
			wire, _ := aSend.Protect(rtpPacket(uint16(i+1), 0x33))
			_, _ = a.sock.WriteToUDP(wire, toA)
			if raw, ok := readUDPWithin(t, b.sock, 200*time.Millisecond); ok {
				got = raw
			}
		}
		pkt, ok := media.ParseRTP(got)
		if !ok || !bytes.Equal(pkt.Payload, bytes.Repeat([]byte{0x33}, 160)) {
			t.Fatal("the UDP callee did not receive plain RTP audio")
		}
		hangup(t, r.dcs)
		if rec := pbx.nextCDR(t); rec.SRTP != cdr.SRTPCaller {
			t.Fatalf("CDR srtp = %q, want caller", rec.SRTP)
		}
	})

	t.Run("a UDP caller's crypto is stripped and plain RTP answered", func(t *testing.T) {
		pbx := startPBX(t, srtpDevices(secmedia.Optional, secmedia.Optional), withMedia(false), trustLoopback)
		a, b := newSRTPPhone(t, pbx, "a1", "pa", true), newSRTPPhone(t, pbx, "b1", "pb1", true)
		registerOver(t, a.phone, "udp")
		registerOver(t, b.phone, "tls")
		b.setCallee(answerAfter(nil))
		ch := callOver(t, a, "200", "udp")
		waitReq(t, b.invites, "INVITE to b")
		r := waitCall(t, ch)
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		if body := string(r.dcs.InviteResponse.Body()); strings.Contains(body, "a=crypto") {
			t.Fatalf("a UDP caller was answered with crypto:\n%s", body)
		}
		hangup(t, r.dcs)
		if rec := pbx.nextCDR(t); rec.SRTP != cdr.SRTPCallee {
			t.Fatalf("CDR srtp = %q, want callee", rec.SRTP)
		}
	})

	t.Run("policy off offers no crypto on TLS", func(t *testing.T) {
		pbx := startPBX(t, srtpDevices(secmedia.Off, secmedia.Off), withMedia(false), trustLoopback)
		a, b := newSRTPPhone(t, pbx, "a1", "pa", true), newSRTPPhone(t, pbx, "b1", "pb1", true)
		registerOver(t, a.phone, "tls")
		registerOver(t, b.phone, "tls")
		b.setCallee(answerAfter(nil))
		ch := callOver(t, a, "200", "tls")
		inv := waitReq(t, b.invites, "INVITE to b")
		r := waitCall(t, ch)
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		if strings.Contains(string(inv.Body()), "a=crypto") || strings.Contains(string(r.dcs.InviteResponse.Body()), "a=crypto") {
			t.Fatal("crypto offered or answered with the policy off")
		}
		hangup(t, r.dcs)
		if rec := pbx.nextCDR(t); rec.SRTP != cdr.SRTPNone {
			t.Fatalf("CDR srtp = %q, want none", rec.SRTP)
		}
	})

	t.Run("a callee that declines crypto leaves that leg plain under optional", func(t *testing.T) {
		pbx := startPBX(t, srtpDevices(secmedia.Optional, secmedia.Optional), withMedia(false), trustLoopback)
		a, b := newSRTPPhone(t, pbx, "a1", "pa", true), newSRTPPhone(t, pbx, "b1", "pb1", false)
		registerOver(t, a.phone, "tls")
		registerOver(t, b.phone, "tls")
		b.setCallee(answerAfter(nil))
		ch := callOver(t, a, "200", "tls")
		waitReq(t, b.invites, "INVITE to b")
		r := waitCall(t, ch)
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		hangup(t, r.dcs)
		if rec := pbx.nextCDR(t); rec.SRTP != cdr.SRTPCaller {
			t.Fatalf("CDR srtp = %q, want caller", rec.SRTP)
		}
		if n := pbx.metric(t, "hello_srtp_negotiations_total", map[string]string{"result": "plain"}); n != 1 {
			t.Fatalf("plain negotiations = %v, want 1", n)
		}
	})
}

// TestSRTPPolicyRefusals fails if a required policy lets a call through in the
// clear: a required caller must be on TLS and offer crypto, an SRTP-only offer
// needs a leg that can carry it, a required callee that answers plain ends the
// call, and a required callee that is not on TLS is not rung. Mutation:
// ignoring the policy in srtpGate or learnCalleeCrypto fails the matching case.
func TestSRTPPolicyRefusals(t *testing.T) {
	invite := func(a *srtpPhone, transport string, edit func(string) string) *sip.Request {
		req := viaEdge(a.inviteReq("200"), transport)
		req.SetBody([]byte(edit(a.sdp)))
		return req
	}
	savp := func(s string) string { return strings.Replace(s, "RTP/AVP", "RTP/SAVP", 1) }
	noCrypto := func(s string) string {
		var out []string
		for _, l := range strings.Split(s, "\r\n") {
			if !strings.HasPrefix(l, "a=crypto") {
				out = append(out, l)
			}
		}
		return strings.Join(out, "\r\n")
	}
	for _, tc := range []struct {
		name      string
		caller    secmedia.Policy
		transport string
		edit      func(string) string
	}{
		{"a required caller on UDP", secmedia.Required, "udp", savp},
		{"a required caller that offers no crypto", secmedia.Required, "tls", noCrypto},
		{"an SRTP-only offer on a UDP leg", secmedia.Optional, "udp", savp},
		{"an SRTP-only offer with the policy off", secmedia.Off, "tls", savp},
	} {
		t.Run(tc.name+" is refused with 488", func(t *testing.T) {
			pbx := startPBX(t, srtpDevices(tc.caller, secmedia.Optional), withMedia(false), trustLoopback)
			a, b := newSRTPPhone(t, pbx, "a1", "pa", true), newSRTPPhone(t, pbx, "b1", "pb1", true)
			registerOver(t, a.phone, tc.transport)
			registerOver(t, b.phone, "tls")
			_, err := a.callReq(t.Context(), invite(a, tc.transport, tc.edit))
			if responseCode(err) != 488 {
				t.Fatalf("call = %v, want 488", err)
			}
			noReq(t, b.invites, 300*time.Millisecond, "INVITE to the callee of a refused call")
			rec := pbx.nextCDR(t)
			if rec.FinalStatus != 488 || rec.SRTP != cdr.SRTPNone {
				t.Fatalf("CDR = %d, srtp %q", rec.FinalStatus, rec.SRTP)
			}
			if n := pbx.metric(t, "hello_srtp_negotiations_total", map[string]string{"result": "refused"}); n != 1 {
				t.Fatalf("refused negotiations = %v, want 1", n)
			}
		})
	}

	t.Run("a required caller on TLS with crypto goes through, encrypted", func(t *testing.T) {
		pbx := startPBX(t, srtpDevices(secmedia.Required, secmedia.Optional), withMedia(false), trustLoopback)
		a, b := newSRTPPhone(t, pbx, "a1", "pa", true), newSRTPPhone(t, pbx, "b1", "pb1", true)
		registerOver(t, a.phone, "tls")
		registerOver(t, b.phone, "tls")
		b.setCallee(answerAfter(nil))
		ch := make(chan callResult, 1)
		go func() {
			dcs, err := a.callReq(t.Context(), invite(a, "tls", savp))
			ch <- callResult{dcs, err}
		}()
		waitReq(t, b.invites, "INVITE to b")
		r := waitCall(t, ch)
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		if ans := cryptoOf(t, r.dcs.InviteResponse.Body()); !ans.SRTPOnly || len(ans.Crypto) != 1 {
			t.Fatalf("the SAVP offer was not answered with SAVP and one key (SAVP %v, %d keys)", ans.SRTPOnly, len(ans.Crypto))
		}
		hangup(t, r.dcs)
		pbx.nextCDR(t)
	})

	t.Run("a required callee is offered SAVP and a plain answer ends the call", func(t *testing.T) {
		pbx := startPBX(t, srtpDevices(secmedia.Optional, secmedia.Required), withMedia(false), trustLoopback)
		a, b := newSRTPPhone(t, pbx, "a1", "pa", true), newSRTPPhone(t, pbx, "b1", "pb1", false)
		registerOver(t, a.phone, "tls")
		registerOver(t, b.phone, "tls")
		b.setCallee(answerAfter(nil))
		ch := callOver(t, a, "200", "tls")
		inv := waitReq(t, b.invites, "INVITE to b")
		if off := cryptoOf(t, inv.Body()); !off.SRTPOnly || len(off.Crypto) == 0 {
			t.Fatalf("a required callee was not offered RTP/SAVP with crypto:\n%s", inv.Body())
		}
		r := waitCall(t, ch)
		if responseCode(r.err) != 488 {
			t.Fatalf("call = %v, want 488 (the callee answered without SRTP)", r.err)
		}
		waitReq(t, b.byes, "BYE to the callee that answered in the clear")
		rec := pbx.nextCDR(t)
		if rec.FinalStatus != 488 {
			t.Fatalf("CDR status = %d", rec.FinalStatus)
		}
	})

	t.Run("a required callee that is not on TLS is not rung", func(t *testing.T) {
		pbx := startPBX(t, srtpDevices(secmedia.Optional, secmedia.Required), withMedia(false), trustLoopback)
		a, b := newSRTPPhone(t, pbx, "a1", "pa", true), newSRTPPhone(t, pbx, "b1", "pb1", true)
		registerOver(t, a.phone, "tls")
		registerOver(t, b.phone, "udp")
		_, err := a.callReq(t.Context(), viaEdge(a.inviteReq("200"), "tls"))
		if err == nil {
			t.Fatal("a call to a required device on a UDP leg connected")
		}
		noReq(t, b.invites, 300*time.Millisecond, "INVITE to a required device over UDP")
		cd := pbx.nextCDR(t)
		if !strings.Contains(traceText(cd.Trace), "SRTP is required but the leg is not on TLS") {
			t.Fatalf("the trace does not say why: %s", traceText(cd.Trace))
		}
	})
}

// TestSRTPSurvivesHold fails if a hold and resume re-INVITE drops a leg's
// encryption: the answers and the peer's offer must carry the leg's key again
// on the profile it negotiated, or a phone could fall back to clear media.
func TestSRTPSurvivesHold(t *testing.T) {
	pbx := startPBX(t, srtpDevices(secmedia.Optional, secmedia.Optional), withMedia(false), trustLoopback)
	a, b := newSRTPPhone(t, pbx, "a1", "pa", true), newSRTPPhone(t, pbx, "b1", "pb1", true)
	registerOver(t, a.phone, "tls")
	registerOver(t, b.phone, "tls")
	b.setCallee(answerAfter(nil))
	ch := callOver(t, a, "200", "tls")
	waitReq(t, b.invites, "INVITE to b")
	r := waitCall(t, ch)
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	first := cryptoOf(t, r.dcs.InviteResponse.Body())
	res := a.reinviteAsCaller(t, r.dcs, a.sdp+"a=sendonly\r\n")
	if res.StatusCode != 200 {
		t.Fatalf("hold re-INVITE = %d", res.StatusCode)
	}
	held := cryptoOf(t, res.Body())
	if len(held.Crypto) != 1 || held.Crypto[0].Attribute() != first.Crypto[0].Attribute() {
		t.Fatal("the hold answer lost the leg's key or changed it")
	}
	peer := waitReq(t, b.reinvites, "hold re-INVITE to the callee")
	if got := cryptoOf(t, peer.Body()); len(got.Crypto) != 1 || got.SRTPOnly {
		t.Fatalf("the callee's hold offer has %d keys (SAVP %v), want its leg's one key on RTP/AVP", len(got.Crypto), got.SRTPOnly)
	}
	hangup(t, r.dcs)
	if rec := pbx.nextCDR(t); rec.SRTP != cdr.SRTPBoth {
		t.Fatalf("CDR srtp = %q, want both", rec.SRTP)
	}
}
