package media

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

// tonePCM makes n samples of a sine at freq Hz sampled at rate, amplitude amp.
func sineAt(freq float64, rate, n int, amp float64) []byte {
	out := make([]byte, 2*n)
	for i := 0; i < n; i++ {
		v := int16(math.Round(amp * math.Sin(2*math.Pi*freq*float64(i)/float64(rate))))
		out[2*i] = byte(v)
		out[2*i+1] = byte(uint16(v) >> 8)
	}
	return out
}

// rms of the middle half of the PCM, away from the edge transients.
func rmsMid(pcm []byte) float64 {
	n := len(pcm) / 2
	var sum float64
	var cnt int
	for i := n / 4; i < 3*n/4; i++ {
		v := float64(int16(uint16(pcm[2*i]) | uint16(pcm[2*i+1])<<8))
		sum += v * v
		cnt++
	}
	return math.Sqrt(sum / float64(cnt))
}

// TestResampleLengthAndLevel fails if a 24 kHz or 44.1 kHz tone inside the
// telephone band does not come out at 8 kHz with the right sample count and
// roughly the same level.
func TestResampleLengthAndLevel(t *testing.T) {
	for _, rate := range []int{16000, 22050, 24000, 44100, 48000} {
		in := sineAt(1000, rate, rate, 10000) // one second
		out, err := Resample(in, rate, 8000)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(out) / 2; got != 8000 {
			t.Errorf("%d Hz: %d samples out, want 8000", rate, got)
		}
		if got, want := rmsMid(out), rmsMid(in); math.Abs(got-want)/want > 0.03 {
			t.Errorf("%d Hz: rms %.0f, want about %.0f", rate, got, want)
		}
	}
}

// TestResampleRejectsAlias fails if a tone above the 8 kHz Nyquist (6 kHz
// is fine, 10 kHz is not) leaks into the output instead of being filtered.
func TestResampleRejectsAlias(t *testing.T) {
	in := sineAt(10000, 48000, 48000, 10000)
	out, err := Resample(in, 48000, 8000)
	if err != nil {
		t.Fatal(err)
	}
	if got := rmsMid(out); got > 300 { // below ~-30 dB of the 7071 input rms
		t.Errorf("10 kHz tone leaked at rms %.0f", got)
	}
}

// TestResampleUpsample fails if going up (8 kHz to 16 kHz) breaks the length
// or the level.
func TestResampleUpsample(t *testing.T) {
	in := sineAt(500, 8000, 8000, 8000)
	out, err := Resample(in, 8000, 16000)
	if err != nil {
		t.Fatal(err)
	}
	if len(out)/2 != 16000 {
		t.Fatalf("%d samples", len(out)/2)
	}
	if got, want := rmsMid(out), rmsMid(in); math.Abs(got-want)/want > 0.03 {
		t.Errorf("rms %.0f, want about %.0f", got, want)
	}
}

func TestResampleEdgeCases(t *testing.T) {
	in := sineAt(440, 8000, 100, 1000)
	same, err := Resample(in, 8000, 8000)
	if err != nil || string(same) != string(in) {
		t.Fatalf("same rate: %v", err)
	}
	same[0] ^= 0xff
	if in[0] == same[0] {
		t.Error("same-rate result aliases the input")
	}
	if _, err := Resample([]byte{1, 2, 3}, 24000, 8000); err == nil {
		t.Error("odd byte count accepted")
	}
	if _, err := Resample(in, 0, 8000); err == nil {
		t.Error("zero rate accepted")
	}
	if out, err := Resample(nil, 24000, 8000); err != nil || len(out) != 0 {
		t.Errorf("empty = %v, %v", out, err)
	}
	// Full-scale input must clip, not wrap.
	loud := sineAt(300, 24000, 2400, 32767)
	if _, err := Resample(loud, 24000, 8000); err != nil {
		t.Fatal(err)
	}
}

// dB is the level of got relative to ref in decibels.
func dB(got, ref float64) float64 { return 20 * math.Log10((got+1e-9)/ref) }

