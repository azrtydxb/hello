package sip

import (
	"bytes"
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/secmedia"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/pion/stun/v3"
)

// dtlsPhone is a test phone that speaks DTLS-SRTP: a media socket read by a
// media.DTLSEndpoint, and an SDP on UDP/TLS/RTP/SAVP with the endpoint's
// fingerprint and the given setup role (plus any extra attribute lines).
type dtlsPhone struct {
	*srtpPhone
	ep *media.DTLSEndpoint
}

func newDTLSSIPPhone(t *testing.T, pbx *testPBX, user, pass, setup string, extra ...string) *dtlsPhone {
	t.Helper()
	sp := newSRTPPhone(t, pbx, user, pass, false)
	ep, err := media.NewDTLSEndpoint(sp.sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ep.Close)
	sp.sdp = "v=0\r\no=" + user + " 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
		"m=audio " + strconv.Itoa(sp.sock.LocalAddr().(*net.UDPAddr).Port) + " UDP/TLS/RTP/SAVP 0 101\r\n" +
		"a=rtpmap:101 telephone-event/8000\r\na=fingerprint:" + ep.Fingerprint().String() + "\r\na=setup:" + setup + "\r\na=rtcp-mux\r\n" +
		strings.Join(extra, "")
	return &dtlsPhone{srtpPhone: sp, ep: ep}
}

// handshake runs the phone's side of the handshake against the anchor leg the
// SDP in body names, as the client or the server, checking the fingerprint
// the SDP carries.
func (d *dtlsPhone) handshake(t *testing.T, body []byte, client bool) {
	t.Helper()
	a := cryptoOf(t, body)
	if !a.OffersDTLS() {
		t.Fatalf("the SDP carries no DTLS offer:\n%s", body)
	}
	peer := &net.UDPAddr{IP: net.ParseIP(a.Address), Port: a.Port}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	if err := d.ep.Handshake(ctx, client, peer, a.DTLS.Fingerprints...); err != nil {
		t.Fatalf("%s handshake: %v", d.sock.LocalAddr(), err)
	}
}

func dtlsDevices(a, b secmedia.Policy, keyExB string, iceB bool) []snapshot.Device {
	ds := srtpDevices(a, b)
	ds[1].SRTPKeyEx, ds[1].MediaICE = keyExB, iceB
	return ds
}

