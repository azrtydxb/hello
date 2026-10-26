package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"net"
	"testing"
	"time"
)

// tone16 renders n 16 kHz samples of the sum of the given frequencies.
func tone16(n int, amp float64, freqs ...float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		var v float64
		for _, f := range freqs {
			v += math.Sin(2 * math.Pi * f * float64(i) / 16000)
		}
		out[i] = int16(amp * v / float64(len(freqs)))
	}
	return out
}

func wideOffer(pt uint8) []byte {
	return BuildAudioSDP("127.0.0.1", 9, pt, 101, 8000)
}

func wideAnchors(t *testing.T, offer []byte) (a, b Session) {
	t.Helper()
	anchorA, anchorB := NewAnchor("127.0.0.1", testLogger(t)), NewAnchor("127.0.0.1", testLogger(t))
	answerB, sB, err := anchorB.Answer(offer)
	if err != nil {
		t.Fatalf("B answer: %v", err)
	}
	t.Cleanup(func() { _ = sB.Close() })
	_, sA, err := anchorA.Answer(answerB)
	if err != nil {
		t.Fatalf("A answer: %v", err)
	}
	t.Cleanup(func() { _ = sA.Close() })
	return sA, sB
}

func TestSessionSampleRatePerCodec(t *testing.T) {
	for _, c := range []struct {
		name  string
		offer []byte
		rate  int
		pt    uint8
	}{
		{"pcmu", BuildAudioSDP("127.0.0.1", 9, 0, 101, 8000), 8000, 0},
		{"pcma", BuildAudioSDP("127.0.0.1", 9, 8, 101, 8000), 8000, 8},
		{"g722", wideOffer(9), 16000, 9},
		{"g722 preferred over g711", []byte("v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 9 RTP/AVP 0 8 9\r\n"), 16000, 9},
		{"opus preferred over g722 and g711", []byte("v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 9 RTP/AVP 9 96 0\r\na=rtpmap:96 opus/48000/2\r\n"), 16000, 96},
		{"opus only", []byte("v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 9 RTP/AVP 96\r\na=rtpmap:96 opus/48000/2\r\n"), 16000, 96},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.pt == 96 && !OpusAvailable {
				t.Skip("no Opus codec in this build")
			}
			ans, s, err := NewAnchor("127.0.0.1", testLogger(t)).Answer(c.offer)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			if got := s.SampleRate(); got != c.rate {
				t.Errorf("sample rate %d, want %d", got, c.rate)
			}
			parsed, err := ParseAudioSDP(ans)
			if err != nil || parsed.PayloadType != c.pt || len(parsed.Codecs) != 1 {
				t.Errorf("answer codec %d (%v), want only %d:\n%s", parsed.PayloadType, err, c.pt, ans)
			}
		})
	}
}

// A G.722 session plays and records 16 kHz audio, 5 kHz content included,
// which a G.711 session could not carry.
func TestG722SessionLoopback(t *testing.T) {
	a, b := wideAnchors(t, wideOffer(9))
	if a.SampleRate() != 16000 || b.SampleRate() != 16000 {
		t.Fatalf("rates %d %d", a.SampleRate(), b.SampleRate())
	}
	pcm := Int16ToPCM(tone16(20*320, 12000, 440, 5000))
	done := make(chan []byte, 1)
	go func() {
		data, _ := b.Record(context.Background(), 600*time.Millisecond, nil)
		done <- data
	}()
	if err := a.Play(context.Background(), pcm); err != nil {
		t.Fatalf("play: %v", err)
	}
	var rec []byte
	select {
	case rec = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("record did not return")
	}
	frame := 640 // 20 ms of 16 kHz 16-bit audio
	if want := 30 * frame; len(rec) < want-frame || len(rec) > want+frame {
		t.Errorf("recording is %d bytes, want about %d", len(rec), want)
	}
	samples := PCMToInt16(rec)
	if e := goertzel(samples, 5000, 16000); e < 1000 {
		t.Errorf("5 kHz energy %.0f: the wideband content did not arrive", e)
	}
	if e := goertzel(samples, 440, 16000); e < 1000 {
		t.Errorf("440 Hz energy %.0f", e)
	}
}

