//go:build !race

package media

import (
	"runtime"
	"sync"
	"testing"
)

// TestOpusFromFreshGoroutines decodes with every call made from a goroutine
// that has just started (the smallest stack) while the GC runs. The library
// must not depend on the caller's stack: with the old per-call pin, which
// the compiler had moved to the heap, the first frame of such a goroutine
// aborted in SILK.
func TestOpusFromFreshGoroutines(t *testing.T) {
	enc, err := newOpusEncoder(16000)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.close()
	pcm := make([]int16, 320)
	for i := range pcm {
		pcm[i] = int16(i * 37 % 9000)
	}
	pkt := enc.encode(pcm)
	if len(pkt) == 0 {
		t.Fatal("encoder produced nothing")
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	bad := 0
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := newOpusDecoder(16000)
			if err != nil {
				mu.Lock()
				bad++
				mu.Unlock()
				return
			}
			defer d.close()
			for i := 0; i < 5; i++ {
				if i == 2 {
					runtime.GC()
				}
				if len(d.decode(pkt)) != 320 {
					mu.Lock()
					bad++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	if bad != 0 {
		t.Errorf("%d of 320 frames were lost", bad)
	}
}
