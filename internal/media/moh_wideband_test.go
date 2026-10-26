package media

import (
	"context"
	"math"
	"testing"
	"time"
)

// Music on hold into a G.722 leg is G.722 (160-byte frames, clock step 160),
// and into an Opus leg is Opus on its dynamic payload type.
func TestPlayStreamWidebandLegs(t *testing.T) {
	for _, c := range []struct {
		name  string
		codec Codec
		pt    uint8
		step  uint32
	}{
		{"g711 pcma", Codec{PT: 8, Name: "PCMA", ClockRate: 8000}, 8, 160},
		{"g722", Codec{PT: 9, Name: "G722", ClockRate: 8000}, 9, 160},
		{"opus", Codec{PT: 111, Name: "opus", ClockRate: 48000, Channels: 2}, 111, 960},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.pt == 111 && !OpusAvailable {
				t.Skip("opus not built in")
			}
			r, _, pb := testRelay(t)
			r.SetCodecs("b", []Codec{c.codec}, 101)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			track := NewPCMTrack(DefaultMOHPCM()[:2*8000], 8000) // one second
			go func() {
				_ = r.PlayStream(ctx, "b", func() Track { return track })
			}()
			first := pb.recv(t)
			second := pb.recv(t)
			if first.PayloadType != c.pt || second.PayloadType != c.pt {
				t.Fatalf("music pt %d/%d, want %d", first.PayloadType, second.PayloadType, c.pt)
			}
			if c.pt == 9 && len(first.Payload) != 160 {
				t.Errorf("G.722 frame is %d bytes, want 160", len(first.Payload))
			}
			if d := second.Timestamp - first.Timestamp; d != c.step {
				t.Errorf("timestamp step %d, want %d", d, c.step)
			}
		})
	}
}

// toneAmp is the amplitude of the component at freq in pcm (16-bit mono at
// rate), for a sine of amplitude A it returns about A.
func toneAmp(pcm []byte, freq float64, rate int) float64 {
	n := len(pcm) / 2
	w := 2 * math.Pi * freq / float64(rate)
	var re, im float64
	for i := range n {
		v := float64(int16(uint16(pcm[2*i]) | uint16(pcm[2*i+1])<<8))
		re += v * math.Cos(w*float64(i))
		im += v * math.Sin(w*float64(i))
	}
	return 2 * math.Hypot(re, im) / float64(n)
}

// TestPlayStreamPerCodecRate fails if a leg is fed audio that was squashed
// to a lower rate on the way. A tone only a higher sampling rate can carry
// must reach a G.722 leg (16 kHz, tone at 6 kHz) and an Opus leg (48 kHz,
// tone at 12 kHz when the source has the bandwidth) at close to full level,
// from a 16 kHz and from a 48 kHz source, while a G.711 leg, which cannot
// carry a 6 kHz tone, must get it filtered out rather than aliased down into
// the voice band as a different tone.
func TestPlayStreamPerCodecRate(t *testing.T) {
	const amp = 8000.0
	for _, src := range []int{16000, 48000} {
		for _, c := range []struct {
			name  string
			codec Codec
			wide  bool
		}{
			{"g711 pcma", Codec{PT: 8, Name: "PCMA", ClockRate: 8000}, false},
			{"g722", Codec{PT: 9, Name: "G722", ClockRate: 8000}, true},
			{"opus", Codec{PT: 111, Name: "opus", ClockRate: 48000, Channels: 2}, true},
		} {
			t.Run(c.name+"/"+map[int]string{16000: "from 16k", 48000: "from 48k"}[src], func(t *testing.T) {
				if c.codec.PT == 111 && !OpusAvailable {
					t.Skip("opus not built in")
				}
				tone := 6000.0
				if c.codec.PT == 111 && src == 48000 {
					tone = 12000 // beyond a 16 kHz stream: only a real 48 kHz path has it
				}
				r, _, pb := testRelay(t)
				r.SetCodecs("b", []Codec{c.codec}, 101)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				track := NewPCMTrack(sineAt(tone, src, 2*src, amp), src) // two seconds
				go func() {
					_ = r.PlayStream(ctx, "b", func() Track { return track })
				}()
				// Decode at the rate playback runs at.
				dec := &Decoder{codec: c.codec}
				switch c.codec.Kind() {
				case nameOpus:
					od, err := newOpusDecoder(c.codec.PlaybackRate())
					if err != nil {
						t.Fatal(err)
					}
					dec.opus = od
				case nameG722:
					dec.g722 = NewG722Decoder()
				}
				defer dec.Close()
				var got []byte
				for i := range 40 {
					p := pb.recv(t)
					if i >= 10 { // skip the encoder and filter warm-up
						got = append(got, dec.Decode(p.Payload)...)
					}
				}
				rate := c.codec.PlaybackRate()
				if want := 30 * rate / 50 * 2; len(got) != want {
					t.Fatalf("decoded %d bytes at %d Hz, want %d: frames are not %d samples", len(got), rate, want, rate/50)
				}
				if c.wide {
					if a := toneAmp(got, tone, rate); a < 0.5*amp {
						t.Errorf("%.0f Hz tone arrived at amplitude %.0f of %.0f: wideband content was lost", tone, a, amp)
					}
					return
				}
				if rms := rmsMid(got); rms > 0.02*amp {
					t.Errorf("G.711 leg got rms %.0f from a tone above its band: aliased instead of filtered", rms)
				}
			})
		}
	}
}