func TestOpusSessionLoopback(t *testing.T) {
	if !OpusAvailable {
		t.Skip("no Opus codec in this build")
	}
	offer := []byte("v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 9 RTP/AVP 96\r\na=rtpmap:96 opus/48000/2\r\n")
	a, b := wideAnchors(t, offer)
	pcm := Int16ToPCM(tone16(40*320, 12000, 440))
	done := make(chan []byte, 1)
	go func() {
		data, _ := b.Record(context.Background(), 1200*time.Millisecond, nil)
		done <- data
	}()
	if err := a.Play(context.Background(), pcm); err != nil {
		t.Fatalf("play: %v", err)
	}
	var rec []byte
	select {
	case rec = <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("record did not return")
	}
	if e := goertzel(PCMToInt16(rec), 440, 16000); e < 1000 {
		t.Errorf("440 Hz energy %.0f after an Opus round trip", e)
	}
}

func TestOpusCodecRoundTrip(t *testing.T) {
	if !OpusAvailable {
		t.Skip("no Opus codec in this build")
	}
	c := Codec{PT: 111, Name: "opus", ClockRate: 48000, Channels: 2}
	enc, err := NewEncoder(c)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	dec, err := NewDecoder(c)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	pcm := tone16(50*320, 10000, 1000)
	var out []int16
	for off := 0; off+320 <= len(pcm); off += 320 {
		pkt := enc.Encode(Int16ToPCM(pcm[off : off+320]))
		if len(pkt) == 0 || len(pkt) > 400 {
			t.Fatalf("opus packet of %d bytes", len(pkt))
		}
		out = append(out, PCMToInt16(dec.Decode(pkt))...)
	}
	if len(out) != len(pcm) {
		t.Fatalf("decoded %d samples, want %d", len(out), len(pcm))
	}
	if e := goertzel(out[len(out)/2:], 1000, 16000); e < 4000 {
		t.Errorf("1 kHz energy %.0f after the round trip", e)
	}
	// Garbage in an RTP payload costs a frame, never a panic.
	if got := dec.Decode([]byte{0xff, 0x00, 0x13}); len(got) > 2*5760 {
		t.Errorf("garbage decoded to %d bytes", len(got))
	}
	_ = dec.Decode(nil)
}

func TestEncoderDecoderG722FrameSizes(t *testing.T) {
	c, _ := StaticCodec(9)
	enc, _ := NewEncoder(c)
	defer enc.Close()
	frame := Int16ToPCM(tone16(c.FrameSamples(), 8000, 300))
	if got := len(enc.Encode(frame)); got != 160 {
		t.Errorf("a 20 ms G.722 frame is %d bytes, want 160", got)
	}
	dec, _ := NewDecoder(c)
	defer dec.Close()
	if got := len(dec.Decode(make([]byte, 160))); got != 640 {
		t.Errorf("a 160 byte G.722 payload decodes to %d bytes of PCM, want 640", got)
	}
	if _, err := NewEncoder(Codec{PT: 18, Name: "G729", ClockRate: 8000}); err == nil {
		t.Error("G.729 has no encoder")
	}
}

func TestHalfbandResamplers(t *testing.T) {
	// A 1 kHz tone survives 16 kHz to 8 kHz and back at about its level.
	in := tone16(16000, 10000, 1000)
	down := NewDown2().Process(in)
	if n := len(down); n < 7900 || n > 8000 {
		t.Fatalf("down gave %d samples for 16000", n)
	}
	if e := goertzel(down[200:], 1000, 8000); math.Abs(e-10000) > 800 {
		t.Errorf("1 kHz level after down %.0f, want about 10000", e)
	}
	up := NewUp2().Process(down)
	if e := goertzel(up[400:], 1000, 16000); math.Abs(e-10000) > 800 {
		t.Errorf("1 kHz level after up %.0f, want about 10000", e)
	}
	// A 6 kHz tone is above the 8 kHz Nyquist and must be removed, not
	// aliased into the band.
	high := NewDown2().Process(tone16(16000, 10000, 6000))
	var peak float64
	for f := 100.0; f < 4000; f += 100 {
		peak = math.Max(peak, goertzel(high[200:], f, 8000))
	}
	if peak > 300 {
		t.Errorf("a 6 kHz tone left %.0f of alias energy below 4 kHz", peak)
	}
	// Chunking must not matter: frame-by-frame equals one go.
	whole := NewDown2().Process(in)
	d := NewDown2()
	var chunked []int16
	for off := 0; off < len(in); off += 320 {
		chunked = append(chunked, d.Process(in[off:off+320])...)
	}
	if len(whole) != len(chunked) {
		t.Fatalf("whole %d chunked %d samples", len(whole), len(chunked))
	}
	for i := range whole {
		if whole[i] != chunked[i] {
			t.Fatalf("sample %d differs between chunked and whole conversion", i)
		}
	}
}

