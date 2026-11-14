package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memSink struct {
	mu   sync.Mutex
	objs map[string][]byte
	fail atomic.Bool
}

func newMemSink() *memSink { return &memSink{objs: map[string][]byte{}} }

func (m *memSink) Put(_ context.Context, key string, data []byte) error {
	if m.fail.Load() {
		return errors.New("object store down")
	}
	m.mu.Lock()
	m.objs[key] = append([]byte(nil), data...)
	m.mu.Unlock()
	return nil
}

func (m *memSink) get(key string) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.objs[key]
}

// bw is a bit writer for building an SPS.
type bw struct {
	b []byte
	n int
}

func (w *bw) bit(v uint) {
	if w.n%8 == 0 {
		w.b = append(w.b, 0)
	}
	w.b[len(w.b)-1] |= byte(v&1) << (7 - uint(w.n%8))
	w.n++
}

func (w *bw) bits(v uint, n int) {
	for i := n - 1; i >= 0; i-- {
		w.bit(v >> uint(i))
	}
}

func (w *bw) ue(v uint) {
	v++
	l := 0
	for x := v; x > 1; x >>= 1 {
		l++
	}
	w.bits(0, l)
	w.bits(v, l+1)
}

// testSPS is a Constrained Baseline SPS for a width x height picture.
func testSPS(width, height int) []byte {
	w := &bw{}
	w.bits(66, 8)
	w.bits(0xc0, 8)
	w.bits(30, 8)
	w.ue(0)                  // sps id
	w.ue(0)                  // log2_max_frame_num - 4
	w.ue(2)                  // poc type
	w.ue(1)                  // max ref frames
	w.bit(0)                 // gaps
	w.ue(uint(width/16 - 1)) //nolint:gosec // test sizes
	w.ue(uint(height/16 - 1))
	w.bit(1) // frame mbs only
	w.bit(1) // direct 8x8
	w.bit(0) // cropping
	w.bit(0) // vui
	w.bit(1) // rbsp stop bit
	return append([]byte{0x67}, w.b...)
}

var testPPS = []byte{0x68, 0xce, 0x3c, 0x80}

// h264Stream makes frames: a keyframe (SPS, PPS, a large IDR) every gop frames,
// the rest small P slices; each is its own picture at 30 fps.
type h264Frame struct {
	nals [][]byte
	key  bool
}

func makeH264(n, gop int) []h264Frame {
	var out []h264Frame
	for i := 0; i < n; i++ {
		if i%gop == 0 {
			idr := append([]byte{0x65}, bytes.Repeat([]byte{byte(i + 1)}, 3000)...)
			out = append(out, h264Frame{nals: [][]byte{testSPS(640, 480), testPPS, idr}, key: true})
		} else {
			out = append(out, h264Frame{nals: [][]byte{append([]byte{0x41}, bytes.Repeat([]byte{byte(i + 1)}, 150)...)}})
		}
	}
	return out
}

// packetize splits frames into RTP packets of at most mtu: SPS and PPS go in a
// STAP-A, big NAL units in FU-A.
func packetizeH264(frames []h264Frame, mtu int, seq *uint16, ts0 uint32) []RTPPacket {
	var out []RTPPacket
	for fi, f := range frames {
		ts := ts0 + uint32(fi)*3000
		var payloads [][]byte
		var stap [][]byte
		flush := func() {
			if len(stap) == 0 {
				return
			}
			p := []byte{24}
			for _, n := range stap {
				p = append(p, byte(len(n)>>8), byte(len(n)))
				p = append(p, n...)
			}
			payloads = append(payloads, p)
			stap = nil
		}
		for _, n := range f.nals {
			switch t := n[0] & 0x1f; {
			case t == 7 || t == 8:
				stap = append(stap, n)
			case len(n) <= mtu:
				flush()
				payloads = append(payloads, n)
			default:
				flush()
				body := n[1:]
				for off := 0; off < len(body); off += mtu - 2 {
					end := min(off+mtu-2, len(body))
					fu := []byte{n[0]&0xe0 | 28, n[0] & 0x1f}
					if off == 0 {
						fu[1] |= 0x80
					}
					if end == len(body) {
						fu[1] |= 0x40
					}
					payloads = append(payloads, append(fu, body[off:end]...))
				}
			}
		}
		flush()
		for i, p := range payloads {
			out = append(out, RTPPacket{PayloadType: 96, Marker: i == len(payloads)-1, Sequence: *seq, Timestamp: ts, SSRC: 7, Payload: p})
			*seq++
		}
	}
	return out
}