// TestResampleDownAntiAlias fails if a tone above the target's Nyquist
// frequency is only weakened instead of removed. A naive decimation or box
// average lets these tones fold back into the band: 5.123 kHz sampled at
// 8 kHz would reappear at 2.877 kHz. Each case is a tone that would alias,
// and it must come out at least 60 dB below where it went in; the speech and
// music band below the cut-off must pass within half a dB.
func TestResampleDownAntiAlias(t *testing.T) {
	cases := []struct {
		from, to int
		freq     float64
	}{
		{48000, 8000, 4321}, {48000, 8000, 5123}, {48000, 8000, 7777}, {48000, 8000, 12345}, {48000, 8000, 23001},
		{48000, 16000, 8765}, {48000, 16000, 11111}, {48000, 16000, 20123},
		{16000, 8000, 4567}, {16000, 8000, 6789},
		{44100, 8000, 5000.5}, {44100, 16000, 9876},
	}
	for _, c := range cases {
		in := sineAt(c.freq, c.from, 2*c.from, 10000)
		out, err := Resample(in, c.from, c.to)
		if err != nil {
			t.Fatal(err)
		}
		if att := dB(rmsMid(out), rmsMid(in)); att > -60 {
			t.Errorf("%d -> %d Hz: %.0f Hz tone only %.1f dB down, want at most -60 dB (aliasing)", c.from, c.to, c.freq, att)
		}
	}
	for _, c := range []struct {
		from, to int
		freq     float64
	}{{48000, 8000, 300}, {48000, 8000, 1234}, {48000, 8000, 3100}, {48000, 16000, 6000}, {16000, 8000, 3000}, {44100, 8000, 2000}} {
		in := sineAt(c.freq, c.from, 2*c.from, 10000)
		out, _ := Resample(in, c.from, c.to)
		if d := dB(rmsMid(out), rmsMid(in)); math.Abs(d) > 0.5 {
			t.Errorf("%d -> %d Hz: %.0f Hz in-band tone changed by %.2f dB", c.from, c.to, c.freq, d)
		}
	}
}

// TestResampleUpPreservesBand fails if going from 8 kHz up to 16 or 48 kHz
// changes the level of what was there or invents energy in the new band
// (spectral images of the 1 kHz tone).
func TestResampleUpPreservesBand(t *testing.T) {
	in := sineAt(1000, 8000, 16000, 10000)
	for _, to := range []int{16000, 48000} {
		out, err := Resample(in, 8000, to)
		if err != nil {
			t.Fatal(err)
		}
		if d := dB(rmsMid(out), rmsMid(in)); math.Abs(d) > 0.3 {
			t.Errorf("8 -> %d: level changed %.2f dB", to, d)
		}
		// The image of 1 kHz around the old sample rate sits at 7 kHz.
		if img := dB(toneAmp(out, 7000, to), 10000); img > -60 {
			t.Errorf("8 -> %d: image at 7 kHz only %.1f dB down", to, img)
		}
	}
}

// TestResampleLongInputMatchesShort fails if the parallel path (long inputs
// are split over several goroutines) produces different samples than a
// single pass, e.g. at the chunk joins.
func TestResampleLongInputMatchesShort(t *testing.T) {
	in := sineAt(777, 48000, 48000*3, 9000)
	long, err := Resample(in, 48000, 16000)
	if err != nil {
		t.Fatal(err)
	}
	rs := &resampler{pcm: in, n: len(in) / 2, l: 3, m: 1, gain: 2 * 0.5 / 3 * resampleDownCutoff}
	rs.halfWidth = resampleZeroCrossings / rs.gain
	rs.table = []phaseKernel{rs.kernel(0)}
	one := make([]byte, len(long))
	rs.out = one
	rs.run(0, len(long)/2)
	if !bytesEqual(long, one) {
		t.Fatal("chunked resample differs from a single pass")
	}
}

func bytesEqual(a, b []byte) bool { return string(a) == string(b) }

// TestResampleLongTrack fails if a long 48 kHz track does not come out at
// exactly the expected length. (Speed is covered by BenchmarkResample48to8: a
// wall-clock bound in a unit test is flaky on loaded CI runners.)
func TestResampleLongTrack(t *testing.T) {
	out, err := Resample(make([]byte, 2*48000*60), 48000, 8000)
	if err != nil || len(out) != 2*8000*60 {
		t.Fatalf("len %d err %v", len(out), err)
	}
}

// BenchmarkResample48to8 times one minute of 48 kHz audio down to 8 kHz.
func BenchmarkResample48to8(b *testing.B) {
	in := make([]byte, 2*48000*60)
	for range b.N {
		if _, err := Resample(in, 48000, 8000); err != nil {
			b.Fatal(err)
		}
	}
}

