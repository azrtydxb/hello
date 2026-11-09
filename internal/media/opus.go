//go:build !race

package media

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

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

// The library keeps addresses of its own Go stack variables as plain
// integers (uintptr), which the runtime cannot fix up. If the goroutine
// stack moves while a call is in flight, the library goes on writing to the
// old copy: silk_decode_frame stores the frame length through such an
// address, the caller reads back 0, and SILK aborts the frame with
// "assertion failed: inLen >= S->Fs_in_kHz" (the line in the voicemail
// call's log). A stack moves when a call chain outgrows it (a goroutine
// starts at a few KB, the library needs tens) and when a GC shrinks it.
//
// The earlier workaround held a 512 KB array in the calling goroutine to
// make the stack big before the call. It never worked: the compiler moves
// any local variable over 128 KB to the heap, so the array pinned nothing
// and each call only allocated 512 KB of garbage. Whether a call survived
// then depended on the stack its caller happened to have (an RTP receive
// loop that a GC had shrunk while it waited in a read, a goroutine just
// started), which is why the failure was intermittent and wanders across
// unrelated PRs.
//
// Every call into the library now runs on one of a few dedicated worker
// goroutines. A worker builds a stack of opusStackPin bytes once, from
// frames the compiler keeps on the stack, and holds them for its whole life:
// the library always has the room it needs, and the held frames keep usage
// above a quarter of the stack so a GC never shrinks it. A call that moves
// the stack anyway is reported as a failed call, which the callers turn into
// a lost frame and a rebuilt decoder.
const opusStackPin = 512 << 10

type opusJob struct {
	fn   func()
	done chan any // receives nil, or the panic value of fn
}

var (
	opusStartWorkers sync.Once
	opusJobs         chan *opusJob
	opusDonePool     = sync.Pool{New: func() any { return make(chan any, 1) }}
)

//go:noinline
func addrOf(p *byte) uintptr { return uintptr(unsafe.Pointer(p)) } //nolint:gosec // an address only compared, never dereferenced

// opusWorker serves jobs until the process ends. Its stack is the library's
// stack: see above.
func opusWorker() { opusPinStack(opusStackPin/opusPinFrame, serveOpus) }

// opusPinFrame is the size of one pad frame of the pinned stack. The
// compiler moves a local variable bigger than 128 KB to the heap, where it
// pins nothing (the earlier withOpusStack held one 512 KB array and did
// exactly that: it cost a 512 KB heap allocation per call and never touched
// the stack), so the pinned stack is built from several smaller frames.
const opusPinFrame = 64 << 10

// opusPinStack calls fn from the bottom of n nested opusPinFrame frames.
// Each frame is kept live and checked, so the compiler can neither drop it
// nor reuse it.
//
//go:noinline
func opusPinStack(n int, fn func()) {
	var pad [opusPinFrame]byte
	mark := byte(n & 0xff)
	pad[0], pad[len(pad)-1] = mark, mark
	if n > 1 {
		opusPinStack(n-1, fn)
	} else {
		fn()
	}
	if pad[0] != mark || pad[len(pad)-1] != mark {
		panic("media: opus stack pin corrupted")
	}
}

// serveOpus runs library calls for opusWorker, on its pinned stack. The
// pin must stay a plain local of the loop below: captured by a closure it
// would be moved to the heap.
//
//go:noinline
func serveOpus() {
	var probe byte
	for j := range opusJobs {
		at := addrOf(&probe)
		res := runOpusJob(j.fn)
		if addrOf(&probe) != at {
			res = errOpusStackMoved
		}
		j.done <- res
	}
}

var errOpusStackMoved = errors.New("media: opus stack moved during a library call")

// runOpusJob calls fn and returns its panic value, or nil.
func runOpusJob(fn func()) (res any) {
	defer func() {
		if r := recover(); r != nil {
			res = r
		}
	}()
	fn()
	return nil
}

// withOpusStack runs fn, a call into the Opus library, on a worker
// goroutine and waits for it. A panic in fn is re-raised in the caller.
func withOpusStack(fn func()) {
	opusStartWorkers.Do(func() {
		opusJobs = make(chan *opusJob)
		for i := 0; i < max(2, runtime.GOMAXPROCS(0)); i++ {
			go opusWorker()
		}
	})
	done := opusDonePool.Get().(chan any)
	opusJobs <- &opusJob{fn: fn, done: done}
	r := <-done
	opusDonePool.Put(done)
	if r != nil {
		panic(r)
	}
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
