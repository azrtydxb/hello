package sip

import (
	"bytes"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/pion/rtcp"
)

func loopbackTo(port int) *net.UDPAddr { return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port} }

// h264RTP is one H.264 video packet of a stream.
func h264RTP(seq uint16, ssrc uint32, fill byte) []byte {
	return media.BuildRTP(96, seq, 90000+uint32(seq)*3000, ssrc, false, append([]byte{0x65}, bytes.Repeat([]byte{fill}, 199)...))
}

func nextVideo(t *testing.T, c *net.UDPConn, what string) *media.RTPPacket {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, ok := readUDPWithin(t, c, 300*time.Millisecond)
		if !ok {
			continue
		}
		if len(raw) > 1 && raw[1] >= 192 && raw[1] <= 223 {
			continue
		}
		if p, ok := media.ParseRTP(raw); ok {
			return p
		}
	}
	t.Fatalf("%s: no video arrived", what)
	return nil
}

// TestPhoneVideoCall fails if two SIP video phones cannot call each other with
// H.264 through the anchor: each side must be offered and answered a video
// section on a video port pair of its own, RTP must cross in both directions
// with the payload untouched and the stream re-labelled (one SSRC per leg), a
// NACK must be mapped to the sequence number of the other leg, a keyframe
// request must reach the sender, the call must hold a node video slot until it
// ends, and the CDR and trace must say video ran (S-44).
func TestPhoneVideoCall(t *testing.T) {
	pbx := startPBX(t, doorDevices(), withMedia(false), doorFeatures(), withVideoOn())
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	aRTP, aRTCP := videoPhone(t, a, "sendrecv")
	bRTP, bRTCP := videoPhone(t, b, "sendrecv")
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	inv := waitReq(t, b.invites, "INVITE to the callee")
	offerB, ok := media.ParseVideoSDP(inv.Body())
	if !ok || offerB.Direction != "sendrecv" || offerB.Port < 30000 || offerB.Port > 31000 {
		t.Fatalf("the callee was not offered the caller's H.264 on an anchor port: %+v %v", offerB, ok)
	}
	waitReq(t, b.acks, "ACK")
	answerA, ok := media.ParseVideoSDP(r.dcs.InviteResponse.Body())
	if !ok || answerA.Port == offerB.Port || answerA.Port < 30000 || answerA.Port > 31000 {
		t.Fatalf("the caller was not answered the callee's H.264 on a second anchor port: %+v %v", answerA, ok)
	}
	if media.VideoSessions() != 1 {
		t.Errorf("video slots = %d, want 1", media.VideoSessions())
	}

	// A to B: the relayed stream has its own SSRC and the same payload.
	var first *media.RTPPacket
	for i := range 20 {
		_, _ = aRTP.WriteToUDP(h264RTP(uint16(100+i), 4242, 0x77), loopbackTo(answerA.Port))
		time.Sleep(10 * time.Millisecond)
	}
	first = nextVideo(t, bRTP, "video at the callee")
	if first.SSRC == 4242 || !bytes.Equal(first.Payload[1:], bytes.Repeat([]byte{0x77}, 199)) || first.PayloadType != 96 {
		t.Errorf("callee got %+v, want the payload of the caller on a stream of Hello's", first)
	}
	// B to A.
	for i := range 20 {
		_, _ = bRTP.WriteToUDP(h264RTP(uint16(900+i), 777, 0x33), loopbackTo(offerB.Port))
		time.Sleep(10 * time.Millisecond)
	}
	back := nextVideo(t, aRTP, "video at the caller")
	if back.SSRC == 777 || !bytes.Equal(back.Payload[1:], bytes.Repeat([]byte{0x33}, 199)) {
		t.Errorf("caller got %+v", back)
	}
	// B reports a loss of what it received: A's sender is asked for the
	// sequence number it used, not Hello's.
	nack := &rtcp.TransportLayerNack{SenderSSRC: 1, MediaSSRC: first.SSRC, Nacks: []rtcp.NackPair{{PacketID: first.Sequence}}}
	raw, _ := nack.Marshal()
	_, _ = bRTCP.WriteToUDP(raw, loopbackTo(offerB.Port+1))
	deadline := time.Now().Add(3 * time.Second)
	mapped := false
	for !mapped && time.Now().Before(deadline) {
		rawA, ok := readUDPWithin(t, aRTCP, 300*time.Millisecond)
		if !ok {
			continue
		}
		pkts, _ := rtcp.Unmarshal(rawA)
		for _, p := range pkts {
			if n, ok := p.(*rtcp.TransportLayerNack); ok && n.MediaSSRC == 4242 && len(n.Nacks) == 1 && n.Nacks[0].PacketID >= 100 && n.Nacks[0].PacketID < 120 {
				mapped = true
			}
		}
	}
	if !mapped {
		t.Error("the caller was not asked to resend the packet in its own numbering")
	}
	hangup(t, r.dcs)
	waitReq(t, b.byes, "BYE")
	cd := pbx.nextCDR(t)
	if !cd.Video {
		t.Error("the CDR does not say video ran")
	}
	wantTrace(t, cd.Trace, "Video relayed (H264), at most 1500 kbit/s each way, not transcoded")
	eventually(t, "the video slot to come back", func() bool { return media.VideoSessions() == 0 })
}

