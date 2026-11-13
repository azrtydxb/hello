package media

import (
	"net"
	"strings"
	"testing"
	"time"
)

const doorOfferSDP = "v=0\r\no=- 1 1 IN IP4 192.0.2.50\r\ns=-\r\nc=IN IP4 192.0.2.50\r\nt=0 0\r\n" +
	"m=audio 5004 RTP/AVP 0 101\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:101 telephone-event/8000\r\na=sendrecv\r\n" +
	"m=video 5006 RTP/AVPF 96 97 98\r\nc=IN IP4 192.0.2.51\r\n" +
	"a=rtpmap:96 H264/90000\r\na=fmtp:96 profile-level-id=42e01f;packetization-mode=1\r\n" +
	"a=rtpmap:97 VP8/90000\r\na=rtpmap:98 H264/90000\r\na=fmtp:98 profile-level-id=64001f\r\n" +
	"a=rtcp-fb:* nack\r\na=rtcp-fb:96 nack pli\r\na=rtcp:5011\r\na=sendonly\r\n"

// TestVideoSDP fails if the H.264 section of an offer is not read (address,
// port, RTCP port, direction, the H.264 payload types only with their fmtp
// and feedback), if a refused, SRTP, non-H.264 or absent section is read as
// video, or if the section Hello writes for its own ports is not a valid
// m-line that parses back with the new port and the same payload types.
func TestVideoSDP(t *testing.T) {
	v, ok := ParseVideoSDP([]byte(doorOfferSDP))
	if !ok || v.Address != "192.0.2.51" || v.Port != 5006 || v.RTCPPort != 5011 || v.Direction != "sendonly" || v.Proto != "RTP/AVPF" {
		t.Fatalf("parsed = %+v, %v", v, ok)
	}
	if len(v.Codecs) != 2 || v.Codecs[0].PT != 96 || v.Codecs[1].PT != 98 || v.Codecs[0].ClockRate != 90000 {
		t.Fatalf("codecs = %+v, want the two H.264 types only", v.Codecs)
	}
	if v.Codecs[0].Fmtp != "profile-level-id=42e01f;packetization-mode=1" || strings.Join(v.Codecs[0].FB, ",") != "nack,nack pli" {
		t.Errorf("payload 96 = %+v", v.Codecs[0])
	}
	if a := v.RTCPAddr(); a.Port != 5011 || a.IP.String() != "192.0.2.51" {
		t.Errorf("RTCP address = %v", a)
	}

	for name, sdp := range map[string]string{
		"refused":   strings.Replace(doorOfferSDP, "m=video 5006", "m=video 0", 1),
		"srtp":      strings.Replace(doorOfferSDP, "RTP/AVPF", "RTP/SAVPF", 1),
		"no H.264":  strings.NewReplacer("H264/90000", "H265/90000").Replace(doorOfferSDP),
		"audio":     doorOfferSDP[:strings.Index(doorOfferSDP, "m=video")],
		"no c line": strings.NewReplacer("c=IN IP4 192.0.2.50\r\n", "", "c=IN IP4 192.0.2.51\r\n", "").Replace(doorOfferSDP),
	} {
		if v, ok := ParseVideoSDP([]byte(sdp)); ok {
			t.Errorf("%s was read as video: %+v", name, v)
		}
	}

	sect := v.Section("203.0.113.9", 30010, "")
	for _, want := range []string{"m=video 30010 RTP/AVPF 96 98\r\n", "c=IN IP4 203.0.113.9\r\n", "a=rtpmap:96 H264/90000\r\n",
		"a=fmtp:98 profile-level-id=64001f\r\n", "a=rtcp-fb:96 nack pli\r\n", "a=rtcp:30011 IN IP4 203.0.113.9\r\n", "a=sendonly\r\n"} {
		if !strings.Contains(sect, want) {
			t.Errorf("section lacks %q:\n%s", want, sect)
		}
	}
	if strings.Contains(sect, "VP8") || strings.Contains(sect, "192.0.2.5") {
		t.Errorf("section keeps the door's own address or a payload type it does not carry:\n%s", sect)
	}
	audio := BuildAudioSDP("203.0.113.9", 30000, 0, 101, 8000)
	back, ok := ParseVideoSDP(append(audio, sect...))
	if !ok || back.Port != 30010 || back.Address != "203.0.113.9" || len(back.Codecs) != 2 {
		t.Errorf("round trip = %+v, %v", back, ok)
	}
	if a, err := ParseAudioSDP(append(audio, sect...)); err != nil || a.Port != 30000 {
		t.Errorf("the audio of an SDP with video = %+v, %v", a, err)
	}
	if got := RejectedVideoSection(v); got != "m=video 0 RTP/AVP 96\r\n" {
		t.Errorf("rejected = %q", got)
	}
}

