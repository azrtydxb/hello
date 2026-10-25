package media

import (
	"errors"
	"fmt"
	"math"
)

// resampleZeroCrossings is the number of sinc zero crossings kept on each
// side of the kernel centre. 16 gives well over 60 dB of stop-band
// rejection with the Hann window below, which is ample for telephone speech.
const resampleZeroCrossings = 16

// Resample converts 16-bit little-endian mono PCM from one sample rate to
// another with a Hann-windowed sinc low-pass kernel. When the target rate is
// below the source rate the cut-off sits at the target's Nyquist frequency,
// so nothing above it aliases back into the band. Equal rates return a copy.
// The result has round(samples*to/from) samples.
func Resample(pcm []byte, from, to int) ([]byte, error) {
	if from <= 0 || to <= 0 {
		return nil, fmt.Errorf("media: invalid sample rate %d -> %d", from, to)
	}
	if len(pcm)%2 != 0 {
		return nil, errors.New("media: PCM has an odd byte count")
	}
	if from == to {
		return append([]byte(nil), pcm...), nil
	}
	n := len(pcm) / 2
	in := make([]float64, n)
	for i := range in {
		in[i] = float64(int16(uint16(pcm[2*i]) | uint16(pcm[2*i+1])<<8))
	}
	nOut := int(math.Round(float64(n) * float64(to) / float64(from)))
	out := make([]byte, 2*nOut)
	step := float64(from) / float64(to) // input samples per output sample
	// Normalised cut-off in cycles per input sample: Nyquist of the lower
	// rate. gain = 2*fc keeps the DC gain of the kernel at one.
	fc := 0.5 * math.Min(1, float64(to)/float64(from))
	gain := 2 * fc
	halfWidth := float64(resampleZeroCrossings) / gain // input samples
	for j := 0; j < nOut; j++ {
		centre := float64(j) * step
		lo := int(math.Ceil(centre - halfWidth))
		hi := int(math.Floor(centre + halfWidth))
		var acc, norm float64
		for k := lo; k <= hi; k++ {
			u := float64(k) - centre
			w := gain * sinc(gain*u) * (0.5 + 0.5*math.Cos(math.Pi*u/halfWidth))
			norm += w
			if k >= 0 && k < n {
				acc += in[k] * w
			}
		}
		// norm sums the whole kernel, including taps that fall outside the
		// signal, so the level holds at the edges (the missing taps are
		// silence) instead of being rescaled by a partial kernel.
		if norm != 0 {
			acc /= norm
		}
		v := int(math.Round(acc))
		if v > math.MaxInt16 {
			v = math.MaxInt16
		} else if v < math.MinInt16 {
			v = math.MinInt16
		}
		out[2*j] = byte(v)
		out[2*j+1] = byte(v >> 8)
	}
	return out, nil
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	return math.Sin(math.Pi*x) / (math.Pi * x)
}

// NormalizeWAV makes a WAV file playable by the call plane, which assumes
// 8 kHz 16-bit mono PCM. A file already at 8 kHz is returned unchanged; any
// other rate is resampled to 8 kHz and re-wrapped. Files that are not 16-bit
// mono PCM, or hold no audio, are refused with an error naming the problem.
func NormalizeWAV(data []byte) ([]byte, error) {
	pcm, rate, err := DecodeWAV(data)
	if err != nil {
		return nil, err
	}
	if len(pcm) < 2 {
		return nil, errors.New("the WAV holds no audio")
	}
	if rate == sampleRateHz {
		return data, nil
	}
	out, err := Resample(pcm[:len(pcm)&^1], rate, sampleRateHz)
	if err != nil {
		return nil, err
	}
	return EncodeWAV(out, sampleRateHz), nil
}
