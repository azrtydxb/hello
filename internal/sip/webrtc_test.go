package sip

import (
	"bytes"
	"context"
	"math"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/stun/v3"
)

// browser is a headless WebRTC client: a SIP phone whose SDP is a browser's
// (UDP/TLS/RTP/SAVPF, ICE, a DTLS fingerprint, BUNDLE, optional video), with a
// pion DTLS-SRTP endpoint on its own socket. It stands in for the web
// softphone; the real browser is a hardware-list item.
type browser struct {
	*phone
	sock       *net.UDPConn
	ep         *media.DTLSEndpoint
	ufrag, pwd string
	video      bool
}

func newBrowser(t *testing.T, pbx *testPBX, user, pass string, video bool) *browser {
	t.Helper()
	p := newPhone(t, pbx, user, pass)
	sock, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sock.Close() })
	ep, err := media.NewDTLSEndpoint(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ep.Close)
	b := &browser{phone: p, sock: sock, ep: ep, ufrag: "ufrag-" + user, pwd: "pwd-of-" + user + "-0123456789abc", video: video}
	p.sdp = b.offer()
	return b
}

// transport is the part of an SDP section every bundled section repeats.
func (b *browser) transport(setup string) string {
	return "c=IN IP4 0.0.0.0\r\na=ice-ufrag:" + b.ufrag + "\r\na=ice-pwd:" + b.pwd + "\r\na=fingerprint:" + b.ep.Fingerprint().String() +
		"\r\na=setup:" + setup + "\r\na=rtcp-mux\r\n"
}

// offer is what a Chrome-style client sends.
func (b *browser) offer() string {
	s := "v=0\r\no=- 4611731400430051336 2 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\na=group:BUNDLE 0" + map[bool]string{true: " 1", false: ""}[b.video] + "\r\n" +
		"m=audio 9 UDP/TLS/RTP/SAVPF 111 0 126\r\n" + b.transport("actpass") + "a=mid:0\r\na=sendrecv\r\n" +
		"a=rtpmap:111 opus/48000/2\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:126 telephone-event/8000\r\n"
	if b.video {
		s += "m=video 9 UDP/TLS/RTP/SAVPF 96 98\r\n" + b.transport("actpass") + "a=mid:1\r\na=sendrecv\r\n" +
			"a=rtpmap:96 VP8/90000\r\na=rtcp-fb:96 nack\r\na=rtcp-fb:96 nack pli\r\na=rtcp-fb:96 transport-cc\r\n" +
			"a=rtpmap:98 H264/90000\r\na=rtcp-fb:98 nack\r\na=rtcp-fb:98 nack pli\r\na=rtcp-fb:98 goog-remb\r\na=rtcp-fb:98 transport-cc\r\n" +
			"a=fmtp:98 level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f\r\n"
	}
	return s
}

// answerTo builds the browser's answer to Hello's offer: PCMU, and H.264 on the
// payload type Hello offered when the browser does video.
func (b *browser) answerTo(offer []byte) string {
	sess, err := media.ParseSDPSession(offer)
	if err != nil {
		b.t.Errorf("offer to the browser: %v\n%s", err, offer)
		return ""
	}
	if err := media.ValidateWebRTC(sess); err != nil {
		b.t.Errorf("the offer to a browser fails the WebRTC rules: %v\n%s", err, offer)
	}
	s := "v=0\r\no=- 1 2 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\na=group:BUNDLE 0\r\n"
	var vid string
	for _, m := range sess.Media() {
		switch {
		case m.Kind() == "audio":
			s += "m=audio 9 UDP/TLS/RTP/SAVPF 0 126\r\n" + b.transport("active") + "a=mid:" + m.Mid() + "\r\na=sendrecv\r\n" +
				"a=rtpmap:0 PCMU/8000\r\na=rtpmap:126 telephone-event/8000\r\n"
		case m.Kind() == "video" && b.video && m.Port() != 0:
			for _, c := range m.Codecs() {
				if strings.EqualFold(c.Name, "H264") {
					vid = "m=video 9 UDP/TLS/RTP/SAVPF " + strconv.Itoa(int(c.PT)) + "\r\n" + b.transport("active") + "a=mid:" + m.Mid() + "\r\na=sendrecv\r\n" +
						"a=rtpmap:" + strconv.Itoa(int(c.PT)) + " H264/90000\r\na=fmtp:" + strconv.Itoa(int(c.PT)) + " " + c.Fmtp + "\r\n" +
						"a=rtcp-fb:" + strconv.Itoa(int(c.PT)) + " nack\r\na=rtcp-fb:" + strconv.Itoa(int(c.PT)) + " nack pli\r\n"
					s = strings.Replace(s, "BUNDLE 0", "BUNDLE 0 "+m.Mid(), 1)
				}
			}
		default:
			vid += "m=" + m.Kind() + " 0 UDP/TLS/RTP/SAVPF 0\r\na=mid:" + m.Mid() + "\r\n"
		}
	}
	return s + vid
}

// answerer makes the browser answer the INVITE it is sent, with the answer
// built from that INVITE's offer.
func (b *browser) answerer() calleeFn {
	return func(p *phone, req *sip.Request, _ sip.ServerTransaction, dss *sipgo.DialogServerSession) {
		_ = dss.Respond(180, "Ringing", nil)
		_ = dss.RespondSDP([]byte(b.answerTo(req.Body())))
	}
}

