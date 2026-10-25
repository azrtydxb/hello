package media

import (
	"context"
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
			track := DefaultMOHPCM()[:2*8000] // one second
			go func() {
				_ = r.PlayStream(ctx, "b", func() []byte { return track })
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
