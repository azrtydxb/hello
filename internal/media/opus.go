//go:build !race

package media

import (
	"fmt"
	"runtime"

	opuslib "github.com/kazzmir/opus-go/opus"
)

// OpusAvailable reports whether this build can encode and decode Opus.
//
// The codec is github.com/kazzmir/opus-go: libopus transpiled to Go (no
// cgo), so the static binary and the Alpine image build as they are. It is
// built out under the race detector (opus_race.go), whose checkptr
// instrumentation aborts the process on the transpiled code's pointer
// arithmetic.
const OpusAvailable = true

// The library is C turned into Go and is fed by RTP from the network;
// every call into it is fenced so a defect there costs a frame, not the
// SIP node.

// opusStackPin is the stack the Opus library runs on. The transpiled code
// passes addresses of its Go stack variables around as plain integers, which
// the runtime cannot fix up. When the goroutine stack moves mid-call (it
// grows when the call chain gets deep, and a GC shrinks it when under a
// quarter is in use) the library keeps writing to the old copy and aborts
// frames with "assertion failed: inLen >= S->Fs_in_kHz" or "pseudostack
// overflow". The runtime has no switch to forbid moves, so withOpusStack
// grows the stack once, before the library is entered, to well beyond what
// it needs, and holds a frame big enough that the stack is never under a
// quarter used, so it is neither grown nor shrunk during the call.
const opusStackPin = 512 << 10

//go:noinline
func withOpusStack(fn func()) {
	var pin [opusStackPin]byte
	pin[0], pin[len(pin)-1] = 1, 1
	fn()
	if pin[0] != 1 || pin[len(pin)-1] != 1 {
		panic("media: opus stack pin corrupted")
	}
	runtime.KeepAlive(&pin)
}

type opusEnc struct{ e *opuslib.Encoder }

type opusDec struct {
	d    *opuslib.Decoder
	rate int
}

func newOpusEncoder(rate int) (enc opusEncoder, err error) {
	defer func() {
		if r := recover(); r != nil {
			enc, err = nil, fmt.Errorf("media: opus encoder: %v", r)
		}
	}()
	var e *opuslib.Encoder
	withOpusStack(func() { e, err = opuslib.NewEncoder(rate, 1, opuslib.ApplicationVoIP) })
	if err != nil {
		return nil, fmt.Errorf("media: opus encoder: %w", err)
	}
	return &opusEnc{e: e}, nil
}

func newOpusDecoder(rate int) (dec opusDecoder, err error) {
	defer func() {
		if r := recover(); r != nil {
			dec, err = nil, fmt.Errorf("media: opus decoder: %v", r)
		}
	}()
	var d *opuslib.Decoder
	withOpusStack(func() { d, err = opuslib.NewDecoder(rate, 1) })
	if err != nil {
		return nil, fmt.Errorf("media: opus decoder: %w", err)
	}
	return &opusDec{d: d, rate: rate}, nil
}

func (o *opusEnc) encode(pcm []int16) (out []byte) {
	defer func() {
		if r := recover(); r != nil {
			out = nil
		}
	}()
	buf := make([]byte, 1275)
	var n int
	var err error
	withOpusStack(func() { n, err = o.e.Encode(pcm, len(pcm), buf) })
	if err != nil || n <= 0 {
		return nil
	}
	return buf[:n]
}

func (o *opusEnc) close() { withOpusStack(func() { _ = o.e.Close() }) }

func (o *opusDec) decode(payload []byte) (out []int16) {
	defer func() {
		if r := recover(); r != nil {
			out = nil
		}
	}()
	// 120 ms is the longest Opus frame.
	pcm := make([]int16, o.rate*120/1000)
	var n int
	var err error
	withOpusStack(func() { n, err = o.d.Decode(payload, pcm, len(pcm), false) })
	if err != nil || n <= 0 {
		return nil
	}
	return pcm[:n]
}

func (o *opusDec) close() { withOpusStack(func() { _ = o.d.Close() }) }