func TestNormalizeWAV(t *testing.T) {
	// 8 kHz passes through byte for byte.
	w8 := EncodeWAV(sineAt(440, 8000, 800, 5000), 8000)
	got, err := NormalizeWAV(w8)
	if err != nil || string(got) != string(w8) {
		t.Fatalf("8 kHz changed: %v", err)
	}
	// Rates up to 48 kHz are kept, not squashed to 8 kHz.
	for _, rate := range []int{16000, 22050, 44100, 48000} {
		w := EncodeWAV(sineAt(440, rate, rate, 5000), rate)
		got, err := NormalizeWAV(w)
		if err != nil {
			t.Fatal(err)
		}
		pcm, gotRate, err := DecodeWAV(got)
		if err != nil || gotRate != rate || len(pcm)/2 != rate {
			t.Errorf("%d Hz upload stored at %d Hz with %d samples (err %v)", rate, gotRate, len(pcm)/2, err)
		}
	}
	// Above 48 kHz is reduced to 48 kHz.
	w96 := EncodeWAV(sineAt(440, 96000, 96000, 5000), 96000)
	got, err = NormalizeWAV(w96)
	if err != nil {
		t.Fatal(err)
	}
	pcm, rate, err := DecodeWAV(got)
	if err != nil || rate != 48000 || len(pcm)/2 != 48000 {
		t.Fatalf("96 kHz: rate=%d samples=%d err=%v", rate, len(pcm)/2, err)
	}
	// Empty audio and junk are refused.
	if _, err := NormalizeWAV(EncodeWAV(nil, 8000)); err == nil {
		t.Error("empty audio accepted")
	}
	if _, err := NormalizeWAV([]byte("not a wav at all")); err == nil {
		t.Error("junk accepted")
	}
}

// TestNormalizeWAVStereoDownmix fails if a stereo upload is refused, keeps
// only one channel, or does not keep its sample rate: a 48 kHz stereo song
// must come out 48 kHz mono with the channels averaged.
func TestNormalizeWAVStereoDownmix(t *testing.T) {
	const rate, n = 48000, 4800
	left := sineAt(1000, rate, n, 8000)
	right := sineAt(1000, rate, n, 4000)
	inter := make([]byte, 4*n)
	for i := range n {
		copy(inter[4*i:], left[2*i:2*i+2])
		copy(inter[4*i+2:], right[2*i:2*i+2])
	}
	w := rawWAV(1, 2, rate, 16, inter)
	got, err := NormalizeWAV(w)
	if err != nil {
		t.Fatal(err)
	}
	pcm, gotRate, err := DecodeWAV(got)
	if err != nil || gotRate != rate || len(pcm)/2 != n {
		t.Fatalf("rate=%d samples=%d err=%v", gotRate, len(pcm)/2, err)
	}
	if want, have := toneAmp(sineAt(1000, rate, n, 6000), 1000, rate), toneAmp(pcm, 1000, rate); math.Abs(have-want) > 20 {
		t.Errorf("down-mix amplitude %.0f, want %.0f (mean of 8000 and 4000)", have, want)
	}
	// Opposite phase cancels: the mean of +x and -x is silence, which proves
	// the channels are averaged rather than one taken.
	neg := make([]byte, 4*n)
	for i := range n {
		copy(neg[4*i:], left[2*i:2*i+2])
		v := -int16(binary.LittleEndian.Uint16(left[2*i:])) //nolint:gosec // test sample
		binary.LittleEndian.PutUint16(neg[4*i+2:], uint16(v))
	}
	got, err = NormalizeWAV(rawWAV(1, 2, rate, 16, neg))
	if err != nil {
		t.Fatal(err)
	}
	pcm, _, _ = DecodeWAV(got)
	if r := rmsMid(pcm); r > 1 {
		t.Errorf("out-of-phase stereo down-mixed to rms %.1f, want silence", r)
	}
}

