package media

import (
	"bytes"
	"testing"
)

func TestRTPRoundTrip(t *testing.T) {
	payload := []byte{1, 2, 3, 4, 5}
	wire := BuildRTP(0, 0xBEEF, 0xDEADBEEF, 0x11223344, true, payload)
	pkt, ok := ParseRTP(wire)
	if !ok {
		t.Fatal("parse failed")
	}
	if pkt.PayloadType != 0 || !pkt.Marker {
		t.Errorf("payload type %d marker %v, want 0 true", pkt.PayloadType, pkt.Marker)
	}
	if pkt.Sequence != 0xBEEF || pkt.Timestamp != 0xDEADBEEF || pkt.SSRC != 0x11223344 {
		t.Errorf("seq %#x ts %#x ssrc %#x", pkt.Sequence, pkt.Timestamp, pkt.SSRC)
	}
	if !bytes.Equal(pkt.Payload, payload) {
		t.Errorf("payload %v, want %v", pkt.Payload, payload)
	}
	// Version 2 in the first two bits, as every receiver checks.
	if wire[0]>>6 != 2 {
		t.Errorf("version byte %#x, want version 2", wire[0])
	}
	// Sequence wraps at 16 bits, timestamps keep counting past it.
	wire2 := BuildRTP(0, 0xFFFF, 160, 1, false, payload)
	wire3 := BuildRTP(0, 0x0000, 320, 1, false, payload)
	p2, _ := ParseRTP(wire2)
	p3, _ := ParseRTP(wire3)
	if p2.Sequence != 0xFFFF || p3.Sequence != 0 {
		t.Errorf("wrap: %#x then %#x", p2.Sequence, p3.Sequence)
	}
	if !seqWithin(p3.Sequence, p2.Sequence, dtmfEndWindow) {
		// 0x0000 is one packet after 0xFFFF: it wrapped, it is near.
		t.Error("a sequence just past the wrap must count as within the window")
	}
	if seqWithin(p2.Sequence, p3.Sequence, dtmfEndWindow) {
		t.Error("a sequence before the wrap must not count as after it")
	}
	if !seqWithin(0x0005, 0xFFFF-26, dtmfEndWindow) {
		t.Error("a wrapped sequence inside the window must count as within")
	}
}

func TestRTPRejectsMalformed(t *testing.T) {
	good := BuildRTP(0, 1, 2, 3, false, []byte{9})
	tests := []struct {
		name string
		data []byte
	}{
		{"nil", nil},
		{"short header", good[:11]},
		{"wrong version 1", func() []byte { b := append([]byte(nil), good...); b[0] = 1 << 6; return b }()},
		{"wrong version 3", func() []byte { b := append([]byte(nil), good...); b[0] = 3 << 6; return b }()},
		{"declared csrc beyond end", func() []byte { b := append([]byte(nil), good...); b[0] |= 0x0F; return b }()},
		{"extension beyond end", func() []byte {
			b := append([]byte(nil), good...)
			b[0] |= 0x10
			return b
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if pkt, ok := ParseRTP(tt.data); ok {
				t.Fatalf("parsed %+v, want rejection", pkt)
			}
		})
	}
	// A declared extension that fits is parsed past, not rejected. The
	// extension (profile, one 32-bit word) sits between the header and the
	// payload, as RFC 3550 lays it out.
	ext := BuildRTP(0, 1, 2, 3, false, nil)
	ext[0] |= 0x10
	ext = append(ext, 0, 1, 0, 1, 0xAB, 0xCD, 0, 0, 9)
	pkt, ok := ParseRTP(ext)
	if !ok {
		t.Fatal("a fitting extension must parse")
	}
	if !bytes.Equal(pkt.Payload, []byte{9}) {
		t.Errorf("payload %v, want [9]", pkt.Payload)
	}
}

func TestParseTelephoneEvent(t *testing.T) {
	// Short 4-byte form: E flag in bit 7 of byte 1.
	short := []byte{0x01, 0x0A, 0x03, 0xE8}
	ev, ok := ParseTelephoneEvent(short)
	if !ok || ev.Code != 1 || ev.End {
		t.Errorf("short start: %+v ok=%v", ev, ok)
	}
	short[1] |= 0x80
	if ev, ok = ParseTelephoneEvent(short); !ok || !ev.End {
		t.Errorf("short end: %+v ok=%v", ev, ok)
	}
	// Long form: E flag in bit 7 of the last byte.
	long := []byte{0x02, 0x00, 0x03, 0xE8, 0x01, 0x80}
	if ev, ok = ParseTelephoneEvent(long); !ok || ev.Code != 2 || !ev.End {
		t.Errorf("long end: %+v ok=%v", ev, ok)
	}
	long[5] = 0x00
	if ev, ok = ParseTelephoneEvent(long); !ok || ev.End {
		t.Errorf("long start: %+v ok=%v", ev, ok)
	}
	if _, ok := ParseTelephoneEvent([]byte{1, 2, 3}); ok {
		t.Error("a 3-byte payload must not parse as an event")
	}
}

// The filter is the guarantee "each event code once per press"; these
// cases create every stream shape it must survive.
func TestDTMFFilterOncePerPress(t *testing.T) {
	t.Run("marker then end delivers once", func(t *testing.T) {
		var f DTMFFilter
		if code, ok := f.Observe(DTMFEvent{Code: '5', Sequence: 1}, true); !ok || code != '5' {
			t.Fatalf("marker delivery: %q %v", code, ok)
		}
		if _, ok := f.Observe(DTMFEvent{Code: '5', End: true, Sequence: 3}, false); ok {
			t.Error("end for a marker-delivered press must be absorbed")
		}
	})
	t.Run("end without marker delivers, repeats suppressed", func(t *testing.T) {
		var f DTMFFilter
		if code, ok := f.Observe(DTMFEvent{Code: '9', End: true, Sequence: 10}, false); !ok || code != '9' {
			t.Fatalf("end delivery: %q %v", code, ok)
		}
		if _, ok := f.Observe(DTMFEvent{Code: '9', End: true, Sequence: 11}, false); ok {
			t.Error("repeated end inside the window must be suppressed")
		}
		if code, ok := f.Observe(DTMFEvent{Code: '9', End: true, Sequence: 10 + dtmfEndWindow + 1}, false); !ok {
			t.Error("a later press of the same digit must deliver again")
		} else if code != '9' {
			t.Errorf("later press delivered %q", code)
		}
	})
	t.Run("out-of-window duplicates and new presses", func(t *testing.T) {
		var f DTMFFilter
		f.Observe(DTMFEvent{Code: '1', End: true, Sequence: 100}, false)
		if _, ok := f.Observe(DTMFEvent{Code: '2', End: true, Sequence: 101}, false); !ok {
			t.Error("a different code must never be suppressed as a duplicate")
		}
	})
	t.Run("mid packets never deliver", func(t *testing.T) {
		var f DTMFFilter
		if _, ok := f.Observe(DTMFEvent{Code: '3', Sequence: 5}, false); ok {
			t.Error("a mid-press packet must not deliver")
		}
	})
}
