package media

// G.722 (ITU-T Rec. G.722, 64 kbit/s mode 1): sub-band ADPCM wideband speech
// at a 16 kHz sample rate. The 16 kHz signal is split by a 24-tap QMF into a
// low band (0-4 kHz, 6-bit ADPCM) and a high band (4-8 kHz, 2-bit ADPCM);
// every pair of input samples becomes one byte. This is a pure-Go
// implementation of the standard's fixed-point algorithm (no cgo).
//
// RTP quirk (RFC 3551 section 4.5.2): G.722 audio is sampled at 16 kHz but
// its RTP clock rate is 8000 Hz, so a 20 ms frame is 320 samples, 160 bytes
// and an RTP timestamp step of 160.

// g722QMF are the 12 distinct taps of the symmetric 24-tap QMF.
var g722QMF = [12]int{3, -11, 12, 32, -210, 951, 3876, -805, 362, -156, 53, -11}

var (
	g722Q6   = [32]int{0, 35, 72, 110, 150, 190, 233, 276, 323, 370, 422, 473, 530, 587, 650, 714, 786, 858, 940, 1023, 1121, 1219, 1339, 1458, 1612, 1765, 1980, 2195, 2557, 2919, 0, 0}
	g722ILN  = [32]int{0, 63, 62, 31, 30, 29, 28, 27, 26, 25, 24, 23, 22, 21, 20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 0}
	g722ILP  = [32]int{0, 61, 60, 59, 58, 57, 56, 55, 54, 53, 52, 51, 50, 49, 48, 47, 46, 45, 44, 43, 42, 41, 40, 39, 38, 37, 36, 35, 34, 33, 32, 0}
	g722WL   = [8]int{-60, -30, 58, 172, 334, 538, 1198, 3042}
	g722RL42 = [16]int{0, 7, 6, 5, 4, 3, 2, 1, 7, 6, 5, 4, 3, 2, 1, 0}
	g722ILB  = [32]int{2048, 2093, 2139, 2186, 2233, 2282, 2332, 2383, 2435, 2489, 2543, 2599, 2656, 2714, 2774, 2834, 2896, 2960, 3025, 3091, 3158, 3228, 3298, 3371, 3444, 3520, 3597, 3676, 3756, 3838, 3922, 4008}
	g722QM4  = [16]int{0, -20456, -12896, -8968, -6288, -4240, -2584, -1200, 20456, 12896, 8968, 6288, 4240, 2584, 1200, 0}
	g722QM2  = [4]int{-7408, -1616, 7408, 1616}
	g722QM6  = [64]int{
		-136, -136, -136, -136, -24808, -21904, -19008, -16704, -14984, -13512, -12280, -11192,
		-10232, -9360, -8576, -7856, -7192, -6576, -6000, -5456, -4944, -4464, -4008, -3576,
		-3168, -2776, -2400, -2032, -1688, -1360, -1040, -728, 24808, 21904, 19008, 16704,
		14984, 13512, 12280, 11192, 10232, 9360, 8576, 7856, 7192, 6576, 6000, 5456,
		4944, 4464, 4008, 3576, 3168, 2776, 2400, 2032, 1688, 1360, 1040, 728,
		432, 136, -432, -136,
	}
	g722IHN = [3]int{0, 1, 0}
	g722IHP = [3]int{0, 3, 2}
	g722WH  = [3]int{0, -214, 798}
	g722RH2 = [4]int{2, 1, 2, 1}
)

// g722Band is one sub-band's adaptive predictor and quantiser scale state.
type g722Band struct {
	s, sp, sz int
	r, a, ap  [3]int
	p, d, b   [7]int
	bp        [7]int
	sg        [7]int
	nb, det   int
}

func g722Sat(v int) int {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return v
}