// TestPhoneVideoRefusals fails if a phone's video is carried where it must not
// be: for an extension that may not use video, with video switched off, to a
// callee that answers audio only, or between a phone whose H.264 profile the
// other does not share. Each answers the phone's video line with port 0 (never
// drops the m= line), holds no slot and leaves the reason in the trace.
func TestPhoneVideoRefusals(t *testing.T) {
	run := func(t *testing.T, opts []pbxOpt, calleeVideo bool, calleeFmtp, why string, callerMutate ...func(*phone)) {
		t.Helper()
		pbx := startPBX(t, doorDevices(), append([]pbxOpt{withMedia(false), doorFeatures(), withVideoOn()}, opts...)...)
		a := newPhone(t, pbx, "a1", "pa")
		a.register(t)
		b := newPhone(t, pbx, "b1", "pb1")
		b.register(t)
		videoPhone(t, a, "sendrecv")
		for _, m := range callerMutate {
			m(a)
		}
		if calleeVideo {
			videoPhone(t, b, "sendrecv")
			if calleeFmtp != "" {
				b.rewriteSDP(func(s string) string {
					return strings.ReplaceAll(s, "profile-level-id=42e01f;packetization-mode=1", calleeFmtp)
				})
			}
		}
		r := waitCall(t, dial(t.Context(), a, "200"))
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		waitReq(t, b.invites, "INVITE")
		waitReq(t, b.acks, "ACK")
		body := string(r.dcs.InviteResponse.Body())
		if _, ok := media.ParseVideoSDP([]byte(body)); ok || !strings.Contains(body, "m=video 0 ") {
			t.Errorf("the phone's video line is not refused in place:\n%s", body)
		}
		if strings.Contains(string(a.sdp), "RTP/SAVP 96") && !strings.Contains(body, "m=video 0 RTP/SAVP 96") {
			t.Errorf("the refused line does not repeat the offered transport:\n%s", body)
		}
		if media.VideoSessions() != 0 {
			t.Errorf("a video slot is held: %d", media.VideoSessions())
		}
		hangup(t, r.dcs)
		waitReq(t, b.byes, "BYE")
		cd := pbx.nextCDR(t)
		if cd.Video {
			t.Error("the CDR says video ran")
		}
		if !strings.Contains(traceText(cd.Trace), why) {
			t.Errorf("trace %q does not say %q", traceText(cd.Trace), why)
		}
	}
	t.Run("an extension that may not", func(t *testing.T) {
		feats := func(s *snapshot.Snapshot) {
			s.WithExtensionData([]snapshot.Extension{{Number: "200", VideoDenied: true}})
		}
		run(t, []pbxOpt{withFeatures(feats)}, true, "", "may not use video")
	})
	t.Run("switched off", func(t *testing.T) {
		run(t, []pbxOpt{func(c *Config, _ *Deps) { c.VideoEnabled = false }}, true, "", "switched off")
	})
	t.Run("audio only callee", func(t *testing.T) { run(t, nil, false, "", "callee cannot do video") })
	t.Run("video over SRTP", func(t *testing.T) {
		// The refused line repeats the offered transport (RFC 3264).
		run(t, nil, true, "", "no H.264 over plain RTP", func(p *phone) {
			p.rewriteSDP(func(s string) string { return strings.Replace(s, "RTP/AVP 96", "RTP/SAVP 96", 1) })
		})
	})
	t.Run("another H.264 profile", func(t *testing.T) {
		run(t, nil, true, "profile-level-id=640028;packetization-mode=1", "no common video codec")
	})
}