type boxInfo struct {
	typ  string
	data []byte // payload
	full []byte
}

func boxes(t *testing.T, b []byte) []boxInfo {
	t.Helper()
	var out []boxInfo
	for len(b) > 0 {
		if len(b) < 8 {
			t.Fatalf("trailing %d bytes", len(b))
		}
		n := int(binary.BigEndian.Uint32(b))
		if n < 8 || n > len(b) {
			t.Fatalf("box %q size %d of %d", b[4:8], n, len(b))
		}
		out = append(out, boxInfo{string(b[4:8]), b[8:n], b[:n]})
		b = b[n:]
	}
	return out
}

func find(bs []boxInfo, typ string) *boxInfo {
	for i := range bs {
		if bs[i].typ == typ {
			return &bs[i]
		}
	}
	return nil
}

// mp4Samples reads every sample of a fragmented file: its bytes, duration,
// flags and the fragment's decode time.
type mp4Sample struct {
	data []byte
	dur  uint32
	key  bool
	base uint64
}

func readFragments(t *testing.T, file []byte) (samples []mp4Sample, fragments int) {
	t.Helper()
	bs := boxes(t, file)
	for i := 0; i < len(bs); i++ {
		if bs[i].typ != "moof" {
			continue
		}
		fragments++
		if i+1 >= len(bs) || bs[i+1].typ != "mdat" {
			t.Fatal("a moof without its mdat")
		}
		moof, mdat := bs[i], bs[i+1]
		traf := find(boxes(t, moof.data), "traf")
		tb := boxes(t, traf.data)
		tfdt := find(tb, "tfdt")
		base := binary.BigEndian.Uint64(tfdt.data[4:])
		trun := find(tb, "trun")
		count := int(binary.BigEndian.Uint32(trun.data[4:]))
		off := int(binary.BigEndian.Uint32(trun.data[8:]))
		if off != len(moof.full)+8 {
			t.Fatalf("trun data offset %d, want %d", off, len(moof.full)+8)
		}
		pos := 0
		for k := 0; k < count; k++ {
			e := trun.data[12+k*12:]
			dur := binary.BigEndian.Uint32(e)
			size := int(binary.BigEndian.Uint32(e[4:]))
			flags := binary.BigEndian.Uint32(e[8:])
			samples = append(samples, mp4Sample{data: mdat.data[pos : pos+size], dur: dur, key: flags == 0x02000000, base: base})
			base += uint64(dur)
			pos += size
		}
		if pos != len(mdat.data) {
			t.Fatalf("samples cover %d of %d mdat bytes", pos, len(mdat.data))
		}
	}
	return samples, fragments
}

func avccOf(nals [][]byte) []byte {
	var out []byte
	for _, n := range nals {
		if t := n[0] & 0x1f; t == 7 || t == 8 {
			continue
		}
		out = append(out, byte(len(n)>>24), byte(len(n)>>16), byte(len(n)>>8), byte(len(n)))
		out = append(out, n...)
	}
	return out
}

func joinStream(sink *memSink, info VideoStreamInfo) []byte {
	out := append([]byte(nil), sink.get(info.InitObject())...)
	for _, s := range info.Segments {
		out = append(out, sink.get(s.Object)...)
	}
	return out
}

// feed gives the recorder packets at the pace of a call: the sink's goroutine
// gets a moment after every picture, as it does when pictures are 33 ms apart.
func feed(rec *VideoRecorder, leg, codec string, pkts []RTPPacket) {
	for i := range pkts {
		rec.Packet(leg, codec, &pkts[i])
		if pkts[i].Marker {
			time.Sleep(300 * time.Microsecond)
		}
	}
}

