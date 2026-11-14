// Depacketizers for video recording (clients-webrtc-mobile-video S-42): RTP
// payloads of H.264 (RFC 6184: single NAL unit, STAP-A and FU-A) and VP8
// (RFC 7741) become whole frames, without decoding or re-encoding anything. A
// frame is kept as the sender coded it: an H.264 access unit as length-prefixed
// NAL units (what an MP4 sample holds) and a VP8 frame as it is. Packets are
// reordered within a small window by sequence number; a loss that cannot be
// repaired drops the frame in progress and every frame until the next keyframe.
package media

import (
	"encoding/binary"
	"errors"
	"sort"
	"time"
)

// VideoFrame is one coded picture.
type VideoFrame struct {
	// Data is the sample: for H.264 the access unit's NAL units each prefixed
	// by a 4-byte length (SPS and PPS are not in it, they are the codec
	// configuration), for VP8 the frame.
	Data []byte
	// TS is the RTP timestamp (90 kHz) of the picture.
	TS  uint32
	Key bool
	// At is when the frame's last packet arrived.
	At time.Time
}

// vdepay turns packets of one stream into frames.
type vdepay interface {
	// push takes the next in-order packet and returns the frame it completes,
	// if any.
	push(pkt *RTPPacket, now time.Time) (*VideoFrame, error)
	// reset forgets the frame in progress (a loss happened).
	reset()
	// config is the codec configuration seen so far (H.264: SPS and PPS; VP8
	// none), width and height of the picture when known.
	size() (w, h int)
}

var errVideoPayload = errors.New("media: malformed video payload")

// ---- H.264 ----------------------------------------------------------------------

type h264Depay struct {
	sps, pps  []byte
	w, h      int
	ts        uint32
	have      bool
	au        []byte // length-prefixed NAL units of the access unit in progress
	key       bool
	fu        []byte // NAL unit being rebuilt from FU-A fragments
	fuOpen    bool
	spsChange bool
}

func newH264Depay() *h264Depay { return &h264Depay{} }

func (d *h264Depay) reset() {
	d.au, d.key, d.fu, d.fuOpen, d.have = nil, false, nil, false, false
}

func (d *h264Depay) size() (int, int) { return d.w, d.h }

// avcc returns the codec configuration once SPS and PPS are both known.
func (d *h264Depay) avcc() (sps, pps []byte, ok bool) {
	return d.sps, d.pps, d.sps != nil && d.pps != nil
}

func (d *h264Depay) addNAL(n []byte) {
	if len(n) == 0 {
		return
	}
	switch n[0] & 0x1f {
	case 7:
		if string(n) != string(d.sps) {
			d.sps = append([]byte(nil), n...)
			d.w, d.h = parseSPSSize(n)
			d.spsChange = true
		}
		return
	case 8:
		d.pps = append([]byte(nil), n...)
		return
	case 9: // access unit delimiter
		return
	case 5:
		d.key = true
	}
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(n))) //nolint:gosec // a NAL unit is well under 4 GiB
	d.au = append(d.au, l[:]...)
	d.au = append(d.au, n...)
}

func (d *h264Depay) push(pkt *RTPPacket, now time.Time) (*VideoFrame, error) {
	var out *VideoFrame
	if d.have && pkt.Timestamp != d.ts {
		// A new picture began without the marker of the last one (lost): the
		// access unit in progress is complete as far as it goes.
		out = d.finish(now)
	}
	d.ts, d.have = pkt.Timestamp, true
	p := pkt.Payload
	if len(p) < 1 {
		return out, errVideoPayload
	}
	switch t := p[0] & 0x1f; {
	case t >= 1 && t <= 23:
		d.addNAL(p)
	case t == 24: // STAP-A
		rest := p[1:]
		for len(rest) >= 2 {
			n := int(binary.BigEndian.Uint16(rest))
			rest = rest[2:]
			if n > len(rest) {
				return out, errVideoPayload
			}
			d.addNAL(rest[:n])
			rest = rest[n:]
		}
	case t == 28: // FU-A
		if len(p) < 2 {
			return out, errVideoPayload
		}
		start, end := p[1]&0x80 != 0, p[1]&0x40 != 0
		if start {
			d.fu = append(d.fu[:0], p[0]&0xe0|p[1]&0x1f)
			d.fuOpen = true
		}
		if !d.fuOpen {
			return out, errVideoPayload // the start was lost
		}
		d.fu = append(d.fu, p[2:]...)
		if end {
			d.addNAL(d.fu)
			d.fu, d.fuOpen = nil, false
		}
	default:
		// STAP-B, MTAP and FU-B need interleaved mode, which is not used.
		return out, errVideoPayload
	}
	if pkt.Marker {
		if f := d.finish(now); f != nil {
			return f, nil
		}
	}
	return out, nil
}

