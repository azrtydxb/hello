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
	for {
		pcm := next()
		if len(pcm) < frameBytes {
			return nil
		}
		for off := 0; off+frameBytes <= len(pcm); off += frameBytes {
			encode, pt := l.encoder()
			pkt := BuildRTP(pt, seq, ts, ssrc, first, encode(pcm[off:off+frameBytes]))
			first = false
			seq++
			ts += frameSamples
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

// encoder is the leg's outbound audio encoder and payload type: PCMU
// unless the leg negotiated PCMA. Every audio path that originates audio on
// a leg (announcements, music on hold) encodes through it.
func (l *relayLeg) encoder() (func([]byte) []byte, uint8) {
	if l.audioPT == 8 {
		return EncodePCMA, 8
	}
	return EncodePCMU, 0
}