func newTestRecorder(sink VideoSink, keyReqs *atomic.Int32, mutate func(*VideoRecorderConfig)) *VideoRecorder {
	cfg := VideoRecorderConfig{Prefix: "rec/1-call", Epoch: time.Now(), Sink: sink,
		Keyframe: func(string) {
			if keyReqs != nil {
				keyReqs.Add(1)
			}
		}}
	if mutate != nil {
		mutate(&cfg)
	}
	return NewVideoRecorder(cfg)
}

// TestVideoRecordingMux fails if H.264 RTP (single NAL units, STAP-A and FU-A,
// reordered and duplicated packets) is not written as a fragmented MP4 that a
// parser reads back with the original frames: the first sample a keyframe, one
// sample per picture with the NAL units untouched, 3000-tick durations and
// timestamps that only grow, the picture size taken from the SPS, a keyframe
// asked for when the stream starts. It also fails if the init segment lacks the
// SPS or PPS.
func TestVideoRecordingMux(t *testing.T) {
	sink := newMemSink()
	var keyReqs atomic.Int32
	rec := newTestRecorder(sink, &keyReqs, func(c *VideoRecorderConfig) { c.SegmentBytes = 1 << 30; c.SegmentEvery = time.Hour })
	frames := makeH264(60, 30)
	seq := uint16(65500) // wraps
	// A delta frame before the first keyframe is not recorded.
	pre := packetizeH264([]h264Frame{{nals: [][]byte{{0x41, 1, 2, 3}}}}, 1200, &seq, 4294950000)
	pkts := append(pre, packetizeH264(frames, 1200, &seq, 4294960000)...)
	// Reorder neighbours inside the stream and duplicate one packet.
	for i := 10; i+1 < len(pkts); i += 17 {
		pkts[i], pkts[i+1] = pkts[i+1], pkts[i]
	}
	pkts = append(pkts[:30], append([]RTPPacket{pkts[29]}, pkts[30:]...)...)
	feed(rec, "caller", "H264", pkts)
	infos := rec.Stop()
	if len(infos) != 1 {
		t.Fatalf("streams = %d, want 1", len(infos))
	}
	in := infos[0]
	if in.Codec != "h264" || in.Container != "mp4" || in.Width != 640 || in.Height != 480 || !strings.HasSuffix(in.Prefix, "v1-caller-in.parts") || in.Final != "rec/1-call/v1-caller-in.mp4" {
		t.Errorf("info = %+v", in)
	}
	if keyReqs.Load() == 0 {
		t.Error("no keyframe was requested when the stream began")
	}
	file := joinStream(sink, in)
	top := boxes(t, file)
	if top[0].typ != "ftyp" || find(top, "moov") == nil {
		t.Fatalf("not an init segment: %v", top[0].typ)
	}
	if !bytes.Contains(file, testSPS(640, 480)) || !bytes.Contains(file, testPPS) {
		t.Error("the init segment does not carry the SPS and PPS")
	}
	samples, _ := readFragments(t, file)
	if len(samples) != len(frames) {
		t.Fatalf("samples = %d, want %d", len(samples), len(frames))
	}
	var prevEnd uint64
	for i, s := range samples {
		if !bytes.Equal(s.data, avccOf(frames[i].nals)) {
			t.Fatalf("sample %d is not the picture's NAL units untouched", i)
		}
		if s.key != frames[i].key {
			t.Errorf("sample %d key = %v", i, s.key)
		}
		if i < len(samples)-1 && s.dur != 3000 {
			t.Errorf("sample %d duration %d", i, s.dur)
		}
		if s.base < prevEnd {
			t.Errorf("timestamps go back at %d", i)
		}
		prevEnd = s.base + uint64(s.dur)
	}
	if !samples[0].key {
		t.Error("the file does not start on a keyframe")
	}
}