func TestWAVToRate(t *testing.T) {
	wav8 := EncodeWAV(Int16ToPCM(tone16(8000, 8000, 440)[:8000]), 8000)
	same, err := WAVToRate(wav8, 8000)
	if err != nil || len(same) != 16000 {
		t.Fatalf("8k to 8k: %d bytes, %v", len(same), err)
	}
	up, err := WAVToRate(wav8, 16000)
	if err != nil || len(up) != 32000 {
		t.Fatalf("8k to 16k: %d bytes, %v", len(up), err)
	}
	wav16 := EncodeWAV(up, 16000)
	down, err := WAVToRate(wav16, 8000)
	if err != nil || len(down) != 16000 {
		t.Fatalf("16k to 8k: %d bytes, %v", len(down), err)
	}
	if _, err := WAVToRate([]byte("nope"), 8000); err == nil {
		t.Error("garbage is not a WAV")
	}
	if len(BeepPCMRate(16000)) != 2*len(BeepPCM()) || len(PageTonePCMRate(16000)) != 2*len(PageTonePCM()) {
		t.Error("the tones at 16 kHz are twice as long in bytes")
	}
}

// The recorder decodes G.722 and Opus and mixes at the highest rate.
func TestRecorderWideband(t *testing.T) {
	g722, _ := StaticCodec(9)
	enc, _ := NewEncoder(g722)
	defer enc.Close()
	rec := NewRecorder()
	pcm := tone16(25*320, 8000, 700)
	var gotLen int
	for i := 0; i < 25; i++ {
		rec.Write("a", 9, enc.Encode(Int16ToPCM(pcm[i*320:(i+1)*320])))
		rec.Write("b", 0, EncodePCMU(make([]byte, 320))) // 20 ms of G.711 silence
		gotLen += 640
	}
	wav, codec, dur := rec.Result()
	if codec != "WAV" {
		t.Fatalf("codec %q", codec)
	}
	data, rate, err := DecodeWAV(wav)
	if err != nil || rate != 16000 {
		t.Fatalf("wav rate %d err %v, want a 16 kHz mix", rate, err)
	}
	if len(data) != gotLen {
		t.Errorf("mix is %d bytes, want %d (the G.711 side was resampled up to the longer one)", len(data), gotLen)
	}
	if dur < 480 || dur > 520 {
		t.Errorf("duration %d ms, want about 500", dur)
	}
	if e := goertzel(PCMToInt16(data), 700, 16000); e < 1000 {
		t.Errorf("700 Hz energy %.0f in the recording", e)
	}

	// Opus on a dynamic payload type needs SetCodec.
	if OpusAvailable {
		op := Codec{PT: 111, Name: "opus", ClockRate: 48000, Channels: 2}
		oe, _ := NewEncoder(op)
		defer oe.Close()
		rec = NewRecorder()
		rec.SetCodec(op)
		for i := 0; i < 25; i++ {
			rec.Write("a", 111, oe.Encode(Int16ToPCM(pcm[i*320:(i+1)*320])))
		}
		wav, codec, _ = rec.Result()
		if codec != "WAV" {
			t.Fatalf("opus recording codec %q", codec)
		}
		data, rate, _ = DecodeWAV(wav)
		if rate != 16000 || goertzel(PCMToInt16(data), 700, 16000) < 1000 {
			t.Errorf("opus recording: rate %d", rate)
		}
	}

	// An unknown dynamic type is still stored as it came.
	rec = NewRecorder()
	rec.Write("a", 97, []byte{1, 2, 3})
	if audio, codec, _ := rec.Result(); codec != "pt/97" || !bytes.Equal(audio, []byte{1, 2, 3}) {
		t.Errorf("unknown payload: %q %v", codec, audio)
	}
}

