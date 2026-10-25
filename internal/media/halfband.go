package media

import "math"

// Streaming 2:1 sample-rate converters between 8 kHz and 16 kHz. Resample
// converts whole buffers; a call's audio arrives 20 ms at a time and the
// converters must carry their filter state from one frame to the next, or
// every frame edge clicks. Both use a 31-tap Hann-windowed sinc low-pass at
// a quarter of the 16 kHz rate (the 8 kHz Nyquist frequency); the group
// delay is 15 samples at 16 kHz.

const halfbandHalf = 15 // taps on each side of the centre

var halfbandTaps = func() [2*halfbandHalf + 1]float64 {
	var h [2*halfbandHalf + 1]float64
	var sum float64
	for k := -halfbandHalf; k <= halfbandHalf; k++ {
		w := 0.5 + 0.5*math.Cos(math.Pi*float64(k)/(halfbandHalf+1))
		h[k+halfbandHalf] = 0.5 * sinc(0.5*float64(k)) * w
		sum += h[k+halfbandHalf]
	}
	for i := range h {
		h[i] /= sum // unity gain at DC
	}
	return h
}()

// firStream runs the half-band filter over a stream, emitting one output per
// step input samples.
type firStream struct {
	buf  []float64
	step int
	gain float64
}

func newFIRStream(step int, gain float64) firStream {
	// Pre-roll with silence so the first output sees a full kernel.
	return firStream{buf: make([]float64, halfbandHalf), step: step, gain: gain}
}

func (f *firStream) process(in []float64) []int16 {
	f.buf = append(f.buf, in...)
	var out []int16
	c := halfbandHalf
	for ; c+halfbandHalf < len(f.buf); c += f.step {
		var acc float64
		for k := 0; k < len(halfbandTaps); k++ {
			acc += halfbandTaps[k] * f.buf[c-halfbandHalf+k]
		}
		v := math.Round(acc * f.gain)
		switch {
		case v > math.MaxInt16:
			v = math.MaxInt16
		case v < math.MinInt16:
			v = math.MinInt16
		}
		out = append(out, int16(v))
	}
	keep := c - halfbandHalf
	f.buf = append(f.buf[:0], f.buf[keep:]...)
	return out
}

// Down2 converts a 16 kHz stream to 8 kHz. Not safe for concurrent use.
type Down2 struct{ f firStream }

// NewDown2 returns a 16 kHz to 8 kHz converter.
func NewDown2() *Down2 { return &Down2{f: newFIRStream(2, 1)} }

// Process converts the next chunk of samples; the output is about half as
// long, delayed by the filter.
func (d *Down2) Process(in []int16) []int16 {
	x := make([]float64, len(in))
	for i, v := range in {
		x[i] = float64(v)
	}
	return d.f.process(x)
}

// Up2 converts an 8 kHz stream to 16 kHz. Not safe for concurrent use.
type Up2 struct{ f firStream }

// NewUp2 returns an 8 kHz to 16 kHz converter.
func NewUp2() *Up2 { return &Up2{f: newFIRStream(1, 2)} }

// Process converts the next chunk of samples; the output is twice as long
// (less the filter's delay at the start).
func (u *Up2) Process(in []int16) []int16 {
	x := make([]float64, 2*len(in))
	for i, v := range in {
		x[2*i] = float64(v)
	}
	return u.f.process(x)
}
