package api

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/media"
)

// TestAnnouncementLimitHoldsASong fails if the upload limit is back below
// what a five minute 48 kHz stereo 16-bit WAV needs (48000 * 4 bytes * 300 s
// = 57.6 MB), which is the song the console is meant to accept.
func TestAnnouncementLimitHoldsASong(t *testing.T) {
	const song = 48000 * 4 * 300
	if maxAnnouncementBytes < song {
		t.Fatalf("maxAnnouncementBytes = %d, below a 5 minute 48 kHz stereo WAV (%d)", maxAnnouncementBytes, song)
	}
	if maxAnnouncementUpload <= maxAnnouncementBytes {
		t.Fatal("no headroom for the multipart frame")
	}
}

// TestReadUpload fails if readUpload loses bytes, ignores the limit, or
// reallocates a buffer that was sized from the Content-Length hint.
func TestReadUpload(t *testing.T) {
	data := bytes.Repeat([]byte{7}, 100000)
	for _, hint := range []int64{-1, 0, 100100, 5} {
		got, err := readUpload(bytes.NewReader(data), 1<<20, hint)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("hint %d: %d bytes, %v", hint, len(got), err)
		}
	}
	got, err := readUpload(bytes.NewReader(data), 1000, 100100)
	if err != nil || len(got) != 1000 {
		t.Fatalf("limit: %d bytes, %v", len(got), err)
	}
	got, _ = readUpload(bytes.NewReader(data), 1<<20, 100100)
	if cap(got) > 120000 {
		t.Errorf("buffer grew to %d for a 100000 byte upload with a hint", cap(got))
	}
}

// TestCheckAnnouncementAudio fails if the upload check squashes a 48 kHz
// stereo song to 8 kHz, refuses stereo, or answers a compressed WAV without
// saying what to upload instead. It needs no database.
func TestCheckAnnouncementAudio(t *testing.T) {
	stereo := media.EncodeWAV(make([]byte, 4*4800), 48000)
	binary.LittleEndian.PutUint16(stereo[22:], 2)
	binary.LittleEndian.PutUint32(stereo[28:], 48000*4)
	binary.LittleEndian.PutUint16(stereo[32:], 4)
	var f fieldErrs
	out := checkAnnouncementAudio(&f, stereo)
	if len(f) > 0 {
		t.Fatalf("stereo refused: %v", f)
	}
	pcm, rate, err := media.DecodeWAV(out)
	if err != nil || rate != 48000 || len(pcm)/2 != 4800 {
		t.Fatalf("stored rate=%d samples=%d err=%v", rate, len(pcm)/2, err)
	}
	adpcm := media.EncodeWAV(make([]byte, 400), 8000)
	binary.LittleEndian.PutUint16(adpcm[20:], 0x0002)
	f = nil
	checkAnnouncementAudio(&f, adpcm)
	if len(f) == 0 || !strings.Contains(f[0].Message, "uncompressed PCM") {
		t.Fatalf("compressed WAV answer = %v", f)
	}
}