// TestDTLSCallThroughTheAnchor fails if a call between two TLS phones that do
// DTLS-SRTP is not encrypted on both legs, if audio does not survive the
// anchor, if the callee device set to dtls is not offered DTLS (a device set
// to sdes still gets SDES), or if the CDR, the metrics and the trace do not
// say what was used. Mutation: not installing the handshake's keys, or
// offering SDES to a dtls device, fails the matching case.
func TestDTLSCallThroughTheAnchor(t *testing.T) {
	t.Run("DTLS on both legs", func(t *testing.T) {
		pbx := startPBX(t, dtlsDevices(secmedia.Optional, secmedia.Optional, secmedia.KeyExDTLS, false), withMedia(false), trustLoopback)
		a := newDTLSSIPPhone(t, pbx, "a1", "pa", "actpass")
		b := newDTLSSIPPhone(t, pbx, "b1", "pb1", "active")
		registerOver(t, a.phone, "tls")
		registerOver(t, b.phone, "tls")
		b.setCallee(answerAfter(nil))
		ch := callOver(t, a.srtpPhone, "200", "tls")
		inv := waitReq(t, b.invites, "INVITE to b")
		r := waitCall(t, ch)
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		offB := cryptoOf(t, inv.Body())
		if !offB.OffersDTLS() || offB.DTLS.Setup != "actpass" || len(offB.Crypto) != 0 || !offB.RTCPMux {
			t.Fatalf("the callee device set to dtls was not offered DTLS (actpass, mux, no crypto):\n%s", inv.Body())
		}
		ansA := cryptoOf(t, r.dcs.InviteResponse.Body())
		if !ansA.OffersDTLS() || ansA.Proto != "UDP/TLS/RTP/SAVP" || ansA.DTLS.Setup != "active" || !ansA.RTCPMux {
			t.Fatalf("the caller was not answered with DTLS (active, mux):\n%s", r.dcs.InviteResponse.Body())
		}
		if len(ansA.DTLS.Fingerprints) != 1 || string(ansA.DTLS.Fingerprints[0].Sum) != string(offB.DTLS.Fingerprints[0].Sum) {
			t.Fatal("both legs should present the node's certificate")
		}
		// Hello is the DTLS client on the caller leg (the phone offered
		// actpass, Hello answered active) and the server on the callee leg
		// (the phone answered active to Hello's actpass).
		done := make(chan struct{}, 2)
		go func() { a.handshake(t, r.dcs.InviteResponse.Body(), false); done <- struct{}{} }()
		go func() { b.handshake(t, inv.Body(), true); done <- struct{}{} }()
		for range 2 {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("handshakes did not finish")
			}
		}
		eventually(t, "both legs to be encrypted", func() bool { return pbx.metric(t, "hello_srtp_legs", nil) == 2 })

		aKeys, _ := a.ep.Keys()
		bKeys, _ := b.ep.Keys()
		toA := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: ansA.Port}
		var got []byte
		for i := 0; i < 20 && got == nil; i++ {
			wire, err := aKeys.Protect(rtpPacket(uint16(i+1), 0x5a))
			if err != nil {
				t.Fatal(err)
			}
			_, _ = a.sock.WriteToUDP(wire, toA)
			select {
			case raw := <-b.ep.Datagrams():
				got = raw
			case <-time.After(200 * time.Millisecond):
			}
		}
		if got == nil {
			t.Fatal("no audio reached the callee")
		}
		if bytes.Contains(got, bytes.Repeat([]byte{0x5a}, 32)) {
			t.Fatal("the callee received clear audio on a DTLS-SRTP leg")
		}
		plain, err := bKeys.Unprotect(got)
		if err != nil {
			t.Fatalf("the callee cannot decrypt: %v", err)
		}
		if pkt, ok := media.ParseRTP(plain); !ok || !bytes.Equal(pkt.Payload, bytes.Repeat([]byte{0x5a}, 160)) {
			t.Fatal("the audio changed on the way")
		}
		if v := pbx.metric(t, "hello_dtls_handshakes_total", map[string]string{"result": "ok"}); v != 2 {
			t.Fatalf("hello_dtls_handshakes_total{ok} = %v, want 2", v)
		}
		if v := pbx.metric(t, "hello_srtp_negotiations_total", map[string]string{"result": "dtls"}); v != 2 {
			t.Fatalf("dtls negotiations = %v, want 2", v)
		}
		hangup(t, r.dcs)
		rec := pbx.nextCDR(t)
		if rec.SRTP != cdr.SRTPBoth || rec.SRTPKeyEx != cdr.KeyExDTLS || rec.ICE {
			t.Fatalf("CDR srtp %q keyex %q ice %v, want both dtls false", rec.SRTP, rec.SRTPKeyEx, rec.ICE)
		}
		if tr := traceText(rec.Trace); !strings.Contains(tr, "SRTP on the caller leg") || !strings.Contains(tr, "DTLS") {
			t.Fatalf("the trace does not say DTLS was used: %s", tr)
		}
		eventually(t, "the SRTP legs gauge to drop", func() bool { return pbx.metric(t, "hello_srtp_legs", nil) == 0 })
	})

	t.Run("a DTLS caller to an SDES device is mixed", func(t *testing.T) {
		pbx := startPBX(t, dtlsDevices(secmedia.Optional, secmedia.Optional, secmedia.KeyExSDES, false), withMedia(false), trustLoopback)
		a := newDTLSSIPPhone(t, pbx, "a1", "pa", "actpass")
		b := newSRTPPhone(t, pbx, "b1", "pb1", true)
		registerOver(t, a.phone, "tls")
		registerOver(t, b.phone, "tls")
		b.setCallee(answerAfter(nil))
		ch := callOver(t, a.srtpPhone, "200", "tls")
		inv := waitReq(t, b.invites, "INVITE to b")
		r := waitCall(t, ch)
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		offB := cryptoOf(t, inv.Body())
		if offB.OffersDTLS() || len(offB.Crypto) != 2 {
			t.Fatalf("an sdes device was not offered SDES:\n%s", inv.Body())
		}
		a.handshake(t, r.dcs.InviteResponse.Body(), false)
		eventually(t, "both legs to be encrypted", func() bool { return pbx.metric(t, "hello_srtp_legs", nil) == 2 })
		hangup(t, r.dcs)
		if rec := pbx.nextCDR(t); rec.SRTPKeyEx != cdr.KeyExMixed || rec.SRTP != cdr.SRTPBoth {
			t.Fatalf("CDR keyex %q srtp %q, want mixed both", rec.SRTPKeyEx, rec.SRTP)
		}
	})

	t.Run("a handshake that never happens ends the call", func(t *testing.T) {
		defer media.SetDTLSTimeout(700 * time.Millisecond)()
		pbx := startPBX(t, dtlsDevices(secmedia.Optional, secmedia.Optional, secmedia.KeyExDTLS, false), withMedia(false), trustLoopback)
		a := newDTLSSIPPhone(t, pbx, "a1", "pa", "actpass")
		b := newDTLSSIPPhone(t, pbx, "b1", "pb1", "active")
		registerOver(t, a.phone, "tls")
		registerOver(t, b.phone, "tls")
		b.setCallee(answerAfter(nil))
		ch := callOver(t, a.srtpPhone, "200", "tls")
		waitReq(t, b.invites, "INVITE to b")
		r := waitCall(t, ch)
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		// Neither phone starts a handshake: Hello's ClientHello to the
		// caller goes unanswered and nothing arrives from the callee.
		waitReq(t, b.byes, "BYE to the callee after the failed handshake")
		rec := pbx.nextCDR(t)
		if !strings.Contains(traceText(rec.Trace), "DTLS-SRTP handshake failed") {
			t.Fatalf("the trace does not say why: %s", traceText(rec.Trace))
		}
		if v := pbx.metric(t, "hello_dtls_handshakes_total", map[string]string{"result": "timeout"}); v < 1 {
			t.Fatalf("no timeout counted (%v)", v)
		}
		if v := pbx.metric(t, "hello_srtp_negotiations_total", map[string]string{"result": "dtls_failed"}); v < 1 {
			t.Fatalf("no failed negotiation counted (%v)", v)
		}
	})
}

