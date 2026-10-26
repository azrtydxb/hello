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

// Track is one piece of music on hold, able to deliver itself at the playback
// rate of whatever codec a leg speaks: 8 kHz for G.711, 16 kHz for G.722,
// 48 kHz for Opus (see Codec.PlaybackRate). A track stored at 48 kHz therefore reaches an Opus leg
// untouched and a G.711 leg through one low-pass filtered downsample, never
// through an 8 kHz detour.
type Track interface {
	// PCMAt returns the track as 16-bit mono little-endian PCM at rate. The
	// slice may be shared between calls and must not be modified.
	PCMAt(rate int) ([]byte, error)
}

// PCMTrack is a Track over PCM held in memory. It resamples to a requested
// rate once and keeps the result, so every stream playing the same track
// shares one copy per rate.
type PCMTrack struct {
	src  []byte
	rate int
	mu   sync.Mutex
	conv map[int][]byte
}

// NewPCMTrack wraps 16-bit mono PCM sampled at rate.
func NewPCMTrack(pcm []byte, rate int) *PCMTrack {
	return &PCMTrack{src: pcm, rate: rate}
}

// PCMAt implements Track.
func (t *PCMTrack) PCMAt(rate int) ([]byte, error) {
	if rate == t.rate {
		return t.src, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if out, ok := t.conv[rate]; ok {
		return out, nil
	}
	out, err := Resample(t.src, t.rate, rate)
	if err != nil {
		return nil, err
	}
	if t.conv == nil {
		t.conv = map[int][]byte{}
	}
	t.conv[rate] = out
	return out, nil
}

var mohTrackOnce sync.Once
var mohTrack *PCMTrack

// DefaultMOHTrack is DefaultMOHPCM as a shared Track.
func DefaultMOHTrack() Track {
	mohTrackOnce.Do(func() { mohTrack = NewPCMTrack(DefaultMOHPCM(), sampleRateHz) })
	return mohTrack
}

// PlayStream plays tracks into one leg as one continuous RTP stream (a
// fresh SSRC with its own sequence and timestamp space) until ctx ends or
// the relay closes. next returns the next track; it is called once per
// track, so a playlist can loop or shuffle, and a nil or empty result ends
// the stream. Each track is delivered at the sample rate of the leg's
// current codec (see Track). PlayStream returns ctx.Err() when cancelled.
func (r *Relay) PlayStream(ctx context.Context, legName string, next func() Track) error {
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
		track := next()
		if track == nil {
			return nil
		}
		for idx := 0; ; idx++ {
			// The codec is resolved per frame: a mid-call re-INVITE can
			// change it, and the next frame follows.
			frame, codec, done, err := enc.frame(l, track, idx)
			if err != nil {
				return err
			}
			if done {
				if idx == 0 {
					return nil // a track with no audio ends the stream
				}
				break
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

// legEncoder encodes a track in whatever codec a leg currently speaks. It
// keeps the stateful encoder and the track's PCM at the codec's sample rate
// between frames, and rebuilds them when the leg's codec or the track
// changes.
type legEncoder struct {
	codec Codec
	enc   *Encoder
	track Track  // the track pcm was taken from
	rate  int    // the sample rate pcm is at
	pcm   []byte // track at the codec's sample rate
}

func (e *legEncoder) close() {
	if e.enc != nil {
		e.enc.Close()
		e.enc = nil
	}
}

// frame returns the encoded idx-th 20 ms frame of track and the codec it is
// encoded in. done is true once idx is past the end of the track.
func (e *legEncoder) frame(l *relayLeg, track Track, idx int) (frame []byte, codec Codec, done bool, err error) {
	codec = l.speaks()
	if e.enc == nil || e.codec.PT != codec.PT || e.codec.Kind() != codec.Kind() {
		e.close()
		enc, err := NewPlaybackEncoder(codec)
		if err != nil {
			return nil, codec, false, err
		}
		e.enc, e.codec = enc, codec
	}
	if rate := codec.PlaybackRate(); e.track != track || e.rate != rate {
		pcm, err := track.PCMAt(rate)
		if err != nil {
			return nil, codec, false, fmt.Errorf("media: music resample: %w", err)
		}
		e.track, e.rate, e.pcm = track, rate, pcm
	}
	n := 2 * codec.PlaybackFrameSamples()
	lo := idx * n
	if lo >= len(e.pcm) {
		return nil, codec, true, nil
	}
	chunk := make([]byte, n) // silence-padded when the track ends mid-frame
	copy(chunk, e.pcm[lo:min(lo+n, len(e.pcm))])
	return e.enc.Encode(chunk), codec, false, nil
}
