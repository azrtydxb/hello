package media

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

// Music on hold audio (phone-feature-parity S-33): the built-in track and
// the relay's continuous playlist stream.

var (
	mohOnce sync.Once
	mohPCM  []byte
)

// DefaultMOHPCM is the built-in music on hold track: a slow, soft rising and
// falling arpeggio of decaying sine notes, two phrases a fifth apart with a
// rest between them (16-bit mono at 8 kHz). It is computed once and shared;
// callers must not modify it. It is the track a class with no announcements
// plays, and the one a class falls back to when none of its announcements
// can be loaded.
func DefaultMOHPCM() []byte {
	mohOnce.Do(func() {
		// C major pentatonic, up then down, one note every 400 ms.
		notes := []float64{261.63, 293.66, 329.63, 392.00, 440.00, 523.25, 440.00, 392.00, 329.63, 293.66}
		const (
			noteMs   = 700
			stepMs   = 400
			restMs   = 1600
			amp      = 6000
			attackMs = 15
		)
		ms := func(n int) int { return n * sampleRateHz / 1000 }
		phrase := len(notes)*ms(stepMs) + ms(restMs)
		total := 2 * phrase
		mix := make([]float64, total)
		for p := range 2 {
			for n, f := range notes {
				if p == 1 {
					f *= 1.5
				}
				start := p*phrase + n*ms(stepMs)
				length := ms(noteMs)
				for i := 0; i < length && start+i < total; i++ {
					t := float64(i) / sampleRateHz
					env := math.Exp(-4 * float64(i) / float64(length))
					if i < ms(attackMs) {
						env *= float64(i) / float64(ms(attackMs))
					}
					mix[start+i] += amp * env * (math.Sin(2*math.Pi*f*t) + 0.3*math.Sin(2*math.Pi*2*f*t))
				}
			}
		}
		out := make([]byte, 2*total)
		for i, v := range mix {
			v = math.Max(-30000, math.Min(30000, v))
			//nolint:gosec // G115: the byte pattern of the sample, not a bounded value.
			binary.LittleEndian.PutUint16(out[2*i:], uint16(int16(v)))
		}
		mohPCM = out
	})
	return mohPCM
}

// Mute stops (or resumes) forwarding the audio that arrives on leg to its
// peer. The leg is still read, counted and tapped; its packets just do not
// reach the other side. A hold mutes the holder's leg while the music plays
// to the held party, so the holder's own stream does not mix with it.
func (r *Relay) Mute(legName string, on bool) {
	if l := r.findLeg(legName); l != nil {
		l.muted.Store(on)
	}
}

// PlayStream plays tracks into one leg as one continuous RTP stream (a
// fresh SSRC with its own sequence and timestamp space) until ctx ends or
// the relay closes. next returns the next track's PCM (16-bit mono, 8 kHz);
// it is called once per track, so a playlist can loop or shuffle, and an
// empty result ends the stream. PlayStream returns ctx.Err() when
// cancelled.
func (r *Relay) PlayStream(ctx context.Context, legName string, next func() []byte) error {
	l := r.findLeg(legName)
	if l == nil {
		return errors.New("media: unknown relay leg " + legName)
	}
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Errorf("media: music stream: %w", err)
	}
	seq := binary.BigEndian.Uint16(b[0:2])
	ts := binary.BigEndian.Uint32(b[2:6])
	ssrc := binary.BigEndian.Uint32(b[6:10])
	ticker := time.NewTicker(frameDur)
	defer ticker.Stop()
	first := true
	var enc legEncoder
	defer enc.close()
	for {
		pcm := next()
		if len(pcm) < frameBytes {
			return nil
		}
		for idx := 0; (idx+1)*frameBytes <= len(pcm); idx++ {
			// The codec is resolved per frame: a mid-call re-INVITE can
			// change it, and the next frame follows.
			frame, codec, err := enc.frame(l, pcm, idx)
			if err != nil {
				return err
			}
			pkt := BuildRTP(codec.PT, seq, ts, ssrc, first, frame)
			first = false
			seq++
			ts += codec.TimestampStep()
			l.remoteMu.Lock()
			dst := l.remote
			l.remoteMu.Unlock()
			if dst != nil {
				_, _ = l.conn.WriteToUDP(pkt, dst)
			}
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return ctx.Err()
			case <-r.ctx.Done():
				return errors.New("media: relay closed")
			}
		}
	}
}

// legEncoder encodes 8 kHz track audio in whatever codec a leg currently
// speaks: G.711, or G.722 / Opus with the track resampled up. It keeps the
// stateful encoder and the resampled copy of the current track between
// frames, and rebuilds them when the leg's codec or the track changes.
type legEncoder struct {
	codec Codec
	enc   *Encoder
	src   []byte // the 8 kHz track the cached copy was made from
	pcm   []byte // src at the codec's sample rate
}

func (e *legEncoder) close() {
	if e.enc != nil {
		e.enc.Close()
		e.enc = nil
	}
}

// frame returns the encoded idx-th 20 ms frame of the 8 kHz track pcm and
// the codec it is encoded in.
func (e *legEncoder) frame(l *relayLeg, pcm []byte, idx int) ([]byte, Codec, error) {
	codec := l.speaks()
	if e.enc == nil || e.codec.PT != codec.PT || e.codec.Kind() != codec.Kind() {
		e.close()
		enc, err := NewEncoder(codec)
		if err != nil {
			return nil, codec, err
		}
		e.enc, e.codec, e.src = enc, codec, nil
	}
	if len(e.src) != len(pcm) || len(pcm) == 0 || &e.src[0] != &pcm[0] {
		out := pcm
		if rate := codec.SampleRate(); rate != sampleRateHz {
			var err error
			if out, err = Resample(pcm, sampleRateHz, rate); err != nil {
				return nil, codec, fmt.Errorf("media: music resample: %w", err)
			}
		}
		e.src, e.pcm = pcm, out
	}
	n := 2 * codec.FrameSamples()
	chunk := make([]byte, n) // silence-padded when the resampled track runs short
	if lo := idx * n; lo < len(e.pcm) {
		copy(chunk, e.pcm[lo:min(lo+n, len(e.pcm))])
	}
	return e.enc.Encode(chunk), codec, nil
}
