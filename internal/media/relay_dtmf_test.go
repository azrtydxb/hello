package media

import (
	"context"
	"errors"
	"testing"
)

// TestRelaySendDTMF fails if SendDTMF does not put the digits on the leg as
// telephone events in order, with the negotiated payload type, one start
// marker per press, a growing duration, a repeated end packet and a timestamp
// that stays fixed within one press and moves between presses; or if it
// invents a payload type for a leg that offered none.
func TestRelaySendDTMF(t *testing.T) {
	r, pa, pb := testRelay(t)
	_ = pa
	r.SetPayloadTypes("b", 0, 96)
	done := make(chan error, 1)
	go func() { done <- r.SendDTMF(context.Background(), "b", "*5#") }()

	type press struct {
		code      byte
		markers   int
		ends      int
		lastDur   uint16
		ts        uint32
		consistTS bool
	}
	var presses []press
	for len(presses) < 3 || presses[2].ends < dtmfEndRepeats {
		pkt := pb.recv(t)
		if pkt.PayloadType != 96 {
			t.Fatalf("payload type %d, want the negotiated telephone-event type 96", pkt.PayloadType)
		}
		if len(pkt.Payload) != 4 {
			t.Fatalf("event payload is %d bytes, want 4", len(pkt.Payload))
		}
		code, end := pkt.Payload[0], pkt.Payload[1]&0x80 != 0
		dur := uint16(pkt.Payload[2])<<8 | uint16(pkt.Payload[3])
		if len(presses) == 0 || presses[len(presses)-1].code != code || pkt.Marker {
			presses = append(presses, press{code: code, ts: pkt.Timestamp, consistTS: true})
		}
		p := &presses[len(presses)-1]
		if pkt.Marker {
			p.markers++
		}
		if pkt.Timestamp != p.ts {
			p.consistTS = false
		}
		if end {
			p.ends++
		} else if dur < p.lastDur {
			t.Fatalf("duration went from %d to %d inside one press", p.lastDur, dur)
		}
		p.lastDur = dur
	}
	if err := <-done; err != nil {
		t.Fatalf("SendDTMF: %v", err)
	}
	want := []byte{10, 5, 11}
	if len(presses) != len(want) {
		t.Fatalf("%d presses, want %d", len(presses), len(want))
	}
	for i, p := range presses {
		if p.code != want[i] || p.markers != 1 || p.ends != dtmfEndRepeats || !p.consistTS {
			t.Errorf("press %d = %+v, want code %d, one marker, %d end packets, one timestamp", i, p, want[i], dtmfEndRepeats)
		}
		if i > 0 && p.ts <= presses[i-1].ts {
			t.Errorf("press %d timestamp %d does not follow %d", i, p.ts, presses[i-1].ts)
		}
	}

	r.SetPayloadTypes("a", 0, 0)
	if err := r.SendDTMF(context.Background(), "a", "5"); !errors.Is(err, ErrNoTelephoneEvent) {
		t.Errorf("a leg with no telephone-event: err = %v, want ErrNoTelephoneEvent", err)
	}
	if err := r.SendDTMF(context.Background(), "b", "5x"); err == nil {
		t.Error("a non-DTMF character was sent")
	}
}
