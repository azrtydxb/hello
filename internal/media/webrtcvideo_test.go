package media

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/stun/v3"
)

// chromeVideoOffer is a Chrome-style offer with audio and video: H.264 in two
// packetization modes and a high profile, VP8, retransmission and FEC types,
// transport-wide congestion control and abs-send-time (which the relay must not
// carry), BUNDLE and rtcp-mux.
func chromeVideoOffer(fp Fingerprint, ufrag, pwd string, videoFirst bool) []byte {
	common := "c=IN IP4 0.0.0.0\r\na=ice-ufrag:" + ufrag + "\r\na=ice-pwd:" + pwd + "\r\na=fingerprint:" + fp.String() +
		"\r\na=setup:actpass\r\na=rtcp-mux\r\n"
	audio := "m=audio 9 UDP/TLS/RTP/SAVPF 111 0 126\r\n" + common + "a=mid:0\r\na=sendrecv\r\n" +
		"a=rtpmap:111 opus/48000/2\r\na=fmtp:111 minptime=10;useinbandfec=1\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:126 telephone-event/8000\r\n"
	video := "m=video 9 UDP/TLS/RTP/SAVPF 96 97 98 99 100 101 102\r\n" + common + "a=mid:1\r\na=sendrecv\r\n" +
		"a=extmap:2 http://www.webrtc.org/experiments/rtp-hdrext/abs-send-time\r\n" +
		"a=extmap:3 http://www.ietf.org/id/draft-holmer-rmcat-transport-wide-cc-extensions-01\r\n" +
		"a=rtpmap:96 VP8/90000\r\na=rtcp-fb:96 goog-remb\r\na=rtcp-fb:96 transport-cc\r\na=rtcp-fb:96 ccm fir\r\na=rtcp-fb:96 nack\r\na=rtcp-fb:96 nack pli\r\n" +
		"a=rtpmap:97 rtx/90000\r\na=fmtp:97 apt=96\r\n" +
		"a=rtpmap:98 H264/90000\r\na=rtcp-fb:98 goog-remb\r\na=rtcp-fb:98 transport-cc\r\na=rtcp-fb:98 ccm fir\r\na=rtcp-fb:98 nack\r\na=rtcp-fb:98 nack pli\r\n" +
		"a=fmtp:98 level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f\r\n" +
		"a=rtpmap:99 H264/90000\r\na=fmtp:99 level-asymmetry-allowed=1;packetization-mode=0;profile-level-id=42e01f\r\n" +
		"a=rtpmap:100 H264/90000\r\na=fmtp:100 level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=640c1f\r\n" +
		"a=rtpmap:101 red/90000\r\na=rtpmap:102 ulpfec/90000\r\n"
	order := audio + video
	group := "a=group:BUNDLE 0 1\r\n"
	if videoFirst {
		order = video + audio
		group = "a=group:BUNDLE 1 0\r\n"
	}
	return []byte("v=0\r\no=- 4611731400430051336 2 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\n" + group + order)
}

func parse(t *testing.T, body []byte) *SDPSession {
	t.Helper()
	s, err := ParseSDPSession(body)
	if err != nil {
		t.Fatalf("%v\n%s", err, body)
	}
	return s
}

