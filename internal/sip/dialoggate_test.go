package sip

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestDialogGate: the gate lets one holder per key at a time, keys do not
// wait on each other, and a key's slot is dropped once nobody holds or waits
// for it. It fails if two holders of one key overlap, if another key is
// blocked by a held one, or if slots leak.
func TestDialogGate(t *testing.T) {
	g := newDialogGate()
	a, b := new(int), new(int)

	var in, peak atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			defer g.lock(a)()
			n := in.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			in.Add(-1)
		})
	}

	unlockA := g.lock(a)
	done := make(chan struct{})
	go func() {
		defer g.lock(b)()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a held gate blocked another dialog's")
	}
	unlockA()
	wg.Wait()
	if p := peak.Load(); p != 1 {
		t.Fatalf("%d holders of one dialog's gate at once, want 1", p)
	}
	if n := g.size(); n != 0 {
		t.Fatalf("%d gate slots left, want 0", n)
	}
	g.lock(nil)() // ungated
}
