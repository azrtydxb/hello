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
	d     *opuslib.Decoder
	rate  int
	fails int // consecutive packets the library refused
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

// opusMusicBitrate is the target bit rate of the Opus encoder that plays
// stored audio: enough for a stereo-quality-sounding mono song up to the
// full band, still well under a G.711 call's 64 kbit/s.
const opusMusicBitrate = 48000

// newOpusMusicEncoder is an encoder for one-way playback of stored audio at
// rate: the audio application (which keeps high frequencies a speech-tuned
// encoder drops) at a fixed music bit rate and a moderate complexity, since
// the encoder is pure Go and runs once per held call.
func newOpusMusicEncoder(rate int) (enc opusEncoder, err error) {
	defer func() {
		if r := recover(); r != nil {
			enc, err = nil, fmt.Errorf("media: opus encoder: %v", r)
		}
	}()
	var e *opuslib.Encoder
	withOpusStack(func() { e, err = opuslib.NewEncoder(rate, 1, opuslib.ApplicationAudio) })
	if err != nil {
		return nil, fmt.Errorf("media: opus encoder: %w", err)
	}
	if err := e.SetBitrate(opusMusicBitrate); err != nil {
		_ = e.Close()
		return nil, fmt.Errorf("media: opus encoder: %w", err)
	}
	_ = e.SetComplexity(5) // a hint: keep the library default when refused
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

// opusDecodeCall is the library call behind decode; tests replace it.
var opusDecodeCall = func(d *opuslib.Decoder, payload []byte, pcm []int16) (int, error) {
	return d.Decode(payload, pcm, len(pcm), false)
}

func (o *opusDec) decode(payload []byte) (out []int16) {
	panicked := true
	defer func() {
		if r := recover(); r != nil {
			out = nil
		}
		switch {
		case panicked:
			o.fails = 0
			o.rebuild()
		case out == nil && len(payload) > 0:
			// A packet the library refused: one stray packet is the
			// network's, a run of them is a decoder that has gone bad.
			if o.fails++; o.fails >= opusRebuildAfter {
				o.fails = 0
				o.rebuild()
			}
		case out != nil:
			o.fails = 0
		}
	}()
	// 120 ms is the longest Opus frame.
	pcm := make([]int16, o.rate*120/1000)
	var n int
	var err error
	withOpusStack(func() { n, err = opusDecodeCall(o.d, payload, pcm) })
	panicked = false
	if err != nil || n <= 0 {
		return nil
	}
	return pcm[:n]
}

// opusRebuildAfter is how many packets in a row the decoder may refuse
// before it is replaced.
const opusRebuildAfter = 5

// rebuild replaces the decoder after a library failure. The library keeps
// state on a Go stack the runtime may have moved (see opusStackPin); once
// that happens every later frame fails the same assertion, so a call whose
// decoder broke would stay deaf (a voicemail records silence, a bridged call
// is mute one way). A fresh decoder loses only the codec's history.
func (o *opusDec) rebuild() {
	var d *opuslib.Decoder
	var err error
	func() {
		defer func() { _ = recover() }()
		withOpusStack(func() { d, err = opuslib.NewDecoder(o.rate, 1) })
	}()
	if err != nil || d == nil {
		return
	}
	old := o.d
	o.d = d
	func() {
		defer func() { _ = recover() }()
		withOpusStack(func() { _ = old.Close() })
	}()
}

func (o *opusDec) close() { withOpusStack(func() { _ = o.d.Close() }) }