// TestDTLSPolicyRefusals fails if a DTLS offer is carried where it must not
// be: on a UDP leg, with the policy off, on a node with DTLS off, or without a
// fingerprint. Mutation: ignoring the leg in srtpGate fails the first two.
func TestDTLSPolicyRefusals(t *testing.T) {
	for _, tc := range []struct {
		name      string
		caller    secmedia.Policy
		transport string
		node      bool // DTLS enabled on the node
		edit      func(string) string
	}{
		{"a DTLS offer on a UDP leg", secmedia.Optional, "udp", true, nil},
		{"a DTLS offer with the policy off", secmedia.Off, "tls", true, nil},
		{"a DTLS offer on a node with DTLS off", secmedia.Optional, "tls", false, nil},
		{"a DTLS transport without a fingerprint", secmedia.Optional, "tls", true, func(s string) string {
			var out []string
			for _, l := range strings.Split(s, "\r\n") {
				if !strings.HasPrefix(l, "a=fingerprint") {
					out = append(out, l)
				}
			}
			return strings.Join(out, "\r\n")
		}},
	} {
		t.Run(tc.name+" is refused with 488", func(t *testing.T) {
			if !tc.node {
				media.SetFeatures(false, true)
				defer media.SetFeatures(true, true)
			}
			pbx := startPBX(t, srtpDevices(tc.caller, secmedia.Optional), withMedia(false), trustLoopback)
			a := newDTLSSIPPhone(t, pbx, "a1", "pa", "actpass")
			b := newSRTPPhone(t, pbx, "b1", "pb1", true)
			registerOver(t, a.phone, tc.transport)
			registerOver(t, b.phone, "tls")
			req := viaEdge(a.inviteReq("200"), tc.transport)
			if tc.edit != nil {
				req.SetBody([]byte(tc.edit(a.sdp)))
			}
			_, err := a.callReq(t.Context(), req)
			if responseCode(err) != 488 {
				t.Fatalf("call = %v, want 488", err)
			}
			noReq(t, b.invites, 300*time.Millisecond, "INVITE to the callee of a refused call")
			if rec := pbx.nextCDR(t); rec.FinalStatus != 488 || rec.SRTPKeyEx != cdr.KeyExNone {
				t.Fatalf("CDR = %d, keyex %q", rec.FinalStatus, rec.SRTPKeyEx)
			}
		})
	}

	t.Run("a required caller that offers DTLS on TLS goes through", func(t *testing.T) {
		pbx := startPBX(t, srtpDevices(secmedia.Required, secmedia.Optional), withMedia(false), trustLoopback)
		a := newDTLSSIPPhone(t, pbx, "a1", "pa", "actpass")
		b := newSRTPPhone(t, pbx, "b1", "pb1", false)
		registerOver(t, a.phone, "tls")
		registerOver(t, b.phone, "tls")
		b.setCallee(answerAfter(nil))
		ch := callOver(t, a.srtpPhone, "200", "tls")
		waitReq(t, b.invites, "INVITE to b")
		r := waitCall(t, ch)
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		a.handshake(t, r.dcs.InviteResponse.Body(), false)
		hangup(t, r.dcs)
		pbx.nextCDR(t)
	})

	t.Run("a required callee is offered DTLS and a plain answer ends the call", func(t *testing.T) {
		pbx := startPBX(t, dtlsDevices(secmedia.Optional, secmedia.Required, secmedia.KeyExDTLS, false), withMedia(false), trustLoopback)
		a := newDTLSSIPPhone(t, pbx, "a1", "pa", "actpass")
		b := newSRTPPhone(t, pbx, "b1", "pb1", false)
		registerOver(t, a.phone, "tls")
		registerOver(t, b.phone, "tls")
		b.setCallee(answerAfter(nil))
		ch := callOver(t, a.srtpPhone, "200", "tls")
		inv := waitReq(t, b.invites, "INVITE to b")
		if off := cryptoOf(t, inv.Body()); !off.OffersDTLS() {
			t.Fatalf("a required dtls callee was not offered DTLS:\n%s", inv.Body())
		}
		r := waitCall(t, ch)
		if responseCode(r.err) != 488 {
			t.Fatalf("call = %v, want 488 (the callee answered without DTLS)", r.err)
		}
		waitReq(t, b.byes, "BYE to the callee that answered in the clear")
		pbx.nextCDR(t)
	})
}

