package media

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// Codec is one audio codec as an SDP rtpmap line describes it.
//
// The numbers to keep apart:
//   - ClockRate is the RTP timestamp clock from the rtpmap line. For G.722
//     it is 8000 although the audio is sampled at 16 kHz (RFC 3551 section
//     4.5.2); for Opus it is 48000.
//   - SampleRate is the rate Hello processes the audio at: 8000 for G.711,
//     16000 for G.722, 16000 for Opus (decoded and encoded at 16 kHz, the
//     wideband rate; the RTP clock stays 48000).
type Codec struct {
	PT        uint8
	Name      string // as written in the rtpmap line ("PCMU", "G722", "opus")
	ClockRate int    // rtpmap clock rate
	Channels  int    // rtpmap channel count; 0 when the line omits it
	Fmtp      string // the a=fmtp parameters, empty when none
}

// Canonical codec names, lower-cased for comparison.
const (
	nameG722 = "g722"
	nameOpus = "opus"
	namePCMU = "pcmu"
	namePCMA = "pcma"
	nameDTMF = "telephone-event"
)

// StaticCodec returns the codec RFC 3551 assigns to a static payload type,
// false for dynamic or unsupported ones.
func StaticCodec(pt uint8) (Codec, bool) {
	switch pt {
	case 0:
		return Codec{PT: 0, Name: "PCMU", ClockRate: 8000}, true
	case 8:
		return Codec{PT: 8, Name: "PCMA", ClockRate: 8000}, true
	case 9:
		return Codec{PT: 9, Name: "G722", ClockRate: 8000}, true
	}
	return Codec{}, false
}

// Kind is the lower-cased codec name.
func (c Codec) Kind() string { return strings.ToLower(c.Name) }

// Supported reports whether Hello can name this codec in an offer or answer:
// G.722, Opus, PCMU and PCMA.
func (c Codec) Supported() bool {
	switch c.Kind() {
	case nameG722, nameOpus, namePCMU, namePCMA:
		return true
	}
	return false
}

// IsG711 reports PCMU or PCMA.
func (c Codec) IsG711() bool { return c.Kind() == namePCMU || c.Kind() == namePCMA }

// Wideband reports a codec Hello handles above 8 kHz (G.722, Opus).
func (c Codec) Wideband() bool { return c.SampleRate() > 8000 }

// SampleRate is the PCM sample rate Hello uses with the codec.
func (c Codec) SampleRate() int {
	switch c.Kind() {
	case nameG722, nameOpus:
		return 16000
	}
	return 8000
}

// FrameSamples is the PCM sample count of a 20 ms frame at SampleRate.
func (c Codec) FrameSamples() int { return c.SampleRate() / 50 }

// TimestampStep is the RTP timestamp advance of one 20 ms frame.
func (c Codec) TimestampStep() uint32 {
	rate := c.ClockRate
	if rate <= 0 {
		rate = 8000
	}
	//nolint:gosec // G115: a clock rate divided by 50 is small and positive.
	return uint32(rate / 50)
}

// Rtpmap is the rtpmap attribute value ("96 opus/48000/2").
func (c Codec) Rtpmap() string {
	s := fmt.Sprintf("%d %s/%d", c.PT, c.Name, c.ClockRate)
	if c.Channels > 1 {
		s += fmt.Sprintf("/%d", c.Channels)
	}
	return s
}

// PCMToInt16 reinterprets 16-bit little-endian PCM bytes as samples. A
// trailing odd byte is dropped.
func PCMToInt16(pcm []byte) []int16 {
	out := make([]int16, len(pcm)/2)
	for i := range out {
		//nolint:gosec // G115: reinterpreting two bytes as a signed sample is the point.
		out[i] = int16(binary.LittleEndian.Uint16(pcm[2*i:]))
	}
	return out
}

