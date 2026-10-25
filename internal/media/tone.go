package media

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"time"
)

// PageTonePCM returns one cycle of the tone a page caller hears while no
// member has answered yet: two 200 ms beeps of 880 Hz 150 ms apart, then a
// second of silence (16-bit mono at 8 kHz, 10 ms fades so the edges do not
// click). It differs from the voicemail beep (425 Hz, one long beep) so the
// two are not confused. Like BeepPCM it is computed, not stored.
func PageTonePCM() []byte { return PageTonePCMRate(sampleRateHz) }

// PageTonePCMRate is PageTonePCM at another sample rate.
func PageTonePCMRate(rate int) []byte {
	const (
		frequency = 880.0
		beepMs    = 200
		gapMs     = 150
		restMs    = 1000
		fadeMs    = 10
		amplitude = 10000
	)
	ms := func(n int) int { return n * rate / 1000 }
	beep, gap, rest, fade := ms(beepMs), ms(gapMs), ms(restMs), ms(fadeMs)
	out := make([]byte, 2*(2*beep+gap+rest))
	for b := range 2 {
		start := b * (beep + gap)
		for i := range beep {
			v := amplitude * math.Sin(2*math.Pi*frequency*float64(i)/float64(rate))
			if i < fade {
				v *= float64(i) / float64(fade)
			}
			if tail := beep - 1 - i; tail < fade {
				v *= float64(tail) / float64(fade)
			}
			//nolint:gosec // G115: the byte pattern of the sample, not a bounded value.
			binary.LittleEndian.PutUint16(out[2*(start+i):], uint16(int16(v)))
		}
	}
	return out
}

// streamPCM sends pcm (at c's sample rate) as an RTP stream of its own
// (fresh SSRC, own sequence and timestamp space) at real time through send,
// once or, with loop, until ctx ends. It returns ctx.Err() when cancelled
// and nil when a single pass finished.
func streamPCM(ctx context.Context, pcm []byte, c Codec, loop bool, send func(pkt []byte)) error {
	frame := 2 * c.FrameSamples()
	if len(pcm) < frame {
		return nil
	}
	enc, err := NewEncoder(c)
	if err != nil {
		return err
	}
	defer enc.Close()
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Errorf("media: tone stream: %w", err)
	}
	seq := binary.BigEndian.Uint16(b[0:2])
	ts := binary.BigEndian.Uint32(b[2:6])
	ssrc := binary.BigEndian.Uint32(b[6:10])
	ticker := time.NewTicker(frameDur)
	defer ticker.Stop()
	first := true
	for {
		for off := 0; off+frame <= len(pcm); off += frame {
			send(BuildRTP(c.PT, seq, ts, ssrc, first, enc.Encode(pcm[off:off+frame])))
			first = false
			seq++
			ts += c.TimestampStep()
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if !loop {
			return nil
		}
	}
}

// PlayCaller streams pcm to the caller from the caller-side socket, so the
// tone comes from the address the caller's SDP names. It aims at the
// address the caller's own RTP arrived from once that is known (NAT), and
// at dst until then. With loop it runs until ctx ends.
func (f *Fanout) PlayCaller(ctx context.Context, pcm []byte, dst *net.UDPAddr, loop bool) error {
	return streamPCM(ctx, f.tonePCM(pcm), f.codec, loop, func(pkt []byte) {
		to := dst
		if a := f.callerAddr.Load(); a != nil {
			to = a
		}
		if to != nil {
			_, _ = f.caller.WriteToUDP(pkt, to)
		}
	})
}

// PlayMembers streams pcm once to every current member from the member-side
// socket (the test page's tone). It returns when the pass is over or ctx
// ends.
func (f *Fanout) PlayMembers(ctx context.Context, pcm []byte) error {
	return streamPCM(ctx, f.tonePCM(pcm), f.codec, false, func(pkt []byte) {
		f.mu.Lock()
		targets := make([]*net.UDPAddr, 0, len(f.targets))
		for _, a := range f.targets {
			targets = append(targets, a)
		}
		f.mu.Unlock()
		for _, a := range targets {
			_, _ = f.member.WriteToUDP(pkt, a)
		}
	})
}