// TestICELiteCall fails if a phone that offers ICE is not answered as
// ICE-lite, if a callee device with the ICE setting is not offered it, if the
// anchor does not answer a connectivity check on the leg, or if the CDR does
// not say ICE was used.
func TestICELiteCall(t *testing.T) {
	pbx := startPBX(t, dtlsDevices(secmedia.Optional, secmedia.Optional, secmedia.KeyExSDES, true), withMedia(false), trustLoopback)
	a, b := newSRTPPhone(t, pbx, "a1", "pa", false), newSRTPPhone(t, pbx, "b1", "pb1", false)
	ufrag, pwd := "callerUf", "callerPasswordAtLeast22Chars"
	a.sdp += "a=ice-ufrag:" + ufrag + "\r\na=ice-pwd:" + pwd + "\r\na=rtcp-mux\r\n" +
		"a=candidate:1 1 udp 2130706431 127.0.0.1 " + strconv.Itoa(a.sock.LocalAddr().(*net.UDPAddr).Port) + " typ host\r\n"
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
	if offB.ICE == nil || !offB.ICE.Lite || offB.ICE.Ufrag == ufrag || len(offB.ICE.Candidates) != 1 {
		t.Fatalf("the callee device with ICE was not offered ICE-lite with its own credentials: %+v", offB.ICE)
	}
	ansA := cryptoOf(t, r.dcs.InviteResponse.Body())
	if ansA.ICE == nil || !ansA.ICE.Lite || ansA.ICE.Ufrag == offB.ICE.Ufrag || ansA.ICE.Pwd == offB.ICE.Pwd || !ansA.RTCPMux {
		t.Fatalf("the caller was not answered as ICE-lite with credentials of its own leg: %+v", ansA.ICE)
	}
	if strings.Contains(string(r.dcs.InviteResponse.Body()), pwd) {
		t.Fatal("the phone's own ICE password was echoed")
	}
	// A connectivity check from the phone is answered by the anchor leg.
	check := stunBindingRequest(t, ansA.ICE.Ufrag+":"+ufrag, ansA.ICE.Pwd)
	_, _ = a.sock.WriteToUDP(check, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: ansA.Port})
	resp, ok := readUDPWithin(t, a.sock, 2*time.Second)
	if !ok || len(resp) < 20 || resp[0] != 0x01 || resp[1] != 0x01 {
		t.Fatalf("no binding success from the anchor (%v, % x)", ok, resp)
	}
	if v := pbx.metric(t, "hello_ice_checks_total", map[string]string{"result": "ok"}); v != 1 {
		t.Fatalf("ice checks = %v, want 1", v)
	}
	hangup(t, r.dcs)
	if rec := pbx.nextCDR(t); !rec.ICE || rec.SRTP != cdr.SRTPNone {
		t.Fatalf("CDR ice %v srtp %q, want ice and no srtp", rec.ICE, rec.SRTP)
	}
}