func (d *h264Depay) finish(now time.Time) *VideoFrame {
	if len(d.au) == 0 || d.fuOpen {
		d.au, d.key, d.fu, d.fuOpen = nil, false, nil, false
		return nil
	}
	f := &VideoFrame{Data: d.au, TS: d.ts, Key: d.key, At: now}
	d.au, d.key = nil, false
	return f
}

// parseSPSSize reads the picture size out of an SPS NAL unit (ITU-T H.264
// 7.3.2.1.1): profile, level, the exp-Golomb coded fields up to the size, and
// the frame cropping. 0, 0 when it cannot be read.
func parseSPSSize(nal []byte) (w, h int) {
	// Remove emulation prevention bytes after the NAL header.
	var rbsp []byte
	zeros := 0
	for _, b := range nal[1:] {
		if zeros >= 2 && b == 3 {
			zeros = 0
			continue
		}
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
		rbsp = append(rbsp, b)
	}
	if len(rbsp) < 4 {
		return 0, 0
	}
	br := &bitReader{b: rbsp}
	profile := br.bits(8)
	br.bits(8) // constraint flags
	br.bits(8) // level
	br.ue()    // seq_parameter_set_id
	chroma := 1
	switch profile {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		chroma = int(br.ue())
		if chroma == 3 {
			br.bits(1)
		}
		br.ue()    // bit depth luma
		br.ue()    // bit depth chroma
		br.bits(1) // qpprime
		if br.bits(1) == 1 {
			n := 8
			if chroma == 3 {
				n = 12
			}
			for i := 0; i < n; i++ {
				if br.bits(1) == 1 {
					size := 16
					if i >= 6 {
						size = 64
					}
					last, next := 8, 8
					for j := 0; j < size; j++ {
						if next != 0 {
							delta := br.se()
							next = (last + delta + 256) % 256
						}
						if next != 0 {
							last = next
						}
					}
				}
			}
		}
	}
	br.ue() // log2_max_frame_num
	switch br.ue() {
	case 0:
		br.ue()
	case 1:
		br.bits(1)
		br.se()
		br.se()
		n := int(br.ue())
		for i := 0; i < n && !br.err; i++ {
			br.se()
		}
	}
	br.ue() // max_num_ref_frames
	br.bits(1)
	mbW := int(br.ue()) + 1
	mbH := int(br.ue()) + 1
	frameMbsOnly := br.bits(1)
	if frameMbsOnly == 0 {
		br.bits(1)
	}
	br.bits(1) // direct_8x8
	cl, cr, ct, cb := 0, 0, 0, 0
	if br.bits(1) == 1 {
		cl, cr, ct, cb = int(br.ue()), int(br.ue()), int(br.ue()), int(br.ue())
	}
	if br.err {
		return 0, 0
	}
	cx, cy := 2, 2 // chroma 4:2:0 crop units
	switch chroma {
	case 0:
		cx, cy = 1, 1
	case 2:
		cy = 1
	case 3:
		cx, cy = 1, 1
	}
	if frameMbsOnly == 0 {
		cy *= 2
	}
	w = mbW*16 - (cl+cr)*cx
	h = (2-int(frameMbsOnly))*mbH*16 - (ct+cb)*cy
	return w, h
}

type bitReader struct {
	b   []byte
	pos int
	err bool
}

func (r *bitReader) bits(n int) uint32 {
	var v uint32
	for i := 0; i < n; i++ {
		if r.pos>>3 >= len(r.b) {
			r.err = true
			return 0
		}
		v = v<<1 | uint32(r.b[r.pos>>3]>>(7-uint(r.pos&7))&1)
		r.pos++
	}
	return v
}

func (r *bitReader) ue() uint32 {
	zeros := 0
	for r.bits(1) == 0 {
		if r.err || zeros > 31 {
			r.err = true
			return 0
		}
		zeros++
	}
	return 1<<uint(zeros) - 1 + r.bits(zeros)
}

func (r *bitReader) se() int {
	k := r.ue()
	if k&1 == 1 {
		return int(k+1) / 2
	}
	return -int(k / 2)
}

// ---- VP8 ------------------------------------------------------------------------

type vp8Depay struct {
	ts     uint32
	have   bool
	frame  []byte
	broken bool
	w, h   int
}

