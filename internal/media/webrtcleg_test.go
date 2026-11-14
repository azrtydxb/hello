package media

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pion/stun/v3"
)

// browserOfferSDP is what a Chrome-style client sends for one audio call:
// SAVPF, BUNDLE, rtcp-mux, ICE credentials and its certificate fingerprint.
func browserOfferSDP(fp Fingerprint, ufrag, pwd string, codecs ...string) []byte {
	pts := make([]string, 0, len(codecs))
	var maps strings.Builder
	for _, c := range codecs {
		switch c {
		case "opus":
			pts = append(pts, "111")
			maps.WriteString("a=rtpmap:111 opus/48000/2\r\na=fmtp:111 minptime=10;useinbandfec=1\r\n")
		case "pcmu":
			pts = append(pts, "0")
			maps.WriteString("a=rtpmap:0 PCMU/8000\r\n")
		case "pcma":
			pts = append(pts, "8")
			maps.WriteString("a=rtpmap:8 PCMA/8000\r\n")
		}
	}
	pts = append(pts, "126")
	return []byte("v=0\r\no=- 4611731400430051336 2 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\na=group:BUNDLE 0\r\n" +
		"m=audio 9 UDP/TLS/RTP/SAVPF " + strings.Join(pts, " ") + "\r\nc=IN IP4 0.0.0.0\r\n" +
		"a=ice-ufrag:" + ufrag + "\r\na=ice-pwd:" + pwd + "\r\na=ice-options:trickle\r\n" +
		"a=fingerprint:" + fp.String() + "\r\na=setup:actpass\r\na=mid:0\r\na=sendrecv\r\na=rtcp-mux\r\n" +
		maps.String() + "a=rtpmap:126 telephone-event/8000\r\n")
}

// webrtcCall is a browser (pion DTLS-SRTP client of the test above) on a mux
// leg and a plain RTP phone on the other leg of one relay.
type webrtcCall struct {
	r       *Relay
	node    *WebRTCNode
	browser *dtlsPhone
	phone   *relayPeer
	answer  *SDPSession
	ufrag   string
	pwd     string
	fp      Fingerprint
	failed  chan string
}

func newWebRTCCall(t *testing.T, claim *Fingerprint, codecs []string, phoneCodecs []string, prefs []string, phonePT uint8) *webrtcCall {
	t.Helper()
	mux := newTestMux(t)
	node := &WebRTCNode{mux: mux, hosts: []string{"127.0.0.1"}}
	r, err := NewRelay(testRelayMin+300, testRelayMin+350)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	portA, err := r.AddLeg("a")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.AddMuxLeg("b", mux); err != nil {
		t.Fatal(err)
	}
	c := &webrtcCall{r: r, node: node, failed: make(chan string, 4)}
	c.browser = newDTLSPhone(t, mux.Port())
	c.phone = startPeer(t, loopback(portA))
	c.phone.only = portA
	r.OnSecureFail(func(leg, reason string) { c.failed <- leg + ":" + reason })

	fp := c.browser.fingerprint()
	if claim != nil {
		fp = *claim
	}
	offer := browserOfferSDP(fp, "brws", "browserpassword0123456789", codecs...)
	body, err := r.AnswerWebRTC("b", SignalingHop{Encrypted: true}, node, offer, func(o AudioSDP) AudioSDP {
		chosen, ok := o.Filter(phoneCodecs).Pick(prefs...)
		if !ok {
			t.Fatal("the phone found no codec")
		}
		return AnswerFor(o, chosen)
	})
	if err != nil {
		t.Fatalf("AnswerWebRTC: %v", err)
	}
	ans, err := ParseSDPSession(body)
	if err != nil {
		t.Fatalf("answer: %v\n%s", err, body)
	}
	if err := ValidateWebRTC(ans); err != nil {
		t.Fatalf("answer fails the WebRTC rules: %v\n%s", err, body)
	}
	c.answer = ans
	m := ans.Media()[0]
	ice := m.ICE(ans)
	c.ufrag, c.pwd = ice.Ufrag, ice.Pwd
	c.fp = mustFP(t, m.DTLS(ans).Fingerprints[0])
	r.SetPayloadTypes("a", phonePT, 101)
	r.SetPayloadTypes("b", phonePT, 126)
	r.SetTarget("a", c.phone.addr())
	r.Start()
	return c
}

