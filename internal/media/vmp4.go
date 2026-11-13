// Fragmented MP4 (ISO BMFF, CMAF style) for recorded H.264 video, and the
// finalizer that turns an init segment and its fragments into one progressive
// file with a seek index (sidx). Only the boxes a player needs are written;
// nothing here decodes video.
package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
)

const mp4Timescale = 90000

func box(typ string, parts ...[]byte) []byte {
	n := 8
	for _, p := range parts {
		n += len(p)
	}
	b := make([]byte, 8, n)
	binary.BigEndian.PutUint32(b, uint32(n)) //nolint:gosec // a few MiB at most
	copy(b[4:], typ)
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

func fullBox(typ string, version byte, flags uint32, parts ...[]byte) []byte {
	hdr := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)} //nolint:gosec // the low byte of each is the point
	return box(typ, append([][]byte{hdr}, parts...)...)
}

func u16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func u32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func u64(v uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }

// MP4Init is the init segment of an H.264 track: ftyp and moov with the avcC
// built from the stream's SPS and PPS.
func MP4Init(sps, pps []byte, width, height int) ([]byte, error) {
	if len(sps) < 4 || len(pps) < 1 {
		return nil, errors.New("media: mp4 init needs an SPS and a PPS")
	}
	if width <= 0 || height <= 0 {
		return nil, errors.New("media: mp4 init needs the picture size")
	}
	ftyp := box("ftyp", []byte("isom"), u32(512), []byte("isomiso5iso6avc1mp41"))
	avcC := box("avcC", []byte{1, sps[1], sps[2], sps[3], 0xff, 0xe1}, u16(uint16(len(sps))), sps, []byte{1}, u16(uint16(len(pps))), pps) //nolint:gosec // SPS and PPS are tiny
	avc1 := box("avc1",
		make([]byte, 6), u16(1), // reserved, data reference index
		make([]byte, 16),                        // pre-defined, reserved
		u16(uint16(width)), u16(uint16(height)), //nolint:gosec // bounded by the SPS
		u32(0x00480000), u32(0x00480000), // 72 dpi
		u32(0), u16(1), make([]byte, 32), u16(0x18), u16(0xffff), avcC)
	stsd := fullBox("stsd", 0, 0, u32(1), avc1)
	empty := func(typ string) []byte { return fullBox(typ, 0, 0, u32(0)) }
	stbl := box("stbl", stsd, empty("stts"), fullBox("stsc", 0, 0, u32(0)), fullBox("stsz", 0, 0, u32(0), u32(0)), empty("stco"))
	dinf := box("dinf", fullBox("dref", 0, 0, u32(1), fullBox("url ", 0, 1)))
	minf := box("minf", fullBox("vmhd", 0, 1, make([]byte, 8)), dinf, stbl)
	mdhd := fullBox("mdhd", 0, 0, u32(0), u32(0), u32(mp4Timescale), u32(0), u16(0x55c4), u16(0))
	hdlr := fullBox("hdlr", 0, 0, u32(0), []byte("vide"), make([]byte, 12), []byte("VideoHandler\x00"))
	mdia := box("mdia", mdhd, hdlr, minf)
	matrix := []byte{0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x40, 0, 0, 0}
	tkhd := fullBox("tkhd", 0, 3, u32(0), u32(0), u32(1), u32(0), u32(0), make([]byte, 8), u16(0), u16(0), u16(0), u16(0), matrix,
		u32(uint32(width)<<16), u32(uint32(height)<<16)) //nolint:gosec // bounded by the SPS
	trak := box("trak", tkhd, mdia)
	mvhd := fullBox("mvhd", 0, 0, u32(0), u32(0), u32(mp4Timescale), u32(0), u32(0x00010000), u16(0x0100), u16(0), make([]byte, 8), matrix,
		make([]byte, 24), u32(2))
	trex := fullBox("trex", 0, 0, u32(1), u32(1), u32(0), u32(0), u32(0))
	moov := box("moov", mvhd, trak, box("mvex", trex))
	return append(ftyp, moov...), nil
}

// MP4Sample is one coded picture of a fragment.
type MP4Sample struct {
	Data     []byte
	Duration uint32 // ticks of the 90 kHz clock
	Key      bool
}

