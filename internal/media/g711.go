package media

import "encoding/binary"

// EncodePCMU encodes 16-bit little-endian PCM to ITU-T G.711 u-law, one
// wire byte per sample. PCMU is RTP payload type 0, the codec most phones
// offer first.
func EncodePCMU(pcm []byte) []byte {
	return encodeSamplewise(pcm, linearToULaw)
}

// DecodePCMU decodes a u-law payload to 16-bit little-endian PCM, two bytes
// per wire byte.
func DecodePCMU(data []byte) []byte {
	return decodeSamplewise(data, ulawToLinear)
}

// EncodePCMA encodes 16-bit little-endian PCM to G.711 A-law, one wire byte
// per sample. PCMA is RTP payload type 8, the European variant.
func EncodePCMA(pcm []byte) []byte {
	return encodeSamplewise(pcm, linearToALaw)
}

// DecodePCMA decodes an A-law payload to 16-bit little-endian PCM.
func DecodePCMA(data []byte) []byte {
	return decodeSamplewise(data, alawToLinear)
}

// encodeSamplewise maps 16-bit little-endian PCM to one G.711 byte per
// sample; a trailing half sample is dropped rather than guessed at.
func encodeSamplewise(pcm []byte, f func(int16) byte) []byte {
	out := make([]byte, len(pcm)/2)
	for i := range out {
		//nolint:gosec // G115: the bytes are raw 16-bit sample bits, not a bounded value.
		out[i] = f(int16(binary.LittleEndian.Uint16(pcm[2*i:])))
	}
	return out
}

// decodeSamplewise maps G.711 bytes back to 16-bit little-endian PCM. A
// payload of arbitrary length decodes fully; the session pads the result so
// 16-bit alignment always holds.
func decodeSamplewise(data []byte, f func(byte) int16) []byte {
	out := make([]byte, 2*len(data))
	for i, b := range data {
		//nolint:gosec // G115: the byte pattern of the sample, not a bounded value.
		binary.LittleEndian.PutUint16(out[2*i:], uint16(f(b)))
	}
	return out
}

// The G.711 companding constants and segment edges below are the classic
// piecewise algorithm: each 0x10-byte segment doubles in width, so a
// runtime search over eight edges is the whole encoder.
const (
	ulawBias = 0x84
	ulawSeg  = 4 // bits of segment in the wire byte
	alawXor  = 0x55
	alawSign = 0x80
)

// segAEnd holds the inclusive upper edge of each A-law segment in the
// 13-bit rescaled domain the A-law encoder works in (16-bit sample >> 3).
var segAEnd = [...]int{0x1F, 0x3F, 0x7F, 0xFF, 0x1FF, 0x3FF, 0x7FF, 0xFFF}

// linearToULaw quantises a 16-bit sample to one u-law wire byte using the
// classic piecewise algorithm (sign, 3-bit exponent, 4-bit mantissa,
// inverted). The 0x84 bias keeps small samples off the segment boundary.
func linearToULaw(pcm int16) byte {
	v := int(pcm)
	sign := byte(0)
	if v < 0 {
		v = -v
		sign = 0x80
	}
	if v > 32635 {
		v = 32635
	}
	v += ulawBias
	exp := byte(7)
	for mask := 0x4000; exp > 0 && v&mask == 0; mask >>= 1 {
		exp--
	}
	//nolint:gosec // G115: masked to 4 mantissa bits below.
	mant := byte(v>>(exp+3)) & 0x0F
	return ^(sign | exp<<ulawSeg | mant)
}

// ulawToLinear expands one u-law wire byte back to a 16-bit sample.
func ulawToLinear(u byte) int16 {
	u = ^u
	t := int16(u&0x0F)<<3 + ulawBias
	t <<= (u & 0x70) >> 4
	if u&0x80 != 0 {
		t = ulawBias - t
	} else {
		t -= ulawBias
	}
	return t
}

// linearToALaw quantises a 16-bit sample to one A-law wire byte. A-law XORs
// every byte with 0x55 so that all-zero payloads cannot occur on the wire,
// which keeps clock recovery alive on media gateways.
func linearToALaw(pcm int16) byte {
	v := int(pcm) >> 3
	var mask byte
	if v >= 0 {
		mask = alawSign | alawXor // 0xD5
	} else {
		mask = alawXor // 0x55
		v = -v - 1
	}
	seg := 0
	for seg < 8 && v > segAEnd[seg] {
		seg++
	}
	if seg == 8 {
		return 0x7F ^ mask
	}
	var aval byte
	if seg < 2 {
		//nolint:gosec // G115: masked to 4 mantissa bits below.
		aval = byte(v>>1) & 0x0F
	} else {
		//nolint:gosec // G115: masked to 4 mantissa bits below.
		aval = byte(v>>uint(seg)) & 0x0F
	}
	aval |= byte(seg) << ulawSeg
	return aval ^ mask
}

// alawToLinear expands one A-law wire byte back to a 16-bit sample. The
// 0x108 offset is the second segment's base in the 13-bit rescaled domain.
func alawToLinear(a byte) int16 {
	a ^= alawXor
	t := int16(a&0x0F) << 4
	seg := byte(a&0x70) >> 4
	switch seg {
	case 0:
		t += 8
	case 1:
		t += 0x108
	default:
		t += 0x108
		t <<= seg - 1
	}
	if a&alawSign != 0 {
		return t
	}
	return -t
}
