package media

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync"
)

// Resampler design. The kernel is a Kaiser-windowed sinc low-pass. Kaiser
// (beta 8) holds the stop band about 80 dB down, where the Hann window the
// first version used stops at about 44 dB, which let loud treble above the
// target Nyquist fold back into the band as audible aliasing on music.
const (
	// resampleZeroCrossings is the number of sinc zero crossings kept on
	// each side of the kernel centre, counted at the cut-off frequency.
	resampleZeroCrossings = 24
	// resampleBeta is the Kaiser window shape parameter.
	resampleBeta = 8.0
	// resampleDownCutoff places the cut-off at this fraction of the target
	// Nyquist frequency when reducing the rate, so the transition band ends
	// at the Nyquist frequency rather than straddling it.
	resampleDownCutoff = 0.95
	// maxKernelPhases bounds the precomputed kernel table. A rate pair that
	// needs more phases (odd ratios such as 44100 -> 47999) computes its
	// kernel per output sample instead.
	maxKernelPhases = 4096
	// parallelMinOutput is the output length from which Resample splits the
	// work over several goroutines.
	parallelMinOutput = 1 << 15
)

// Resample converts 16-bit little-endian mono PCM from one sample rate to
// another with a Kaiser-windowed sinc low-pass kernel evaluated as a
// polyphase filter (the kernel for each output phase is computed once, not
// per sample). When the target rate is below the source rate the cut-off
// sits just below the target's Nyquist frequency, so nothing above it
// aliases back into the band: a tone above the target Nyquist comes out
// 60 dB or more down. Equal rates return a copy. The result has
// round(samples*to/from) samples.
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
	nOut := int(math.Round(float64(n) * float64(to) / float64(from)))
	out := make([]byte, 2*nOut)
	if nOut == 0 {
		return out, nil
	}
	g := gcd(from, to)
	l, m := from/g, to/g // input samples and output samples per period
	// Normalised cut-off in cycles per input sample. gain = 2*fc keeps the
	// DC gain of the sinc at one.
	fc := 0.5 * math.Min(1, float64(to)/float64(from))
	if to < from {
		fc *= resampleDownCutoff
	}
	gain := 2 * fc
	halfWidth := float64(resampleZeroCrossings) / gain // input samples
	rs := &resampler{pcm: pcm, n: n, out: out, l: l, m: m, gain: gain, halfWidth: halfWidth}
	if m <= maxKernelPhases {
		rs.table = make([]phaseKernel, m)
		for ph := range rs.table {
			rs.table[ph] = rs.kernel(ph)
		}
	}
	workers := 1
	if rs.table != nil && nOut >= parallelMinOutput {
		workers = min(runtime.GOMAXPROCS(0), 8)
	}
	if workers == 1 {
		rs.run(0, nOut)
		return out, nil
	}
	var wg sync.WaitGroup
	chunk := (nOut + workers - 1) / workers
	for lo := 0; lo < nOut; lo += chunk {
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			rs.run(lo, hi)
		}(lo, min(lo+chunk, nOut))
	}
	wg.Wait()
	return out, nil
}

// phaseKernel is the filter taps for one output phase: w[t] weights input
// sample base+t, where base = period*l + first.
type phaseKernel struct {
	first int
	w     []float64
}

type resampler struct {
	pcm       []byte
	n         int
	out       []byte
	l, m      int // output j sits at input position (j/m)*l + (j%m)*l/m
	gain      float64
	halfWidth float64
	table     []phaseKernel // nil when m is too large to precompute
}

// kernel builds the taps for output phase ph. The weights are normalised to
// sum to one over the whole kernel, including taps that fall outside the
// signal (silence), so the level holds at the edges.
func (r *resampler) kernel(ph int) phaseKernel {
	centre := float64(ph) * float64(r.l) / float64(r.m) // within the period
	lo := int(math.Ceil(centre - r.halfWidth))
	hi := int(math.Floor(centre + r.halfWidth))
	w := make([]float64, hi-lo+1)
	var norm float64
	for t := range w {
		u := float64(lo+t) - centre
		w[t] = r.gain * sinc(r.gain*u) * kaiser(u/r.halfWidth)
		norm += w[t]
	}
	if norm != 0 {
		for t := range w {
			w[t] /= norm
		}
	}
	return phaseKernel{first: lo, w: w}
}

