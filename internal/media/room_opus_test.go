package media

import (
	"math"
	"net"
	"strconv"
	"testing"
	"time"
)

// TestPartyPrefersOpus fails if a conference participant that offers G.722
// and Opus is not answered with Opus and its 48000 telephone-event, if the
// Opus audio does not cross the mixer both ways, if the party's RTP
// timestamp does not advance by 960 per 20 ms frame, or if a digit sent as a
// telephone-event is lost.
func TestPartyPrefersOpus(t *testing.T) {
	if !OpusAvailable {
		t.Skip("no Opus codec in this build")
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	port := conn.LocalAddr().(*net.UDPAddr).Port
	offer := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio " + strconv.Itoa(port) +
		" RTP/AVP 9 111 0 126 127\r\na=rtpmap:111 opus/48000/2\r\na=rtpmap:126 telephone-event/8000\r\na=rtpmap:127 telephone-event/48000\r\n"
	ans, party, err := NewParty("127.0.0.1", 0, 0, []byte(offer))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(party.Close)
	sdp, err := ParseAudioSDP(ans)
	if err != nil || sdp.PayloadType != 111 || len(sdp.Codecs) != 1 || sdp.DTMFPayloadType != 127 || sdp.DTMFRate != 48000 {
		t.Fatalf("answer = %q (%v), want Opus 111 with telephone-event 127/48000", ans, err)
	}
	dst := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: sdp.Port}

	// Digit as a telephone-event on the 48000 clock.
	_, _ = conn.WriteToUDP(BuildRTP(127, 1, 1000, 77, true, []byte{5, 10, 0x00, 0x00}), dst)
	_, _ = conn.WriteToUDP(BuildRTP(127, 2, 1000, 77, false, []byte{5, 0x80 | 10, 0x07, 0x80}), dst)
	select {
	case got := <-party.DTMF():
		if got != '5' {
			t.Fatalf("digit = %q, want '5'", got)
		}
	case <-time.After(time.Second):
		t.Fatal("the telephone-event/48000 digit never arrived")
	}

	r := NewRoom()
	defer r.Close()
	narrow := newConfPhone(t)
	r.Add("n", narrow.party, false, false)
	r.Add("o", party, false, false)

	// The Opus participant talks; the G.711 one hears it.
	enc, err := NewEncoder(sdp.Codec)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	go func() {
		var seq uint16 = 10
		var ts uint32
		for i := 0; i < 40; i++ {
			wide := make([]int16, 320)
			for j := range wide {
				wide[j] = int16(8000 * math.Sin(2*math.Pi*500*float64(i*320+j)/16000))
			}
			_, _ = conn.WriteToUDP(BuildRTP(111, seq, ts, 99, i == 0, enc.Encode(SamplesToPCM(wide))), dst)
			seq++
			ts += 960
			time.Sleep(frameDur)
		}
	}()
	time.Sleep(300 * time.Millisecond)
	narrow.drain()
	if got := narrow.heard(400 * time.Millisecond); got < 3000 {
		t.Fatalf("the G.711 participant heard peak %d from the Opus one", got)
	}

	// The Opus participant hears the G.711 one, 960 timestamp ticks apart.
	go narrow.talk(500, 8000, 800*time.Millisecond)
	var stamps []uint32
	var peakSeen int
	dec, err := NewDecoder(sdp.Codec)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	buf := make([]byte, 2048)
	deadline := time.Now().Add(1200 * time.Millisecond)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		pkt, ok := ParseRTP(buf[:n])
		if !ok || pkt.PayloadType != 111 {
			continue
		}
		stamps = append(stamps, pkt.Timestamp)
		peakSeen = max(peakSeen, peak(PCMToInt16(dec.Decode(pkt.Payload))))
	}
	if peakSeen < 3000 {
		t.Fatalf("the Opus participant heard peak %d from the G.711 one", peakSeen)
	}
	if len(stamps) < 3 {
		t.Fatalf("only %d Opus packets", len(stamps))
	}
	for i := 1; i < len(stamps); i++ {
		if stamps[i]-stamps[i-1] != 960 {
			t.Fatalf("timestamp step %d, want 960", stamps[i]-stamps[i-1])
		}
	}
}