// TestVideoRecordingLoss fails if a packet lost inside a picture does not drop
// that picture and every one after it until the next keyframe (never a
// corrupted frame in the file), does not ask the sender for a keyframe, or if
// the recording does not carry on from that keyframe in the same file.
func TestVideoRecordingLoss(t *testing.T) {
	sink := newMemSink()
	var keyReqs atomic.Int32
	rec := newTestRecorder(sink, &keyReqs, nil)
	frames := makeH264(90, 30)
	seq := uint16(100)
	pkts := packetizeH264(frames[:], 1200, &seq, 1000)
	// Drop a packet in the middle of picture 40 (a P frame of the second GOP).
	var kept []RTPPacket
	dropped := false
	for _, p := range pkts {
		if p.Timestamp == 1000+40*3000 && !dropped {
			dropped = true
			continue
		}
		kept = append(kept, p)
	}
	now := time.Now()
	rec.cfg.Now = func() time.Time { now = now.Add(20 * time.Millisecond); return now }
	feed(rec, "caller", "H264", kept)
	infos := rec.Stop()
	if len(infos) != 1 {
		t.Fatalf("streams = %d", len(infos))
	}
	samples, _ := readFragments(t, joinStream(sink, infos[0]))
	// Pictures 0-39 (40), then 41..59 are dropped with the gap, 60..89 (30) follow.
	keys := 0
	for _, s := range samples {
		if s.key {
			keys++
		}
	}
	if len(samples) >= len(frames) || len(samples) < 60 {
		t.Fatalf("samples = %d of %d; the damaged run was not dropped", len(samples), len(frames))
	}
	if keys != 2 && keys != 3 {
		t.Errorf("keyframes = %d", keys)
	}
	// Every kept sample is a picture of the source, untouched.
	want := map[string]bool{}
	for _, f := range frames {
		want[string(avccOf(f.nals))] = true
	}
	for i, s := range samples {
		if !want[string(s.data)] {
			t.Fatalf("sample %d is not one of the source pictures", i)
		}
	}
	if keyReqs.Load() < 2 {
		t.Errorf("keyframe requests = %d, want one at the start and one after the loss", keyReqs.Load())
	}
}

// TestVideoRecordingSegments fails if fragments are not cut at a keyframe once
// the size or time limit is passed, if the stored objects are not a valid file
// when concatenated, or if the finalized file (header with a sidx and the
// fragments) does not hold the same samples with an index whose sizes add up.
func TestVideoRecordingSegments(t *testing.T) {
	sink := newMemSink()
	rec := newTestRecorder(sink, nil, func(c *VideoRecorderConfig) { c.SegmentBytes = 6 << 10; c.SegmentEvery = time.Hour })
	frames := makeH264(120, 30)
	seq := uint16(1)
	pkts := packetizeH264(frames, 1200, &seq, 90000)
	feed(rec, "callee", "H264", pkts)
	in := rec.Stop()[0]
	if len(in.Segments) < 3 {
		t.Fatalf("segments = %d, want several", len(in.Segments))
	}
	var total int64
	for i, s := range in.Segments {
		total += s.Bytes
		if !s.Key {
			t.Errorf("segment %d does not start on a keyframe", i)
		}
		if int64(len(sink.get(s.Object))) != s.Bytes {
			t.Errorf("segment %d: stored %d bytes, info says %d", i, len(sink.get(s.Object)), s.Bytes)
		}
		if i > 0 && s.Base != in.Segments[i-1].Base+in.Segments[i-1].Ticks {
			t.Errorf("segment %d base %d does not follow the last", i, s.Base)
		}
	}
	if in.Bytes != total {
		t.Errorf("stream bytes %d, segments %d", in.Bytes, total)
	}
	progressive, n := readFragments(t, joinStream(sink, in))
	if len(progressive) != len(frames) || n != len(in.Segments) {
		t.Fatalf("concatenated fragments hold %d samples in %d fragments", len(progressive), n)
	}

	// The finalizer's file.
	var infos []FragInfo
	for _, s := range in.Segments {
		infos = append(infos, FragInfo{Size: s.Bytes, Ticks: s.Ticks, Key: s.Key, StartMs: s.StartMs})
	}
	var out bytes.Buffer
	err := WriteFinalMP4(&out, sink.get(in.InitObject()), infos, func(i int) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(sink.get(in.Segments[i].Object))), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if int64(out.Len()) != MP4HeaderSize(sink.get(in.InitObject()), len(infos))+total {
		t.Errorf("final size %d, header+fragments %d", out.Len(), MP4HeaderSize(sink.get(in.InitObject()), len(infos))+total)
	}
	top := boxes(t, out.Bytes())
	sidx := find(top, "sidx")
	if sidx == nil {
		t.Fatal("the finalized file has no sidx")
	}
	refs := int(binary.BigEndian.Uint16(sidx.data[4+4+4+8+8+2:]))
	if refs != len(in.Segments) {
		t.Errorf("sidx references = %d, want %d", refs, len(in.Segments))
	}
	final, _ := readFragments(t, out.Bytes())
	if len(final) != len(frames) {
		t.Errorf("finalized samples = %d", len(final))
	}
	// A sidx reference's size is the fragment's.
	for i := 0; i < refs; i++ {
		e := sidx.data[4+4+4+8+8+4+i*12:]
		if got := int64(binary.BigEndian.Uint32(e)); got != in.Segments[i].Bytes {
			t.Errorf("sidx reference %d size %d, fragment %d", i, got, in.Segments[i].Bytes)
		}
	}
}