// stunBindingRequest is an ICE connectivity check with the given USERNAME
// ("local:remote" ufrags) authenticated with pwd.
func stunBindingRequest(t *testing.T, user, pwd string) []byte {
	t.Helper()
	m, err := stun.Build(stun.TransactionID, stun.BindingRequest, stun.NewUsername(user),
		stun.RawAttribute{Type: stun.AttrICEControlling, Value: []byte{1, 2, 3, 4, 5, 6, 7, 8}},
		stun.NewShortTermIntegrity(pwd), stun.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	return m.Raw
}

// TestDTLSVoicemail fails if a phone that does DTLS-SRTP cannot leave a
// voicemail: the answer must be DTLS (the voicemail session shares the
// handshake code of the relay legs), the greeting must be SRTP under the keys
// of the handshake, and the CDR must say DTLS. Mutation: answering voicemail
// with plain RTP, or without the handshake's keys, fails it.
func TestDTLSVoicemail(t *testing.T) {
	vm := newVMStore()
	vm.boxes["200"] = VoicemailBox{ID: 5, Extension: "200"}
	feats := func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{{Number: "200", VoicemailEnabled: true, VoicemailBoxID: 5}})
	}
	pbx := startPBX(t, srtpDevices(secmedia.Optional, secmedia.Optional), shortRing(200*time.Millisecond),
		withFeatures(feats), withVoicemail(vm, &fakeObjects{}), withMedia(false), trustLoopback)
	anchored(pbx)
	a := newDTLSSIPPhone(t, pbx, "a1", "pa", "actpass")
	b := newSRTPPhone(t, pbx, "b1", "pb1", true)
	registerOver(t, a.phone, "tls")
	registerOver(t, b.phone, "tls")
	b.setCallee(ringForever())
	r := waitCall(t, callOver(t, a.srtpPhone, "200", "tls"))
	if r.err != nil {
		t.Fatalf("voicemail call: %v", r.err)
	}
	ans := cryptoOf(t, r.dcs.InviteResponse.Body())
	if !ans.OffersDTLS() || ans.DTLS.Setup != "active" {
		t.Fatalf("voicemail did not answer with DTLS:\n%s", r.dcs.InviteResponse.Body())
	}
	a.handshake(t, r.dcs.InviteResponse.Body(), false)
	keys, _ := a.ep.Keys()
	select {
	case raw := <-a.ep.Datagrams():
		if _, err := keys.Unprotect(raw); err != nil {
			t.Fatalf("the greeting is not SRTP under the handshake's keys: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no greeting audio")
	}
	hangup(t, r.dcs)
	if rec := pbx.nextCDR(t); rec.SRTP != cdr.SRTPCaller || rec.SRTPKeyEx != cdr.KeyExDTLS {
		t.Fatalf("CDR srtp %q keyex %q, want caller dtls", rec.SRTP, rec.SRTPKeyEx)
	}
}