// TestVideoCodecChoice fails if the relay offers or accepts a codec it cannot
// relay: H.264 Constrained Baseline in packetization mode 1 comes first, VP8
// second, and the high profile, mode 0, retransmission, FEC and H.265 are
// never chosen; transport-cc is dropped from the feedback it carries.
func TestVideoCodecChoice(t *testing.T) {
	offer, ok := ParseVideoOffer(chromeVideoOffer(mustCert(t).fp, "abcd", "browserpassword0123456789", false))
	if !ok {
		t.Fatal("no video section parsed")
	}
	got := SupportedVideo(offer.Codecs)
	if len(got) != 2 || got[0].Name != "H264" || got[0].PT != 98 || got[1].Name != "VP8" || got[1].PT != 96 {
		t.Fatalf("supported = %+v, want H264 pt 98 then VP8 pt 96", got)
	}
	for _, f := range got[0].Feedback {
		if strings.Contains(f, "transport-cc") {
			t.Fatalf("transport-cc survived: %v", got[0].Feedback)
		}
	}
	if v := LegFor(got[0]); !v.FB.NACK || !v.FB.PLI || !v.FB.FIR || !v.FB.REMB || v.Codec != "H264" {
		t.Fatalf("leg = %+v", v)
	}
	for name, codecs := range map[string][]SDPCodec{
		"vp8 only":    {{PT: 96, Name: "VP8", Rate: 90000}},
		"h264 pm0":    {{PT: 99, Name: "H264", Rate: 90000, Fmtp: "packetization-mode=0;profile-level-id=42e01f"}},
		"high":        {{PT: 100, Name: "H264", Rate: 90000, Fmtp: "packetization-mode=1;profile-level-id=640c1f"}},
		"h265":        {{PT: 104, Name: "H265", Rate: 90000}},
		"rtx and fec": {{PT: 97, Name: "rtx", Rate: 90000, Fmtp: "apt=96"}, {PT: 102, Name: "ulpfec", Rate: 90000}},
	} {
		s := SupportedVideo(codecs)
		switch name {
		case "vp8 only":
			if len(s) != 1 || s[0].Name != "VP8" {
				t.Errorf("%s: %+v", name, s)
			}
		default:
			if len(s) != 0 {
				t.Errorf("%s: relayed %+v", name, s)
			}
		}
	}
	if !SameVideoCodec(SDPCodec{Name: "H264", Fmtp: "packetization-mode=1;profile-level-id=42e01f"}, SDPCodec{Name: "h264", Fmtp: "profile-level-id=42c01f;packetization-mode=1"}) {
		t.Error("two constrained baseline H.264 descriptions differ")
	}
	if SameVideoCodec(SDPCodec{Name: "H264", Fmtp: "packetization-mode=1;profile-level-id=42e01f"}, SDPCodec{Name: "VP8"}) {
		t.Error("H.264 equals VP8")
	}
}

