//go:build !race

package media

import (
	"fmt"

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
	e, err := opuslib.NewEncoder(rate, 1, opuslib.ApplicationVoIP)
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
	d, err := opuslib.NewDecoder(rate, 1)
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
	n, err := o.e.Encode(pcm, len(pcm), buf)
	if err != nil || n <= 0 {
		return nil
	}
	return buf[:n]
}

func (o *opusEnc) close() { _ = o.e.Close() }

func (o *opusDec) decode(payload []byte) (out []int16) {
	defer func() {
		if r := recover(); r != nil {
			out = nil
		}
	}()
	// 120 ms is the longest Opus frame.
	pcm := make([]int16, o.rate*120/1000)
	n, err := o.d.Decode(payload, pcm, len(pcm), false)
	if err != nil || n <= 0 {
		return nil
	}
	return pcm[:n]
}

func (o *opusDec) close() { _ = o.d.Close() }