// TestVideoRecordingObjectStoreDown fails if a sink that stops answering does
// not end that stream's recording with the reason after the pending fragments
// are used up, blocks the packet path, or panics; objects stored before stay.
func TestVideoRecordingObjectStoreDown(t *testing.T) {
	sink := newMemSink()
	var changed atomic.Int32
	rec := newTestRecorder(sink, nil, func(c *VideoRecorderConfig) {
		c.SegmentBytes = 4 << 10
		c.Changed = func(VideoStreamInfo) { changed.Add(1) }
	})
	frames := makeH264(300, 10)
	seq := uint16(1)
	pkts := packetizeH264(frames, 1200, &seq, 90000)
	start := time.Now()
	feed(rec, "caller", "H264", pkts[:len(pkts)/4])
	sink.fail.Store(true)
	feed(rec, "caller", "H264", pkts[len(pkts)/4:])
	if time.Since(start) > 2*time.Second {
		t.Fatalf("the packet path waited for the object store (%v)", time.Since(start))
	}
	infos := rec.Stop()
	if len(infos) != 1 || infos[0].Failed != "object_store" {
		t.Fatalf("infos = %+v, want a stream that failed on the object store", infos)
	}
	if len(infos[0].Segments) == 0 {
		t.Error("what was stored before the failure is not kept in the info")
	}
	if changed.Load() == 0 {
		t.Error("nobody was told")
	}
}

// ---- VP8 and WebM ---------------------------------------------------------------------

func vp8Packets(frames [][]byte, keyEvery int, seq *uint16, ts0 uint32) []RTPPacket {
	var out []RTPPacket
	for i, f := range frames {
		ts := ts0 + uint32(i)*3000
		for off := 0; off < len(f) || off == 0; off += 1000 {
			end := min(off+1000, len(f))
			// X=1 with picture id (15 bit), S set on the first packet.
			d := []byte{0x80, 0x80, 0x80 | byte(i>>8&0x7f), byte(i)}
			if off == 0 {
				d[0] |= 0x10
			}
			out = append(out, RTPPacket{PayloadType: 96, Marker: end == len(f), Sequence: *seq, Timestamp: ts, SSRC: 9, Payload: append(d, f[off:end]...)})
			*seq++
			if end == len(f) {
				break
			}
		}
	}
	return out
}

func vp8Frame(key bool, n int, w, h int) []byte {
	b := make([]byte, 10, 10+n)
	if !key {
		b[0] = 1
	}
	if key {
		b[3], b[4], b[5] = 0x9d, 0x01, 0x2a
		binary.LittleEndian.PutUint16(b[6:], uint16(w))
		binary.LittleEndian.PutUint16(b[8:], uint16(h))
	}
	return append(b, bytes.Repeat([]byte{0x5a}, n)...)
}

// ebmlElem reads one element at b: id, data, and the rest.
func ebmlElem(t *testing.T, b []byte) (id uint32, data, rest []byte) {
	t.Helper()
	l := 1
	for m := byte(0x80); b[0]&m == 0 && l < 4; m >>= 1 {
		l++
	}
	for i := 0; i < l; i++ {
		id = id<<8 | uint32(b[i])
	}
	b = b[l:]
	sl := 1
	for m := byte(0x80); b[0]&m == 0; m >>= 1 {
		sl++
	}
	v := uint64(b[0]) & (0xff >> uint(sl))
	allOnes := v == uint64(0xff>>uint(sl))
	for i := 1; i < sl; i++ {
		v = v<<8 | uint64(b[i])
		allOnes = allOnes && b[i] == 0xff
	}
	b = b[sl:]
	if allOnes {
		return id, b, nil // unknown size: the rest
	}
	if int(v) > len(b) {
		t.Fatalf("element %x of %d bytes in %d", id, v, len(b))
	}
	return id, b[:v], b[v:]
}

