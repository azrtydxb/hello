package media

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
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
	putWAVHeader(out, len(pcm), sampleRate)
	copy(out[wavHeaderLen:], pcm)
	return out
}

// putWAVHeader writes the canonical 16-bit mono header for dataLen bytes of
// PCM into the first wavHeaderLen bytes of dst.
func putWAVHeader(dst []byte, dataLen, sampleRate int) {
	le := binary.LittleEndian
	copy(dst, "RIFF")
	// RIFF size: everything after the first 8 bytes. The field is uint32 by
	// definition; a recording would have to outgrow 4 GiB to wrap it.
	//nolint:gosec // G115: RIFF length fields are uint32 by format definition.
	le.PutUint32(dst[4:], uint32(wavHeaderLen-8+dataLen))
	copy(dst[8:], "WAVE")
	copy(dst[12:], "fmt ")
	le.PutUint32(dst[16:], 16) // fmt body length
	le.PutUint16(dst[20:], 1)  // WAVE_FORMAT_PCM
	le.PutUint16(dst[22:], 1)  // mono
	//nolint:gosec // G115: a sample rate is positive and far below 2^32.
	le.PutUint32(dst[24:], uint32(sampleRate)) // samples per second
	// byte rate = sampleRate * channels * bytes per sample.
	//nolint:gosec // G115: as above.
	le.PutUint32(dst[28:], uint32(sampleRate)*2)
	le.PutUint16(dst[32:], 2)  // block align: one 16-bit sample
	le.PutUint16(dst[34:], 16) // bits per sample
	copy(dst[36:], "data")
	//nolint:gosec // G115: RIFF length fields are uint32 by format definition.
	le.PutUint32(dst[40:], uint32(dataLen))
}

// maxWAVChannels bounds the channel count a WAV may declare; real files
// have one to eight.
const maxWAVChannels = 32

// decodedWAV is what decodeWAV found.
type decodedWAV struct {
	pcm  []byte // 16-bit mono little-endian PCM
	rate int
	// exact reports the file already was 16-bit mono PCM, so pcm aliases
	// the input.
	exact bool
	// room, when set, is a buffer holding wavHeaderLen spare bytes followed
	// by pcm, so a converted file can be wrapped in a header without
	// another copy of the audio.
	room []byte
}

// DecodeWAV parses a RIFF/WAVE file and returns its audio as 16-bit
// little-endian mono PCM and the sample rate from its fmt chunk. It accepts
// integer PCM at 8, 16, 24 and 32 bits and 32/64-bit float PCM (also in a
// WAVE_FORMAT_EXTENSIBLE wrapper), with any number of channels: multi-channel
// audio is down-mixed to mono by averaging, and samples are reduced to 16
// bits. A 16-bit mono file is returned as a view of data without copying.
// It is deliberately forgiving about what comes between the chunks: real
// recorders emit LIST, fact and vendor-specific chunks, and a player must
// not refuse such a file. It is strict about the things that would produce
// garbage audio: the file must actually be RIFF/WAVE and the codec must be
// uncompressed PCM or float (ADPCM, A-law, mu-law, MP3 are refused).
func DecodeWAV(data []byte) (pcm []byte, sampleRate int, err error) {
	d, err := decodeWAV(data)
	if err != nil {
		return nil, 0, err
	}
	return d.pcm, d.rate, nil
}

