package media

import (
	"encoding/binary"
	"math"
	"testing"
)

// sineSweepPCM renders one sine per frequency, all at a speech-level
// amplitude, as 16-bit little-endian PCM.
func sineSweepPCM(freqs []float64, samples int) []byte {
	out := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		sum := 0.0
		for _, f := range freqs {
			sum += math.Sin(2 * math.Pi * f * float64(i) / sampleRateHz)
		}
		v := int16(8000 * sum / float64(len(freqs)))
		binary.LittleEndian.PutUint16(out[2*i:], uint16(v))
	}
	return out
}

func TestG711RoundTrip(t *testing.T) {
	pcm := sineSweepPCM([]float64{50, 200, 440, 1000, 3000}, sampleRateHz)
	for _, law := range []struct {
		name   string
		encode func([]byte) []byte
		decode func([]byte) []byte
	}{
		{"PCMU", EncodePCMU, DecodePCMU},
		{"PCMA", EncodePCMA, DecodePCMA},
	} {
		t.Run(law.name, func(t *testing.T) {
			wire := law.encode(pcm)
			if len(wire) != len(pcm)/2 {
				t.Fatalf("wire is %d bytes, want %d", len(wire), len(pcm)/2)
			}
			back := law.decode(wire)
			if len(back) != len(pcm) {
				t.Fatalf("decoded %d bytes, want %d", len(back), len(pcm))
			}
			var sum float64
			for i := 0; i < len(pcm)/2; i++ {
				want := int16(binary.LittleEndian.Uint16(pcm[2*i:]))
				got := int16(binary.LittleEndian.Uint16(back[2*i:]))
				d := float64(got - want)
				if d < 0 {
					d = -d
				}
				sum += d
			}
			mean := sum / float64(len(pcm))
			if mean >= 300 {
				t.Errorf("mean absolute error %.1f, want below 300", mean)
			}
		})
	}
}

// The zero and full-scale pins are the values every G.711 implementation
// is checked against: u-law silence is 0xFF, A-law silence is 0xD5.
func TestG711Pins(t *testing.T) {
	if got := DecodePCMU([]byte{0xFF})[0:2]; binary.LittleEndian.Uint16(got) != 0 {
		t.Errorf("u-law 0xFF decodes to %d, want 0", int16(binary.LittleEndian.Uint16(got)))
	}
	if wire := EncodePCMU([]byte{0, 0})[0]; wire != 0xFF {
		t.Errorf("PCM 0 encodes to u-law %#x, want 0xFF", wire)
	}
	if wire := EncodePCMA([]byte{0, 0})[0]; wire != 0xD5 {
		t.Errorf("PCM 0 encodes to A-law %#x, want 0xD5", wire)
	}
	if got := int16(binary.LittleEndian.Uint16(DecodePCMA([]byte{0xD5}))); got != 8 {
		t.Errorf("A-law 0xD5 decodes to %d, want 8 (the A-law zero quirk)", got)
	}
	// Near full scale must survive both directions without collapsing.
	for _, v := range []int16{32000, -32000} {
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], uint16(v))
		for _, law := range []struct {
			name   string
			encode func([]byte) []byte
			decode func([]byte) []byte
		}{{"PCMU", EncodePCMU, DecodePCMU}, {"PCMA", EncodePCMA, DecodePCMA}} {
			back := int16(binary.LittleEndian.Uint16(law.decode(law.encode(b[:]))))
			// |back| stays within G.711's ±32124 (u-law) / ±32256 (A-law)
			// range and within 1000 of the input.
			d := int(back) - int(v)
			if d < 0 {
				d = -d
			}
			if d > 1000 {
				t.Errorf("%s: %d round-tripped to %d", law.name, v, back)
			}
		}
	}
}
