package media

import (
	"math"
	"sort"
)

// The audio mixer of a server conference room (spec S-34). Every party's
// audio is brought to one mixing rate (MixRate) as 16-bit samples, the
// loudest few talkers are summed, and each party gets that sum minus its own
// voice, so nobody hears an echo of themselves. The functions here are
// pure: the clocked network side is Room.

const (
	// MixRate is the common mixing rate in Hz. G.711 is already 8 kHz; a
	// wideband codec (G.722, Opus) is resampled to and from this rate by its
	// codec adapter, so the mixer itself never cares which codec a party uses.
	MixRate = sampleRateHz
	// MixFrame is the samples in one 20 ms mixing frame.
	MixFrame = frameSamples
	// MaxTalkers is how many simultaneous talkers are summed. Summing every
	// party would add the noise floor of all of them and clip; a room of
	// ten still has at most a few people speaking at once.
	MaxTalkers = 4
	// talkGate is the mean-square energy a frame needs to count as speech
	// (about -46 dBFS RMS): below it a party contributes nothing, so a
	// room of idle microphones adds no hiss.
	talkGate = 50 * 50
)

// energy is a frame's mean-square sample value.
func energy(f []int16) float64 {
	if len(f) == 0 {
		return 0
	}
	var sum float64
	for _, v := range f {
		sum += float64(v) * float64(v)
	}
	return sum / float64(len(f))
}

func clamp16(v int32) int16 {
	switch {
	case v > math.MaxInt16:
		return math.MaxInt16
	case v < math.MinInt16:
		return math.MinInt16
	}
	return int16(v)
}

// MixFrames returns, for every id in the output set, the frame it hears:
// the sum of the loudest talkers among in, without its own contribution. in
// holds each unmuted party's latest frame (a missing id or an empty frame is
// silence); out lists the ids to produce a frame for (every party, muted or
// not). Frames shorter than n are zero-padded; all returned frames are n
// samples long.
func MixFrames(in map[string][]int16, out []string, n int) map[string][]int16 {
	type talker struct {
		id string
		e  float64
	}
	var talkers []talker
	for id, f := range in {
		if e := energy(f); e >= talkGate {
			talkers = append(talkers, talker{id, e})
		}
	}
	// Loudest first; the id breaks ties so the choice is deterministic.
	sort.Slice(talkers, func(i, j int) bool {
		if talkers[i].e != talkers[j].e {
			return talkers[i].e > talkers[j].e
		}
		return talkers[i].id < talkers[j].id
	})
	if len(talkers) > MaxTalkers {
		talkers = talkers[:MaxTalkers]
	}
	sum := make([]int32, n)
	picked := map[string]bool{}
	for _, t := range talkers {
		picked[t.id] = true
		for i, v := range in[t.id] {
			if i >= n {
				break
			}
			sum[i] += int32(v)
		}
	}
	res := make(map[string][]int16, len(out))
	for _, id := range out {
		frame := make([]int16, n)
		own := in[id]
		subtract := picked[id]
		for i := range frame {
			v := sum[i]
			if subtract && i < len(own) {
				v -= int32(own[i])
			}
			frame[i] = clamp16(v)
		}
		res[id] = frame
	}
	return res
}

// AddInto adds src into dst (clamped), up to the shorter of the two.
func AddInto(dst, src []int16) {
	for i := 0; i < len(dst) && i < len(src); i++ {
		dst[i] = clamp16(int32(dst[i]) + int32(src[i]))
	}
}

// MixAll is the sum of the loudest talkers, nobody subtracted: what a
// recording of the room captures.
func MixAll(in map[string][]int16, n int) []int16 {
	res := MixFrames(in, []string{""}, n)
	return res[""]
}

// PCMToSamples converts 16-bit little-endian PCM to samples.
func PCMToSamples(pcm []byte) []int16 {
	out := make([]int16, len(pcm)/2)
	for i := range out {
		out[i] = int16(uint16(pcm[2*i]) | uint16(pcm[2*i+1])<<8) //nolint:gosec // 16-bit sample
	}
	return out
}

// SamplesToPCM converts samples to 16-bit little-endian PCM.
func SamplesToPCM(s []int16) []byte {
	out := make([]byte, 2*len(s))
	for i, v := range s {
		out[2*i] = byte(v)        //nolint:gosec // low byte of a 16-bit sample
		out[2*i+1] = byte(v >> 8) //nolint:gosec // high byte
	}
	return out
}

// ToConfRate resamples PCM sampled at rate Hz to the mixing rate.
func ToConfRate(pcm []byte, rate int) ([]int16, error) {
	if rate == MixRate {
		return PCMToSamples(pcm), nil
	}
	out, err := Resample(pcm, rate, MixRate)
	if err != nil {
		return nil, err
	}
	return PCMToSamples(out), nil
}

// FromConfRate resamples mixing-rate samples to a codec's rate.
func FromConfRate(s []int16, rate int) ([]int16, error) {
	if rate == MixRate {
		return s, nil
	}
	out, err := Resample(SamplesToPCM(s), MixRate, rate)
	if err != nil {
		return nil, err
	}
	return PCMToSamples(out), nil
}