// The relay passes a negotiated dynamic payload type through unchanged and
// counts it as audio; announcements into a G.722 leg are G.722.
func TestRelayDynamicPayloadAndG722Announcement(t *testing.T) {
	r, pa, pb := testRelay(t)
	opus := Codec{PT: 111, Name: "opus", ClockRate: 48000, Channels: 2}
	r.SetCodecs("a", []Codec{opus}, 126)
	r.SetCodecs("b", []Codec{opus}, 126)
	payload := []byte{0xaa, 0xbb, 0xcc, 0xdd}
	pa.send(111, payload, false)
	got := pb.recv(t)
	if got.PayloadType != 111 || !bytes.Equal(got.Payload, payload) {
		t.Fatalf("relayed pt %d payload %v, want the Opus packet untouched", got.PayloadType, got.Payload)
	}
	pb.send(111, payload, false)
	if back := pa.recv(t); back.PayloadType != 111 {
		t.Fatalf("return pt %d", back.PayloadType)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.Metrics().Directions["a>b"].Packets > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := r.Metrics().Directions["a>b"].Packets; got != 1 {
		t.Errorf("the dynamic payload type was counted as %d audio packets, want 1", got)
	}
	if got := r.Codecs("a"); len(got) != 1 || got[0].PT != 111 {
		t.Errorf("leg codecs %v", got)
	}

	g722, _ := StaticCodec(9)
	r.SetCodecs("b", []Codec{g722}, 101)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// 100 ms of 8 kHz announcement audio.
	go func() { _ = r.PlayTo(ctx, "b", Int16ToPCM(tone16(800, 8000, 440)[:800])) }()
	pkt := pb.recv(t)
	if pkt.PayloadType != 9 || len(pkt.Payload) != 160 {
		t.Fatalf("announcement pt %d with %d payload bytes, want G.722 frames of 160", pkt.PayloadType, len(pkt.Payload))
	}
	next := pb.recv(t)
	if next.Timestamp-pkt.Timestamp != 160 {
		t.Errorf("timestamp step %d, want 160 (G.722's clock is 8000)", next.Timestamp-pkt.Timestamp)
	}
}

// A G.722 page reaches a G.722 member untouched and a PCMU member as G.711
// rendered from it.
func TestFanoutG722Fallback(t *testing.T) {
	g722, _ := StaticCodec(9)
	f, err := NewFanoutCodec(31200, 31300, g722)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	caller := startPeer(t, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: f.CallerPort()})
	wide := startPeer(t, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: f.MemberPort()})
	narrow := startPeer(t, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: f.MemberPort()})
	f.AddTarget("wide", wide.addr())
	f.AddFallbackTarget("narrow", narrow.addr())
	if f.Targets() != 2 {
		t.Fatalf("targets %d", f.Targets())
	}
	enc := NewG722Encoder()
	pcm := tone16(30*320, 10000, 800)
	for i := 0; i < 30; i++ {
		caller.ts = uint32(i * 160)
		caller.send(9, enc.Encode(pcm[i*320:(i+1)*320]), false)
	}
	var first *RTPPacket
	for i := 0; i < 30; i++ {
		p := wide.recv(t)
		if p.PayloadType != 9 || len(p.Payload) != 160 {
			t.Fatalf("wide member got pt %d len %d", p.PayloadType, len(p.Payload))
		}
		if first == nil {
			first = p
		}
	}
	var g711 []byte
	for i := 0; i < 30; i++ {
		p := narrow.recv(t)
		if p.PayloadType != 0 {
			t.Fatalf("narrow member got pt %d, want PCMU", p.PayloadType)
		}
		g711 = append(g711, p.Payload...)
	}
	if e := goertzel(PCMToInt16(DecodePCMU(g711))[400:], 800, 8000); e < 2000 {
		t.Errorf("800 Hz energy %.0f in the G.711 rendition", e)
	}
	if _, err := NewFanoutCodec(31200, 31300, Codec{PT: 111, Name: "opus", ClockRate: 48000}); err == nil {
		t.Error("a page cannot be Opus")
	}
	_ = binary.BigEndian
}
