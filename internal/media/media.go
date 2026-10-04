// Package media is Hello's anchored-media recorder and player for voicemail
// calls. The B2BUA anchors media (terminates RTP in this package) only when
// a call is routed to voicemail; every other call stays direct RTP between
// the end points. Audio is 8 kHz mono 16-bit little-endian PCM throughout;
// on the wire it is G.711 (PCMU or PCMA) in RTP, with DTMF as RFC 2833/4733
// telephone events. Recorded messages are stored as WAV files in MinIO.
package media

import (
	"context"
	"time"
)

// sampleRateHz and the frame constants describe the one audio format the
// whole package speaks. frameSamples at sampleRateHz is a 20 ms RTP frame,
// the packetisation nearly every SIP endpoint offers.
const (
	sampleRateHz = 8000
	frameSamples = 160 // 20 ms of audio
	frameDur     = 20 * time.Millisecond
	frameBytes   = frameSamples * 2 // 16-bit samples
)

// Session is one anchored media session on the B2BUA leg of a voicemail
// call. The B2BUA creates it with Anchor.Answer, plays prompts into it with
// Play and captures the caller's message with Record.
type Session interface {
	// Play plays PCM (16-bit mono, 8 kHz) to the remote party, in real
	// time. It returns once every frame has been sent, or ctx.Err() when
	// ctx is cancelled mid-way.
	Play(ctx context.Context, audio []byte) error

	// Record records PCM (16-bit mono, 8 kHz) from the remote party.
	//
	// It returns the recorded PCM when the remote party presses DTMF '#'
	// (that digit is delivered on dtmf like every other digit), when maxDur
	// has elapsed, or when ctx is cancelled — in the last case it returns
	// whatever has been recorded so far plus ctx.Err(), never nothing. A
	// recording shorter than one second of non-silence is still returned;
	// discarding it is the caller's decision.
	//
	// Every DTMF digit received during the recording is delivered on dtmf,
	// non-blockingly: a caller that stops reading loses digits but never
	// stalls the recording. '*' is not special inside Record; the caller
	// watches dtmf and cancels ctx to retry a message.
	//
	// Record tolerates a remote that sends no media at all (the silence is
	// recorded as PCM of zeros), out-of-order or lost RTP (sequence gaps
	// are accepted, nothing panics) and a remote that renegotiates nothing
	// mid-call.
	Record(ctx context.Context, maxDur time.Duration, dtmf chan<- byte) ([]byte, error)

	// Close stops the session's receive loop and closes its UDP socket. It
	// is safe to call more than once.
	Close() error
}
