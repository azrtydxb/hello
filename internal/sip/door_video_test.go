package sip

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/media"
)

// udpConn binds a loopback UDP socket on a free port.
func udpConn(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func portOf4(c *net.UDPConn) int { return c.LocalAddr().(*net.UDPAddr).Port }

// videoPhone gives a test phone an SDP with an audio and an H.264 video
// stream on sockets of its own and returns the sockets: video RTP and RTCP.
func videoPhone(t *testing.T, p *phone, dir string) (rtp, rtcp *net.UDPConn) {
	t.Helper()
	audio, rtp, rtcp := udpConn(t), udpConn(t), udpConn(t)
	p.sdp = fmt.Sprintf("v=0\r\no=%s 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n"+
		"m=audio %d RTP/AVP 0 101\r\na=rtpmap:101 telephone-event/8000\r\n"+
		"m=video %d RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\na=fmtp:96 profile-level-id=42e01f;packetization-mode=1\r\n"+
		"a=rtcp:%d\r\na=%s\r\n", p.user, portOf4(audio), portOf4(rtp), portOf4(rtcp), dir)
	return rtp, rtcp
}

func readUDP(t *testing.T, c *net.UDPConn, what string) []byte {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1500)
	n, _, err := c.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	return buf[:n]
}

// TestDoorVideoPassthrough fails if a door call offering H.264 video to a
// callee whose SDP accepts video loses the video m-line on either side, does
// not relay the video RTP and RTCP through ports of its own, adds video to a
// callee that did not offer it (the door's answer must refuse the stream with
// port 0), or carries video on a call without a door station (spec S-49).
func TestDoorVideoPassthrough(t *testing.T) {
	pbx := startPBX(t, doorDevices(), withMedia(false), doorFeatures())
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	user := newPhone(t, pbx, "a1", "pa")
	user.register(t)
	door := newPhone(t, pbx, "d1", "pd")
	door.register(t)

	t.Run("the callee accepts the video", func(t *testing.T) {
		doorRTP, doorRTCP := videoPhone(t, door, "sendonly")
		bRTP, bRTCP := videoPhone(t, b, "recvonly")
		r := waitCall(t, dial(t.Context(), door, "800"))
		if r.err != nil {
			t.Fatalf("door call: %v", r.err)
		}
		inv := waitReq(t, b.invites, "INVITE to the callee")
		offerB, ok := media.ParseVideoSDP(inv.Body())
		if !ok || offerB.Port < 30000 || offerB.Port > 31000 || offerB.Port == portOf4(doorRTP) || offerB.Direction != "sendonly" ||
			len(offerB.Codecs) != 1 || offerB.Codecs[0].PT != 96 || offerB.Codecs[0].Fmtp != "profile-level-id=42e01f;packetization-mode=1" {
			t.Fatalf("the callee's offer = %+v, %v; want the door's H.264 on an anchor port, sendonly", offerB, ok)
		}
		waitReq(t, b.acks, "ACK to the callee")
		answerA, ok := media.ParseVideoSDP(r.dcs.InviteResponse.Body())
		if !ok || answerA.Port < 30000 || answerA.Port > 31000 || answerA.Port == offerB.Port || answerA.Direction != "recvonly" {
			t.Fatalf("the door's answer = %+v, %v; want the callee's H.264 on a second anchor port, recvonly", answerA, ok)
		}
		if a, err := media.ParseAudioSDP(r.dcs.InviteResponse.Body()); err != nil || a.Port == answerA.Port {
			t.Errorf("audio of the answer = %+v, %v", a, err)
		}

		// Video RTP from the door reaches the callee unchanged in payload.
		payload := []byte{0x67, 0x42, 0xe0, 0x1f, 0x99}
		pkt := media.BuildRTP(96, 1, 3000, 77, true, payload)
		if _, err := doorRTP.WriteToUDP(pkt, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: answerA.Port}); err != nil {
			t.Fatal(err)
		}
		got, ok := media.ParseRTP(readUDP(t, bRTP, "video RTP at the callee"))
		if !ok || got.PayloadType != 96 || !got.Marker || string(got.Payload) != string(payload) {
			t.Errorf("callee got %+v, %v", got, ok)
		}
		// A keyframe request from the callee's phone reaches the door.
		pli := []byte{0x81, 206, 0, 2, 0, 0, 0, 1, 0, 0, 0, 2}
		if _, err := bRTCP.WriteToUDP(pli, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: offerB.Port + 1}); err != nil {
			t.Fatal(err)
		}
		if got := readUDP(t, doorRTCP, "RTCP at the door"); string(got) != string(pli) {
			t.Errorf("door got RTCP %x, want the picture loss indication %x", got, pli)
		}
		hangup(t, r.dcs)
		waitReq(t, b.byes, "BYE to the callee")
		cd := pbx.nextCDR(t)
		wantTrace(t, cd.Trace, "Video offered by the caller (H.264), relayed through the anchor", "Video accepted by the callee (H.264)")
	})

	t.Run("a callee without video gets none and the door is told", func(t *testing.T) {
		videoPhone(t, door, "sendonly")
		b.sdp = strings.Split(b.sdp, "m=video")[0] // audio only
		r := waitCall(t, dial(t.Context(), door, "800"))
		if r.err != nil {
			t.Fatalf("door call: %v", r.err)
		}
		waitReq(t, b.invites, "INVITE to the callee")
		waitReq(t, b.acks, "ACK to the callee")
		body := string(r.dcs.InviteResponse.Body())
		if v, ok := media.ParseVideoSDP([]byte(body)); ok {
			t.Errorf("the door was given video the callee never accepted: %+v", v)
		}
		if !strings.Contains(body, "m=video 0 ") {
			t.Errorf("the door's answer does not refuse its video stream:\n%s", body)
		}
		if a, err := media.ParseAudioSDP([]byte(body)); err != nil || a.Port < 30000 {
			t.Errorf("audio of the answer = %+v, %v", a, err)
		}
		hangup(t, r.dcs)
		waitReq(t, b.byes, "BYE to the callee")
		cd := pbx.nextCDR(t)
		wantTrace(t, cd.Trace, "Video refused by the callee")
	})

	t.Run("a call without a door station carries no video", func(t *testing.T) {
		videoPhone(t, user, "sendrecv")
		videoPhone(t, b, "sendrecv")
		r := waitCall(t, dial(t.Context(), user, "200"))
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		inv := waitReq(t, b.invites, "INVITE to the callee")
		if strings.Contains(string(inv.Body()), "m=video") || strings.Contains(string(r.dcs.InviteResponse.Body()), "m=video") {
			t.Error("video was carried on a call with no door station")
		}
		hangup(t, r.dcs)
		pbx.nextCDR(t)
	})

	t.Run("an audio-only door call stays audio-only", func(t *testing.T) {
		door.sdp = strings.Split(door.sdp, "m=video")[0]
		videoPhone(t, b, "recvonly")
		r := waitCall(t, dial(t.Context(), door, "800"))
		if r.err != nil {
			t.Fatalf("door call: %v", r.err)
		}
		inv := waitReq(t, b.invites, "INVITE to the callee")
		if strings.Contains(string(inv.Body()), "m=video") || strings.Contains(string(r.dcs.InviteResponse.Body()), "m=video") {
			t.Error("video appeared on a door call that offered none")
		}
		hangup(t, r.dcs)
		pbx.nextCDR(t)
	})
}
