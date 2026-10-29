//go:build !race

package media

import (
	"errors"
	"testing"

	opuslib "github.com/kazzmir/opus-go/opus"
)

// TestOpusDecoderRecoversFromLibraryFailure fails if a decoder whose library
// state went bad (the silk resampler assertion that filled the voicemail
// call's log) keeps failing every later frame instead of being replaced.
func TestOpusDecoderRecoversFromLibraryFailure(t *testing.T) {
	e, err := newOpusEncoder(48000)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]int16, 960)
	for i := range pcm {
		pcm[i] = int16(i * 7 % 3000)
	}
	pkt := e.encode(pcm)
	d, err := newOpusDecoder(48000)
	if err != nil {
		t.Fatal(err)
	}
	real := opusDecodeCall
	defer func() { opusDecodeCall = real }()
	broken := d.(*opusDec).d
	opusDecodeCall = func(dec *opuslib.Decoder, p []byte, out []int16) (int, error) {
		if dec == broken { // the damaged decoder fails every frame, as the incident did
			panic("assertion failed: inLen >= S->Fs_in_kHz")
		}
		return real(dec, p, out)
	}
	if got := d.decode(pkt); got != nil {
		t.Fatalf("the failing frame decoded to %d samples", len(got))
	}
	if got := d.decode(pkt); len(got) != 960 {
		t.Fatalf("frame after the failure decoded to %d samples, want 960", len(got))
	}

	// A run of refused packets replaces the decoder too.
	d2, _ := newOpusDecoder(48000)
	bad := d2.(*opusDec).d
	opusDecodeCall = func(dec *opuslib.Decoder, p []byte, out []int16) (int, error) {
		if dec == bad {
			return 0, errors.New("opus: decode failed")
		}
		return real(dec, p, out)
	}
	for i := 0; i < opusRebuildAfter; i++ {
		d2.decode(pkt)
	}
	if got := d2.decode(pkt); len(got) != 960 {
		t.Fatalf("after %d refused packets decoded %d samples, want 960", opusRebuildAfter, len(got))
	}
}