func decodeWAV(data []byte) (decodedWAV, error) {
	if len(data) == 0 {
		return decodedWAV{}, errors.New("media: empty WAV")
	}
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return decodedWAV{}, errors.New("media: not a RIFF/WAVE file")
	}
	le := binary.LittleEndian
	var (
		raw     []byte
		f       wavFormat
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
				return decodedWAV{}, errors.New("media: truncated fmt chunk")
			}
			var err error
			if f, err = parseWAVFormat(data[body:min(body+size, len(data))]); err != nil {
				return decodedWAV{}, err
			}
			sawFmt = true
		case "data":
			// A data chunk that declares more than the file holds is
			// truncated audio: keep what is there rather than failing.
			n := size
			if n > len(data)-body {
				n = len(data) - body
			}
			raw = data[body : body+n]
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
		return decodedWAV{}, errors.New("media: WAV without fmt chunk")
	}
	if !sawData {
		return decodedWAV{}, errors.New("media: WAV without data chunk")
	}
	if f.rate <= 0 {
		return decodedWAV{}, errors.New("media: WAV with invalid sample rate")
	}
	if f.bits == 16 && f.channels == 1 && !f.float {
		return decodedWAV{pcm: raw, rate: f.rate, exact: true}, nil
	}
	frameBytes := f.channels * f.bits / 8
	frames := len(raw) / frameBytes
	room := make([]byte, wavHeaderLen+2*frames)
	mixToMono16(room[wavHeaderLen:], raw, f, frames)
	return decodedWAV{pcm: room[wavHeaderLen:], rate: f.rate, room: room}, nil
}

// wavFormat is the part of a fmt chunk the decoder needs.
type wavFormat struct {
	channels, bits, rate int
	float                bool
}

// parseWAVFormat reads a fmt chunk body (at least 16 bytes).
func parseWAVFormat(b []byte) (wavFormat, error) {
	le := binary.LittleEndian
	tag := le.Uint16(b)
	if tag == 0xFFFE && len(b) >= 26 { // WAVE_FORMAT_EXTENSIBLE: the real tag opens the sub-format GUID
		tag = le.Uint16(b[24:])
	}
	f := wavFormat{
		channels: int(le.Uint16(b[2:])),
		rate:     int(le.Uint32(b[4:])),
		bits:     int(le.Uint16(b[14:])),
	}
	switch tag {
	case 1:
		switch f.bits {
		case 8, 16, 24, 32:
		default:
			return f, fmt.Errorf("media: unsupported WAV sample size (%d-bit PCM; 8, 16, 24 and 32-bit are supported)", f.bits)
		}
	case 3:
		f.float = true
		if f.bits != 32 && f.bits != 64 {
			return f, fmt.Errorf("media: unsupported WAV sample size (%d-bit float; 32 and 64-bit are supported)", f.bits)
		}
	default:
		return f, fmt.Errorf("media: unsupported WAV codec 0x%04x (only uncompressed PCM and float PCM)", tag)
	}
	if f.channels < 1 || f.channels > maxWAVChannels {
		return f, fmt.Errorf("media: unsupported WAV channel count %d", f.channels)
	}
	return f, nil
}

// mixToMono16 converts frames interleaved frames of raw to 16-bit mono in
// dst by averaging the channels.
func mixToMono16(dst, raw []byte, f wavFormat, frames int) {
	le := binary.LittleEndian
	step := f.bits / 8
	for i := range frames {
		var sum float64
		base := i * f.channels * step
		for c := range f.channels {
			p := raw[base+c*step:]
			switch {
			case f.float && f.bits == 32:
				sum += float64(math.Float32frombits(le.Uint32(p))) * 32768
			case f.float:
				sum += math.Float64frombits(le.Uint64(p)) * 32768
			case f.bits == 8:
				sum += float64((int(p[0]) - 128) << 8)
			case f.bits == 16:
				sum += float64(int16(le.Uint16(p))) //nolint:gosec // G115: signed sample.
			case f.bits == 24:
				sum += float64((int(p[0])<<8|int(p[1])<<16|int(int8(p[2]))<<24)>>8) / 256 //nolint:gosec // G115: sign-extended 24-bit sample.
			default:
				sum += float64(int32(le.Uint32(p))) / 65536 //nolint:gosec // G115: signed sample.
			}
		}
		v := math.Round(sum / float64(f.channels))
		if math.IsNaN(v) {
			v = 0
		}
		v = math.Max(math.MinInt16, math.Min(math.MaxInt16, v))
		le.PutUint16(dst[2*i:], uint16(int16(v))) //nolint:gosec // G115: v is clamped to the int16 range.
	}
}