// Int16ToPCM is the inverse of PCMToInt16.
func Int16ToPCM(s []int16) []byte {
	out := make([]byte, 2*len(s))
	for i, v := range s {
		//nolint:gosec // G115: the byte pattern of the sample.
		binary.LittleEndian.PutUint16(out[2*i:], uint16(v))
	}
	return out
}

// ErrUnsupportedCodec is returned for a codec Hello cannot encode or decode.
var ErrUnsupportedCodec = errors.New("media: unsupported codec")

// Encoder turns PCM at the codec's SampleRate into RTP payloads, one 20 ms
// frame per call. Stateful codecs (G.722, Opus) keep their state across
// calls, so one Encoder serves one stream. Not safe for concurrent use.
type Encoder struct {
	codec Codec
	g722  *G722Encoder
	opus  opusEncoder
}

// NewEncoder returns an encoder for c.
func NewEncoder(c Codec) (*Encoder, error) {
	e := &Encoder{codec: c}
	switch c.Kind() {
	case namePCMU, namePCMA:
	case nameG722:
		e.g722 = NewG722Encoder()
	case nameOpus:
		o, err := newOpusEncoder(c.SampleRate())
		if err != nil {
			return nil, err
		}
		e.opus = o
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedCodec, c.Name)
	}
	return e, nil
}

// Codec is the codec the encoder produces.
func (e *Encoder) Codec() Codec { return e.codec }

// Encode encodes one frame of 16-bit little-endian PCM at the codec's
// sample rate.
func (e *Encoder) Encode(pcm []byte) []byte {
	switch {
	case e.g722 != nil:
		return e.g722.Encode(PCMToInt16(pcm))
	case e.opus != nil:
		return e.opus.encode(PCMToInt16(pcm))
	case e.codec.Kind() == namePCMA:
		return EncodePCMA(pcm)
	}
	return EncodePCMU(pcm)
}

// Close releases the encoder's resources; it is safe to call twice.
func (e *Encoder) Close() {
	if e.opus != nil {
		e.opus.close()
		e.opus = nil
	}
}

// Decoder turns RTP payloads into PCM at the codec's SampleRate. Not safe
// for concurrent use.
type Decoder struct {
	codec Codec
	g722  *G722Decoder
	opus  opusDecoder
}

// NewDecoder returns a decoder for c.
func NewDecoder(c Codec) (*Decoder, error) {
	d := &Decoder{codec: c}
	switch c.Kind() {
	case namePCMU, namePCMA:
	case nameG722:
		d.g722 = NewG722Decoder()
	case nameOpus:
		o, err := newOpusDecoder(c.SampleRate())
		if err != nil {
			return nil, err
		}
		d.opus = o
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedCodec, c.Name)
	}
	return d, nil
}

// Codec is the codec the decoder consumes.
func (d *Decoder) Codec() Codec { return d.codec }

// Decode decodes one RTP payload to 16-bit little-endian PCM at the codec's
// sample rate. A payload the codec rejects decodes to nothing.
func (d *Decoder) Decode(payload []byte) []byte {
	switch {
	case d.g722 != nil:
		return Int16ToPCM(d.g722.Decode(payload))
	case d.opus != nil:
		return Int16ToPCM(d.opus.decode(payload))
	case d.codec.Kind() == namePCMA:
		return DecodePCMA(payload)
	}
	return DecodePCMU(payload)
}

// Close releases the decoder's resources; it is safe to call twice.
func (d *Decoder) Close() {
	if d.opus != nil {
		d.opus.close()
		d.opus = nil
	}
}

// opusEncoder and opusDecoder are the Opus library behind a seam: the real
// one (opus.go) is a transpiled C codebase that the race detector's pointer
// checks reject, so a -race build gets the stub in opus_race.go and Opus is
// then unavailable (OpusAvailable is false).
type opusEncoder interface {
	encode(pcm []int16) []byte
	close()
}

type opusDecoder interface {
	decode(payload []byte) []int16
	close()
}