func newVP8Depay() *vp8Depay { return &vp8Depay{} }

func (d *vp8Depay) reset() { d.frame, d.have, d.broken = nil, false, false }

func (d *vp8Depay) size() (int, int) { return d.w, d.h }

func (d *vp8Depay) push(pkt *RTPPacket, now time.Time) (*VideoFrame, error) {
	var out *VideoFrame
	if d.have && pkt.Timestamp != d.ts {
		out = d.finish(now)
	}
	d.ts, d.have = pkt.Timestamp, true
	p := pkt.Payload
	if len(p) < 1 {
		return out, errVideoPayload
	}
	// Payload descriptor (RFC 7741 4.2).
	x, start := p[0]&0x80 != 0, p[0]&0x10 != 0
	pid := p[0] & 0x07
	i := 1
	if x {
		if len(p) < 2 {
			return out, errVideoPayload
		}
		ext := p[1]
		i = 2
		if ext&0x80 != 0 { // picture id
			if i >= len(p) {
				return out, errVideoPayload
			}
			if p[i]&0x80 != 0 {
				i++
			}
			i++
		}
		if ext&0x40 != 0 { // TL0PICIDX
			i++
		}
		if ext&0x30 != 0 { // TID, KEYIDX
			i++
		}
	}
	if i > len(p) {
		return out, errVideoPayload
	}
	if start && pid == 0 {
		d.frame, d.broken = d.frame[:0], false
	} else if len(d.frame) == 0 {
		d.broken = true // the start of the frame was lost
	}
	if !d.broken {
		d.frame = append(d.frame, p[i:]...)
	}
	if pkt.Marker {
		if f := d.finish(now); f != nil {
			return f, nil
		}
	}
	return out, nil
}

func (d *vp8Depay) finish(now time.Time) *VideoFrame {
	if d.broken || len(d.frame) < 3 {
		d.frame, d.broken = nil, false
		return nil
	}
	key := d.frame[0]&1 == 0
	if key && len(d.frame) >= 10 && d.frame[3] == 0x9d && d.frame[4] == 0x01 && d.frame[5] == 0x2a {
		d.w = int(binary.LittleEndian.Uint16(d.frame[6:8]) & 0x3fff)
		d.h = int(binary.LittleEndian.Uint16(d.frame[8:10]) & 0x3fff)
	}
	f := &VideoFrame{Data: d.frame, TS: d.ts, Key: key, At: now}
	d.frame = nil
	return f
}

// ---- reordering ------------------------------------------------------------------

// reorder releases packets in sequence order. A packet may wait for a missing
// one for at most reorderWait or reorderDepth packets; after that the gap is a
// loss, reported so the caller can drop what it was assembling.
type reorder struct {
	next    uint16
	started bool
	buf     map[uint16]reorderEntry
}

type reorderEntry struct {
	pkt RTPPacket
	at  time.Time
}

const (
	reorderDepth = 24
	reorderWait  = 150 * time.Millisecond
)

func newReorder() *reorder { return &reorder{buf: map[uint16]reorderEntry{}} }

// add takes a packet (copying its payload) and returns the packets now ready in
// order, and whether a gap was given up on.
func (r *reorder) add(p *RTPPacket, now time.Time) (ready []RTPPacket, lost bool) {
	cp := *p
	cp.Payload = append([]byte(nil), p.Payload...)
	if !r.started {
		r.started, r.next = true, p.Sequence
	}
	if d := p.Sequence - r.next; d >= 0x8000 {
		return nil, false // late or duplicate: already passed
	}
	r.buf[p.Sequence] = reorderEntry{cp, now}
	drain := func() {
		for {
			e, ok := r.buf[r.next]
			if !ok {
				return
			}
			delete(r.buf, r.next)
			ready = append(ready, e.pkt)
			r.next++
		}
	}
	drain()
	for len(r.buf) > 0 {
		oldest := now
		for _, e := range r.buf {
			if e.at.Before(oldest) {
				oldest = e.at
			}
		}
		if len(r.buf) < reorderDepth && now.Sub(oldest) < reorderWait {
			break
		}
		// Give up on the gap: continue at the earliest packet we hold.
		seqs := make([]uint16, 0, len(r.buf))
		for s := range r.buf {
			seqs = append(seqs, s)
		}
		sort.Slice(seqs, func(i, j int) bool { return seqs[i]-r.next < seqs[j]-r.next })
		r.next = seqs[0]
		lost = true
		drain()
	}
	return ready, lost
}
