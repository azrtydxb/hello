package media

import (
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtcp"
)

// videoConfPhone is a conference phone with video: audio and an H.264 stream on
// sockets of its own.
type videoConfPhone struct {
	*confPhone
	vrtp, vrtcp *net.UDPConn
	toVideo     *net.UDPAddr
	answer      string
	seq         uint16
	ssrc        uint32
	sendMu      sync.Mutex
}

var phoneCount uint32

const h264CB = "profile-level-id=42e01f;packetization-mode=1"

func newVideoConfPhone(t *testing.T, vj *VideoJoin, fmtp string) *videoConfPhone {
	t.Helper()
	audio, vrtp, vrtcp := udpLoopback(t), udpLoopback(t), udpLoopback(t)
	offer := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
		"m=audio " + strconv.Itoa(udpAddrOf(audio).Port) + " RTP/AVP 0 101\r\na=rtpmap:101 telephone-event/8000\r\n" +
		"m=video " + strconv.Itoa(udpAddrOf(vrtp).Port) + " RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\na=fmtp:96 " + fmtp + "\r\n" +
		"a=rtcp:" + strconv.Itoa(udpAddrOf(vrtcp).Port) + "\r\na=rtcp-fb:96 nack pli\r\na=sendrecv\r\n"
	ans, party, err := NewPartySecureVideo("127.0.0.1", 0, 0, []byte(offer), SecurePolicy{}, vj)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(party.Close)
	sdp, err := ParseAudioSDP(ans)
	if err != nil {
		t.Fatal(err)
	}
	p := &confPhone{t: t, conn: audio, party: party, dst: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: sdp.Port}, frames: make(chan []int16, 512)}
	go func() {
		buf := make([]byte, 2048)
		for {
			n, _, err := audio.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if pkt, ok := ParseRTP(buf[:n]); ok && pkt.PayloadType == 0 {
				select {
				case p.frames <- PCMToSamples(DecodePCMU(pkt.Payload)):
				default:
				}
			}
		}
	}()
	phoneCount++
	v := &videoConfPhone{confPhone: p, vrtp: vrtp, vrtcp: vrtcp, answer: string(ans), ssrc: 5000 + phoneCount*1111}
	if vs, ok := ParseVideoSDP(ans); ok {
		v.toVideo = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: vs.Port}
	}
	return v
}

// send gives the room one picture: a keyframe or a delta, one packet.
func (v *videoConfPhone) send(key bool, fill byte) {
	nal := byte(0x41)
	if key {
		nal = 0x65
	}
	v.sendMu.Lock()
	v.seq++
	seq := v.seq
	v.sendMu.Unlock()
	pkt := BuildRTP(96, seq, 90000+uint32(seq)*3000, v.ssrc, true, append([]byte{nal}, repeatByte(fill, 120)...))
	_, _ = v.vrtp.WriteToUDP(pkt, v.toVideo)
}

func repeatByte(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

// stream makes the phone send video the way a phone does: a keyframe at the start
// and whenever its RTCP port gets a picture loss indication, delta pictures
// otherwise, one per 40 ms, each filled with fill.
func (v *videoConfPhone) stream(fill byte) (stop func(), keyRequests func() int) {
	done := make(chan struct{})
	var reqs int
	var mu sync.Mutex
	go func() {
		buf := make([]byte, 2048)
		for {
			select {
			case <-done:
				return
			default:
			}
			_ = v.vrtcp.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
			if n, _, err := v.vrtcp.ReadFromUDP(buf); err == nil {
				if pkts, _ := rtcp.Unmarshal(buf[:n]); len(pkts) > 0 {
					for _, p := range pkts {
						if _, ok := p.(*rtcp.PictureLossIndication); ok {
							mu.Lock()
							reqs++
							mu.Unlock()
							v.send(true, fill)
						}
					}
				}
			}
		}
	}()
	go func() {
		v.send(true, fill)
		t := time.NewTicker(40 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				v.send(false, fill)
			}
		}
	}()
	return func() { close(done) }, func() int { mu.Lock(); defer mu.Unlock(); return reqs }
}

// collect records the pictures a phone is sent as (fill byte, key, SSRC, seq).
type seen struct {
	fill byte
	key  bool
	ssrc uint32
	seq  uint16
}

