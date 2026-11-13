// WebM (Matroska) for recorded VP8 video: an init segment (EBML header, the
// start of an unknown-size Segment, Info and Tracks), one Cluster per fragment,
// and the finalizer's header with a Cues index. Concatenated, init and clusters
// are a valid progressive file; the finalized file adds seeking. Only the
// elements a browser needs are written; nothing here decodes video.
package media

import (
	"io"
)

// ebmlID writes an element id (already in its on-wire form).
func ebmlID(id uint32) []byte {
	switch {
	case id > 0xffffff:
		return []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)} //nolint:gosec // the low byte of each is the point
	case id > 0xffff:
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)} //nolint:gosec // the low byte of each is the point
	case id > 0xff:
		return []byte{byte(id >> 8), byte(id)} //nolint:gosec // the low byte of each is the point
	}
	return []byte{byte(id)}
}

// ebmlSize writes a data size as a vint of the fewest bytes (8 for >= 2^49).
func ebmlSize(n uint64) []byte {
	for l := 1; l <= 8; l++ {
		if n < (1<<(7*uint(l)))-1 {
			b := make([]byte, l)
			v := n | 1<<(7*uint(l))
			for i := l - 1; i >= 0; i-- {
				b[i] = byte(v)
				v >>= 8
			}
			return b
		}
	}
	return []byte{0x01, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
}

// ebmlSize8 is ebmlSize padded to 8 bytes, for sizes patched in later.
func ebmlSize8(n uint64) []byte {
	b := make([]byte, 8)
	b[0] = 0x01
	for i := 7; i >= 1; i-- {
		b[i] = byte(n)
		n >>= 8
	}
	return b
}

func el(id uint32, parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := append(ebmlID(id), ebmlSize(uint64(n))...) //nolint:gosec // a length
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func elUint(id uint32, v uint64) []byte {
	var b []byte
	for x := v; ; x >>= 8 {
		b = append([]byte{byte(x)}, b...)
		if x < 256 {
			break
		}
	}
	return el(id, b)
}

func elStr(id uint32, s string) []byte { return el(id, []byte(s)) }

func elFloat(id uint32, f float64) []byte {
	return el(id, u64(mathFloat64bits(f)))
}

// Matroska element ids.
const (
	idEBML        = 0x1A45DFA3
	idSegment     = 0x18538067
	idSeekHead    = 0x114D9B74
	idSeek        = 0x4DBB
	idSeekID      = 0x53AB
	idSeekPos     = 0x53AC
	idInfo        = 0x1549A966
	idTimeScale   = 0x2AD7B1
	idDuration    = 0x4489
	idMuxing      = 0x4D80
	idWriting     = 0x5741
	idTracks      = 0x1654AE6B
	idTrackEntry  = 0xAE
	idTrackNumber = 0xD7
	idTrackUID    = 0x73C5
	idTrackType   = 0x83
	idCodecID     = 0x86
	idVideo       = 0xE0
	idPixelW      = 0xB0
	idPixelH      = 0xBA
	idCues        = 0x1C53BB6B
	idCuePoint    = 0xBB
	idCueTime     = 0xB3
	idCuePos      = 0xB7
	idCueTrack    = 0xF7
	idCueCluster  = 0xF1
	idCluster     = 0x1F43B675
	idTimecode    = 0xE7
	idSimpleBlock = 0xA3
	idVoid        = 0xEC
)

// unknownSize is the data size of a Segment whose end is not known yet.
var unknownSize = []byte{0x01, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}

func webmInfo(durationMs float64) []byte {
	parts := [][]byte{elUint(idTimeScale, 1000000), elStr(idMuxing, "hello"), elStr(idWriting, "hello")}
	if durationMs > 0 {
		parts = append(parts, elFloat(idDuration, durationMs))
	}
	return el(idInfo, parts...)
}

func webmTracks(width, height int) []byte {
	return el(idTracks, el(idTrackEntry,
		elUint(idTrackNumber, 1), elUint(idTrackUID, 1), elUint(idTrackType, 1), elStr(idCodecID, "V_VP8"),
		el(idVideo, elUint(idPixelW, uint64(width)), elUint(idPixelH, uint64(height))))) //nolint:gosec // positive
}

func webmEBMLHeader() []byte {
	return el(idEBML, elUint(0x4286, 1), elUint(0x42F7, 1), elUint(0x42F2, 4), elUint(0x42F3, 8), elStr(0x4282, "webm"),
		elUint(0x4287, 4), elUint(0x4285, 2))
}

// WebMInit is the init segment: the EBML header and the head of an open Segment.
func WebMInit(width, height int) []byte {
	out := webmEBMLHeader()
	out = append(out, ebmlID(idSegment)...)
	out = append(out, unknownSize...)
	out = append(out, webmInfo(0)...)
	return append(out, webmTracks(width, height)...)
}

// WebMFrame is one VP8 frame of a cluster; At is ms from the cluster's start.
type WebMFrame struct {
	Data []byte
	At   int16
	Key  bool
}

// WebMCluster writes a Cluster starting at startMs.
func WebMCluster(startMs uint64, frames []WebMFrame) []byte {
	parts := [][]byte{elUint(idTimecode, startMs)}
	for _, f := range frames {
		flags := byte(0)
		if f.Key {
			flags = 0x80
		}
		blk := []byte{0x81, byte(uint16(f.At) >> 8), byte(f.At), flags} //nolint:gosec // track 1, the timecode's two bytes, flags
		parts = append(parts, el(idSimpleBlock, blk, f.Data))
	}
	return el(idCluster, parts...)
}

// WebMHeader is what precedes the clusters of the finalized file: the EBML
// header, a Segment whose size covers everything, a SeekHead, Info with the
// duration, Tracks and Cues. clusterBytes is the total size of the clusters.
func WebMHeader(width, height int, durationMs float64, frags []FragInfo) []byte {
	info := webmInfo(durationMs)
	tracks := webmTracks(width, height)
	cuesFor := func(base uint64) []byte {
		var cps [][]byte
		off := base
		for _, f := range frags {
			if f.Key {
				cps = append(cps, el(idCuePoint, elUint(idCueTime, uint64(f.StartMs)), //nolint:gosec // non-negative
					el(0xB7, elUint(idCueTrack, 1), elUint(idCueCluster, off))))
			}
			off += uint64(f.Size) //nolint:gosec // positive
		}
		return el(idCues, cps...)
	}
	// The positions are written four bytes wide, so the SeekHead's size does not
	// depend on them and can be measured before they are known.
	seekHead := func(infoPos, tracksPos, cuesPos uint64) []byte {
		pad := func(id uint32, pos uint64) []byte {
			return el(idSeek, el(idSeekID, ebmlID(id)), el(idSeekPos, []byte{byte(pos >> 24), byte(pos >> 16), byte(pos >> 8), byte(pos)})) //nolint:gosec // four bytes of a position under 4 GiB
		}
		return el(idSeekHead, pad(idInfo, infoPos), pad(idTracks, tracksPos), pad(idCues, cuesPos))
	}
	shLen := uint64(len(seekHead(0, 0, 0)))
	infoPos := shLen
	tracksPos := infoPos + uint64(len(info))
	cuesPos := tracksPos + uint64(len(tracks))
	cuesLen := uint64(len(cuesFor(0)))
	clusterBase := cuesPos + cuesLen // positions are relative to the Segment's data
	cues := cuesFor(clusterBase)
	if uint64(len(cues)) != cuesLen { // positions grew the encoding: measure again
		cuesLen = uint64(len(cues))
		clusterBase = cuesPos + cuesLen
		cues = cuesFor(clusterBase)
	}
	var total uint64
	for _, f := range frags {
		total += uint64(f.Size) //nolint:gosec // positive
	}
	body := append(seekHead(infoPos, tracksPos, cuesPos), info...)
	body = append(body, tracks...)
	body = append(body, cues...)
	segSize := uint64(len(body)) + total
	out := webmEBMLHeader()
	out = append(out, ebmlID(idSegment)...)
	out = append(out, ebmlSize8(segSize)...)
	return append(out, body...)
}

// WriteFinalWebM streams the finalized file.
func WriteFinalWebM(w io.Writer, width, height int, durationMs float64, frags []FragInfo, open func(i int) (io.ReadCloser, error)) error {
	if _, err := w.Write(WebMHeader(width, height, durationMs, frags)); err != nil {
		return err
	}
	return copyFragments(w, frags, open)
}
