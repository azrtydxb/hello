package media

import (
	"runtime"
	"testing"
)

// The Opus library is C transpiled to Go and keeps pointers to Go stack
// variables as plain integers. If the goroutine stack moves in the middle of
// a call (it grows when the call chain gets deep, and a GC shrinks it again)
// the library writes through a stale address, and SILK later aborts with
// "assertion failed: inLen >= S->Fs_in_kHz", costing the frame. Forcing a GC
// before every call makes the shrink-then-grow sequence happen on every
// frame instead of once in tens of thousands.
func TestOpusSurvivesStackMoves(t *testing.T) {
	if !OpusAvailable {
		t.Skip("no Opus codec in this build")
	}
	c := Codec{PT: 111, Name: "opus", ClockRate: 48000, Channels: 2}
	enc, err := NewEncoder(c)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	dec, err := NewDecoder(c)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	pcm := tone16(100*320, 10000, 1000)
	bad := 0
	for off := 0; off+320 <= len(pcm); off += 320 {
		runtime.GC()
		pkt := enc.Encode(Int16ToPCM(pcm[off : off+320]))
		runtime.GC()
		if len(pkt) == 0 {
			bad++
			continue
		}
		if got := len(dec.Decode(pkt)); got != 640 {
			bad++
		}
	}
	if bad != 0 {
		t.Errorf("%d of 100 frames were lost to a stack move inside the Opus library", bad)
	}
}
