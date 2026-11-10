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

// TestVideoRelay fails if RTP does not cross a video relay byte for byte in
// payload (marker and payload type kept), or RTCP does not cross in both
// directions unchanged on the ports next to the RTP ports.
func TestVideoRelay(t *testing.T) {
	v, err := NewVideoRelay(testRelayMin+500, testRelayMax)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	pa, pb := v.LegPort("a"), v.LegPort("b")
	if pa%2 != 0 || pb%2 != 0 || v.RTCPPort("a") != pa+1 || v.RTCPPort("b") != pb+1 {
		t.Fatalf("ports a %d/%d b %d/%d, want even RTP ports with RTCP above", pa, v.RTCPPort("a"), pb, v.RTCPPort("b"))
	}
	listen := func() *net.UDPConn {
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		return c
	}
	doorRTP, doorRTCP, phoneRTP, phoneRTCP := listen(), listen(), listen(), listen()
	addr := func(c *net.UDPConn) *net.UDPAddr { return c.LocalAddr().(*net.UDPAddr) }
	v.SetTarget("a", addr(doorRTP))
	v.SetTarget("b", addr(phoneRTP))
	v.SetRTCPTarget("a", addr(doorRTCP))
	v.SetRTCPTarget("b", addr(phoneRTCP))
	v.Start()
	dst := func(port int) *net.UDPAddr { return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port} }

	// RTP from the door reaches the phone with its payload type, marker and
	// payload intact.
	payload := []byte{0x65, 1, 2, 3, 4}
	pkt := BuildRTP(96, 7, 9000, 55, true, payload)
	if _, err := doorRTP.WriteToUDP(pkt, dst(pa)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1500)
	n, _, err := phoneRTP.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("no RTP at the phone: %v", err)
	}
	got, ok := ParseRTP(buf[:n])
	if !ok || got.PayloadType != 96 || !got.Marker || string(got.Payload) != string(payload) {
		t.Errorf("relayed RTP = %+v, %v", got, ok)
	}

	// RTCP: a picture loss indication from the phone reaches the door, a
	// sender report from the door reaches the phone.
	pli := []byte{0x81, 206, 0, 2, 0, 0, 0, 1, 0, 0, 0, 2}
	if _, err := phoneRTCP.WriteToUDP(pli, dst(v.RTCPPort("b"))); err != nil {
		t.Fatal(err)
	}
	n, _, err = doorRTCP.ReadFromUDP(buf)
	if err != nil || string(buf[:n]) != string(pli) {
		t.Errorf("RTCP phone to door: %v %x", err, buf[:n])
	}
	sr := []byte{0x80, 200, 0, 6, 0, 0, 0, 5}
	if _, err := doorRTCP.WriteToUDP(sr, dst(v.RTCPPort("a"))); err != nil {
		t.Fatal(err)
	}
	n, _, err = phoneRTCP.ReadFromUDP(buf)
	if err != nil || string(buf[:n]) != string(sr) {
		t.Errorf("RTCP door to phone: %v %x", err, buf[:n])
	}
}

// TestVideoRelayBindsIPv4 fails if a video leg binds dual-stack. Every other RTP socket (anchor, relay legs, fan-out, conference
// parties) is udp4; on BSD kernels a dual-stack bind succeeds on a port an
// IPv4 socket already holds and IPv4 datagrams then go to the IPv4 socket, so
// a video leg could share a port with an audio leg of another call and its
// packets would arrive there. With udp4 the bind is refused on every kernel.
func TestVideoRelayBindsIPv4(t *testing.T) {
	hold := func() (*net.UDPConn, int) {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c, c.LocalAddr().(*net.UDPAddr).Port
	}
	_, port := hold()
	r, err := NewRelay(30000, 31000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	if _, err := r.addLegAt("a", port); err == nil {
		t.Errorf("a video leg bound port %d that an IPv4 socket already holds", port)
	}
}