// TestDecodeWAVSampleFormats fails if 8, 24 and 32-bit integer or float WAVs
// (also in the WAVE_FORMAT_EXTENSIBLE wrapper) do not decode to the same
// 16-bit level, or if a compressed codec is not refused with its name.
func TestDecodeWAVSampleFormats(t *testing.T) {
	// A half-scale ramp: 0, +16384, 0, -16384 in 16-bit terms.
	want := []int16{0, 16384, 0, -16384}
	build := func(tag uint16, bits int, enc func(v int16) []byte) []byte {
		var data []byte
		for _, v := range want {
			data = append(data, enc(v)...)
		}
		return rawWAV(tag, 1, 16000, bits, data)
	}
	le := binary.LittleEndian
	cases := map[string][]byte{
		"8-bit": build(1, 8, func(v int16) []byte { return []byte{byte(int(v)>>8 + 128)} }),
		"24-bit": build(1, 24, func(v int16) []byte {
			x := int32(v) << 8
			return []byte{byte(x), byte(x >> 8), byte(x >> 16)}
		}),
		"32-bit": build(1, 32, func(v int16) []byte { return le.AppendUint32(nil, uint32(int32(v)<<16)) }),
		"float32": build(3, 32, func(v int16) []byte {
			return le.AppendUint32(nil, math.Float32bits(float32(v)/32768))
		}),
		"float64": build(3, 64, func(v int16) []byte {
			return le.AppendUint64(nil, math.Float64bits(float64(v)/32768))
		}),
	}
	for name, w := range cases {
		pcm, rate, err := DecodeWAV(w)
		if err != nil || rate != 16000 || len(pcm) != 8 {
			t.Errorf("%s: rate=%d len=%d err=%v", name, rate, len(pcm), err)
			continue
		}
		for i, v := range want {
			if got := int16(le.Uint16(pcm[2*i:])); got < v-1 || got > v+1 { //nolint:gosec // test sample
				t.Errorf("%s: sample %d = %d, want %d", name, i, got, v)
			}
		}
	}
	// WAVE_FORMAT_EXTENSIBLE carrying 24-bit PCM.
	ext := rawWAV(0xFFFE, 1, 16000, 24, []byte{0, 0, 0, 0, 0, 0x40, 0, 0, 0, 0, 0, 0xC0})
	pcm, _, err := DecodeWAV(ext)
	if err != nil || len(pcm) != 8 || int16(le.Uint16(pcm[2:])) != 16384 { //nolint:gosec // test sample
		t.Errorf("extensible 24-bit: %v len=%d", err, len(pcm))
	}
	// Float beyond full scale clips instead of wrapping; NaN is silence.
	loud := rawWAV(3, 1, 8000, 32, le.AppendUint32(le.AppendUint32(nil, math.Float32bits(4)), math.Float32bits(float32(math.NaN()))))
	pcm, _, err = DecodeWAV(loud)
	if err != nil || int16(le.Uint16(pcm)) != math.MaxInt16 || le.Uint16(pcm[2:]) != 0 { //nolint:gosec // test sample
		t.Errorf("clip/NaN: %v %v", err, pcm)
	}
	// Compressed codecs are refused, naming the problem.
	for tag, name := range map[uint16]string{0x0002: "ADPCM", 0x0006: "A-law", 0x0007: "mu-law", 0x0055: "MP3"} {
		_, _, err := DecodeWAV(rawWAV(tag, 1, 8000, 8, []byte{1, 2, 3, 4}))
		if err == nil || !strings.Contains(err.Error(), "unsupported WAV codec") {
			t.Errorf("%s accepted or unclear: %v", name, err)
		}
	}
	if _, _, err := DecodeWAV(rawWAV(1, 1, 8000, 12, []byte{1, 2, 3, 4})); err == nil || !strings.Contains(err.Error(), "12-bit") {
		t.Errorf("12-bit: %v", err)
	}
}

// rawWAV builds a WAV with the given format tag, channel count, rate and
// sample width around data.
func rawWAV(tag uint16, channels, rate, bits int, data []byte) []byte {
	le := binary.LittleEndian
	f := make([]byte, 16)
	le.PutUint16(f, tag)
	le.PutUint16(f[2:], uint16(channels))
	le.PutUint32(f[4:], uint32(rate))
	le.PutUint32(f[8:], uint32(rate*channels*bits/8))
	le.PutUint16(f[12:], uint16(channels*bits/8))
	le.PutUint16(f[14:], uint16(bits))
	if tag == 0xFFFE { // extensible: cbSize, valid bits, channel mask, sub-format GUID
		f = append(f, 22, 0)
		f = le.AppendUint16(f, uint16(bits))
		f = le.AppendUint32(f, 0)
		f = le.AppendUint16(f, 1) // PCM sub-format
		f = append(f, 0, 0, 0, 0, 0x10, 0, 0x80, 0, 0, 0xAA, 0, 0x38, 0x9B, 0x71)
	}
	body := append([]byte("WAVE"), wavChunk("fmt ", f)...)
	body = append(body, wavChunk("data", data)...)
	out := le.AppendUint32([]byte("RIFF"), uint32(len(body)))
	return append(out, body...)
}