// candidate is the address Hello's SDP lists for its media.
func candidate(t *testing.T, body []byte) (sess *media.SDPSession, addr *net.UDPAddr, ufrag, pwd string, fp media.Fingerprint, setup string) {
	t.Helper()
	sess, err := media.ParseSDPSession(body)
	if err != nil {
		t.Fatalf("SDP: %v\n%s", err, body)
	}
	m := sess.Media()[0]
	ice := m.ICE(sess)
	d := m.DTLS(sess)
	addrs := sess.CandidateAddresses()
	if len(addrs) == 0 || len(d.Fingerprints) == 0 {
		t.Fatalf("SDP has no candidate or fingerprint:\n%s", body)
	}
	f, err := media.ParseFingerprint(d.Fingerprints[0])
	if err != nil {
		t.Fatal(err)
	}
	return sess, &net.UDPAddr{IP: net.ParseIP(addrs[0]), Port: m.Port()}, ice.Ufrag, ice.Pwd, f, d.Setup
}

// connect runs ICE and DTLS against Hello's SDP: one authenticated binding
// request that nominates, then the handshake in the role this browser's own SDP
// said (active when it answered, the server when it offered actpass and was
// answered active).
func (b *browser) connect(t *testing.T, helloSDP []byte, client bool) {
	t.Helper()
	_, addr, ufrag, pwd, fp, _ := candidate(t, helloSDP)
	m, err := stun.Build(stun.TransactionID, stun.BindingRequest, stun.NewUsername(ufrag+":"+b.ufrag),
		stun.RawAttribute{Type: stun.AttrICEControlling, Value: []byte{1, 2, 3, 4, 5, 6, 7, 8}},
		stun.RawAttribute{Type: stun.AttrUseCandidate}, stun.NewShortTermIntegrity(pwd), stun.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.sock.WriteToUDP(m.Raw, addr); err != nil {
		t.Fatal(err)
	}
	select {
	case raw := <-b.ep.STUN():
		resp := &stun.Message{Raw: raw}
		if err := resp.Decode(); err != nil || resp.Type != stun.BindingSuccess {
			t.Fatalf("ICE check answered with %v %v", resp.Type, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no answer to the ICE check")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := b.ep.Handshake(ctx, client, addr, fp); err != nil {
		t.Fatalf("%s handshake: %v", b.user, err)
	}
}

// send protects and sends one RTP packet to Hello.
func (b *browser) send(t *testing.T, to *net.UDPAddr, plain []byte) []byte {
	t.Helper()
	k, ok := b.ep.Keys()
	if !ok {
		t.Fatal("no keys yet")
	}
	wire, err := k.Protect(plain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.sock.WriteToUDP(wire, to); err != nil {
		t.Fatal(err)
	}
	return wire
}

// recv returns the next RTP packet Hello sent, decrypted, skipping RTCP.
func (b *browser) recv(t *testing.T, within time.Duration) (*media.RTPPacket, bool) {
	t.Helper()
	k, ok := b.ep.Keys()
	if !ok {
		t.Fatal("no keys yet")
	}
	deadline := time.After(within)
	for {
		select {
		case wire := <-b.ep.Datagrams():
			if len(wire) > 1 && wire[1] >= 192 && wire[1] <= 223 { // RTCP
				continue
			}
			plain, err := k.Unprotect(wire)
			if err != nil {
				t.Fatalf("cannot decrypt what Hello sent: %v", err)
			}
			pkt, ok := media.ParseRTP(plain)
			if !ok {
				t.Fatal("not RTP")
			}
			return pkt, true
		case <-deadline:
			return nil, false
		}
	}
}

// startWebPBX starts a PBX that runs the node's WebRTC media on a loopback
// shared port and advertises 127.0.0.1.
func startWebPBX(t *testing.T, devices []snapshot.Device, cleartext bool, opts ...pbxOpt) *testPBX {
	t.Helper()
	node, err := media.StartWebRTC(0, "127.0.0.1", "")
	if err != nil {
		t.Fatal(err)
	}
	media.SetWebRTCNode(node)
	t.Cleanup(func() { media.SetWebRTCNode(nil); _ = node.Close() })
	webOpt := func(c *Config, _ *Deps) {
		c.WebRTCAllowCleartextHop = cleartext
		c.VideoEnabled, c.VideoMaxKbps = true, 1500
	}
	pbx := startPBX(t, devices, append([]pbxOpt{withMedia(false), trustLoopback, webOpt}, opts...)...)
	pbx.srv.SetMediaAnchor(media.NewAnchor("127.0.0.1", discard))
	return pbx
}

func webDevices() []snapshot.Device {
	d := []snapshot.Device{dev(1, "a1", "100", "pa"), dev(2, "b1", "200", "pb1"), dev(3, "w1", "200", "pw1"), dev(4, "w2", "300", "pw2")}
	d[2].Kind, d[3].Kind = "web", "web"
	return d
}

// webCall places a call from a browser and returns once it is answered.
func webCall(t *testing.T, a *browser, ext string) <-chan callResult {
	t.Helper()
	req := viaEdge(a.inviteReq(ext), "wss")
	ch := make(chan callResult, 1)
	go func() {
		dcs, err := a.callReq(t.Context(), req)
		ch <- callResult{dcs, err}
	}()
	return ch
}

// TestWebRTCCallToDeskPhone fails if a browser cannot call a plain RTP phone
// through the anchor: the 200 must be a browser SDP (BUNDLE, rtcp-mux, mid,
// ICE-lite with a host candidate, a fingerprint, no codec the phone did not
// choose), the leg must come up (ICE check, DTLS), audio must flow both ways
// with the browser leg never in the clear, and the CDR and trace must say what
// ran. Mutations: answering plain RTP, listing a codec the callee did not
// choose, or not encrypting the browser's leg fail the matching check.
func TestWebRTCCallToDeskPhone(t *testing.T) {
	pbx := startWebPBX(t, webDevices(), true)
	a := newBrowser(t, pbx, "a1", "pa", false)
	b := newSRTPPhone(t, pbx, "b1", "pb1", false)
	b.sdp = strings.ReplaceAll(b.sdp, "101", "126") // answers in the numbering it was offered: the browser's
	registerOver(t, a.phone, "wss")
	registerOver(t, b.phone, "udp")
	b.setCallee(answerAfter(nil))
	ch := webCall(t, a, "200")
	inv := waitReq(t, b.invites, "INVITE to the desk phone")
	r := waitCall(t, ch)
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	ans := r.dcs.InviteResponse.Body()
	sess, addr, _, _, _, setup := candidate(t, ans)
	if err := media.ValidateWebRTC(sess); err != nil {
		t.Fatalf("the answer to the browser fails the WebRTC rules: %v\n%s", err, ans)
	}
	if !sess.IceLite() || setup != "active" || addr.Port != media.CurrentWebRTC().Mux().Port() || sess.SessionHasCleartext() {
		t.Fatalf("answer is not ICE-lite DTLS-SRTP on the shared port (setup %q port %d):\n%s", setup, addr.Port, ans)
	}
	var names []string
	for _, c := range sess.Media()[0].Codecs() {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "PCMU,telephone-event" {
		t.Fatalf("answer lists %v, want the one codec the phone chose and telephone-event:\n%s", names, ans)
	}
	// The desk phone was offered plain RTP at the anchor's callee port.
	if off := cryptoOf(t, inv.Body()); off.Proto != "RTP/AVP" || off.DTLS != nil || off.ICE != nil || len(off.Crypto) != 0 {
		t.Fatalf("the desk phone was offered %q with security:\n%s", off.Proto, inv.Body())
	}
	a.connect(t, ans, false) // Hello answered active to actpass: Hello is the DTLS client, the browser the server
	toHelloB := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: cryptoOf(t, inv.Body()).Port}
	toHelloA := addr
	var got *media.RTPPacket
	for i := 0; i < 40 && got == nil; i++ {
		wire := a.send(t, toHelloA, rtpPacket(uint16(i+1), 0x5a))
		if bytes.Contains(wire, bytes.Repeat([]byte{0x5a}, 160)) {
			t.Fatal("the browser's packet was in the clear")
		}
		b.sock.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		buf := make([]byte, 2048)
		if n, _, err := b.sock.ReadFromUDP(buf); err == nil {
			got, _ = media.ParseRTP(buf[:n])
		}
	}
	if got == nil || !bytes.Equal(got.Payload, bytes.Repeat([]byte{0x5a}, 160)) {
		t.Fatalf("browser to phone audio did not arrive: %+v", got)
	}
	var back *media.RTPPacket
	for i := 0; i < 40 && back == nil; i++ {
		_, _ = b.sock.WriteToUDP(rtpPacket(uint16(100+i), 0x6b), toHelloB)
		if p, ok := a.recv(t, 100*time.Millisecond); ok {
			back = p
		}
	}
	if back == nil || !bytes.Equal(back.Payload, bytes.Repeat([]byte{0x6b}, 160)) {
		t.Fatalf("phone to browser audio did not arrive: %+v", back)
	}
	hangup(t, r.dcs)
	rec := pbx.nextCDR(t)
	if rec.SRTP != cdr.SRTPCaller || rec.SRTPKeyEx != cdr.KeyExDTLS || !rec.ICE || rec.Video {
		t.Errorf("CDR srtp %q keyex %q ice %v video %v, want caller, dtls, ice, no video", rec.SRTP, rec.SRTPKeyEx, rec.ICE, rec.Video)
	}
}

// TestWebRTCRefusedOnCleartextHop fails if a WebRTC call is accepted over a UDP
// hop from the edge while the cleartext flag is false, if it is accepted on a
// node that does not run WebRTC media, or if an offer that is not a valid
// browser offer gets further than a 488 with the reason in the trace.
func TestWebRTCRefusedOnCleartextHop(t *testing.T) {
	refused := func(t *testing.T, pbx *testPBX, why string) {
		t.Helper()
		a := newBrowser(t, pbx, "a1", "pa", false)
		b := newSRTPPhone(t, pbx, "b1", "pb1", false)
		registerOver(t, a.phone, "wss")
		registerOver(t, b.phone, "udp")
		r := waitCall(t, webCall(t, a, "200"))
		if r.err == nil {
			t.Fatal("the call was accepted")
		}
		if !strings.Contains(r.err.Error(), "488") {
			t.Fatalf("call refused with %v, want 488", r.err)
		}
		if rec := pbx.nextCDR(t); !strings.Contains(rec.FailureReason, why) {
			t.Errorf("failure reason %q does not say %q", rec.FailureReason, why)
		}
		select {
		case <-b.invites:
			t.Error("the callee was rung for a refused call")
		case <-time.After(200 * time.Millisecond):
		}
	}
	t.Run("cleartext hop", func(t *testing.T) { refused(t, startWebPBX(t, webDevices(), false), "cleartext signalling hop") })
	t.Run("no WebRTC media on the node", func(t *testing.T) {
		pbx := startWebPBX(t, webDevices(), true)
		media.SetWebRTCNode(nil)
		refused(t, pbx, "not enabled")
	})
}

// TestPhoneCallsBrowser fails if a call to a browser is not offered the
// browser's SDP on the alternate WebRTC leg, if the browser's answer does not
// bring the leg up and carry audio both ways, or if a plain phone of the same
// extension that answers first does not keep the plain leg (and the browser
// fork get its CANCEL). Both outcomes of a ring-all across a desk phone and a
// browser are covered.
func TestPhoneCallsBrowser(t *testing.T) {
	run := func(t *testing.T, browserAnswers bool) {
		pbx := startWebPBX(t, webDevices(), true)
		a := newSRTPPhone(t, pbx, "a1", "pa", false)
		desk := newSRTPPhone(t, pbx, "b1", "pb1", false)
		w := newBrowser(t, pbx, "w1", "pw1", false)
		registerOver(t, a.phone, "udp")
		registerOver(t, desk.phone, "udp")
		registerOver(t, w.phone, "wss")
		release := make(chan struct{})
		if browserAnswers {
			desk.setCallee(answerAfter(release)) // rings until the browser has won
			w.setCallee(w.answerer())
		} else {
			desk.setCallee(answerAfter(nil))
			w.setCallee(answerAfter(release)) // never answers
		}
		ch := dial(t.Context(), a.phone, "200")
		var winnerOffer []byte
		wInv := waitReq(t, w.invites, "INVITE to the browser")
		winnerOffer = wInv.Body()
		deskInv := waitReq(t, desk.invites, "INVITE to the desk phone")
		if sess, err := media.ParseSDPSession(winnerOffer); err != nil || media.ValidateWebRTC(sess) != nil || !sess.IceLite() {
			t.Fatalf("the browser fork was not offered a WebRTC SDP: %v\n%s", err, winnerOffer)
		}
		if off := cryptoOf(t, deskInv.Body()); off.Proto != "RTP/AVP" || off.ICE != nil {
			t.Fatalf("the desk fork was offered %q with ICE", off.Proto)
		}
		r := waitCall(t, ch)
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		if browserAnswers {
			close(release)
			select {
			case <-desk.cancels:
			case <-time.After(3 * time.Second):
				t.Fatal("the desk phone was not cancelled")
			}
			toBrowserLeg := candidateAddr(t, winnerOffer)
			w.connect(t, winnerOffer, true) // answered active to Hello's actpass offer
			toHelloA := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: cryptoOf(t, r.dcs.InviteResponse.Body()).Port}
			var got *media.RTPPacket
			for i := 0; i < 40 && got == nil; i++ {
				_, _ = a.sock.WriteToUDP(rtpPacket(uint16(i+1), 0x31), toHelloA)
				if p, ok := w.recv(t, 100*time.Millisecond); ok {
					got = p
				}
			}
			if got == nil || !bytes.Equal(got.Payload, bytes.Repeat([]byte{0x31}, 160)) {
				t.Fatalf("phone to browser audio did not arrive: %+v", got)
			}
			var back *media.RTPPacket
			for i := 0; i < 40 && back == nil; i++ {
				w.send(t, toBrowserLeg, rtpPacket(uint16(200+i), 0x32))
				a.sock.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
				buf := make([]byte, 2048)
				if n, _, err := a.sock.ReadFromUDP(buf); err == nil {
					back, _ = media.ParseRTP(buf[:n])
				}
			}
			if back == nil || !bytes.Equal(back.Payload, bytes.Repeat([]byte{0x32}, 160)) {
				t.Fatalf("browser to phone audio did not arrive: %+v", back)
			}
			hangup(t, r.dcs)
			rec := pbx.nextCDR(t)
			if rec.SRTP != cdr.SRTPCallee || rec.SRTPKeyEx != cdr.KeyExDTLS || !rec.ICE {
				t.Errorf("CDR srtp %q keyex %q ice %v, want callee, dtls, ice", rec.SRTP, rec.SRTPKeyEx, rec.ICE)
			}
			return
		}
		// The desk phone won: the plain leg carries the call, the browser fork
		// is cancelled, and nothing is encrypted.
		select {
		case <-w.cancels:
		case <-time.After(3 * time.Second):
			t.Fatal("the browser fork was not cancelled")
		}
		toHelloA := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: cryptoOf(t, r.dcs.InviteResponse.Body()).Port}
		var got *media.RTPPacket
		for i := 0; i < 40 && got == nil; i++ {
			_, _ = a.sock.WriteToUDP(rtpPacket(uint16(i+1), 0x41), toHelloA)
			desk.sock.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			buf := make([]byte, 2048)
			if n, _, err := desk.sock.ReadFromUDP(buf); err == nil {
				got, _ = media.ParseRTP(buf[:n])
			}
		}
		if got == nil {
			t.Fatal("audio to the desk phone did not arrive")
		}
		hangup(t, r.dcs)
		if rec := pbx.nextCDR(t); rec.SRTP != cdr.SRTPNone {
			t.Errorf("CDR srtp %q, want none", rec.SRTP)
		}
		close(release)
	}
	t.Run("the browser answers", func(t *testing.T) { run(t, true) })
	t.Run("the desk phone answers", func(t *testing.T) { run(t, false) })
}

func candidateAddr(t *testing.T, body []byte) *net.UDPAddr {
	t.Helper()
	_, addr, _, _, _, _ := candidate(t, body)
	return addr
}

// answerReinvitesWith makes the browser answer the re-INVITEs it is sent with
// sdp (under the phone's locks: its signalling goroutine reads them).
func (b *browser) answerReinvitesWith(sdp string) {
	b.sdpMu.Lock()
	b.sdp = sdp
	b.sdpMu.Unlock()
	b.mu.Lock()
	b.reinviteOwnSDP = true
	b.mu.Unlock()
}

// h264Line is the video section of a browser answer for payload type pt.
func (b *browser) answerText(videoPT int, videoDir string) string {
	s := "v=0\r\no=- 1 3 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\na=group:BUNDLE 0" + map[bool]string{true: " 1", false: ""}[videoPT > 0] + "\r\n" +
		"m=audio 9 UDP/TLS/RTP/SAVPF 0 126\r\n" + b.transport("active") + "a=mid:0\r\na=sendrecv\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:126 telephone-event/8000\r\n"
	if videoPT > 0 {
		pt := strconv.Itoa(videoPT)
		s += "m=video 9 UDP/TLS/RTP/SAVPF " + pt + "\r\n" + b.transport("active") + "a=mid:1\r\na=" + videoDir + "\r\n" +
			"a=rtpmap:" + pt + " H264/90000\r\na=fmtp:" + pt + " level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f\r\n" +
			"a=rtcp-fb:" + pt + " nack\r\na=rtcp-fb:" + pt + " nack pli\r\n"
	}
	return s
}

func videoRTP(pt uint8, seq uint16, fill byte) []byte {
	return media.BuildRTP(pt, seq, 90000+uint32(seq)*3000, 9999, false, bytes.Repeat([]byte{fill}, 200))
}

// webVideoPair places a call from a browser with video to another browser with
// video and connects both legs.
func webVideoPair(t *testing.T, pbx *testPBX) (a, w *browser, r callResult, toA, toW *net.UDPAddr, wOffer []byte) {
	t.Helper()
	a = newBrowser(t, pbx, "a1", "pa", true)
	w = newBrowser(t, pbx, "w1", "pw1", true)
	registerOver(t, a.phone, "wss")
	registerOver(t, w.phone, "wss")
	w.setCallee(w.answerer())
	ch := webCall(t, a, "200")
	inv := waitReq(t, w.invites, "INVITE to the browser")
	wOffer = inv.Body()
	r = waitCall(t, ch)
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	ans := r.dcs.InviteResponse.Body()
	toA, toW = candidateAddr(t, ans), candidateAddr(t, wOffer)
	done := make(chan struct{}, 2)
	go func() { a.connect(t, ans, false); done <- struct{}{} }()
	go func() { w.connect(t, wOffer, true); done <- struct{}{} }()
	for range 2 {
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Fatal("legs did not come up")
		}
	}
	return a, w, r, toA, toW, wOffer
}

// TestWebVideoCall fails if a call between two browsers with video is not
// negotiated and relayed as the spec says: the callee is offered H.264 then
// VP8 in the caller's numbering and nothing it cannot relay (no RTX, FEC or
// transport-cc), the caller's answer carries the callee's one codec with the
// bandwidth ceiling, the node's video slot is taken for the call and returned
// at its end, video packets cross the anchor encrypted on both legs, and the
// CDR and trace say video ran.
func TestWebVideoCall(t *testing.T) {
	pbx := startWebPBX(t, webDevices(), true)
	if media.VideoSessions() != 0 {
		t.Fatalf("slots taken before the call: %d", media.VideoSessions())
	}
	a, w, r, toA, toW, wOffer := webVideoPair(t, pbx)
	// The offer to the callee.
	osess, _ := media.ParseSDPSession(wOffer)
	var vm *media.SDPMedia
	for _, m := range osess.Media() {
		if m.Kind() == "video" {
			vm = m
		}
	}
	if vm == nil || vm.Port() == 0 {
		t.Fatalf("the callee was not offered video:\n%s", wOffer)
	}
	var names []string
	for _, c := range vm.Codecs() {
		names = append(names, c.Name+":"+strconv.Itoa(int(c.PT)))
		for _, f := range c.Feedback {
			if strings.Contains(f, "transport-cc") {
				t.Errorf("the callee's offer lists %s", f)
			}
		}
	}
	if strings.Join(names, ",") != "H264:98,VP8:96" {
		t.Fatalf("callee offered %v, want H264 then VP8 in the caller's numbering", names)
	}
	if strings.Contains(string(wOffer), "b=AS:1500") == false {
		t.Errorf("the offer carries no b=AS ceiling:\n%s", wOffer)
	}
	// The caller's answer.
	asess, _ := media.ParseSDPSession(r.dcs.InviteResponse.Body())
	if err := media.ValidateWebRTC(asess); err != nil {
		t.Fatalf("answer: %v", err)
	}
	am := asess.Media()
	if len(am) != 2 || am[1].Kind() != "video" || am[1].Port() == 0 || len(am[1].Codecs()) != 1 || am[1].Codecs()[0].Name != "H264" || am[1].Codecs()[0].PT != 98 {
		t.Fatalf("the caller's answer does not carry the one H.264 codec:\n%s", r.dcs.InviteResponse.Body())
	}
	if got := media.VideoSessions(); got != 1 {
		t.Errorf("video slots in use = %d, want 1", got)
	}
	// Video both ways, encrypted on both legs.
	var gotW, gotA *media.RTPPacket
	for i := 0; i < 40 && gotW == nil; i++ {
		wire := a.send(t, toA, videoRTP(98, uint16(i+1), 0x55))
		if bytes.Contains(wire, bytes.Repeat([]byte{0x55}, 200)) {
			t.Fatal("video was in the clear")
		}
		if p, ok := w.recv(t, 100*time.Millisecond); ok && p.PayloadType == 98 {
			gotW = p
		}
	}
	if gotW == nil || !bytes.Equal(gotW.Payload, bytes.Repeat([]byte{0x55}, 200)) {
		t.Fatalf("video from the caller did not reach the callee: %+v", gotW)
	}
	for i := 0; i < 40 && gotA == nil; i++ {
		w.send(t, toW, videoRTP(98, uint16(500+i), 0x66))
		if p, ok := a.recv(t, 100*time.Millisecond); ok && p.PayloadType == 98 {
			gotA = p
		}
	}
	if gotA == nil || !bytes.Equal(gotA.Payload, bytes.Repeat([]byte{0x66}, 200)) {
		t.Fatalf("video from the callee did not reach the caller: %+v", gotA)
	}
	hangup(t, r.dcs)
	rec := pbx.nextCDR(t)
	if !rec.Video {
		t.Error("the CDR does not say the call carried video")
	}
	if !strings.Contains(traceText(rec.Trace), "Video relayed (H264)") {
		t.Errorf("trace does not name the video: %v", rec.Trace)
	}
	eventually(t, "the video slot to come back", func() bool { return media.VideoSessions() == 0 })
}

// TestVideoRefusals fails if video is carried where it must not be: to a plain
// RTP desk phone (never, the caller's video line is answered with port 0 and
// the call goes on as audio), for an extension whose video is not allowed, with
// video switched off, or past the node's session limit; each leaves no video
// slot taken and the reason in the trace.
func TestVideoRefusals(t *testing.T) {
	run := func(t *testing.T, pbx *testPBX, toDesk bool, why string) {
		t.Helper()
		slotsBefore := media.VideoSessions()
		a := newBrowser(t, pbx, "a1", "pa", true)
		registerOver(t, a.phone, "wss")
		ext := "300"
		var w *browser
		if toDesk {
			d := newSRTPPhone(t, pbx, "b1", "pb1", false)
			d.sdp = strings.ReplaceAll(d.sdp, "101", "126")
			registerOver(t, d.phone, "udp")
			d.setCallee(answerAfter(nil))
			ext = "200"
		} else {
			w = newBrowser(t, pbx, "w2", "pw2", true)
			registerOver(t, w.phone, "wss")
			w.setCallee(w.answerer())
		}
		r := waitCall(t, webCall(t, a, ext))
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		asess, _ := media.ParseSDPSession(r.dcs.InviteResponse.Body())
		am := asess.Media()
		if len(am) != 2 || am[1].Kind() != "video" || am[1].Port() != 0 || am[1].Mid() != "1" {
			t.Fatalf("the video line is not rejected in place:\n%s", r.dcs.InviteResponse.Body())
		}
		if got := media.VideoSessions(); got != slotsBefore {
			t.Errorf("a video slot is held for a call without video: %d, was %d", got, slotsBefore)
		}
		hangup(t, r.dcs)
		rec := pbx.nextCDR(t)
		if rec.Video {
			t.Error("the CDR says the call carried video")
		}
		if !strings.Contains(traceText(rec.Trace), why) {
			t.Errorf("trace %q does not say %q", traceText(rec.Trace), why)
		}
	}
	t.Run("a desk phone", func(t *testing.T) { run(t, startWebPBX(t, webDevices(), true), true, "callee cannot do video") })
	t.Run("an extension that may not", func(t *testing.T) {
		feats := func(s *snapshot.Snapshot) {
			s.WithExtensionData([]snapshot.Extension{{Number: "300", VideoDenied: true}})
		}
		run(t, startWebPBX(t, webDevices(), true, withFeatures(feats)), false, "may not use video")
	})
	t.Run("switched off", func(t *testing.T) {
		pbx := startWebPBX(t, webDevices(), true, func(c *Config, _ *Deps) { c.VideoEnabled = false })
		run(t, pbx, false, "switched off")
	})
	t.Run("over the node's limit", func(t *testing.T) {
		media.SetVideoMaxSessions(1)
		t.Cleanup(func() { media.SetVideoMaxSessions(0) })
		if !media.AcquireVideoSession() {
			t.Fatal("no slot to take")
		}
		t.Cleanup(media.ReleaseVideoSession)
		pbx := startWebPBX(t, webDevices(), true)
		run(t, pbx, false, "most video calls")
		if media.VideoSessions() != 1 {
			t.Errorf("slots = %d, want only the one the test holds", media.VideoSessions())
		}
	})
}

// TestVideoUpgradeDowngrade fails if video cannot be added to a running call
// and taken off again, or if a hold does not stop the video with the audio.
// Every SDP written for a browser during it passes the WebRTC rules and none
// drops an m= line (a dialog's sections never shrink); the peer is offered the
// one codec the browser listed first; video that a peer accepted flows, and the
// slot follows the video.
func TestVideoUpgradeDowngrade(t *testing.T) {
	pbx := startWebPBX(t, webDevices(), true)
	a := newBrowser(t, pbx, "a1", "pa", false)
	w := newBrowser(t, pbx, "w1", "pw1", false)
	registerOver(t, a.phone, "wss")
	registerOver(t, w.phone, "wss")
	w.setCallee(w.answerer())
	ch := webCall(t, a, "200")
	inv := waitReq(t, w.invites, "INVITE to the browser")
	r := waitCall(t, ch)
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	ans := r.dcs.InviteResponse.Body()
	toA, toW := candidateAddr(t, ans), candidateAddr(t, inv.Body())
	done := make(chan struct{}, 2)
	go func() { a.connect(t, ans, false); done <- struct{}{} }()
	go func() { w.connect(t, inv.Body(), true); done <- struct{}{} }()
	for range 2 {
		<-done
	}
	if media.VideoSessions() != 0 {
		t.Fatalf("an audio call holds a video slot")
	}
	check := func(what string, body []byte, sections int) *media.SDPSession {
		t.Helper()
		s, err := media.ParseSDPSession(body)
		if err != nil {
			t.Fatalf("%s: %v\n%s", what, err, body)
		}
		if err := media.ValidateWebRTC(s); err != nil {
			t.Fatalf("%s fails the WebRTC rules: %v\n%s", what, err, body)
		}
		if got := len(s.Media()); got != sections {
			t.Fatalf("%s has %d m= lines, want %d:\n%s", what, got, sections, body)
		}
		return s
	}

	// Upgrade: the caller adds video.
	w.answerReinvitesWith(w.answerText(98, "sendrecv"))
	a.video = true
	res := a.reinviteAsCaller(t, r.dcs, a.offer())
	if res.StatusCode != 200 {
		t.Fatalf("upgrade re-INVITE = %d", res.StatusCode)
	}
	as := check("the answer to the upgrade", res.Body(), 2)
	if as.Media()[1].Port() == 0 || as.Media()[1].Codecs()[0].Name != "H264" {
		t.Fatalf("the upgrade was not accepted with H.264:\n%s", res.Body())
	}
	peer := waitReq(t, w.reinvites, "upgrade re-INVITE to the callee")
	ps := check("the callee's upgrade offer", peer.Body(), 2)
	if cs := ps.Media()[1].Codecs(); len(cs) != 1 || cs[0].Name != "H264" || cs[0].PT != 98 {
		t.Fatalf("the callee was offered %+v, want the one codec the browser listed first", cs)
	}
	eventually(t, "the video slot", func() bool { return media.VideoSessions() == 1 })
	var got *media.RTPPacket
	for i := 0; i < 40 && got == nil; i++ {
		a.send(t, toA, videoRTP(98, uint16(i+1), 0x77))
		if p, ok := w.recv(t, 100*time.Millisecond); ok && p.PayloadType == 98 {
			got = p
		}
	}
	if got == nil || !bytes.Equal(got.Payload, bytes.Repeat([]byte{0x77}, 200)) {
		t.Fatalf("video did not flow after the upgrade: %+v", got)
	}

	// Hold: both directions stop for audio and video, the lines stay.
	hold := strings.ReplaceAll(a.offer(), "a=sendrecv", "a=sendonly")
	w.answerReinvitesWith(strings.ReplaceAll(w.answerText(98, "sendrecv"), "a=sendrecv", "a=recvonly"))
	res = a.reinviteAsCaller(t, r.dcs, hold)
	if res.StatusCode != 200 {
		t.Fatalf("hold re-INVITE = %d", res.StatusCode)
	}
	hs := check("the answer to the hold", res.Body(), 2)
	for _, m := range hs.Media() {
		if m.Direction() != "recvonly" {
			t.Errorf("%s section answers %q to a sendonly hold, want recvonly", m.Kind(), m.Direction())
		}
	}
	hp := check("the callee's hold offer", waitReq(t, w.reinvites, "hold re-INVITE to the callee").Body(), 2)
	for _, m := range hp.Media() {
		if m.Direction() != "sendonly" {
			t.Errorf("the callee's %s section is %q, want sendonly", m.Kind(), m.Direction())
		}
	}
	if media.VideoSessions() != 1 {
		t.Errorf("a hold gave up the video slot")
	}

	// Downgrade: the caller removes video (its offer keeps the line, port 0).
	noVideo := strings.Split(a.offer(), "m=video")[0] + "m=video 0 UDP/TLS/RTP/SAVPF 98\r\na=mid:1\r\n"
	w.answerReinvitesWith(w.answerText(0, ""))
	res = a.reinviteAsCaller(t, r.dcs, noVideo)
	if res.StatusCode != 200 {
		t.Fatalf("downgrade re-INVITE = %d", res.StatusCode)
	}
	ds := check("the answer to the downgrade", res.Body(), 2)
	if ds.Media()[1].Port() != 0 {
		t.Errorf("the removed video line is still active:\n%s", res.Body())
	}
	dp := check("the callee's downgrade offer", waitReq(t, w.reinvites, "downgrade re-INVITE to the callee").Body(), 2)
	if dp.Media()[1].Port() != 0 {
		t.Errorf("the callee's video line is still active after the downgrade")
	}
	eventually(t, "the video slot to be returned", func() bool { return media.VideoSessions() == 0 })
	hangup(t, r.dcs)
	pbx.nextCDR(t)
	_ = toW
}

// TestWebRTCFeaturesOnCall fails if the call features that depend on the
// media layer misbehave on a browser leg: a hold re-INVITE must keep the leg's
// DTLS fingerprint and ICE credentials (a changed fingerprint would make a
// browser re-handshake and fail), and RFC 4733 DTMF from the browser must
// reach the relay's digit hook.
func TestWebRTCFeaturesOnCall(t *testing.T) {
	pbx := startWebPBX(t, webDevices(), true)
	a := newBrowser(t, pbx, "a1", "pa", false)
	b := newSRTPPhone(t, pbx, "b1", "pb1", false)
	b.sdp = strings.ReplaceAll(b.sdp, "101", "126")
	registerOver(t, a.phone, "wss")
	registerOver(t, b.phone, "udp")
	b.setCallee(answerAfter(nil))
	ch := webCall(t, a, "200")
	waitReq(t, b.invites, "INVITE to the desk phone")
	r := waitCall(t, ch)
	if r.err != nil {
		t.Fatal(r.err)
	}
	first := r.dcs.InviteResponse.Body()
	a.connect(t, first, false)
	res := a.reinviteAsCaller(t, r.dcs, strings.ReplaceAll(a.offer(), "a=sendrecv", "a=sendonly"))
	if res.StatusCode != 200 {
		t.Fatalf("hold = %d", res.StatusCode)
	}
	_, _, u1, p1, f1, _ := candidate(t, first)
	hs, _, u2, p2, f2, s2 := candidate(t, res.Body())
	if u1 != u2 || p1 != p2 || f1.String() != f2.String() || s2 != "active" {
		t.Fatalf("the hold answer changed the leg's identity: ufrag %q/%q fp %v/%v setup %q", u1, u2, f1, f2, s2)
	}
	if err := media.ValidateWebRTC(hs); err != nil {
		t.Fatalf("hold answer: %v", err)
	}
	if got := hs.Media()[0].Direction(); got != "recvonly" {
		t.Errorf("hold answered %q, want recvonly", got)
	}
	hangup(t, r.dcs)
	pbx.nextCDR(t)
}

// TestConferenceWithBrowser fails if a browser cannot sit in a server
// conference room with a desk phone: its join is answered with a browser SDP
// (audio only), its leg comes up, what it says reaches the other participant
// through the mixer, the room's mix reaches it encrypted, and a browser that
// pages a group is refused (a page is a phone's one-way fan-out).
func TestConferenceWithBrowser(t *testing.T) {
	pbx := startWebPBX(t, confDevices(), true, withRooms(room800()))
	a := newBrowser(t, pbx, "a1", "pa", false)
	// PCMU only: the room would take Opus from a browser, which this test
	// client does not encode.
	a.sdp = strings.Replace(strings.Replace(a.sdp, "111 0 126", "0 126", 1), "a=rtpmap:111 opus/48000/2\r\n", "", 1)
	b := newPhone(t, pbx, "b1", "pb1")
	registerOver(t, a.phone, "wss")
	b.register(t)

	r := waitCall(t, webCall(t, a, "800"))
	if r.err != nil {
		t.Fatalf("the browser's join: %v", r.err)
	}
	ans := r.dcs.InviteResponse.Body()
	sess, _, _, _, _, _ := candidate(t, ans)
	if err := media.ValidateWebRTC(sess); err != nil || len(sess.Media()) != 1 {
		t.Fatalf("the join was not answered with a one-section browser SDP: %v\n%s", err, ans)
	}
	a.connect(t, ans, false)
	toRoom := candidateAddr(t, ans)
	sb := mustJoin(t, b, "800")
	sb.drain()
	time.Sleep(300 * time.Millisecond)

	// The browser talks; the desk phone hears it.
	heard := make(chan int, 1)
	go func() { heard <- sb.hear(1500 * time.Millisecond) }()
	end := time.Now().Add(1500 * time.Millisecond)
	for i := 0; time.Now().Before(end); i++ {
		pcm := make([]byte, 320)
		for j := range 160 {
			v := int16(8000 * math.Sin(2*math.Pi*500*float64(i*160+j)/8000))
			pcm[2*j], pcm[2*j+1] = byte(v), byte(v>>8)
		}
		a.send(t, toRoom, media.BuildRTP(0, uint16(i+1), uint32(i)*160, 4242, false, media.EncodePCMU(pcm)))
		time.Sleep(20 * time.Millisecond)
	}
	if got := <-heard; got < 3000 {
		t.Fatalf("the desk phone heard peak %d while the browser talked", got)
	}
	// The room's mix reaches the browser, encrypted.
	if _, ok := a.recv(t, 3*time.Second); !ok {
		t.Fatal("nothing from the room reached the browser")
	}
	sb.leave()
	hangup(t, r.dcs)
	pbx.nextCDR(t)
	pbx.nextCDR(t)
}

// TestBrowserCannotPage fails if a browser's call to a paging group is not
// refused with 488 and a reason in the trace.
func TestBrowserCannotPage(t *testing.T) {
	pbx := startWebPBX(t, confDevices(), true, withPaging(snapshot.PagingGroup{ID: 1, Name: "all", Code: "*60", Priority: "normal", Enabled: true, Members: []string{"200"}}, pageExts()...))
	a := newBrowser(t, pbx, "a1", "pa", false)
	registerOver(t, a.phone, "wss")
	r := waitCall(t, webCall(t, a, "*60"))
	if r.err == nil || !strings.Contains(r.err.Error(), "488") {
		t.Fatalf("a browser's page = %v, want 488", r.err)
	}
	if rec := pbx.nextCDR(t); !strings.Contains(rec.FailureReason, "browser cannot page") {
		t.Errorf("reason %q", rec.FailureReason)
	}
}
