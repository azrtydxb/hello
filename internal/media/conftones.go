package media

import (
	"bytes"
	"errors"
	"math"
	"sync"
)

// ConfTone names a conference cue. Cues are short synthesised tones, so a
// room works with no uploaded audio; a room may play an announcement instead
// of the entry and exit tones.
type ConfTone int

// The conference cues.
const (
	ToneJoin   ConfTone = iota // someone joined: two rising notes
	ToneLeave                  // someone left: two falling notes
	TonePIN                    // enter your PIN: one steady beep
	ToneMuted                  // you are muted: one low beep
	ToneUnmute                 // you are unmuted: one high beep
	ToneLocked                 // the room locked or unlocked: two even beeps
	ToneDenied                 // refused (wrong PIN, locked, full): three short low beeps
	ToneAdmit                  // a leader arrived, the wait is over: three rising notes
)

type toneSeg struct {
	hz, ms int
}

var toneSegs = map[ConfTone][]toneSeg{
	ToneJoin:   {{660, 110}, {880, 140}},
	ToneLeave:  {{880, 110}, {660, 140}},
	TonePIN:    {{800, 220}},
	ToneMuted:  {{400, 200}},
	ToneUnmute: {{1000, 200}},
	ToneLocked: {{600, 120}, {0, 80}, {600, 120}},
	ToneDenied: {{330, 120}, {0, 80}, {330, 120}, {0, 80}, {330, 120}},
	ToneAdmit:  {{600, 100}, {800, 100}, {1000, 140}},
}

// ConfToneSamples renders a cue at the mixing rate.
func ConfToneSamples(t ConfTone) []int16 {
	const (
		amp  = 7000
		fade = 8 // ms of fade in and out, so a cue does not click
	)
	var out []int16
	for _, s := range toneSegs[t] {
		n := s.ms * MixRate / 1000
		f := fade * MixRate / 1000
		for i := range n {
			var v float64
			if s.hz > 0 {
				v = amp * math.Sin(2*math.Pi*float64(s.hz)*float64(i)/MixRate)
				if i < f {
					v *= float64(i) / float64(f)
				}
				if tail := n - 1 - i; tail < f {
					v *= float64(tail) / float64(f)
				}
			}
			out = append(out, int16(v))
		}
	}
	return out
}

// WaitLoop is the default hold source of a party waiting for the room's
// leader: a soft short beep every 6 seconds, so the caller can tell the line
// is alive. Music on hold replaces it through Room.SetHold.
func WaitLoop() []int16 {
	const (
		beepMs = 250
		loopMs = 6000
		amp    = 2500
	)
	loop := make([]int16, loopMs*MixRate/1000)
	n := beepMs * MixRate / 1000
	for i := range n {
		env := math.Sin(math.Pi * float64(i) / float64(n)) // smooth swell
		loop[i] = int16(amp * env * math.Sin(2*math.Pi*440*float64(i)/MixRate))
	}
	return loop
}

// DefaultMaxConfRecording bounds one conference recording held in memory:
// 64 MB of 16-bit 8 kHz audio, about an hour.
const DefaultMaxConfRecording = 64 << 20

// ConfRecorder collects a room's full mix into one WAV. It is bounded; once
// full it drops further audio and reports Over.
type ConfRecorder struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	max  int
	over bool
}

// NewConfRecorder returns a recorder holding at most max bytes of PCM (zero
// means DefaultMaxConfRecording).
func NewConfRecorder(max int) *ConfRecorder {
	if max <= 0 {
		max = DefaultMaxConfRecording
	}
	return &ConfRecorder{max: max}
}

// Append adds one mixing frame.
func (r *ConfRecorder) Append(frame []int16) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.buf.Len()+2*len(frame) > r.max {
		r.over = true
		return
	}
	r.buf.Write(SamplesToPCM(frame))
}

// Over reports that the bound was hit and audio was dropped.
func (r *ConfRecorder) Over() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.over
}

// Result returns the recording as a WAV and its length in milliseconds.
func (r *ConfRecorder) Result() (wav []byte, durMs int64, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.buf.Len() == 0 {
		return nil, 0, errors.New("media: the conference recording is empty")
	}
	pcm := append([]byte(nil), r.buf.Bytes()...)
	return EncodeWAV(pcm, MixRate), int64(len(pcm)/2) * 1000 / MixRate, nil
}