func g722Abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// block4 adapts one band's predictor with the quantised difference d (the
// standard's block 4).
func (b *g722Band) block4(d int) {
	b.d[0] = d
	b.r[0] = g722Sat(b.s + d)
	b.p[0] = g722Sat(b.sz + d)

	for i := 0; i < 3; i++ {
		b.sg[i] = b.p[i] >> 15
	}
	wd1 := g722Sat(b.a[1] << 2)
	wd2 := wd1
	if b.sg[0] == b.sg[1] {
		wd2 = -wd1
	}
	if wd2 > 32767 {
		wd2 = 32767
	}
	wd3 := -128
	if b.sg[0] == b.sg[2] {
		wd3 = 128
	}
	wd3 += wd2 >> 7
	wd3 += (b.a[2] * 32512) >> 15
	if g722Abs(wd3) > 12288 {
		if wd3 < 0 {
			wd3 = -12288
		} else {
			wd3 = 12288
		}
	}
	b.ap[2] = wd3

	b.sg[0] = b.p[0] >> 15
	b.sg[1] = b.p[1] >> 15
	wd1 = -192
	if b.sg[0] == b.sg[1] {
		wd1 = 192
	}
	wd2 = (b.a[1] * 32640) >> 15
	b.ap[1] = g722Sat(wd1 + wd2)
	wd3 = g722Sat(15360 - b.ap[2])
	if g722Abs(b.ap[1]) > wd3 {
		if b.ap[1] < 0 {
			b.ap[1] = -wd3
		} else {
			b.ap[1] = wd3
		}
	}

	wd1 = 128
	if d == 0 {
		wd1 = 0
	}
	b.sg[0] = d >> 15
	for i := 1; i < 7; i++ {
		b.sg[i] = b.d[i] >> 15
		wd2 = -wd1
		if b.sg[i] == b.sg[0] {
			wd2 = wd1
		}
		wd3 = (b.b[i] * 32640) >> 15
		b.bp[i] = g722Sat(wd2 + wd3)
	}

	for i := 6; i > 0; i-- {
		b.d[i] = b.d[i-1]
		b.b[i] = b.bp[i]
	}
	for i := 2; i > 0; i-- {
		b.r[i] = b.r[i-1]
		b.p[i] = b.p[i-1]
		b.a[i] = b.ap[i]
	}

	wd1 = g722Sat(b.r[1] + b.r[1])
	wd1 = (b.a[1] * wd1) >> 15
	wd2 = g722Sat(b.r[2] + b.r[2])
	wd2 = (b.a[2] * wd2) >> 15
	b.sp = g722Sat(wd1 + wd2)

	b.sz = 0
	for i := 6; i > 0; i-- {
		wd1 = g722Sat(b.d[i] + b.d[i])
		b.sz += (b.b[i] * wd1) >> 15
	}
	b.sz = g722Sat(b.sz)
	b.s = g722Sat(b.sp + b.sz)
}

// scale updates the log scale factor nb from the quantiser's weight w and
// derives the scale factor det; shift is 8 for the low band and 10 for the
// high band.
func (b *g722Band) scale(w, limit, shift int) {
	b.nb = ((b.nb * 127) >> 7) + w
	if b.nb < 0 {
		b.nb = 0
	} else if b.nb > limit {
		b.nb = limit
	}
	i := (b.nb >> 6) & 31
	sh := shift - (b.nb >> 11)
	var v int
	if sh < 0 {
		v = g722ILB[i] << uint(-sh)
	} else {
		v = g722ILB[i] >> uint(sh)
	}
	b.det = v << 2
}

// G722Encoder is a stateful G.722 encoder. It is not safe for concurrent
// use.
type G722Encoder struct {
	x    [24]int
	band [2]g722Band
}

// NewG722Encoder returns an encoder in the standard's initial state.
func NewG722Encoder() *G722Encoder {
	e := &G722Encoder{}
	e.band[0].det = 32
	e.band[1].det = 8
	return e
}

