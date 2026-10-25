package media

import (
	"bytes"
	"math"
	"os"
	"testing"
)

// The vectors in testdata come from the FFmpeg libavcodec G.722 codec, which
// is bit-exact with ITU-T G.722 (64 kbit/s): g722_in.pcm is 0.25 s of a
// chirp plus noise at 16 kHz, g722_ref.g722 its encoding and
// g722_ref_dec.pcm the decoding of that encoding.
func readVector(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestG722EncodeMatchesReference(t *testing.T) {
	in := PCMToInt16(readVector(t, "g722_in.pcm"))
	want := readVector(t, "g722_ref.g722")
	got := NewG722Encoder().Encode(in)
	if !bytes.Equal(got, want) {
		for i := range want {
			if i >= len(got) || got[i] != want[i] {
				t.Fatalf("encoder diverges from the reference at byte %d of %d: got %#02x want %#02x", i, len(want), got[i], want[i])
			}
		}
		t.Fatalf("encoder output is %d bytes, reference %d", len(got), len(want))
	}
}

func TestG722DecodeMatchesReference(t *testing.T) {
	code := readVector(t, "g722_ref.g722")
	want := readVector(t, "g722_ref_dec.pcm")
	got := Int16ToPCM(NewG722Decoder().Decode(code))
	if !bytes.Equal(got, want) {
		for i := 0; i+1 < len(want); i += 2 {
			if i+1 >= len(got) || got[i] != want[i] || got[i+1] != want[i+1] {
				t.Fatalf("decoder diverges from the reference at sample %d", i/2)
			}
		}
		t.Fatalf("decoder output is %d bytes, reference %d", len(got), len(want))
	}
}

func TestG722ChunkedMatchesWhole(t *testing.T) {
	in := PCMToInt16(readVector(t, "g722_in.pcm"))
	whole := NewG722Encoder().Encode(in)
	enc := NewG722Encoder()
	var chunked []byte
	for off := 0; off < len(in); off += 320 {
		chunked = append(chunked, enc.Encode(in[off:min(off+320, len(in))])...)
	}
	if !bytes.Equal(whole, chunked) {
		t.Fatal("encoding frame by frame differs from encoding in one go")
	}
}

func TestG722RoundTripQuality(t *testing.T) {
	// A 300 Hz plus 5 kHz tone: the 5 kHz part lives in the high sub-band,
	// which G.711 cannot carry at all.
	n := 16000
	in := make([]int16, n)
	for i := range in {
		tt := float64(i) / 16000
		in[i] = int16(6000*math.Sin(2*math.Pi*300*tt) + 4000*math.Sin(2*math.Pi*5000*tt))
	}
	out := NewG722Decoder().Decode(NewG722Encoder().Encode(in))
	if len(out) != len(in) {
		t.Fatalf("decoded %d samples, want %d", len(out), len(in))
	}
	// The codec delays the signal; find the best alignment and compare
	// the tone energy at 5 kHz in the second half.
	e5in, e5out := goertzel(in[8000:], 5000, 16000), goertzel(out[8000:], 5000, 16000)
	if e5out < 0.5*e5in {
		t.Fatalf("5 kHz energy lost: in %.0f out %.0f", e5in, e5out)
	}
}

// goertzel returns the magnitude of one frequency in samples.
func goertzel(x []int16, freq, rate float64) float64 {
	w := 2 * math.Pi * freq / rate
	c := 2 * math.Cos(w)
	var s1, s2 float64
	for _, v := range x {
		s0 := float64(v) + c*s1 - s2
		s2, s1 = s1, s0
	}
	return math.Sqrt(s1*s1+s2*s2-c*s1*s2) / float64(len(x)) * 2
}

func TestG722DecoderSurvivesAnyBytes(t *testing.T) {
	junk := make([]byte, 4096)
	seed := uint32(7)
	for i := range junk {
		seed = seed*1103515245 + 12345
		junk[i] = byte(seed >> 16)
	}
	if got := len(NewG722Decoder().Decode(junk)); got != 2*len(junk) {
		t.Fatalf("decoded %d samples from %d bytes", got, len(junk))
	}
}

func FuzzG722Decode(f *testing.F) {
	f.Add([]byte{0, 1, 2, 255, 128})
	f.Fuzz(func(t *testing.T, b []byte) {
		if got := len(NewG722Decoder().Decode(b)); got != 2*len(b) {
			t.Fatalf("decoded %d samples from %d bytes", got, len(b))
		}
	})
}