// MP4Fragment writes one movie fragment (moof and mdat) of seq (from 1) whose
// first sample decodes at base ticks.
func MP4Fragment(seq uint32, base uint64, samples []MP4Sample) []byte {
	var data []byte
	trun := &bytes.Buffer{}
	trun.Write(u32(uint32(len(samples)))) //nolint:gosec // a fragment holds a few hundred samples
	trun.Write(u32(0))                    // data offset, patched below
	for _, s := range samples {
		trun.Write(u32(s.Duration))
		trun.Write(u32(uint32(len(s.Data)))) //nolint:gosec // one picture
		if s.Key {
			trun.Write(u32(0x02000000))
		} else {
			trun.Write(u32(0x01010000))
		}
		data = append(data, s.Data...)
	}
	// flags: data-offset-present, sample-duration, sample-size, sample-flags
	trunBox := fullBox("trun", 0, 0x000701, trun.Bytes())
	tfhd := fullBox("tfhd", 0, 0x020000, u32(1)) // default-base-is-moof
	tfdt := fullBox("tfdt", 1, 0, u64(base))
	traf := box("traf", tfhd, tfdt, trunBox)
	mfhd := fullBox("mfhd", 0, 0, u32(seq))
	moof := box("moof", mfhd, traf)
	// The data offset is from the start of the moof to the first sample byte.
	off := uint32(len(moof) + 8) //nolint:gosec // small
	i := bytes.Index(moof, []byte("trun"))
	binary.BigEndian.PutUint32(moof[i+4+4+4:], off) // after "trun", version/flags and the sample count
	return append(moof, box("mdat", data)...)
}

// FragInfo is what the finalizer needs to know about a fragment.
type FragInfo struct {
	Size  int64
	Ticks uint64 // duration in the stream's clock (90 kHz for MP4)
	Key   bool   // starts with a keyframe
	// StartMs is the first frame's time from the stream's start (WebM Cues).
	StartMs int64
}

// MP4Header builds what precedes the fragments of the finalized file: the init
// segment and a sidx with one reference per fragment, so a player can seek.
func MP4Header(init []byte, frags []FragInfo) []byte {
	b := &bytes.Buffer{}
	b.Write(u32(1))                  // reference_ID
	b.Write(u32(mp4Timescale))       // timescale
	b.Write(u64(0))                  // earliest presentation time
	b.Write(u64(0))                  // first offset
	b.Write(u16(0))                  // reserved
	b.Write(u16(uint16(len(frags)))) //nolint:gosec // bounded by the segment count
	for _, f := range frags {
		b.Write(u32(uint32(f.Size)))  //nolint:gosec // a fragment is a few MiB; the type bit is 0 (media)
		b.Write(u32(uint32(f.Ticks))) //nolint:gosec // a fragment is seconds long
		sap := uint32(0)
		if f.Key {
			sap = 0x90000000 // starts_with_SAP, SAP type 1
		}
		b.Write(u32(sap))
	}
	return append(append([]byte(nil), init...), fullBox("sidx", 1, 0, b.Bytes())...)
}

// WriteFinalMP4 streams the finalized file: header then every fragment through
// open(i). The caller has checked the sizes in frags against the objects.
func WriteFinalMP4(w io.Writer, init []byte, frags []FragInfo, open func(i int) (io.ReadCloser, error)) error {
	if _, err := w.Write(MP4Header(init, frags)); err != nil {
		return err
	}
	return copyFragments(w, frags, open)
}

func copyFragments(w io.Writer, frags []FragInfo, open func(i int) (io.ReadCloser, error)) error {
	for i, f := range frags {
		rc, err := open(i)
		if err != nil {
			return err
		}
		n, err := io.Copy(w, rc)
		_ = rc.Close()
		if err != nil {
			return err
		}
		if n != f.Size {
			return errors.New("media: a fragment is not the size the manifest says")
		}
	}
	return nil
}

// MP4HeaderSize is the size of MP4Header's result without building it.
func MP4HeaderSize(init []byte, nFrags int) int64 {
	return int64(len(init)) + 8 + 4 + 4 + 4 + 8 + 8 + 4 + int64(nFrags)*12
}