func ebmlChildren(t *testing.T, b []byte) map[uint32][][]byte {
	t.Helper()
	out := map[uint32][][]byte{}
	for len(b) > 0 {
		id, d, rest := ebmlElem(t, b)
		out[id] = append(out[id], d)
		b = rest
	}
	return out
}

// TestVideoRecordingWebM fails if VP8 RTP (with payload descriptors, picture
// ids and several packets per picture) is not written as WebM clusters with
// every frame, the keyframe flag where the frame says so, the first block a
// keyframe and the picture size from the keyframe header, and if the finalized
// file's Cues do not point at the clusters it holds.
func TestVideoRecordingWebM(t *testing.T) {
	sink := newMemSink()
	rec := newTestRecorder(sink, nil, func(c *VideoRecorderConfig) { c.SegmentBytes = 12 << 10; c.SegmentEvery = time.Hour })
	var frames [][]byte
	for i := 0; i < 90; i++ {
		frames = append(frames, vp8Frame(i%30 == 0, 2500, 320, 240))
	}
	seq := uint16(10)
	pkts := vp8Packets(frames, 30, &seq, 5000)
	feed(rec, "callee", "VP8", pkts)
	in := rec.Stop()[0]
	if in.Codec != "vp8" || in.Container != "webm" || in.Width != 320 || in.Height != 240 || len(in.Segments) < 2 {
		t.Fatalf("info = %+v", in)
	}
	progressive := joinStream(sink, in)
	// EBML header, then the open Segment: Info, Tracks, Clusters.
	id, _, rest := ebmlElem(t, progressive)
	if id != idEBML {
		t.Fatalf("file begins with %x", id)
	}
	id, seg, _ := ebmlElem(t, rest)
	if id != idSegment {
		t.Fatalf("no segment: %x", id)
	}
	kids := ebmlChildren(t, seg)
	if len(kids[idInfo]) != 1 || len(kids[idTracks]) != 1 || len(kids[idCluster]) != len(in.Segments) {
		t.Fatalf("segment children: info %d tracks %d clusters %d (segments %d)", len(kids[idInfo]), len(kids[idTracks]), len(kids[idCluster]), len(in.Segments))
	}
	blocks, keys := 0, 0
	for ci, c := range kids[idCluster] {
		ck := ebmlChildren(t, c)
		if len(ck[idSimpleBlock]) == 0 {
			t.Fatal("empty cluster")
		}
		for bi, b := range ck[idSimpleBlock] {
			if b[0] != 0x81 {
				t.Fatalf("block track %x", b[0])
			}
			blocks++
			isKey := b[3]&0x80 != 0
			if isKey {
				keys++
			}
			if isKey != (b[4]&1 == 0) {
				t.Errorf("cluster %d block %d: flag %v but the frame says otherwise", ci, bi, isKey)
			}
			if ci == 0 && bi == 0 && !isKey {
				t.Error("the file starts on a delta frame")
			}
		}
	}
	if blocks != len(frames) || keys != 3 {
		t.Errorf("blocks %d keys %d, want %d and 3", blocks, keys, len(frames))
	}
	// The finalized file.
	var infos []FragInfo
	for _, s := range in.Segments {
		infos = append(infos, FragInfo{Size: s.Bytes, Ticks: s.Ticks, Key: s.Key, StartMs: s.StartMs})
	}
	var out bytes.Buffer
	if err := WriteFinalWebM(&out, in.Width, in.Height, 3000, infos, func(i int) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(sink.get(in.Segments[i].Object))), nil
	}); err != nil {
		t.Fatal(err)
	}
	_, _, rest = ebmlElem(t, out.Bytes())
	id, seg, tail := ebmlElem(t, rest)
	if id != idSegment || len(tail) != 0 {
		t.Fatalf("final segment %x with %d bytes after it", id, len(tail))
	}
	fk := ebmlChildren(t, seg)
	if len(fk[idCues]) != 1 || len(fk[idSeekHead]) != 1 || len(fk[idCluster]) != len(in.Segments) {
		t.Fatalf("final children: cues %d seekhead %d clusters %d", len(fk[idCues]), len(fk[idSeekHead]), len(fk[idCluster]))
	}
	// Each cue's cluster position is the offset of a Cluster from the segment's data.
	positions := map[uint64]bool{}
	cues := ebmlChildren(t, fk[idCues][0])
	var cueSeen int
	for _, cp := range cues[idCuePoint] {
		ck := ebmlChildren(t, cp)
		tp := ebmlChildren(t, ck[idCuePos][0])
		var pos uint64
		for _, x := range tp[idCueCluster][0] {
			pos = pos<<8 | uint64(x)
		}
		positions[pos] = true
		cueSeen++
		gid, _, _ := ebmlElem(t, seg[pos:])
		if gid != idCluster {
			t.Errorf("cue points at %x, not a cluster", gid)
		}
	}
	keyed := 0
	for _, s := range in.Segments {
		if s.Key {
			keyed++
		}
	}
	if cueSeen != keyed || keyed == 0 {
		t.Errorf("cues = %d, want one per fragment that starts on a keyframe (%d)", cueSeen, keyed)
	}
	var starts []int
	for _, s := range in.Segments {
		starts = append(starts, int(s.StartMs))
	}
	if !sort.IntsAreSorted(starts) {
		t.Error("cluster times go back")
	}
	_ = fmt.Sprint
}