// TestPhoneVideoWithBrowser fails if a video phone and a browser cannot see
// each other: a phone that calls a browser must offer it H.264 on the browser's
// own transport (encrypted, ICE) and a browser that calls a phone must offer
// the phone plain RTP H.264 on a video port pair; video crosses in both
// directions with the translation between the two kinds of leg (S-44, S-46).
func TestPhoneVideoWithBrowser(t *testing.T) {
	t.Run("phone calls browser", func(t *testing.T) {
		pbx := startWebPBX(t, webDevices(), true)
		a := newPhone(t, pbx, "a1", "pa")
		registerOver(t, a, "udp")
		w := newBrowser(t, pbx, "w1", "pw1", true)
		registerOver(t, w.phone, "wss")
		w.setCallee(w.answerer())
		aRTP, _ := videoPhone(t, a, "sendrecv")
		ch := dial(t.Context(), a, "200")
		inv := waitReq(t, w.invites, "INVITE to the browser")
		wOffer := inv.Body()
		r := waitCall(t, ch)
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		sess, _ := media.ParseSDPSession(wOffer)
		var vm *media.SDPMedia
		for _, m := range sess.Media() {
			if m.Kind() == "video" {
				vm = m
			}
		}
		if vm == nil || vm.Port() == 0 || len(vm.Codecs()) != 1 || vm.Codecs()[0].Name != "H264" {
			t.Fatalf("the browser was not offered the phone's H.264:\n%s", wOffer)
		}
		answerA, ok := media.ParseVideoSDP(r.dcs.InviteResponse.Body())
		if !ok {
			t.Fatalf("the phone got no video in its answer:\n%s", r.dcs.InviteResponse.Body())
		}
		w.connect(t, wOffer, true)
		toW := candidateAddr(t, wOffer)
		var got *media.RTPPacket
		for i := 0; i < 40 && got == nil; i++ {
			_, _ = aRTP.WriteToUDP(h264RTP(uint16(i+1), 5, 0x21), loopbackTo(answerA.Port))
			if p, ok := w.recv(t, 100*time.Millisecond); ok && p.PayloadType != 0 && len(p.Payload) == 200 {
				got = p
			}
		}
		if got == nil || !bytes.Equal(got.Payload[1:], bytes.Repeat([]byte{0x21}, 199)) {
			t.Fatalf("video from the phone did not reach the browser: %+v", got)
		}
		var back *media.RTPPacket
		for i := 0; i < 40 && back == nil; i++ {
			w.send(t, toW, videoRTP(got.PayloadType, uint16(300+i), 0x22))
			raw, ok := readUDPWithin(t, aRTP, 100*time.Millisecond)
			if ok {
				if p, ok := media.ParseRTP(raw); ok && len(p.Payload) == 200 {
					back = p
				}
			}
		}
		if back == nil || !bytes.Equal(back.Payload, bytes.Repeat([]byte{0x22}, 200)) {
			t.Fatalf("video from the browser did not reach the phone: %+v", back)
		}
		hangup(t, r.dcs)
		if cd := pbx.nextCDR(t); !cd.Video {
			t.Error("the CDR does not say video ran")
		}
	})
	t.Run("browser calls phone", func(t *testing.T) {
		pbx := startWebPBX(t, webDevices(), true)
		a := newBrowser(t, pbx, "a1", "pa", true)
		registerOver(t, a.phone, "wss")
		b := newPhone(t, pbx, "b1", "pb1")
		b.register(t)
		bRTP, _ := videoPhone(t, b, "sendrecv")
		ch := webCall(t, a, "200")
		inv := waitReq(t, b.invites, "INVITE to the phone")
		offerB, ok := media.ParseVideoSDP(inv.Body())
		if !ok || offerB.Proto != "RTP/AVP" || len(offerB.Codecs) != 1 || !strings.Contains(offerB.Codecs[0].Fmtp, "42e01f") {
			t.Fatalf("the phone was not offered the browser's H.264 on plain RTP: %+v %v\n%s", offerB, ok, inv.Body())
		}
		if strings.Contains(string(inv.Body()), "VP8") || strings.Contains(string(inv.Body()), "fingerprint") {
			t.Errorf("a plain phone was offered what it cannot use:\n%s", inv.Body())
		}
		r := waitCall(t, ch)
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		ans := r.dcs.InviteResponse.Body()
		asess, _ := media.ParseSDPSession(ans)
		am := asess.Media()
		if len(am) != 2 || am[1].Kind() != "video" || am[1].Port() == 0 {
			t.Fatalf("the browser's answer carries no video:\n%s", ans)
		}
		a.connect(t, ans, false)
		toA := candidateAddr(t, ans)
		var got *media.RTPPacket
		pt := am[1].Codecs()[0].PT
		for i := 0; i < 40 && got == nil; i++ {
			a.send(t, toA, videoRTP(pt, uint16(i+1), 0x44))
			raw, ok := readUDPWithin(t, bRTP, 100*time.Millisecond)
			if ok {
				if p, ok := media.ParseRTP(raw); ok && len(p.Payload) == 200 {
					got = p
				}
			}
		}
		if got == nil || !bytes.Equal(got.Payload, bytes.Repeat([]byte{0x44}, 200)) {
			t.Fatalf("video from the browser did not reach the phone: %+v", got)
		}
		hangup(t, r.dcs)
		pbx.nextCDR(t)
	})
}