// Encode encodes 16 kHz PCM samples, two per output byte. A trailing odd
// sample is ignored.
func (e *G722Encoder) Encode(pcm []int16) []byte {
	out := make([]byte, 0, len(pcm)/2)
	for j := 0; j+1 < len(pcm); j += 2 {
		copy(e.x[:22], e.x[2:])
		e.x[22] = int(pcm[j])
		e.x[23] = int(pcm[j+1])
		var sumeven, sumodd int
		for i := 0; i < 12; i++ {
			sumodd += e.x[2*i] * g722QMF[i]
			sumeven += e.x[2*i+1] * g722QMF[11-i]
		}
		xlow := (sumeven + sumodd) >> 14
		xhigh := (sumeven - sumodd) >> 14

		// Low band: 6-bit quantiser.
		lo := &e.band[0]
		el := g722Sat(xlow - lo.s)
		wd := el
		if el < 0 {
			wd = -(el + 1)
		}
		i := 1
		for ; i < 30; i++ {
			if wd < (g722Q6[i]*lo.det)>>12 {
				break
			}
		}
		var ilow int
		if el < 0 {
			ilow = g722ILN[i]
		} else {
			ilow = g722ILP[i]
		}
		ril := ilow >> 2
		dlow := (lo.det * g722QM4[ril]) >> 15
		lo.scale(g722WL[g722RL42[ril]], 18432, 8)
		lo.block4(dlow)

		// High band: 2-bit quantiser.
		hi := &e.band[1]
		eh := g722Sat(xhigh - hi.s)
		wd = eh
		if eh < 0 {
			wd = -(eh + 1)
		}
		mih := 1
		if wd >= (564*hi.det)>>12 {
			mih = 2
		}
		var ihigh int
		if eh < 0 {
			ihigh = g722IHN[mih]
		} else {
			ihigh = g722IHP[mih]
		}
		dhigh := (hi.det * g722QM2[ihigh]) >> 15
		hi.scale(g722WH[g722RH2[ihigh]], 22528, 10)
		hi.block4(dhigh)

		out = append(out, byte((ihigh<<6)|ilow)) //nolint:gosec // 8-bit code word
	}
	return out
}

// G722Decoder is a stateful G.722 decoder. It is not safe for concurrent
// use.
type G722Decoder struct {
	x    [24]int
	band [2]g722Band
}

// NewG722Decoder returns a decoder in the standard's initial state.
func NewG722Decoder() *G722Decoder {
	d := &G722Decoder{}
	d.band[0].det = 32
	d.band[1].det = 8
	return d
}

// Decode decodes G.722 bytes (64 kbit/s mode) into 16 kHz PCM, two samples
// per input byte.
func (d *G722Decoder) Decode(code []byte) []int16 {
	out := make([]int16, 0, 2*len(code))
	for _, c := range code {
		lo, hi := &d.band[0], &d.band[1]
		wd1 := int(c) & 0x3f
		ihigh := (int(c) >> 6) & 3

		// Low band.
		rlow := lo.s + ((lo.det * g722QM6[wd1]) >> 15)
		if rlow > 16383 {
			rlow = 16383
		} else if rlow < -16384 {
			rlow = -16384
		}
		ril := wd1 >> 2
		dlowt := (lo.det * g722QM4[ril]) >> 15
		lo.scale(g722WL[g722RL42[ril]], 18432, 8)
		lo.block4(dlowt)

		// High band.
		dhigh := (hi.det * g722QM2[ihigh]) >> 15
		rhigh := dhigh + hi.s
		if rhigh > 16383 {
			rhigh = 16383
		} else if rhigh < -16384 {
			rhigh = -16384
		}
		hi.scale(g722WH[g722RH2[ihigh]], 22528, 10)
		hi.block4(dhigh)

		copy(d.x[:22], d.x[2:])
		d.x[22] = rlow + rhigh
		d.x[23] = rlow - rhigh
		var xout1, xout2 int
		for i := 0; i < 12; i++ {
			xout2 += d.x[2*i] * g722QMF[i]
			xout1 += d.x[2*i+1] * g722QMF[11-i]
		}
		out = append(out, int16(g722Sat(xout1>>11)), int16(g722Sat(xout2>>11))) //nolint:gosec // saturated to int16
	}
	return out
}