// run computes output samples [lo, hi).
func (r *resampler) run(lo, hi int) {
	for j := lo; j < hi; j++ {
		period, ph := j/r.m, j%r.m
		var k phaseKernel
		if r.table != nil {
			k = r.table[ph]
		} else {
			k = r.kernel(ph)
		}
		start := period*r.l + k.first
		var acc float64
		if start >= 0 && start+len(k.w) <= r.n {
			b := r.pcm[2*start : 2*(start+len(k.w))]
			for t, w := range k.w {
				//nolint:gosec // G115: reinterpreting the two bytes as a signed sample is the point.
				acc += float64(int16(binary.LittleEndian.Uint16(b[2*t:]))) * w
			}
		} else {
			for t, w := range k.w {
				if idx := start + t; idx >= 0 && idx < r.n {
					//nolint:gosec // G115: reinterpreting the two bytes as a signed sample is the point.
					acc += float64(int16(binary.LittleEndian.Uint16(r.pcm[2*idx:]))) * w
				}
			}
		}
		v := int(math.Round(acc))
		if v > math.MaxInt16 {
			v = math.MaxInt16
		} else if v < math.MinInt16 {
			v = math.MinInt16
		}
		//nolint:gosec // G115: v is clamped to the int16 range above.
		binary.LittleEndian.PutUint16(r.out[2*j:], uint16(int16(v)))
	}
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// kaiser is the Kaiser window at x in [-1, 1].
func kaiser(x float64) float64 {
	if x <= -1 || x >= 1 {
		return 0
	}
	return besselI0(resampleBeta*math.Sqrt(1-x*x)) / besselI0(resampleBeta)
}

// besselI0 is the zeroth-order modified Bessel function of the first kind.
func besselI0(x float64) float64 {
	sum, term := 1.0, 1.0
	for k := 1; k < 64; k++ {
		h := x / (2 * float64(k))
		term *= h * h
		sum += term
		if term < sum*1e-17 {
			break
		}
	}
	return sum
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	return math.Sin(math.Pi*x) / (math.Pi * x)
}

// MaxStoredRate is the highest sample rate Hello keeps an uploaded WAV at.
// 48 kHz is Opus's native rate, so a 44.1 or 48 kHz song reaches an Opus
// leg without a resample and a G.722 or G.711 leg gets one clean downsample
// at play time. Rates up to it are stored as uploaded (an 8 kHz prompt stays
// 8 kHz, a 16 kHz one stays 16 kHz): upsampling at upload would only grow
// the file, and playback resamples to each session's rate anyway. Anything
// above it (88.2, 96, 192 kHz) is reduced to 48 kHz.
const MaxStoredRate = 48000

// NormalizeWAV prepares an uploaded WAV for storage. Any PCM (8, 16, 24 or
// 32 bit) or float WAV with any number of channels is accepted; it is
// down-mixed to mono and stored as 16-bit PCM at its own sample rate, capped
// at MaxStoredRate. The call plane resamples to each call's rate when it
// plays. A file already stored in that shape is returned unchanged. Files in
// another codec, or holding no audio, are refused with an error naming the
// problem.
func NormalizeWAV(data []byte) ([]byte, error) {
	d, err := decodeWAV(data)
	if err != nil {
		return nil, err
	}
	if len(d.pcm) < 2 {
		return nil, errors.New("the WAV holds no audio")
	}
	if d.rate <= MaxStoredRate {
		if d.exact {
			return data, nil
		}
		if d.room != nil { // converted into a buffer with header room
			putWAVHeader(d.room, len(d.pcm), d.rate)
			return d.room, nil
		}
		return EncodeWAV(d.pcm, d.rate), nil
	}
	out, err := Resample(d.pcm, d.rate, MaxStoredRate)
	if err != nil {
		return nil, err
	}
	return EncodeWAV(out, MaxStoredRate), nil
}
