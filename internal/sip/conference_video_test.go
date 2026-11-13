package sip

import (
	"bytes"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
)

func videoRoom() snapshot.Conference {
	r := room800()
	r.Video = true
	return r
}

// TestConferenceVideoPhones fails if a room does not carry its participants'
// video as the spec says: a video phone that joins must be answered a video
// section on a port pair of its own, the first participant's picture (the
// speaker until someone else is louder) must reach the others untouched and not
// come back to the speaker, a room whose video is off must refuse the video line
// in place and stay audio, so must an extension that may not use video, and the
// slots the participants took must come back when they leave (S-45).
func TestConferenceVideoPhones(t *testing.T) {
	pbx := startPBX(t, confDevices(), withMedia(false), withVideoOn(), withRooms(videoRoom()))
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	aRTP, _ := videoPhone(t, a, "sendrecv")
	bRTP, _ := videoPhone(t, b, "sendrecv")
	sa := mustJoin(t, a, "800")
	sb := mustJoin(t, b, "800")
	ansA, okA := media.ParseVideoSDP(sa.dcs.InviteResponse.Body())
	ansB, okB := media.ParseVideoSDP(sb.dcs.InviteResponse.Body())
	if !okA || !okB || ansA.Port == ansB.Port || ansA.Codecs[0].PT != 96 {
		t.Fatalf("answers carry no video of their own: %+v %v / %+v %v", ansA, okA, ansB, okB)
	}
	if media.VideoSessions() != 2 {
		t.Errorf("video slots = %d, want 2", media.VideoSessions())
	}
	// A keyframe and some deltas from A reach B, untouched, and not A itself.
	var got *media.RTPPacket
	for i := 0; i < 60 && got == nil; i++ {
		for _, p := range h264Pictures(1, 4001) {
			for _, raw := range p {
				_, _ = aRTP.WriteToUDP(raw, loopbackTo(ansA.Port))
			}
		}
		if raw, ok := readUDPWithin(t, bRTP, 50*time.Millisecond); ok {
			if p, ok := media.ParseRTP(raw); ok && len(p.Payload) > 100 {
				got = p
			}
		}
	}
	if got == nil || got.PayloadType != 96 || !bytes.Contains(got.Payload, bytes.Repeat([]byte{0x77}, 100)) {
		t.Fatalf("B did not get A's picture: %+v", got)
	}
	if raw, ok := readUDPWithin(t, aRTP, 300*time.Millisecond); ok {
		if p, ok := media.ParseRTP(raw); ok && len(p.Payload) > 100 {
			t.Errorf("the speaker was sent its own picture: %+v", p)
		}
	}
	sa.leave()
	sb.leave()
	pbx.nextCDR(t)
	cd := pbx.nextCDR(t)
	if !cd.Video {
		t.Error("the CDR does not say the call carried video")
	}
	wantTrace(t, cd.Trace, "Conference video: the room forwards the active speaker (H264), at most 1500 kbit/s, not transcoded")
	eventually(t, "the video slots to come back", func() bool { return media.VideoSessions() == 0 })
}

// TestConferenceVideoRefused fails if a video line is carried where the room or
// the policy says it must not be: the room's video switch off, the extension not
// allowed, video switched off for the node, or a codec the room cannot forward.
// Each answers the line with port 0 and keeps the participant in the audio.
func TestConferenceVideoRefused(t *testing.T) {
	run := func(t *testing.T, room snapshot.Conference, opts []pbxOpt, mutate func(p *phone), why string) {
		t.Helper()
		pbx := startPBX(t, confDevices(), append([]pbxOpt{withMedia(false), withVideoOn(), withRooms(room)}, opts...)...)
		a := newPhone(t, pbx, "a1", "pa")
		a.register(t)
		videoPhone(t, a, "sendrecv")
		if mutate != nil {
			mutate(a)
		}
		s := mustJoin(t, a, "800")
		body := string(s.dcs.InviteResponse.Body())
		if _, ok := media.ParseVideoSDP([]byte(body)); ok || !strings.Contains(body, "m=video 0 ") {
			t.Errorf("the video line was not refused in place:\n%s", body)
		}
		if media.VideoSessions() != 0 {
			t.Errorf("a video slot is held: %d", media.VideoSessions())
		}
		s.leave()
		cd := pbx.nextCDR(t)
		if cd.Video {
			t.Error("the CDR says the call carried video")
		}
		if !strings.Contains(traceText(cd.Trace), why) {
			t.Errorf("trace %q does not say %q", traceText(cd.Trace), why)
		}
	}
	t.Run("the room has video off", func(t *testing.T) {
		r := room800()
		r.Video = false
		run(t, r, nil, nil, "this room has video switched off")
	})
	t.Run("an extension that may not", func(t *testing.T) {
		feats := func(s *snapshot.Snapshot) {
			s.WithExtensionData([]snapshot.Extension{{Number: "100", VideoDenied: true}})
		}
		run(t, videoRoom(), []pbxOpt{withFeatures(feats)}, nil, "may not use video")
	})
	t.Run("switched off for the node", func(t *testing.T) {
		run(t, videoRoom(), []pbxOpt{func(c *Config, _ *Deps) { c.VideoEnabled = false }}, nil, "switched off")
	})
	t.Run("another H.264 profile than the room can forward", func(t *testing.T) {
		run(t, videoRoom(), nil, func(p *phone) {
			p.rewriteSDP(func(s string) string {
				return strings.ReplaceAll(s, "profile-level-id=42e01f;packetization-mode=1", "profile-level-id=64001f;packetization-mode=1")
			})
		}, "offers none it can forward")
	})
}