func mustCert(t *testing.T) *Cert {
	t.Helper()
	c, err := newCert()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// videoCall is two browsers (pion DTLS-SRTP stand-ins) on two mux legs of one
// relay, the caller's offer answered with the video the callee chose.
type videoCall struct {
	r       *Relay
	node    *WebRTCNode
	a, b    *dtlsPhone
	ansA    *SDPSession
	offerB  *SDPSession
	fpA     Fingerprint
	ufragA  string
	pwdA    string
	ufragB  string
	pwdB    string
	helloA  struct{ ufrag, pwd string }
	helloB  struct{ ufrag, pwd string }
	chosenA SDPCodec
	chosenB SDPCodec
}

// newVideoCall runs the negotiation the call path does: the caller offers
// audio and video, the callee is offered the supported codecs in the caller's
// numbering, answers with H.264 on its own payload type, and the caller's
// answer carries the same codec on the caller's.
func newVideoCall(t *testing.T, calleeVideo bool) *videoCall {
	t.Helper()
	mux := newTestMux(t)
	node := &WebRTCNode{mux: mux, hosts: []string{"127.0.0.1", "192.0.2.7"}}
	r, err := NewRelay(testRelayMin+400, testRelayMin+450)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	if _, _, err := r.AddMuxLeg("a", mux); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.AddMuxLeg("b", mux); err != nil {
		t.Fatal(err)
	}
	c := &videoCall{r: r, node: node, a: newDTLSPhone(t, mux.Port()), b: newDTLSPhone(t, mux.Port())}
	c.fpA, c.ufragA, c.pwdA = c.a.fingerprint(), "aaaa", "browserApassword0123456789"
	offerA := chromeVideoOffer(c.fpA, c.ufragA, c.pwdA, false)

	// The caller leg is prepared and its audio answered like the call path does.
	if err := r.PrepareWebRTC("a", node, offerA); err != nil {
		t.Fatal(err)
	}
	offAudio, err := ParseAudioSDP(offerA)
	if err != nil {
		t.Fatal(err)
	}
	vo, _ := ParseVideoOffer(offerA)
	cands := SupportedVideo(vo.Codecs)

	// The callee leg: offered the audio and video.
	if err := r.PrepareWebRTCOffer("b", node); err != nil {
		t.Fatal(err)
	}
	bAudio := offAudio.Filter([]string{"PCMU"}).PlainOffer()
	bOffer, err := r.OfferSDP("b", bAudio, WebPolicy())
	if err != nil {
		t.Fatal(err)
	}
	r.SetVideoOffer("b", cands, "sendrecv", 1500)
	body := bOffer.Build("127.0.0.1", mux.Port(), "sendrecv")
	body, err = r.AppendVideo("b", body)
	if err != nil {
		t.Fatal(err)
	}
	c.offerB = parse(t, body)
	if err := ValidateWebRTC(c.offerB); err != nil {
		t.Fatalf("offer to the callee: %v\n%s", err, body)
	}
	ice := c.offerB.Media()[0].ICE(c.offerB)
	c.helloB.ufrag, c.helloB.pwd = ice.Ufrag, ice.Pwd
	c.ufragB, c.pwdB = "bbbb", "browserBpassword0123456789"

	// The callee answers with H.264 on payload type 102 (its own numbering).
	vid := ""
	if calleeVideo {
		vid = "m=video 9 UDP/TLS/RTP/SAVPF 102\r\nc=IN IP4 0.0.0.0\r\na=ice-ufrag:" + c.ufragB + "\r\na=ice-pwd:" + c.pwdB +
			"\r\na=fingerprint:" + c.b.fingerprint().String() + "\r\na=setup:active\r\na=mid:1\r\na=sendrecv\r\na=rtcp-mux\r\n" +
			"a=rtpmap:102 H264/90000\r\na=fmtp:102 level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f\r\n" +
			"a=rtcp-fb:102 goog-remb\r\na=rtcp-fb:102 ccm fir\r\na=rtcp-fb:102 nack\r\na=rtcp-fb:102 nack pli\r\n"
	} else {
		vid = "m=video 0 UDP/TLS/RTP/SAVPF 98\r\na=mid:1\r\n"
	}
	ansB := []byte("v=0\r\no=- 1 2 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\na=group:BUNDLE 0 1\r\n" +
		"m=audio 9 UDP/TLS/RTP/SAVPF 0 126\r\nc=IN IP4 0.0.0.0\r\na=ice-ufrag:" + c.ufragB + "\r\na=ice-pwd:" + c.pwdB +
		"\r\na=fingerprint:" + c.b.fingerprint().String() + "\r\na=setup:active\r\na=mid:0\r\na=sendrecv\r\na=rtcp-mux\r\n" +
		"a=rtpmap:0 PCMU/8000\r\na=rtpmap:126 telephone-event/8000\r\n" + vid)
	ansBAudio, err := ParseAudioSDP(ansB)
	if err != nil {
		t.Fatal(err)
	}
	if !r.LearnAnswer("b", ansBAudio, true) {
		t.Fatal("the callee's answer did not key the leg")
	}
	var bVideo *SDPCodec
	if calleeVideo {
		vb, ok := ParseVideoOffer(ansB)
		if !ok {
			t.Fatal("callee answer has no video")
		}
		for _, oc := range cands {
			if SameVideoCodec(oc, vb.Codecs[0]) {
				cc := vb.Codecs[0]
				bVideo = &cc
				c.chosenB = cc
				// The caller's answer: the callee's choice in the caller's numbering.
				ca := oc
				ca.Fmtp = vb.Codecs[0].Fmtp
				ca.Feedback = intersect(oc.Feedback, vb.Codecs[0].Feedback)
				c.chosenA = ca
			}
		}
		if bVideo == nil {
			t.Fatal("the callee's video codec is not one that was offered")
		}
	}
	r.SetVideoSection("b", bVideo, "sendrecv", 1500)

	// The caller's answer.
	ans, err := r.AnswerSDP("a", offAudio, offAudio.Filter([]string{"PCMU"}), WebPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if bVideo != nil {
		ca := c.chosenA
		r.SetVideoSection("a", &ca, "sendrecv", 1500)
	} else {
		r.SetVideoSection("a", nil, "sendrecv", 0)
	}
	ansBody := ans.Build("127.0.0.1", mux.Port(), "sendrecv")
	ansBody, err = r.AppendVideo("a", ansBody)
	if err != nil {
		t.Fatal(err)
	}
	c.ansA = parse(t, ansBody)
	if err := ValidateWebRTC(c.ansA); err != nil {
		t.Fatalf("answer to the caller: %v\n%s", err, ansBody)
	}
	ice = c.ansA.Media()[0].ICE(c.ansA)
	c.helloA.ufrag, c.helloA.pwd = ice.Ufrag, ice.Pwd
	r.SetPayloadTypes("a", 0, 126)
	r.SetPayloadTypes("b", 0, 126)
	r.Start()
	return c
}

func intersect(a, b []string) []string {
	var out []string
	for _, x := range a {
		for _, y := range b {
			if x == y {
				out = append(out, x)
			}
		}
	}
	return out
}

// connect runs ICE and DTLS for both browsers: A is the DTLS client of Hello's
// answer (Hello answered active... the stand-in picks the role the SDP says),
// B answered active so it is the client.
func (c *videoCall) connect(t *testing.T) {
	t.Helper()
	for _, x := range []struct {
		ph                     *dtlsPhone
		ufrag, pwd, remoteUser string
	}{
		{c.a, c.helloA.ufrag, c.helloA.pwd, c.ufragA},
		{c.b, c.helloB.ufrag, c.helloB.pwd, c.ufragB},
	} {
		m := stunCheck(t, x.ufrag+":"+x.remoteUser, x.pwd, false, true)
		if _, err := x.ph.conn.WriteToUDP(m.Raw, loopback(c.node.mux.Port())); err != nil {
			t.Fatal(err)
		}
		select {
		case raw := <-x.ph.stun:
			resp := &stun.Message{Raw: raw}
			if err := resp.Decode(); err != nil || resp.Type != stun.BindingSuccess {
				t.Fatalf("ICE check answered with %v (%v)", resp.Type, err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("no answer to the ICE check")
		}
	}
	fpHelloA := mustFP(t, c.ansA.Media()[0].DTLS(c.ansA).Fingerprints[0])
	fpHelloB := mustFP(t, c.offerB.Media()[0].DTLS(c.offerB).Fingerprints[0])
	// A offered actpass and Hello answered active: Hello is the client, so A is
	// the DTLS server. B answered active to Hello's actpass: B is the client.
	errs := make(chan error, 2)
	go func() { errs <- c.a.handshake(false, fpHelloA) }()
	go func() { errs <- c.b.handshake(true, fpHelloB) }()
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("handshake: %v", err)
		}
	}
	waitFor(t, "both browser legs keyed", func() bool { return c.r.SecureLegs() == 2 })
}

// sendRTP protects and sends one RTP packet from the browser.
func (p *dtlsPhone) sendRTP(plain []byte) {
	p.t.Helper()
	p.mu.Lock()
	enc := p.enc
	p.mu.Unlock()
	wire, err := enc.EncryptRTP(nil, plain, nil)
	if err != nil {
		p.t.Fatal(err)
	}
	if _, err := p.conn.WriteToUDP(wire, p.relay); err != nil {
		p.t.Fatal(err)
	}
}

// sendRTCP protects and sends RTCP packets from the browser.
func (p *dtlsPhone) sendRTCP(pkts ...rtcp.Packet) {
	p.t.Helper()
	raw, err := rtcp.Marshal(pkts)
	if err != nil {
		p.t.Fatal(err)
	}
	p.mu.Lock()
	enc := p.enc
	p.mu.Unlock()
	wire, err := enc.EncryptRTCP(nil, raw, nil)
	if err != nil {
		p.t.Fatal(err)
	}
	if _, err := p.conn.WriteToUDP(wire, p.relay); err != nil {
		p.t.Fatal(err)
	}
}

// next returns the next RTP packet or the RTCP packets the relay sent the
// browser, within d.
func (p *dtlsPhone) next(d time.Duration) (rtp *RTPPacket, rr []rtcp.Packet, ok bool) {
	p.t.Helper()
	select {
	case wire := <-p.rtp:
		p.mu.Lock()
		dec := p.dec
		p.mu.Unlock()
		if isRTCP(wire) {
			plain, err := dec.DecryptRTCP(nil, wire, nil)
			if err != nil {
				p.t.Fatalf("cannot decrypt RTCP: %v", err)
			}
			pk, err := rtcp.Unmarshal(plain)
			if err != nil {
				p.t.Fatalf("bad RTCP: %v", err)
			}
			return nil, pk, true
		}
		plain, err := dec.DecryptRTP(nil, wire, nil)
		if err != nil {
			p.t.Fatalf("cannot decrypt RTP: %v", err)
		}
		pkt, _ := ParseRTP(plain)
		return pkt, nil, true
	case <-time.After(d):
		return nil, nil, false
	}
}

// nextRTP skips RTCP until an RTP packet arrives.
func (p *dtlsPhone) nextRTP(d time.Duration) (*RTPPacket, bool) {
	p.t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if pkt, _, ok := p.next(time.Until(deadline)); ok && pkt != nil {
			return pkt, true
		}
	}
	return nil, false
}

// nextFeedback skips RTP and reports the RTCP packets that satisfy want.
func (p *dtlsPhone) nextFeedback(d time.Duration, want func(rtcp.Packet) bool) (rtcp.Packet, bool) {
	p.t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		_, pk, ok := p.next(time.Until(deadline))
		if !ok {
			return nil, false
		}
		for _, x := range pk {
			if want(x) {
				return x, true
			}
		}
	}
	return nil, false
}

