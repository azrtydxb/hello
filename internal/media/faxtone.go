package media

import "math"

// FaxTone is a tone that announces a fax machine.
type FaxTone int

// The fax tones (ITU-T T.30): a calling machine sends CNG, 1100 Hz in 0.5 s
// bursts every 3 s; an answering machine sends CED, a steady 2100 Hz.
const (
	ToneNone FaxTone = iota
	ToneCNG
	ToneCED
)

func (t FaxTone) String() string {
	switch t {
	case ToneCNG:
		return "CNG"
	case ToneCED:
		return "CED"
	}
	return "none"
}

// FaxToneDetector finds CNG (1100 Hz) and CED (2100 Hz) in G.711 audio with
// the Goertzel algorithm over 100 ms windows. A tone must fill most of the
// window's energy for several windows in a row, so speech and music do not
// trigger it. Feed it the RTP payload of each packet; it is not safe for
// concurrent use.
type FaxToneDetector struct {
	win []float64
	run [3]int // consecutive windows per tone
}

const (
	faxWindow = 800 // 100 ms at 8 kHz
	// toneShare is the part of a window's energy the tone must hold.
	toneShare = 0.55
	// toneMinRMS is the quietest level, in 16-bit units, a tone may have.
	toneMinRMS = 300.0
	// toneWindows is how many windows in a row confirm a tone (300 ms; a CNG
	// burst lasts 500 ms).
	toneWindows = 3
)

// NewFaxToneDetector returns a detector for 8 kHz G.711 audio.
func NewFaxToneDetector() *FaxToneDetector {
	return &FaxToneDetector{win: make([]float64, 0, faxWindow)}
}

// Feed adds one G.711 payload (PCMU payload type 0 or PCMA 8) and returns the
// tone confirmed by it, ToneNone otherwise. After a tone is confirmed it
// reports it once per burst: the run counters restart when the tone stops.
func (d *FaxToneDetector) Feed(pt uint8, payload []byte) FaxTone {
	var pcm []byte
	switch pt {
	case 0:
		pcm = DecodePCMU(payload)
	case 8:
		pcm = DecodePCMA(payload)
	default:
		return ToneNone
	}
	found := ToneNone
	for i := 0; i+1 < len(pcm); i += 2 {
		d.win = append(d.win, float64(int16(uint16(pcm[i])|uint16(pcm[i+1])<<8))) //nolint:gosec // G115: reinterpreting two bytes as a signed sample
		if len(d.win) < faxWindow {
			continue
		}
		if t := d.window(); t != ToneNone {
			found = t
		}
		d.win = d.win[:0]
	}
	return found
}

func goertzelPower(x []float64, freq float64) float64 {
	k := 2 * math.Cos(2*math.Pi*freq/8000)
	var s1, s2 float64
	for _, v := range x {
		s0 := v + k*s1 - s2
		s2, s1 = s1, s0
	}
	return s1*s1 + s2*s2 - k*s1*s2
}

// window classifies one full window and returns a tone when it is confirmed
// for the first time in its run.
func (d *FaxToneDetector) window() FaxTone {
	var energy float64
	for _, v := range d.win {
		energy += v * v
	}
	n := float64(len(d.win))
	rms := math.Sqrt(energy / n)
	found := ToneNone
	for i, freq := range [...]float64{0, 1100, 2100} {
		if i == 0 {
			continue // index 0 is ToneNone
		}
		// A pure tone of amplitude A has energy n*A^2/2 and a Goertzel
		// magnitude of (n*A/2)^2, so power*2/n^2 / energy is its share.
		// A pure tone of amplitude A has energy n*A^2/2 and a Goertzel power
		// of (n*A/2)^2; the share is the tone's part of the window's energy.
		share := 0.0
		if energy > 0 {
			share = 2 * goertzelPower(d.win, freq) / (n * energy)
		}
		tone := FaxTone(i)
		if rms >= toneMinRMS && share >= toneShare {
			d.run[tone]++
			if d.run[tone] == toneWindows {
				found = tone
			}
		} else {
			d.run[tone] = 0
		}
	}
	return found
}