// TestConferenceVideoBrowser fails if a browser cannot take part in a room's
// video: it must be answered a browser SDP with its video section (the room's
// H.264, on the transport of its audio, one bundle), the picture of a phone that
// is in the room reaches it encrypted, and a phone offered the same room is
// answered the same codec.
func TestConferenceVideoBrowser(t *testing.T) {
	pbx := startWebPBX(t, confDevices(), true, withRooms(videoRoom()))
	a := newBrowser(t, pbx, "a1", "pa", true)
	a.sdp = strings.Replace(strings.Replace(a.sdp, "111 0 126", "0 126", 1), "a=rtpmap:111 opus/48000/2\r\n", "", 1)
	b := newPhone(t, pbx, "b1", "pb1")
	registerOver(t, a.phone, "wss")
	b.register(t)
	bRTP, _ := videoPhone(t, b, "sendrecv")

	r := waitCall(t, webCall(t, a, "800"))
	if r.err != nil {
		t.Fatalf("the browser's join: %v", r.err)
	}
	ans := r.dcs.InviteResponse.Body()
	sess, _, _, _, _, _ := candidate(t, ans)
	if err := media.ValidateWebRTC(sess); err != nil || len(sess.Media()) != 2 || sess.Media()[1].Kind() != "video" || sess.Media()[1].Port() == 0 {
		t.Fatalf("the join was not answered with a two-section browser SDP: %v\n%s", err, ans)
	}
	vc := sess.Media()[1].Codecs()
	if len(vc) != 1 || vc[0].Name != "H264" {
		t.Fatalf("the browser's video codec = %+v, want the one H.264", vc)
	}
	a.connect(t, ans, false)
	toRoom := candidateAddr(t, ans)
	sb := mustJoin(t, b, "800")
	ansB, ok := media.ParseVideoSDP(sb.dcs.InviteResponse.Body())
	if !ok || len(ansB.Codecs) != 1 {
		t.Fatalf("the phone was not answered the room's H.264: %+v %v", ansB, ok)
	}
	// The browser talks (and sends its picture), so it is the speaker once the
	// room's dwell is over; the phone then gets its picture.
	var got *media.RTPPacket
	pt := vc[0].PT
	for i := 0; i < 250 && got == nil; i++ {
		pcm := make([]byte, 320)
		for j := range 160 {
			v := int16(9000 * math.Sin(2*math.Pi*500*float64(i*160+j)/8000))
			pcm[2*j], pcm[2*j+1] = byte(v), byte(v>>8)
		}
		a.send(t, toRoom, media.BuildRTP(0, uint16(i+1), uint32(i)*160, 4242, false, media.EncodePCMU(pcm)))
		if i%2 == 0 {
			a.send(t, toRoom, media.BuildRTP(pt, uint16(i/2+1), 90000+uint32(i/2)*3000, 4343, true, append([]byte{0x65}, bytes.Repeat([]byte{0x31}, 150)...)))
		}
		if raw, ok := readUDPWithin(t, bRTP, 20*time.Millisecond); ok {
			if p, ok := media.ParseRTP(raw); ok && len(p.Payload) > 100 {
				got = p
			}
		}
	}
	if got == nil || !bytes.Contains(got.Payload, bytes.Repeat([]byte{0x31}, 100)) {
		t.Fatalf("the phone did not get the browser's picture: %+v", got)
	}
	sb.leave()
	hangup(t, r.dcs)
	pbx.nextCDR(t)
	pbx.nextCDR(t)
	eventually(t, "the video slots to come back", func() bool { return media.VideoSessions() == 0 })
}