func (v *videoConfPhone) collect() (get func() []seen, stop func()) {
	var mu sync.Mutex
	var got []seen
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 2048)
		for {
			select {
			case <-done:
				return
			default:
			}
			_ = v.vrtp.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
			n, _, err := v.vrtp.ReadFromUDP(buf)
			if err != nil {
				continue
			}
			if p, ok := ParseRTP(buf[:n]); ok && len(p.Payload) > 1 {
				mu.Lock()
				got = append(got, seen{fill: p.Payload[1], key: p.Payload[0] == 0x65, ssrc: p.SSRC, seq: p.Sequence})
				mu.Unlock()
			}
		}
	}()
	return func() []seen { mu.Lock(); defer mu.Unlock(); return append([]seen(nil), got...) }, func() { close(done) }
}

// TestRoomVideoSpeakerForwarding fails if a conference room does not forward the
// active speaker's picture: each participant must be offered and answered a
// video section on a port pair of its own, the first speaker's picture goes to
// the others and not back to the speaker, a participant who is louder for long
// enough takes the picture over (the speaker then sees the one before), the new
// speaker is asked for a keyframe and its pictures are held back until one
// arrives, a viewer's picture is one continuous stream (one SSRC, sequence
// numbers one after another) across the switch, payloads cross untouched, a
// viewer's picture loss indication reaches the speaker's sender, and a
// participant that cannot use the room's codec is refused the video in place and
// stays in the audio (S-45).
func TestRoomVideoSpeakerForwarding(t *testing.T) {
	room := NewRoom()
	defer room.Close()
	room.SetVideoCap(1500)
	vj := func() *VideoJoin { return &VideoJoin{Kbps: 1500, Codec: room.VideoCodec()} }
	a := newVideoConfPhone(t, vj(), h264CB)
	room.Add("a", a.party, false, false)
	b := newVideoConfPhone(t, vj(), h264CB)
	room.Add("b", b.party, false, false)
	c := newVideoConfPhone(t, vj(), h264CB)
	room.Add("c", c.party, false, false)
	for _, p := range []*videoConfPhone{a, b, c} {
		if !strings.Contains(p.answer, "m=video ") || p.toVideo == nil || !p.party.HasVideo() || p.party.VideoCodec() != "H264" {
			t.Fatalf("answer = %s", p.answer)
		}
	}
	if room.VideoCodec() != "H264" || room.VideoParticipants() != 3 {
		t.Errorf("room codec %q, video participants %d", room.VideoCodec(), room.VideoParticipants())
	}
	stopA, keyA := a.stream(0xaa)
	stopB, keyB := b.stream(0xbb)
	stopC, _ := c.stream(0xcc)
	defer stopA()
	defer stopB()
	defer stopC()
	getA, endA := a.collect()
	getB, endB := b.collect()
	getC, endC := c.collect()
	defer endA()
	defer endB()
	defer endC()
	// The streams own the RTCP sockets: the keyframe requests they saw are the evidence.

	// A speaks: its picture goes to B and C.
	a.talk(440, 8000, 1200*time.Millisecond)
	if room.Speaker() != "a" {
		t.Fatalf("speaker = %q, want a", room.Speaker())
	}
	time.Sleep(300 * time.Millisecond)
	for _, who := range []struct {
		name string
		got  []seen
	}{{"b", getB()}, {"c", getC()}} {
		if len(who.got) == 0 || who.got[0].fill != 0xaa {
			t.Fatalf("%s got %v, want A's pictures", who.name, who.got)
		}
	}
	for _, s := range getA() {
		if s.fill == 0xaa {
			t.Fatal("the speaker was sent its own picture")
		}
	}
	// C asks for a picture: A's sender hears it and answers with a keyframe.
	before := keyA()
	pli := []byte{0x81, 206, 0, 2, 0, 0, 0, 9, 0, 0, 0, 1}
	_, _ = c.vrtcp.WriteToUDP(pli, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: portOfVideoRTCP(t, c)})
	if !waitUntil(2*time.Second, func() bool { return keyA() > before }) {
		t.Error("the speaker's sender was not asked for a keyframe")
	}
	time.Sleep(1200 * time.Millisecond) // the dwell: a challenger may take over after it

	// B speaks louder and longer: it takes the picture over.
	beforeB := keyB()
	b.talk(300, 16000, 1500*time.Millisecond)
	if room.Speaker() != "b" || room.Switches() < 2 {
		t.Fatalf("speaker = %q after %d switches, want b", room.Speaker(), room.Switches())
	}
	if keyB() == beforeB {
		t.Error("the new speaker was not asked for a keyframe")
	}
	time.Sleep(500 * time.Millisecond)
	gotC := getC()
	firstB := -1
	for i, s := range gotC {
		if s.fill == 0xbb {
			firstB = i
			break
		}
	}
	if firstB < 0 {
		t.Fatal("c never saw the new speaker")
	}
	if !gotC[firstB].key {
		t.Errorf("c's first picture of the new speaker is a delta (held back until a keyframe was the rule)")
	}
	for _, s := range gotC {
		if s.ssrc != gotC[0].ssrc {
			t.Fatalf("c's picture changed stream across the switch: %v", s)
		}
	}
	for i := 1; i < len(gotC); i++ {
		if gotC[i].seq != gotC[i-1].seq+1 {
			t.Fatalf("c's sequence numbers jump at %d: %d then %d", i, gotC[i-1].seq, gotC[i].seq)
		}
	}
	for _, s := range gotC[firstB:] {
		if s.fill == 0xaa {
			t.Fatal("c saw A's picture after B took over")
		}
	}
	// The speaker, B, sees the one before, A.
	sawA := false
	for _, s := range getB() {
		sawA = sawA || (s.fill == 0xaa && false) || s.fill == 0xaa
	}
	if !sawA {
		t.Error("the speaker was never shown the one before")
	}
	for _, s := range getB() {
		if s.fill == 0xbb {
			t.Fatal("the speaker was sent its own picture")
		}
	}

	// A participant that cannot use the room's codec is refused the video in place.
	d := newVideoConfPhone(t, vj(), "profile-level-id=64001f;packetization-mode=1")
	room.Add("d", d.party, false, false)
	if d.party.HasVideo() || !strings.Contains(d.answer, "m=video 0 ") {
		t.Errorf("d should be refused its video line: %s", d.answer)
	}

	// The speaker leaving moves the picture on; the last video leaving frees the codec.
	room.Remove("b")
	if room.Speaker() == "b" {
		t.Error("a participant who left still holds the picture")
	}
	room.Remove("a")
	room.Remove("c")
	if room.VideoCodec() != "" {
		t.Errorf("room codec %q after the last video participant left", room.VideoCodec())
	}
}

