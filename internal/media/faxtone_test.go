package media

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

// tonePCM is n samples of a sine at freq, amplitude amp, as 16-bit LE PCM.
func sinePCM(freq, amp float64, n int) []byte {
	out := make([]byte, 2*n)
	for i := 0; i < n; i++ {
		v := int16(amp * math.Sin(2*math.Pi*freq*float64(i)/8000))
		out[2*i], out[2*i+1] = byte(v), byte(uint16(v)>>8)
	}
	return out
}

// feedTone pushes pcm through the detector in 20 ms packets and returns every
// tone it reported.
func feedTone(d *FaxToneDetector, pt uint8, pcm []byte) []FaxTone {
	var got []FaxTone
	for off := 0; off+320 <= len(pcm); off += 320 {
		var payload []byte
		if pt == 0 {
			payload = EncodePCMU(pcm[off : off+320])
		} else {
			payload = EncodePCMA(pcm[off : off+320])
		}
		if t := d.Feed(pt, payload); t != ToneNone {
			got = append(got, t)
		}
	}
	return got
}

func TestFaxToneDetection(t *testing.T) {
	for _, pt := range []uint8{0, 8} {
		for _, c := range []struct {
			name string
			freq float64
			want FaxTone
		}{{"CNG", 1100, ToneCNG}, {"CED", 2100, ToneCED}} {
			got := feedTone(NewFaxToneDetector(), pt, sinePCM(c.freq, 8000, 8000))
			if len(got) != 1 || got[0] != c.want {
				t.Errorf("pt %d %s: reported %v, want one %v", pt, c.name, got, c.want)
			}
		}
	}
}

// A CNG burst is 0.5 s; the detector reports it once, then again for the next
// burst, so a caller that hung around is not missed.
func TestFaxToneBursts(t *testing.T) {
	d := NewFaxToneDetector()
	var pcm []byte
	for i := 0; i < 3; i++ {
		pcm = append(pcm, sinePCM(1100, 8000, 4000)...) // 0.5 s on
		pcm = append(pcm, make([]byte, 2*16000)...)     // 2 s of silence
	}
	if got := feedTone(d, 0, pcm); len(got) != 3 {
		t.Fatalf("three bursts reported as %v", got)
	}
}

func TestFaxToneIgnoresSpeechLikeAudio(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	noise := make([]byte, 2*8000*5)
	for i := 0; i < len(noise); i += 2 {
		v := int16(rng.Intn(12000) - 6000)
		noise[i], noise[i+1] = byte(v), byte(uint16(v)>>8)
	}
	if got := feedTone(NewFaxToneDetector(), 0, noise); len(got) != 0 {
		t.Errorf("noise reported as %v", got)
	}
	// Two tones near, but not at, the fax tones.
	a, b := sinePCM(900, 6000, 16000), sinePCM(1300, 6000, 16000)
	mix := make([]byte, len(a))
	for i := 0; i < len(a); i += 2 {
		v := int16(uint16(a[i])|uint16(a[i+1])<<8) + int16(uint16(b[i])|uint16(b[i+1])<<8)
		mix[i], mix[i+1] = byte(v), byte(uint16(v)>>8)
	}
	if got := feedTone(NewFaxToneDetector(), 0, mix); len(got) != 0 {
		t.Errorf("neighbouring tones reported as %v", got)
	}
	// A quiet 1100 Hz hum is below the level floor.
	if got := feedTone(NewFaxToneDetector(), 0, sinePCM(1100, 60, 16000)); len(got) != 0 {
		t.Errorf("a faint tone reported as %v", got)
	}
	// Other payload types are not G.711 and are not examined.
	if NewFaxToneDetector().Feed(9, make([]byte, 160)) != ToneNone {
		t.Error("a G.722 payload reported a tone")
	}
}

func TestRelayUDPTLUnmodified(t *testing.T) {
	r, pa, pb := testRelay(t)
	r.SetUDPTL(true)
	if !r.UDPTL() {
		t.Fatal("UDPTL() is false after SetUDPTL(true)")
	}
	// Not RTP (version bits 00), so the RTP path would drop it.
	udptl := []byte{0x00, 0x07, 0x03, 0xAA, 0xBB, 0xCC, 0x00, 0x00}
	_, _ = pa.conn.WriteToUDP(udptl, pa.to)
	buf := make([]byte, 2048)
	_ = pb.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err := pb.conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("UDPTL did not cross the relay: %v", err)
	}
	if string(buf[:n]) != string(udptl) {
		t.Fatalf("UDPTL changed on the way: % x", buf[:n])
	}
	_, _ = pb.conn.WriteToUDP([]byte{0x00, 0x01, 0x01, 0x42, 0x00, 0x00}, pb.to)
	_ = pa.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if n, _, err = pa.conn.ReadFromUDP(buf); err != nil || n != 6 || buf[3] != 0x42 {
		t.Fatalf("reverse direction: %d %v", n, err)
	}
	// Back to RTP: the rewrite is on again.
	r.SetUDPTL(false)
	pa.send(0, make([]byte, 160), false)
	if got := pb.recv(t); got.SSRC == pa.ssrc {
		t.Fatal("RTP was not rewritten after leaving UDPTL mode")
	}
}

func TestRelayDropsNonRTPByDefault(t *testing.T) {
	_, pa, pb := testRelay(t)
	_, _ = pa.conn.WriteToUDP([]byte{0x00, 0x07, 0x03, 0xAA}, pa.to)
	buf := make([]byte, 2048)
	_ = pb.conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := pb.conn.ReadFromUDP(buf); err == nil {
		t.Fatal("a non-RTP datagram crossed an RTP relay")
	}
}
