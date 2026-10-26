package media

import (
	"encoding/binary"
	"math"
)

// HoldTonePCM returns the built-in music a queued caller hears when no music
// on hold class is configured: a soft 440 Hz tone, 250 ms every 3 seconds
// (16-bit mono at 8 kHz, with 10 ms fades), then silence. It is computed, not
// stored, and is plain PCM: the relay encodes it in whatever codec the
// caller's leg negotiated.
func HoldTonePCM() []byte {
	const (
		frequency = 440.0
		beepMs    = 250
		periodMs  = 3000
		fadeMs    = 10
		amplitude = 4000
	)
	ms := func(n int) int { return n * sampleRateHz / 1000 }
	beep, period, fade := ms(beepMs), ms(periodMs), ms(fadeMs)
	out := make([]byte, 2*period)
	for i := range beep {
		v := amplitude * math.Sin(2*math.Pi*frequency*float64(i)/sampleRateHz)
		if i < fade {
			v *= float64(i) / float64(fade)
		}
		if tail := beep - 1 - i; tail < fade {
			v *= float64(tail) / float64(fade)
		}
		//nolint:gosec // G115: the byte pattern of the sample, not a bounded value.
		binary.LittleEndian.PutUint16(out[2*i:], uint16(int16(v)))
	}
	return out
}