// TestPlainVideoLegs fails if two plain RTP legs (a phone and a door station)
// do not carry video through the translator: each leg gets a video port pair of
// its own next to its audio port, the SDP a leg writes names that port and the
// negotiated codec, a refused or unanswered section is port 0 in place, packets
// arrive with the payload untouched on a stream of the relay's own (new SSRC, the
// leg's payload type), RTCP is regenerated per leg (a picture loss indication
// from one side reaches the sender as the relay's own), and a source that is not
// the leg's peer is dropped (S-44).
func TestPlainVideoLegs(t *testing.T) {
	r, err := NewRelay(30000, 31000)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.AddLeg("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AddLeg("b"); err != nil {
		t.Fatal(err)
	}
	r.Start()
	r.NotePlainVideoOffer("a", true, "96")
	pa, err := r.BindPlainVideo("a")
	if err != nil {
		t.Fatal(err)
	}
	pb, err := r.BindPlainVideo("b")
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := r.BindPlainVideo("a"); again != pa || pa == pb || pa%2 != 0 || pb%2 != 0 {
		t.Fatalf("ports a=%d again=%d b=%d", pa, again, pb)
	}
	if !r.VideoLegOK("a") || !r.HasPlainVideo("b") || r.IsWebLeg("a") {
		t.Error("the legs do not report their video")
	}
	// Before negotiation the caller's offered section is answered refused.
	if sec := r.PlainVideoSection("a", "192.0.2.1"); !strings.HasPrefix(sec, "m=video 0 RTP/AVP 96") {
		t.Errorf("unanswered section = %q", sec)
	}
	if sec := r.PlainVideoSection("b", "192.0.2.1"); sec != "" {
		t.Errorf("a leg that was never asked says %q", sec)
	}
	codec := SDPCodec{PT: 96, Name: "H264", Rate: 90000, Fmtp: "profile-level-id=42e01f;packetization-mode=1", Feedback: []string{"nack", "nack pli"}}
	r.SetVideoOffer("b", []SDPCodec{codec}, "sendonly", 1500)
	off := r.PlainVideoSection("b", "192.0.2.1")
	for _, want := range []string{"m=video ", " RTP/AVP 96", "c=IN IP4 192.0.2.1", "b=AS:1500", "a=rtpmap:96 H264/90000", "a=fmtp:96 profile-level-id=42e01f;packetization-mode=1",
		"a=rtcp-fb:96 nack pli", "a=sendonly"} {
		if !strings.Contains(off, want) {
			t.Errorf("offer lacks %q:\n%s", want, off)
		}
	}
	if !strings.Contains(off, "a=rtcp:") {
		t.Errorf("offer names no RTCP port:\n%s", off)
	}
	phoneA, phoneB := udpLoopback(t), udpLoopback(t)
	rtcpA, rtcpB := udpLoopback(t), udpLoopback(t)
	r.SetPlainVideoTarget("a", udpAddrOf(phoneA), udpAddrOf(rtcpA))
	r.SetPlainVideoTarget("b", udpAddrOf(phoneB), udpAddrOf(rtcpB))
	r.SetVideoCap(1500)
	codecB := codec
	codecB.PT = 100 // the leg's own numbering
	r.SetVideoSection("a", &codec, "sendrecv", 1500)
	r.SetVideoSection("b", &codecB, "sendrecv", 1500)
	if !r.VideoOn() {
		t.Fatal("video is not on")
	}
	if sec := r.PlainVideoSection("b", "192.0.2.1"); !strings.Contains(sec, "RTP/AVP 100") || strings.Contains(sec, "RTP/AVP 100 96") {
		t.Errorf("negotiated section = %q", sec)
	}
	payload := append([]byte{0x65}, make([]byte, 99)...)
	for i := range 10 {
		_, _ = phoneA.WriteToUDP(BuildRTP(96, uint16(10+i), 5000+uint32(i)*3000, 77, true, payload), loopbackAt(pa))
	}
	got := readRTP(t, phoneB)
	if got.PayloadType != 100 || got.SSRC == 77 || len(got.Payload) != 100 || got.Payload[0] != 0x65 {
		t.Errorf("B got %+v", got)
	}
	// A packet of another payload type, and one from a stranger, go nowhere.
	_, _ = phoneA.WriteToUDP(BuildRTP(111, 99, 1, 77, true, payload), loopbackAt(pa))
	stranger := udpLoopback(t)
	_, _ = stranger.WriteToUDP(BuildRTP(96, 1, 1, 5, true, payload), loopbackAt(pa))
	// B asks for a picture; A's sender hears it from the relay.
	pli := []byte{0x81, 206, 0, 2, 0, 0, 0, 9, 0, 0, 0, 1}
	_, _ = rtcpB.WriteToUDP(pli, loopbackAt(pb+1))
	_, _ = phoneB.WriteToUDP(BuildRTP(100, 1, 1000, 88, true, payload), loopbackAt(pb))
	rtcpRaw := readVideoRaw(t, rtcpA)
	if len(rtcpRaw) < 12 || rtcpRaw[1] != 206 {
		t.Errorf("A got RTCP %x, want a picture loss indication", rtcpRaw)
	}
	if s := r.VideoStats(); s.Packets < 10 {
		t.Errorf("stats = %+v", s)
	}
	// Taking the section off makes both legs refuse again.
	r.SetVideoSection("a", nil, "", 0)
	r.SetVideoSection("b", nil, "", 0)
	if r.VideoOn() {
		t.Error("video still on")
	}
	if sec := r.PlainVideoSection("a", "192.0.2.1"); !strings.HasPrefix(sec, "m=video 0 ") {
		t.Errorf("after the video ended, a says %q", sec)
	}
	// An offer without video clears the answer's section.
	r.NotePlainVideoOffer("a", false, "")
	if sec := r.PlainVideoSection("a", "192.0.2.1"); sec != "" {
		t.Errorf("after an offer without video: %q", sec)
	}
}

