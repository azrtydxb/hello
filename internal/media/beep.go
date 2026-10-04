package media

import (
	"encoding/binary"
	"math"
)

// BeepPCM returns the voicemail prompt beep: a 425 Hz sine for 500 ms,
// 16-bit mono at 8 kHz, with 10 ms linear fades in and out so the edges do
// not click on a phone. It is generated on every call; the shape is cheap
// to compute and the source of truth is this function, not a blob in the
// repo.
func BeepPCM() []byte {
	const (
		frequency = 425.0
		beepMs    = 500
		fadeMs    = 10
		// ~ -10 dBFS: loud enough to hear, far from clipping.
		amplitude = 10000
	)
	samples := beepMs * sampleRateHz / 1000
	fadeSamples := fadeMs * sampleRateHz / 1000
	out := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		v := amplitude * math.Sin(2*math.Pi*frequency*float64(i)/sampleRateHz)
		if i < fadeSamples {
			v *= float64(i) / float64(fadeSamples)
		}
		if tail := samples - 1 - i; tail < fadeSamples {
			v *= float64(tail) / float64(fadeSamples)
		}
		//nolint:gosec // G115: the byte pattern of the sample, not a bounded value.
		binary.LittleEndian.PutUint16(out[2*i:], uint16(int16(v)))
	}
	return out
}