// check sends the browser's ICE binding request and expects Hello's success
// response.
func (c *webrtcCall) check(t *testing.T) {
	t.Helper()
	m := stunCheck(t, c.ufrag+":brws", c.pwd, false, true)
	if _, err := c.browser.conn.WriteToUDP(m.Raw, loopback(c.node.mux.Port())); err != nil {
		t.Fatal(err)
	}
	select {
	case raw := <-c.browser.stun:
		resp := &stun.Message{Raw: raw}
		if err := resp.Decode(); err != nil || resp.Type != stun.BindingSuccess {
			t.Fatalf("check answered with %v (%v)", resp.Type, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no answer to the ICE check")
	}
}

func hasOpus() bool { return OpusAvailable }

// TestWebRTCToPhoneAudio fails if a browser leg (ICE-lite on the shared port,
// DTLS-SRTP) and a plain RTP phone cannot exchange audio both ways, with PCMU
// and with Opus, if sequence, timestamp or SSRC are not continuous across the
// relay, or if the browser leg ever carries plaintext: nothing but STUN and
// DTLS before the keys are in, and nothing on the wire in the clear after.
func TestWebRTCToPhoneAudio(t *testing.T) {
	cases := []struct {
		name        string
		codecs      []string
		phone       []string
		prefs       []string
		pt          uint8
		browserPT   uint8
		needsOpus   bool
		wantCodecPT string
	}{
		{"PCMU", []string{"pcmu", "pcma"}, []string{"PCMU", "PCMA"}, []string{"g711"}, 0, 0, false, "PCMU"},
		{"Opus", []string{"opus", "pcmu"}, []string{"opus", "PCMU"}, []string{"opus", "g711"}, 111, 111, true, "opus"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.needsOpus && !hasOpus() {
				t.Skip("this build has no Opus")
			}
			c := newWebRTCCall(t, nil, tc.codecs, tc.phone, tc.prefs, tc.pt)
			// The answer names exactly the phone's codec.
			var got []string
			for _, cd := range c.answer.Media()[0].Codecs() {
				got = append(got, cd.Name)
			}
			if got[0] != tc.wantCodecPT || len(got) != 2 {
				t.Fatalf("answer codecs %v, want %s and telephone-event", got, tc.wantCodecPT)
			}
			if setup := c.answer.Media()[0].DTLS(c.answer).Setup; setup != SetupActive {
				t.Fatalf("Hello answered setup %q to an actpass offer, want active", setup)
			}
			if addrs := c.answer.CandidateAddresses(); len(addrs) != 1 || addrs[0] != "127.0.0.1" {
				t.Fatalf("candidates %v", addrs)
			}

			// Before the keys are in, the leg carries nothing in either direction.
			c.check(t)
			c.phone.send(tc.pt, bytes.Repeat([]byte{0x11}, 20), false)
			select {
			case d := <-c.browser.rtp:
				t.Fatalf("%d bytes of media reached the browser before the DTLS keys", len(d))
			case <-time.After(200 * time.Millisecond):
			}
			if err := c.browser.handshake(false, c.fp); err != nil {
				t.Fatalf("browser handshake: %v", err)
			}
			waitFor(t, "relay to see the browser leg keyed", func() bool {
				return c.r.SecureLegs() == 1
			})

			// Phone to browser: the browser reads SRTP, never the payload.
			var lastSeq uint16
			var ssrc uint32
			for i := 0; i < 6; i++ {
				payload := bytes.Repeat([]byte{byte(0xA0 + i)}, 20)
				c.phone.send(tc.pt, payload, false)
				pkt, wire, ok := c.browser.recvMedia(3 * time.Second)
				if !ok {
					t.Fatalf("packet %d never reached the browser", i)
				}
				if bytes.Contains(wire, payload) {
					t.Fatal("the browser leg carried the payload in the clear")
				}
				if !bytes.Equal(pkt.Payload, payload) || pkt.PayloadType != tc.pt {
					t.Fatalf("browser got pt %d payload %x", pkt.PayloadType, pkt.Payload)
				}
				if i > 0 && (pkt.Sequence != lastSeq+1 || pkt.SSRC != ssrc) {
					t.Fatalf("stream not continuous: seq %d after %d, ssrc %d after %d", pkt.Sequence, lastSeq, pkt.SSRC, ssrc)
				}
				lastSeq, ssrc = pkt.Sequence, pkt.SSRC
			}
			// Browser to phone: what leaves the browser is encrypted, what the
			// phone gets is plain RTP with the relay's own rewrite.
			var pseq uint16
			for i := 0; i < 6; i++ {
				payload := bytes.Repeat([]byte{byte(0xC0 + i)}, 20)
				wire := c.browser.sendMedia(tc.browserPT, payload)
				if bytes.Contains(wire, payload) {
					t.Fatal("the browser's own packet was not encrypted")
				}
				pkt := c.phone.recv(t)
				if !bytes.Equal(pkt.Payload, payload) || pkt.PayloadType != tc.pt {
					t.Fatalf("phone got pt %d payload %x", pkt.PayloadType, pkt.Payload)
				}
				if i > 0 && pkt.Sequence != pseq+1 {
					t.Fatalf("phone stream not continuous: %d after %d", pkt.Sequence, pseq)
				}
				pseq = pkt.Sequence
			}
			// A packet that is not SRTP (a sender in the clear) is not relayed.
			plain := BuildRTP(tc.browserPT, 900, 900, 4242, false, bytes.Repeat([]byte{0xEE}, 20))
			_, _ = c.browser.conn.WriteToUDP(plain, loopback(c.node.mux.Port()))
			// Nothing in the clear may reach the phone.
			_ = c.phone.conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
			buf := make([]byte, 2048)
			for {
				n, _, err := c.phone.conn.ReadFromUDP(buf)
				if err != nil {
					break
				}
				if bytes.Contains(buf[:n], bytes.Repeat([]byte{0xEE}, 20)) {
					t.Fatal("a cleartext packet from the browser was relayed")
				}
			}
			if f := len(c.failed); f != 0 {
				t.Fatalf("a secure failure was reported: %d", f)
			}
		})
	}
}

// TestDTLSFingerprintMismatch fails if a call whose browser presents a
// certificate other than the one in its SDP fingerprint is not failed: the
// handshake must end with reason "fingerprint" and no media may cross in
// either direction.
func TestDTLSFingerprintMismatch(t *testing.T) {
	other, err := newCert()
	if err != nil {
		t.Fatal(err)
	}
	claim := other.fp
	c := newWebRTCCall(t, &claim, []string{"pcmu"}, []string{"PCMU"}, []string{"g711"}, 0)
	c.check(t)
	done := make(chan error, 1)
	go func() { done <- c.browser.handshake(false, c.fp) }()
	select {
	case got := <-c.failed:
		if got != "b:fingerprint" {
			t.Fatalf("failure %q, want b:fingerprint", got)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("a certificate that does not match the SDP fingerprint was accepted")
	}
	if err := <-done; err == nil {
		t.Error("the browser side finished a handshake the anchor refused")
	}
	if c.r.SecureLegs() != 0 {
		t.Error("the leg counts as encrypted after a failed handshake")
	}
	c.phone.send(0, bytes.Repeat([]byte{0x33}, 20), false)
	select {
	case d := <-c.browser.rtp:
		t.Fatalf("%d bytes of media reached a browser whose handshake failed", len(d))
	case <-time.After(300 * time.Millisecond):
	}
}

// TestAnswerWebRTCRefusesCleartextHop fails if an offer that arrived over a
// cleartext edge-to-node hop is answered while the operator has not allowed
// it, or if an encrypted hop or an explicit allowance is refused. The refusal
// happens before the leg is touched: the answer is empty and no handshake
// starts.
func TestAnswerWebRTCRefusesCleartextHop(t *testing.T) {
	for hop, want := range map[SignalingHop]bool{
		{}:                                      false,
		{Encrypted: true}:                       true,
		{AllowCleartext: true}:                  true,
		{Encrypted: true, AllowCleartext: true}: true,
	} {
		if err := hop.Check(); (err == nil) != want {
			t.Errorf("%+v.Check() = %v, want allowed=%v", hop, err, want)
		}
	}

	mux := newTestMux(t)
	node := &WebRTCNode{mux: mux, hosts: []string{"127.0.0.1"}}
	r, err := NewRelay(testRelayMin+360, testRelayMin+380)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	if _, _, err := r.AddMuxLeg("b", mux); err != nil {
		t.Fatal(err)
	}
	browser := newDTLSPhone(t, mux.Port())
	offer := browserOfferSDP(browser.fingerprint(), "brws", "browserpassword0123456789", "111")
	called := false
	body, err := r.AnswerWebRTC("b", SignalingHop{}, node, offer, func(o AudioSDP) AudioSDP {
		called = true
		return AudioSDP{}
	})
	if !errors.Is(err, ErrCleartextHop) || body != nil {
		t.Fatalf("an offer over a cleartext hop: body %q err %v, want ErrCleartextHop", body, err)
	}
	if called {
		t.Error("the codec choice ran for an offer that was refused")
	}
	if !strings.Contains(err.Error(), "HELLO_MEDIA_WEBRTC_ALLOW_CLEARTEXT_HOP") || !strings.Contains(err.Error(), "TLS hop") {
		t.Errorf("the refusal does not say what to do: %v", err)
	}
}