// TestPlainVideoClosesWithRelay fails if a plain leg's video sockets outlive the
// relay (the ports must be free for the next call).
func TestPlainVideoClosesWithRelay(t *testing.T) {
	r, err := NewRelay(30000, 31000)
	if err != nil {
		t.Fatal(err)
	}
	r.AddLeg("a")
	r.AddLeg("b")
	r.Start()
	p, err := r.BindPlainVideo("a")
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: p})
	if err != nil {
		t.Fatalf("video port %d still held after Close: %v", p, err)
	}
	_ = c.Close()
	if _, err := r.BindPlainVideo("b"); err == nil {
		t.Error("a closed relay bound a video socket")
	}
}

func udpLoopback(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func udpAddrOf(c *net.UDPConn) *net.UDPAddr { return c.LocalAddr().(*net.UDPAddr) }

func loopbackAt(port int) *net.UDPAddr { return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port} }

func readVideoRaw(t *testing.T, c *net.UDPConn) []byte {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 2048)
	n, _, err := c.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("nothing arrived: %v", err)
	}
	return buf[:n]
}

func readRTP(t *testing.T, c *net.UDPConn) *RTPPacket {
	t.Helper()
	for range 20 {
		raw := readVideoRaw(t, c)
		if len(raw) > 1 && raw[1] >= 192 && raw[1] <= 223 {
			continue
		}
		if p, ok := ParseRTP(raw); ok {
			return p
		}
	}
	t.Fatal("no RTP arrived")
	return nil
}
