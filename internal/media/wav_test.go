package media

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// wavChunk builds one RIFF chunk, including the pad byte after an
// odd-length body, so tests can assemble files no encoder here would
// produce.
func wavChunk(id string, body []byte) []byte {
	out := make([]byte, 8+len(body)+len(body)%2)
	copy(out, id)
	binary.LittleEndian.PutUint32(out[4:], uint32(len(body)))
	copy(out[8:], body)
	return out
}

func TestWAVRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3, 640, 3201} {
		pcm := make([]byte, n)
		for i := range pcm {
			pcm[i] = byte(i * 7)
		}
		wav := EncodeWAV(pcm, sampleRateHz)
		if len(wav) != wavHeaderLen+n {
			t.Fatalf("len %d samples: WAV is %d bytes, want %d", n, len(wav), wavHeaderLen+n)
		}
		got, rate, err := DecodeWAV(wav)
		if err != nil {
			t.Fatalf("len %d samples: decode: %v", n, err)
		}
		if rate != sampleRateHz {
			t.Errorf("len %d samples: rate %d, want %d", n, rate, sampleRateHz)
		}
		if !bytes.Equal(got, pcm) {
			t.Errorf("len %d samples: PCM changed in the round trip", n)
		}
	}
}

func TestEncodeWAVDefaultsRate(t *testing.T) {
	pcm := []byte{1, 2, 3, 4}
	wav := EncodeWAV(pcm, 0)
	_, rate, err := DecodeWAV(wav)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rate != sampleRateHz {
		t.Errorf("rate %d with sampleRate 0, want %d", rate, sampleRateHz)
	}
}

// decodeErr is one hostile shape the decoder must refuse instead of
// returning garbage audio.
func TestDecodeWAVRejectsMalformed(t *testing.T) {
	good := EncodeWAV([]byte{1, 2, 3, 4}, sampleRateHz)
	pcmOnly := func(f func(wav []byte) []byte) []byte { return f(good) }
	truncatedHeader := good[:8]
	badMagic := append([]byte(nil), good...)
	badMagic[0] = 'X'
	fmtChunkThenJunk := func() []byte {
		out := make([]byte, 8)
		copy(out, "RIFF")
		body := wavChunk("fmt ", []byte{
			1, 0, // PCM
			1, 0, // mono
			0x40, 0x1f, 0, 0, // 8000
			0x00, 0x7d, 0, 0, // byte rate 32000
			2, 0, // block align
			16, 0, // bits
		})
		// Odd-length unknown chunk with its pad byte, between fmt and data.
		body = append(body, wavChunk("JUNK", []byte{9, 9, 9})...)
		body = append(body, wavChunk("data", []byte{1, 2, 3, 4})...)
		out = append(out, "WAVE"...)
		binary.LittleEndian.PutUint32(out[4:], uint32(4+len(body)))
		return append(out, body...)
	}()
	declaredShort := pcmOnly(func(wav []byte) []byte {
		// Claim 1000 bytes of data, ship only the 44-byte header.
		binary.LittleEndian.PutUint32(wav[40:], 1000)
		return wav[:wavHeaderLen]
	})
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"truncated header", truncatedHeader},
		{"bad magic", badMagic},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := DecodeWAV(tt.data); err == nil {
				t.Fatal("decode succeeded, want error")
			}
		})
	}
	// A data chunk that declares more than the file holds is handled, not
	// refused: the decoder keeps what is actually there.
	pcm, rate, dsErr := DecodeWAV(declaredShort)
	if dsErr != nil {
		t.Fatalf("declared-short data: %v", dsErr)
	}
	if rate != sampleRateHz || len(pcm) != 0 {
		t.Errorf("declared-short data: got %d Hz, %d bytes of PCM", rate, len(pcm))
	}
	// The junk-chunk file is well formed: the unknown chunk is skipped and
	// the data survives.
	pcm, rate, err := DecodeWAV(fmtChunkThenJunk)
	if err != nil {
		t.Fatalf("unknown chunk: %v", err)
	}
	if rate != sampleRateHz || !bytes.Equal(pcm, []byte{1, 2, 3, 4}) {
		t.Errorf("unknown chunk: got %d Hz, pcm %v", rate, pcm)
	}
}
