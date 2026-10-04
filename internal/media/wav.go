package media

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// wavHeaderLen is the canonical 44-byte header: RIFF descriptor, one fmt
// chunk (16 bytes of PCM format) and the data chunk header.
const wavHeaderLen = 44

// EncodeWAV wraps 16-bit mono PCM in a canonical 44-byte RIFF/WAVE header,
// the shape every player and the control plane's audio endpoint serve. A
// non-positive sampleRate is recorded as the package's canonical 8 kHz
// rather than a header no player would accept.
func EncodeWAV(pcm []byte, sampleRate int) []byte {
	if sampleRate <= 0 {
		sampleRate = sampleRateHz
	}
	out := make([]byte, wavHeaderLen+len(pcm))
	le := binary.LittleEndian
	copy(out, "RIFF")
	// RIFF size: everything after the first 8 bytes. The field is uint32 by
	// definition; a recording would have to outgrow 4 GiB to wrap it.
	//nolint:gosec // G115: RIFF length fields are uint32 by format definition.
	le.PutUint32(out[4:], uint32(wavHeaderLen-8+len(pcm)))
	copy(out[8:], "WAVE")
	copy(out[12:], "fmt ")
	le.PutUint32(out[16:], 16)                 // fmt body length
	le.PutUint16(out[20:], 1)                  // WAVE_FORMAT_PCM
	le.PutUint16(out[22:], 1)                  // mono
	le.PutUint32(out[24:], uint32(sampleRate)) // samples per second
	// byte rate = sampleRate * channels * bytes per sample.
	le.PutUint32(out[28:], uint32(sampleRate)*2)
	le.PutUint16(out[32:], 2)  // block align: one 16-bit sample
	le.PutUint16(out[34:], 16) // bits per sample
	copy(out[36:], "data")
	//nolint:gosec // G115: RIFF length fields are uint32 by format definition.
	le.PutUint32(out[40:], uint32(len(pcm)))
	copy(out[wavHeaderLen:], pcm)
	return out
}

// DecodeWAV parses a RIFF/WAVE file and returns the PCM of its data chunk
// and the sample rate from its fmt chunk. It is deliberately forgiving about
// what comes between the chunks: real recorders emit LIST, fact and
// vendor-specific chunks, and a player must not refuse such a file. It is
// strict about the things that would produce garbage audio: the file must
// actually be RIFF/WAVE, the codec must be uncompressed PCM, and the layout
// must be 16-bit mono.
func DecodeWAV(data []byte) (pcm []byte, sampleRate int, err error) {
	if len(data) == 0 {
		return nil, 0, errors.New("media: empty WAV")
	}
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, errors.New("media: not a RIFF/WAVE file")
	}
	le := binary.LittleEndian
	var (
		pcmOut  []byte
		rate    int
		sawFmt  bool
		sawData bool
	)
	off := 12
	for off+8 <= len(data) {
		id := string(data[off : off+4])
		size := int(le.Uint32(data[off+4:]))
		body := off + 8
		switch id {
		case "fmt ":
			// The fmt body we need is 16 bytes; a declared size beyond the
			// end of the file is a truncated file, not a codec we skip.
			if size < 16 || body+16 > len(data) {
				return nil, 0, errors.New("media: truncated fmt chunk")
			}
			format := le.Uint16(data[body:])
			if format != 1 {
				return nil, 0, fmt.Errorf("media: unsupported WAV codec 0x%04x (only uncompressed PCM)", format)
			}
			channels := le.Uint16(data[body+2:])
			bits := le.Uint16(data[body+14:])
			if channels != 1 || bits != 16 {
				return nil, 0, fmt.Errorf("media: unsupported WAV layout (channels=%d, bits=%d; only 16-bit mono)", channels, bits)
			}
			rate = int(le.Uint32(data[body+4:]))
			sawFmt = true
		case "data":
			// A data chunk that declares more than the file holds is
			// truncated audio: keep what is there rather than failing.
			n := size
			if n > len(data)-body {
				n = len(data) - body
			}
			pcmOut = data[body : body+n]
			sawData = true
		}
		// Advance past the chunk, honouring the pad byte that follows an
		// odd-length body.
		off = body + size
		if size%2 == 1 {
			off++
		}
	}
	if !sawFmt {
		return nil, 0, errors.New("media: WAV without fmt chunk")
	}
	if !sawData {
		return nil, 0, errors.New("media: WAV without data chunk")
	}
	if rate <= 0 {
		return nil, 0, errors.New("media: WAV with invalid sample rate")
	}
	return pcmOut, rate, nil
}