func videoPacket(pt uint8, seq uint16, ts, ssrc uint32, payload []byte) []byte {
	return BuildRTP(pt, seq, ts, ssrc, false, payload)
}

// TestVideoNegotiation fails if the SDP of a video call is not what a browser
// accepts: the callee's offer carries the audio and the supported video codecs
// in the caller's numbering and nothing it must not (RTX, FEC, extmaps); the
// caller's answer carries the callee's codec in the caller's numbering with
// the shared feedback; a callee without video leaves the caller's video
// section rejected, still in the offer's position; and every SDP passes the
// WebRTC rules with one BUNDLE group over the active sections.
func TestVideoNegotiation(t *testing.T) {
	t.Run("both ends take H.264", func(t *testing.T) {
		c := newVideoCall(t, true)
		ms := c.offerB.Media()
		if len(ms) != 2 || ms[1].Kind() != "video" || ms[1].Port() == 0 {
			t.Fatalf("offer to the callee has %d sections", len(ms))
		}
		var names []string
		for _, cd := range ms[1].Codecs() {
			names = append(names, cd.Name)
		}
		if strings.Join(names, ",") != "H264,VP8" {
			t.Fatalf("callee offered %v, want H264 then VP8", names)
		}
		if ms[1].Codecs()[0].PT != 98 {
			t.Errorf("callee's H.264 is on pt %d, want the caller's 98", ms[1].Codecs()[0].PT)
		}
		for _, bad := range []string{"rtx", "ulpfec", "red", "extmap", "transport-cc"} {
			if strings.Contains(strings.ToLower(string(mustBuild(t, c.offerB))), bad) {
				t.Errorf("the offer to the callee contains %s", bad)
			}
		}
		if got := c.offerB.Bundle(); len(got) != 2 {
			t.Errorf("offer bundle %v", got)
		}
		am := c.ansA.Media()
		if len(am) != 2 || am[1].Port() == 0 {
			t.Fatalf("answer has %d sections, video port %d", len(am), am[1].Port())
		}
		vc := am[1].Codecs()
		if len(vc) != 1 || vc[0].Name != "H264" || vc[0].PT != 98 {
			t.Fatalf("answer video codecs %+v, want one H264 on the caller's pt 98", vc)
		}
		if strings.Join(vc[0].Feedback, ",") == "" || strings.Contains(strings.Join(vc[0].Feedback, ","), "transport-cc") {
			t.Errorf("answer feedback %v", vc[0].Feedback)
		}
		if len(am[1].Extmaps()) != 0 {
			t.Error("the answer negotiates header extensions")
		}
		if am[1].Mid() != "1" || c.ansA.Bundle()[0] != "0" || len(c.ansA.Bundle()) != 2 {
			t.Errorf("mids %q bundle %v", am[1].Mid(), c.ansA.Bundle())
		}
		if c.ansA.SessionHasCleartext() {
			t.Error("a section of the answer is in the clear")
		}
		if got := c.ansA.CandidateAddresses(); len(got) < 2 || got[0] != "127.0.0.1" {
			t.Errorf("candidates %v", got)
		}
	})
	t.Run("a callee without video", func(t *testing.T) {
		c := newVideoCall(t, false)
		am := c.ansA.Media()
		if len(am) != 2 || am[1].Kind() != "video" || am[1].Port() != 0 || am[1].Mid() != "1" {
			t.Fatalf("the caller's video section is not rejected in place: %d sections", len(am))
		}
		if got := c.ansA.Bundle(); len(got) != 1 || got[0] != "0" {
			t.Errorf("bundle %v, want only the audio", got)
		}
		if c.r.VideoOn() {
			t.Error("the relay carries video the callee refused")
		}
	})
	t.Run("video before audio in the offer", func(t *testing.T) {
		mux := newTestMux(t)
		node := &WebRTCNode{mux: mux, hosts: []string{"127.0.0.1"}}
		r, _ := NewRelay(testRelayMin+460, testRelayMin+470)
		t.Cleanup(r.Close)
		_, _, _ = r.AddMuxLeg("a", mux)
		_, _, _ = r.AddMuxLeg("b", mux)
		offer := chromeVideoOffer(mustCert(t).fp, "abcd", "browserpassword0123456789", true)
		if err := r.PrepareWebRTC("a", node, offer); err != nil {
			t.Fatal(err)
		}
		off, _ := ParseAudioSDP(offer)
		ans, err := r.AnswerSDP("a", off, off.Filter([]string{"PCMU"}), WebPolicy())
		if err != nil {
			t.Fatal(err)
		}
		vo, _ := ParseVideoOffer(offer)
		vc := SupportedVideo(vo.Codecs)[0]
		r.SetVideoSection("a", &vc, "sendrecv", 0)
		body, err := r.AppendVideo("a", ans.Build("127.0.0.1", mux.Port(), "sendrecv"))
		if err != nil {
			t.Fatal(err)
		}
		s := parse(t, body)
		ms := s.Media()
		if len(ms) != 2 || ms[0].Kind() != "video" || ms[0].Mid() != "1" || ms[1].Kind() != "audio" || ms[1].Mid() != "0" {
			t.Fatalf("answer order is %s/%s, want the offer's video then audio", ms[0].Kind(), ms[1].Kind())
		}
		if err := ValidateWebRTC(s); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("an offer with a data channel is refused", func(t *testing.T) {
		mux := newTestMux(t)
		node := &WebRTCNode{mux: mux, hosts: []string{"127.0.0.1"}}
		r, _ := NewRelay(testRelayMin+471, testRelayMin+480)
		t.Cleanup(r.Close)
		_, _, _ = r.AddMuxLeg("a", mux)
		_, _, _ = r.AddMuxLeg("b", mux)
		offer := append(chromeVideoOffer(mustCert(t).fp, "abcd", "browserpassword0123456789", false),
			[]byte("m=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\nc=IN IP4 0.0.0.0\r\na=mid:2\r\n")...)
		if err := r.PrepareWebRTC("a", node, offer); err == nil {
			t.Fatal("an offer with a section Hello cannot carry was accepted")
		}
	})
}

func mustBuild(t *testing.T, s *SDPSession) []byte {
	t.Helper()
	b, err := s.Build()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestVideoRelayForwarding fails if video between two browser legs is not
// relayed as the spec says: the payload arrives untouched on the other leg's
// payload type, SSRC and a constant sequence and timestamp offset (so a
// retransmission lands on the original number), both legs are encrypted, a
// picture loss indication and a full intra request reach the sender as a PLI
// on its own stream, a NACK is mapped by the offset, and the first packet of a
// stream makes the relay ask for a keyframe.
func TestVideoRelayForwarding(t *testing.T) {
	c := newVideoCall(t, true)
	c.connect(t)
	if !c.r.VideoOn() {
		t.Fatal("the relay does not carry video on both legs")
	}
	const ssrcA = 7777
	var payloads [][]byte
	var firstOut RTPPacket
	// A sends ten packets of a stream starting at a sequence near the wrap.
	for i := 0; i < 10; i++ {
		p := bytes.Repeat([]byte{byte(0x40 + i)}, 300)
		payloads = append(payloads, p)
		c.a.sendRTP(videoPacket(98, uint16(65530+i), 90000+uint32(i)*3000, ssrcA, p))
	}
	if kf, ok := c.a.nextFeedback(3*time.Second, func(p rtcp.Packet) bool { _, ok := p.(*rtcp.PictureLossIndication); return ok }); !ok {
		t.Fatal("the first packet of a stream did not make the relay ask its sender for a keyframe")
	} else if pli := kf.(*rtcp.PictureLossIndication); pli.MediaSSRC != ssrcA {
		t.Errorf("PLI names ssrc %d, want the sender's %d", pli.MediaSSRC, ssrcA)
	}
	var seqs []uint16
	for i := 0; i < 10; i++ {
		pkt, ok := c.b.nextRTP(3 * time.Second)
		if !ok {
			t.Fatalf("video packet %d never reached the callee", i)
		}
		if i == 0 {
			firstOut = *pkt
		}
		if !bytes.Equal(pkt.Payload, payloads[i]) {
			t.Fatalf("packet %d payload changed", i)
		}
		if pkt.PayloadType != 102 {
			t.Fatalf("callee got pt %d, want its own 102", pkt.PayloadType)
		}
		if pkt.SSRC == ssrcA || pkt.SSRC != firstOut.SSRC {
			t.Fatalf("ssrc %d (sender %d, first %d)", pkt.SSRC, ssrcA, firstOut.SSRC)
		}
		if i > 0 && (pkt.Sequence != firstOut.Sequence+uint16(i) || pkt.Timestamp != firstOut.Timestamp+uint32(i)*3000) {
			t.Fatalf("packet %d: seq %d ts %d, offsets are not constant from seq %d ts %d", i, pkt.Sequence, pkt.Timestamp, firstOut.Sequence, firstOut.Timestamp)
		}
		seqs = append(seqs, pkt.Sequence)
	}

	// The callee asks for packets 3 and 4 again (NACK) and for a picture (PLI,
	// then FIR): the sender hears NACK on its own sequence numbers and a PLI.
	c.b.sendRTCP(&rtcp.TransportLayerNack{SenderSSRC: 1, MediaSSRC: firstOut.SSRC, Nacks: nackPairs([]uint16{seqs[3], seqs[4]})})
	nk, ok := c.a.nextFeedback(3*time.Second, func(p rtcp.Packet) bool { _, ok := p.(*rtcp.TransportLayerNack); return ok })
	if !ok {
		t.Fatal("the NACK did not reach the sender")
	}
	n := nk.(*rtcp.TransportLayerNack)
	var got []uint16
	for _, pair := range n.Nacks {
		pair.Range(func(s uint16) bool { got = append(got, s); return true })
	}
	if len(got) != 2 || got[0] != uint16(65530+3) || got[1] != uint16(65530+4) || n.MediaSSRC != ssrcA {
		t.Fatalf("NACK reached the sender as %v on ssrc %d, want 65533 and 65534 on %d", got, n.MediaSSRC, ssrcA)
	}
	time.Sleep(keyframeSpacing + 50*time.Millisecond)
	c.b.sendRTCP(&rtcp.PictureLossIndication{SenderSSRC: 1, MediaSSRC: firstOut.SSRC})
	if _, ok := c.a.nextFeedback(3*time.Second, func(p rtcp.Packet) bool { _, ok := p.(*rtcp.PictureLossIndication); return ok }); !ok {
		t.Fatal("a PLI from the callee did not reach the sender")
	}
	if st := c.r.VideoStats(); st.Packets != 10 || st.KeyframeRequests < 2 || st.NACKs != 1 {
		t.Errorf("stats %+v", st)
	}

	// The reverse direction is its own stream with its own numbers.
	for i := 0; i < 3; i++ {
		c.b.sendRTP(videoPacket(102, uint16(100+i), 5000+uint32(i)*3000, 8888, bytes.Repeat([]byte{0x70 + byte(i)}, 100)))
	}
	for i := 0; i < 3; i++ {
		pkt, ok := c.a.nextRTP(3 * time.Second)
		if !ok || pkt.PayloadType != 98 || pkt.SSRC == 8888 {
			t.Fatalf("reverse packet %d: %+v %v", i, pkt, ok)
		}
	}
}

// TestVideoSSRCSwitchContinues fails if a sender that restarts its stream
// (new SSRC, as after a re-INVITE) breaks the numbers the receiver sees.
func TestVideoSSRCSwitchContinues(t *testing.T) {
	c := newVideoCall(t, true)
	c.connect(t)
	c.a.sendRTP(videoPacket(98, 10, 1000, 1, []byte("one")))
	c.a.sendRTP(videoPacket(98, 11, 4000, 1, []byte("two")))
	p1, _ := c.b.nextRTP(3 * time.Second)
	p2, ok := c.b.nextRTP(3 * time.Second)
	if p1 == nil || !ok {
		t.Fatal("no packets")
	}
	c.a.sendRTP(videoPacket(98, 500, 900000, 2, []byte("three")))
	p3, ok := c.b.nextRTP(3 * time.Second)
	if !ok {
		t.Fatal("no packet after the switch")
	}
	if p3.SSRC != p2.SSRC || p3.Sequence != p2.Sequence+1 || int32(p3.Timestamp-p2.Timestamp) <= 0 {
		t.Fatalf("after a new source stream: ssrc %d seq %d ts %d, before %d %d %d", p3.SSRC, p3.Sequence, p3.Timestamp, p2.SSRC, p2.Sequence, p2.Timestamp)
	}
}

// TestVideoCapEnforced fails if the relay lets a call exceed its video ceiling
// or does not tell the sender about it: packets above the rate are dropped and
// counted and the sender is sent a REMB at the ceiling.
func TestVideoCapEnforced(t *testing.T) {
	c := newVideoCall(t, true)
	c.r.SetVideoCap(200) // 200 kbit/s: 25 kB/s
	c.connect(t)
	for i := 0; i < 120; i++ {
		c.a.sendRTP(videoPacket(98, uint16(i), uint32(i)*3000, 5, bytes.Repeat([]byte{1}, 1000)))
	}
	waitFor(t, "drops above the ceiling", func() bool { return c.r.VideoStats().DroppedCap > 0 })
	if st := c.r.VideoStats(); st.Packets > 40 {
		t.Errorf("relayed %d kB in a burst, the ceiling is 25 kB per second", st.Packets)
	}
	rb, ok := c.a.nextFeedback(4*time.Second, func(p rtcp.Packet) bool { _, ok := p.(*rtcp.ReceiverEstimatedMaximumBitrate); return ok })
	if !ok {
		t.Fatal("the sender was never told the ceiling")
	}
	if bps := rb.(*rtcp.ReceiverEstimatedMaximumBitrate).Bitrate; bps > 200_000*1.01 {
		t.Errorf("REMB %v bit/s above the ceiling", bps)
	}
}

// TestVideoReports fails if the relay does not terminate RTCP per leg: each
// browser gets sender and receiver reports from the leg's own statistics.
func TestVideoReports(t *testing.T) {
	c := newVideoCall(t, true)
	c.connect(t)
	for i := 0; i < 5; i++ {
		c.a.sendRTP(videoPacket(98, uint16(i), uint32(i)*3000, 5, []byte("frame")))
	}
	rep, ok := c.a.nextFeedback(4*time.Second, func(p rtcp.Packet) bool {
		r, ok := p.(*rtcp.ReceiverReport)
		return ok && len(r.Reports) == 1
	})
	if !ok {
		t.Fatal("the sender of video never got a receiver report about its stream")
	}
	if r := rep.(*rtcp.ReceiverReport).Reports[0]; r.SSRC != 5 || r.LastSequenceNumber < 4 {
		t.Errorf("report %+v", r)
	}
	if _, ok := c.b.nextFeedback(4*time.Second, func(p rtcp.Packet) bool { _, ok := p.(*rtcp.SenderReport); return ok }); !ok {
		t.Fatal("the receiver of video never got a sender report")
	}
}

// TestAlternateLeg fails if a call to a browser cannot share the callee side
// with the plain callee leg: media on the alternate leg is parked until it is
// promoted, the promoted leg is the callee, and a dropped one leaves the plain
// leg alone.
func TestAlternateLeg(t *testing.T) {
	mux := newTestMux(t)
	node := &WebRTCNode{mux: mux, hosts: []string{"127.0.0.1"}}
	r, err := NewRelay(testRelayMin+481, testRelayMin+500)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	if _, err := r.AddLeg("caller"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AddLeg("callee"); err != nil {
		t.Fatal(err)
	}
	r.Start()
	if _, err := r.AddAltMuxLeg(node); err != nil {
		t.Fatal(err)
	}
	if !r.HasAlt() || r.LegPort(AltLeg) != mux.Port() || r.LegPort("callee") == mux.Port() {
		t.Fatal("alternate leg is not on the shared port")
	}
	if !r.PromoteAlt() {
		t.Fatal("promotion failed")
	}
	if r.HasAlt() || r.LegPort("callee") != mux.Port() {
		t.Error("after promotion the callee is not the browser leg")
	}
	if r.PromoteAlt() {
		t.Error("promoted twice")
	}
	r2, _ := NewRelay(testRelayMin+501, testRelayMin+520)
	t.Cleanup(r2.Close)
	_, _ = r2.AddLeg("caller")
	port, _ := r2.AddLeg("callee")
	if _, err := r2.AddAltMuxLeg(node); err != nil {
		t.Fatal(err)
	}
	r2.DropAlt()
	if r2.HasAlt() || r2.LegPort("callee") != port {
		t.Error("dropping the alternate leg touched the callee leg")
	}
}
