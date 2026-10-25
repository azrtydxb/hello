package media

import (
	"math"
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

func TestNormalizeWAV(t *testing.T) {
	// 8 kHz passes through byte for byte.
	w8 := EncodeWAV(sineAt(440, 8000, 800, 5000), 8000)
	got, err := NormalizeWAV(w8)
	if err != nil || string(got) != string(w8) {
		t.Fatalf("8 kHz changed: %v", err)
	}
	// 44.1 kHz is converted.
	w44 := EncodeWAV(sineAt(440, 44100, 44100, 5000), 44100)
	got, err = NormalizeWAV(w44)
	if err != nil {
		t.Fatal(err)
	}
	pcm, rate, err := DecodeWAV(got)
	if err != nil || rate != 8000 || len(pcm)/2 != 8000 {
		t.Fatalf("rate=%d samples=%d err=%v", rate, len(pcm)/2, err)
	}
	// Empty audio and junk are refused.
	if _, err := NormalizeWAV(EncodeWAV(nil, 8000)); err == nil {
		t.Error("empty audio accepted")
	}
	if _, err := NormalizeWAV([]byte("not a wav at all")); err == nil {
		t.Error("junk accepted")
	}
}
