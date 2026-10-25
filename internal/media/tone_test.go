package media

import (
	"context"
	"testing"
	"time"
)

// TestPageTone: one cycle is two beeps then silence, and the tone reaches
// the caller from the caller socket and the members from the member socket
// as G.711, stopping when its context ends. It fails if the cycle is silent
// or has the wrong length, a recipient hears nothing, or looping ignores
// cancellation.
func TestPageTone(t *testing.T) {
	pcm := PageTonePCM()
	if want := 2 * (2*1600 + 1200 + 8000); len(pcm) != want {
		t.Fatalf("cycle is %d bytes, want %d", len(pcm), want)
	}
	loud := false
	for i := 0; i+1 < 3200; i += 2 {
		if v := int16(uint16(pcm[i]) | uint16(pcm[i+1])<<8); v > 5000 || v < -5000 {
			loud = true
		}
	}
	if !loud {
		t.Fatal("the first beep is silent")
	}

	f, err := NewFanout(31200, 31300, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	caller := startPeer(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.PlayCaller(ctx, pcm, caller.addr(), true) }()
	first := caller.recv(t)
	if first.PayloadType != 0 || len(first.Payload) != frameSamples {
		t.Fatalf("caller got pt %d with %d bytes, want PCMU frames", first.PayloadType, len(first.Payload))
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a looping tone ended without an error after cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the looping tone ignored cancellation")
	}

	m := startPeer(t, nil)
	f.AddTarget("m", m.addr())
	if err := f.PlayMembers(context.Background(), pcm[:frameBytes*3]); err != nil {
		t.Fatal(err)
	}
	if pkt := m.recv(t); pkt.PayloadType != 0 {
		t.Fatalf("member got pt %d", pkt.PayloadType)
	}
}