// TestVideoRecordingConfigChange fails if a keyframe that brings a new picture
// size does not end the file and start the next part with its own init
// segment, so no file mixes two sizes.
func TestVideoRecordingConfigChange(t *testing.T) {
	sink := newMemSink()
	rec := newTestRecorder(sink, nil, nil)
	seq := uint16(1)
	a := packetizeH264(makeH264(40, 20), 1200, &seq, 1000)
	small := makeH264(40, 20)
	for i := range small {
		if small[i].key {
			small[i].nals[0] = testSPS(320, 240)
		}
	}
	b := packetizeH264(small, 1200, &seq, 1000+40*3000)
	feed(rec, "caller", "H264", a)
	feed(rec, "caller", "H264", b)
	infos := rec.Stop()
	if len(infos) != 2 || infos[0].Width != 640 || infos[1].Width != 320 || infos[1].Part != infos[0].Part+1 {
		t.Fatalf("infos = %+v", infos)
	}
	for _, in := range infos {
		samples, _ := readFragments(t, joinStream(sink, in))
		if len(samples) != 40 || !samples[0].key {
			t.Errorf("part %d holds %d samples", in.Part, len(samples))
		}
	}
}

// TestH264SPSSize fails if the picture size is not read out of an SPS, with and
// without the 4:2:0 cropping that 1080p streams carry.
func TestH264SPSSize(t *testing.T) {
	for _, c := range []struct{ w, h int }{{640, 480}, {1280, 720}, {176, 144}} {
		if w, h := parseSPSSize(testSPS(c.w, c.h)); w != c.w || h != c.h {
			t.Errorf("%dx%d read as %dx%d", c.w, c.h, w, h)
		}
	}
	// 1920x1080: 120x68 macroblocks cropped by 8 rows.
	w := &bw{}
	w.bits(66, 8)
	w.bits(0xc0, 8)
	w.bits(40, 8)
	w.ue(0)
	w.ue(0)
	w.ue(2)
	w.ue(1)
	w.bit(0)
	w.ue(119)
	w.ue(67)
	w.bit(1)
	w.bit(1)
	w.bit(1) // cropping
	w.ue(0)
	w.ue(0)
	w.ue(0)
	w.ue(4) // bottom: 4 units of 2 rows
	w.bit(0)
	w.bit(1)
	if gw, gh := parseSPSSize(append([]byte{0x67}, w.b...)); gw != 1920 || gh != 1080 {
		t.Errorf("1080p read as %dx%d", gw, gh)
	}
}