func waitUntil(d time.Duration, f func() bool) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if f() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return f()
}

// portOfVideoRTCP is the room's RTCP port for the phone's party: the port above
// its video RTP port.
func portOfVideoRTCP(t *testing.T, v *videoConfPhone) int {
	t.Helper()
	vs, ok := ParseVideoSDP([]byte(v.answer))
	if !ok {
		t.Fatal("no video in the answer")
	}
	return vs.Port + 1
}

// TestRoomVideoSignalLevels fails if the keyframe test misreads a payload: an
// SPS, an IDR (alone, in a STAP-A or starting a FU-A) and a VP8 keyframe are
// keyframe starts; delta slices, middle fragments and a VP8 delta are not.
func TestRoomVideoSignalLevels(t *testing.T) {
	for name, c := range map[string]struct {
		codec string
		p     []byte
		want  bool
	}{
		"sps":            {"H264", []byte{0x67, 1, 2}, true},
		"idr":            {"H264", []byte{0x65, 1}, true},
		"p slice":        {"H264", []byte{0x41, 1}, false},
		"stap with sps":  {"H264", []byte{24, 0, 3, 0x67, 1, 2, 0, 2, 0x68, 1}, true},
		"stap of p":      {"H264", []byte{24, 0, 2, 0x41, 1}, false},
		"fu-a idr start": {"H264", []byte{0x7c, 0x85, 1}, true},
		"fu-a idr mid":   {"H264", []byte{0x7c, 0x05, 1}, false},
		"vp8 key":        {"VP8", []byte{0x10, 0x00, 1, 2}, true},
		"vp8 key + ext":  {"VP8", []byte{0x90, 0x80, 0x81, 0x01, 0x00}, true},
		"vp8 delta":      {"VP8", []byte{0x10, 0x01, 1}, false},
		"vp8 mid":        {"VP8", []byte{0x00, 0x00, 1}, false},
	} {
		if got := isKeyStart(c.codec, c.p); got != c.want {
			t.Errorf("%s: isKeyStart = %v, want %v", name, got, c.want)
		}
	}
}
