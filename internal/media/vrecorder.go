// The video recorder (clients-webrtc-mobile-video S-42): it reads the video the
// relay forwards, one stream per sending leg, depacketizes it without decoding,
// and writes it as fragments (fragmented MP4 for H.264, WebM clusters for VP8)
// that a sink stores one by one, so a recorded call never holds more than a few
// fragments in memory. A stream starts on a keyframe (it asks the sender for
// one), drops frames after a loss it cannot repair until the next keyframe,
// and stops, with the reason, when the sink cannot keep up.
package media

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// VideoSink stores one object of a recording.
type VideoSink interface {
	Put(ctx context.Context, key string, data []byte) error
}

// Defaults of HELLO_RECORDING_VIDEO_SEGMENT.
const (
	DefaultVideoSegmentEvery = 10 * time.Second
	DefaultVideoSegmentBytes = 4 << 20
	// videoMaxPending is how many fragments may wait for the sink before the
	// stream's recording stops.
	videoMaxPending = 3
)

// VideoSegment is one stored fragment of a stream.
type VideoSegment struct {
	Seq     int    `json:"seq"`
	Object  string `json:"object"`
	Bytes   int64  `json:"bytes"`
	Ticks   uint64 `json:"ticks"`   // duration of its pictures, 90 kHz
	Base    uint64 `json:"base"`    // decode time of its first picture from the stream's first, 90 kHz
	StartMs int64  `json:"startMs"` // Base in milliseconds
	Key     bool   `json:"key"`     // starts with a keyframe
}

// VideoStreamInfo describes one recorded stream, as it stands.
type VideoStreamInfo struct {
	Part      int            `json:"part"`
	Leg       string         `json:"leg"`
	Dir       string         `json:"dir"`
	Codec     string         `json:"codec"`     // h264 or vp8
	Container string         `json:"container"` // mp4 or webm
	Prefix    string         `json:"prefix"`    // where the stream's init segment and fragments are: Prefix/init, Prefix/<seq>
	Final     string         `json:"final"`     // the object the finalizer makes of them
	Width     int            `json:"width"`
	Height    int            `json:"height"`
	OffsetMs  int64          `json:"offsetMs"` // of the first frame against the audio's start
	Segments  []VideoSegment `json:"segments"`
	Bytes     int64          `json:"bytes"`
	Started   time.Time      `json:"started"`
	Ended     bool           `json:"ended"`
	Failed    string         `json:"failed,omitempty"` // why the stream stopped early
}

// InitObject is the object holding the stream's init segment.
func (i VideoStreamInfo) InitObject() string { return i.Prefix + "/init" }

// VideoRecorderConfig configures a recorder.
type VideoRecorderConfig struct {
	// Prefix is the call's object prefix (rec/<unix>-<callid>); a stream is
	// Prefix/v<part>-<leg>-in.<mp4|webm> once finalized, its init segment and
	// fragments are Prefix/v<part>-<leg>-in.parts/...
	Prefix string
	// Epoch is when the audio recording began; stream offsets are against it.
	Epoch time.Time
	// StartPart numbers the first stream (later ones follow).
	StartPart    int
	Sink         VideoSink
	SegmentEvery time.Duration
	SegmentBytes int
	// Keyframe asks the sender on leg for a picture; nil does nothing. It must
	// not block.
	Keyframe func(leg string)
	// Changed is told of a stream that opened, stored a fragment, ended or
	// failed. It runs on the packet path or the sink's goroutine and must not
	// block.
	Changed func(VideoStreamInfo)
	Now     func() time.Time
}

// VideoRecorder records the video of one call.
type VideoRecorder struct {
	cfg VideoRecorderConfig

	mu      sync.Mutex
	streams map[string]*vstream // the current stream of each sending leg
	all     []*vstream
	part    int
	closed  bool
}

// NewVideoRecorder returns a recorder for one call.
func NewVideoRecorder(cfg VideoRecorderConfig) *VideoRecorder {
	if cfg.SegmentEvery <= 0 {
		cfg.SegmentEvery = DefaultVideoSegmentEvery
	}
	if cfg.SegmentBytes <= 0 {
		cfg.SegmentBytes = DefaultVideoSegmentBytes
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &VideoRecorder{cfg: cfg, streams: map[string]*vstream{}, part: max(cfg.StartPart, 1) - 1}
}

func (v *VideoRecorder) newStream(leg, codec string, d vdepay, ro *reorder) *vstream {
	s := &vstream{rec: v, leg: leg, codec: codec, depay: d, reorder: ro, state: vsWaitKey}
	v.streams[leg] = s
	v.all = append(v.all, s)
	return s
}

// Packet is the relay's video tap: one inbound packet of leg's stream.
func (v *VideoRecorder) Packet(leg, codec string, pkt *RTPPacket) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return
	}
	codec = strings.ToLower(codec)
	s := v.streams[leg]
	if s != nil && s.codec != codec {
		s.finish(false)
		s = nil
	}
	if s == nil {
		var d vdepay
		switch codec {
		case "h264":
			d = newH264Depay()
		case "vp8":
			d = newVP8Depay()
		default:
			return
		}
		s = v.newStream(leg, codec, d, newReorder())
	}
	for {
		rs := s.packet(pkt)
		if rs == nil {
			return
		}
		// The stream ended on a keyframe that changed the picture or its
		// configuration: the next stream, a new part, starts on that frame.
		s = v.newStream(leg, codec, s.depay, s.reorder)
		s.frame(rs.frame, v.cfg.Now())
		if len(rs.rest) == 0 {
			return
		}
		if rs2 := s.process(rs.rest, false, v.cfg.Now()); rs2 != nil {
			// A second change inside one batch of packets: treat the rest as
			// lost (a keyframe will come again).
			s = v.newStream(leg, codec, s.depay, s.reorder)
			s.frame(rs2.frame, v.cfg.Now())
		}
		return
	}
}

// Stop ends the recording of every stream: open fragments are stored first, and
// the infos come back once the sink has them all.
func (v *VideoRecorder) Stop() []VideoStreamInfo {
	v.mu.Lock()
	v.closed = true
	all := append([]*vstream(nil), v.all...)
	for _, s := range all {
		s.finish(true)
	}
	v.mu.Unlock()
	out := make([]VideoStreamInfo, 0, len(all))
	for _, s := range all {
		s.wait()
		if info, ok := s.snapshot(); ok {
			out = append(out, info)
		}
	}
	return out
}

// Flush stores every open fragment now (a graceful hand-off) without ending the
// streams.
func (v *VideoRecorder) Flush() {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, s := range v.all {
		s.cut()
	}
}

// Streams snapshots what is recorded so far.
func (v *VideoRecorder) Streams() []VideoStreamInfo {
	v.mu.Lock()
	all := append([]*vstream(nil), v.all...)
	v.mu.Unlock()
	var out []VideoStreamInfo
	for _, s := range all {
		if info, ok := s.snapshot(); ok {
			out = append(out, info)
		}
	}
	return out
}

// ---- one stream ---------------------------------------------------------------

const (
	vsWaitKey = iota
	vsRunning
	vsEnded
	vsFailed
)

type vsample struct {
	data []byte
	ts   uint64 // extended RTP timestamp
	dur  uint32 // ticks until the next picture
	key  bool
}

type vupload struct {
	key  string
	data []byte
	seg  *VideoSegment
}

type restart struct {
	frame *VideoFrame
	rest  []RTPPacket
}

type vstream struct {
	rec     *VideoRecorder
	leg     string
	codec   string
	depay   vdepay
	reorder *reorder
	state   int

	lastKeyReq time.Time
	afterLoss  bool
	lossTS     uint32
	haveLossTS bool

	infoMu sync.Mutex
	info   VideoStreamInfo

	prefix   string
	final    string
	extHi    uint64
	lastTS   uint32
	haveTS   bool
	extBase  uint64
	prev     *vsample
	cur      []vsample
	curBytes int
	curStart time.Time
	seq      int
	lastDur  uint32
	cfgSPS   []byte
	cfgPPS   []byte
	opened   bool
	haveBase bool

	up   chan vupload
	done chan struct{}
}

func (s *vstream) snapshot() (VideoStreamInfo, bool) {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	if s.info.Prefix == "" {
		return VideoStreamInfo{}, false
	}
	i := s.info
	i.Segments = append([]VideoSegment(nil), s.info.Segments...)
	return i, true
}

func (s *vstream) ext(ts uint32) uint64 {
	if !s.haveTS {
		s.haveTS, s.lastTS, s.extHi = true, ts, 0
		return uint64(ts)
	}
	if ts < s.lastTS && s.lastTS-ts > 1<<31 {
		s.extHi += 1 << 32
	}
	s.lastTS = ts
	return s.extHi + uint64(ts)
}

func (s *vstream) wantKey(now time.Time) {
	if s.rec.cfg.Keyframe == nil || now.Sub(s.lastKeyReq) < time.Second {
		return
	}
	s.lastKeyReq = now
	s.rec.cfg.Keyframe(s.leg)
}

// packet takes one inbound packet; a non-nil result asks the recorder to start
// the next stream on its frame.
func (s *vstream) packet(pkt *RTPPacket) *restart {
	if s.state == vsFailed || s.state == vsEnded {
		return nil
	}
	now := s.rec.cfg.Now()
	if s.state == vsWaitKey {
		s.wantKey(now)
	}
	ready, lost := s.reorder.add(pkt, now)
	return s.process(ready, lost, now)
}

func (s *vstream) gap(now time.Time) {
	s.depay.reset()
	s.afterLoss, s.haveLossTS = true, false
	if s.state == vsRunning {
		s.prev = nil
		s.cut()
		s.state = vsWaitKey // frames are dropped until the next keyframe
	}
	s.wantKey(now)
}

func (s *vstream) process(ready []RTPPacket, lost bool, now time.Time) *restart {
	if lost {
		s.gap(now)
	}
	for i := range ready {
		p := &ready[i]
		if s.afterLoss {
			// Whatever picture the gap fell into is incomplete: skip it.
			if !s.haveLossTS {
				s.lossTS, s.haveLossTS = p.Timestamp, true
			}
			if p.Timestamp == s.lossTS {
				continue
			}
			s.afterLoss = false
		}
		f, err := s.depay.push(p, now)
		if err != nil {
			s.gap(now)
			continue
		}
		if f == nil {
			continue
		}
		if s.frame(f, now) {
			return &restart{frame: f, rest: ready[i+1:]}
		}
		if s.state == vsFailed {
			return nil
		}
	}
	return nil
}

// frame takes one finished picture. It reports true when the stream must end
// and a new one start on this frame (a keyframe with another size or codec
// configuration).
func (s *vstream) frame(f *VideoFrame, now time.Time) (restart bool) {
	w, h := s.depay.size()
	if s.state == vsWaitKey {
		if !f.Key || w == 0 || h == 0 {
			return false
		}
		if hd, ok := s.depay.(*h264Depay); ok {
			if _, _, ok := hd.avcc(); !ok {
				return false
			}
		}
		if s.opened {
			if s.configChanged(w, h) {
				s.finish(false)
				return true
			}
			s.state = vsRunning // the same stream resumes after its gap
			s.curStart = now
		} else if !s.open(w, h, now) {
			return false
		}
	} else if f.Key && s.configChanged(w, h) {
		s.finish(false)
		return true
	}
	ts := s.ext(f.TS)
	if !s.opened {
		return false
	}
	if !s.haveBase {
		s.extBase, s.haveBase = ts, true
	}
	smp := &vsample{data: f.Data, ts: ts, key: f.Key}
	if s.prev != nil {
		s.push(*s.prev, smp.ts)
	}
	if f.Key && s.shouldCut(now, false) || s.shouldCut(now, true) {
		s.cut()
	}
	s.prev = smp
	return false
}

func (s *vstream) configChanged(w, h int) bool {
	if w != 0 && (w != s.info.Width || h != s.info.Height) {
		return true
	}
	if hd, ok := s.depay.(*h264Depay); ok {
		sps, pps, ok := hd.avcc()
		return ok && (string(sps) != string(s.cfgSPS) || string(pps) != string(s.cfgPPS))
	}
	return false
}

func (s *vstream) shouldCut(now time.Time, hard bool) bool {
	if len(s.cur) == 0 {
		return false
	}
	every, size := s.rec.cfg.SegmentEvery, s.rec.cfg.SegmentBytes
	if hard {
		return now.Sub(s.curStart) >= every*3/2 || s.curBytes >= size*3/2
	}
	return now.Sub(s.curStart) >= every || s.curBytes >= size
}

// open starts the stream: its init segment is stored first. It reports whether
// the stream is running.
func (s *vstream) open(w, h int, now time.Time) bool {
	r := s.rec
	container, ext := "mp4", "mp4"
	var init []byte
	switch s.codec {
	case "h264":
		sps, pps, _ := s.depay.(*h264Depay).avcc()
		var err error
		init, err = MP4Init(sps, pps, w, h)
		if err != nil {
			s.fail("unsupported")
			return false
		}
		s.cfgSPS, s.cfgPPS = append([]byte(nil), sps...), append([]byte(nil), pps...)
	default:
		container, ext = "webm", "webm"
		init = WebMInit(w, h)
	}
	r.part++
	s.prefix = fmt.Sprintf("%s/v%d-%s-in.parts", r.cfg.Prefix, r.part, s.leg)
	s.final = fmt.Sprintf("%s/v%d-%s-in.%s", r.cfg.Prefix, r.part, s.leg, ext)
	s.infoMu.Lock()
	s.info = VideoStreamInfo{Part: r.part, Leg: s.leg, Dir: "in", Codec: s.codec, Container: container, Prefix: s.prefix,
		Final: s.final, Width: w, Height: h, Started: now, OffsetMs: now.Sub(r.cfg.Epoch).Milliseconds()}
	s.infoMu.Unlock()
	s.up = make(chan vupload, videoMaxPending)
	s.done = make(chan struct{})
	go s.uploader(s.up, s.done)
	s.opened = true
	s.state = vsRunning
	s.curStart = now
	s.enqueue(vupload{key: s.prefix + "/init", data: init})
	s.changed()
	return s.state == vsRunning
}

func (s *vstream) changed() {
	if s.rec.cfg.Changed == nil {
		return
	}
	if info, ok := s.snapshot(); ok {
		s.rec.cfg.Changed(info)
	}
}

// push adds a finished sample, with its duration from the next frame's time.
func (s *vstream) push(smp vsample, nextTS uint64) {
	dur := uint32(3000)
	if nextTS > smp.ts && nextTS-smp.ts < 90000*5 {
		dur = uint32(nextTS - smp.ts) //nolint:gosec // under five seconds of ticks
	} else if s.lastDur != 0 {
		dur = s.lastDur
	}
	s.lastDur = dur
	smp.dur = dur
	if len(s.cur) == 0 {
		s.curStart = s.rec.cfg.Now()
	}
	s.cur = append(s.cur, smp)
	s.curBytes += len(smp.data)
}

// cut stores what the open fragment holds.
func (s *vstream) cut() {
	if !s.opened || s.state == vsFailed || len(s.cur) == 0 {
		return
	}
	s.seq++
	var data []byte
	var ticks uint64
	for _, c := range s.cur {
		ticks += uint64(c.dur)
	}
	base := s.cur[0].ts - s.extBase
	startMs := int64(base / 90) //nolint:gosec // a call is not 3e16 ticks long
	switch s.codec {
	case "h264":
		samples := make([]MP4Sample, len(s.cur))
		for i, c := range s.cur {
			samples[i] = MP4Sample{Data: c.data, Duration: c.dur, Key: c.key}
		}
		data = MP4Fragment(uint32(s.seq), base, samples) //nolint:gosec // a call has few fragments
	default:
		frames := make([]WebMFrame, len(s.cur))
		for i, c := range s.cur {
			frames[i] = WebMFrame{Data: c.data, At: int16(min((c.ts-s.cur[0].ts)/90, 32767)), Key: c.key} //nolint:gosec // clamped
		}
		data = WebMCluster(uint64(startMs), frames) //nolint:gosec // non-negative
	}
	seg := VideoSegment{Seq: s.seq, Object: fmt.Sprintf("%s/%d", s.prefix, s.seq), Bytes: int64(len(data)), Ticks: ticks,
		Base: base, StartMs: startMs, Key: s.cur[0].key}
	s.cur, s.curBytes = nil, 0
	s.curStart = s.rec.cfg.Now()
	s.enqueue(vupload{key: seg.Object, data: data, seg: &seg})
}

// enqueue hands an object to the sink's goroutine; a full queue means the
// object store cannot keep up and the stream's recording stops.
func (s *vstream) enqueue(u vupload) {
	if s.up == nil || s.state == vsFailed {
		return
	}
	select {
	case s.up <- u:
	default:
		s.fail("object_store")
	}
}

func (s *vstream) uploader(q <-chan vupload, done chan<- struct{}) {
	defer close(done)
	failed := false
	for u := range q {
		if failed {
			continue
		}
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err = s.rec.cfg.Sink.Put(ctx, u.key, u.data)
			cancel()
			if err == nil {
				break
			}
			time.Sleep(time.Duration(attempt+1) * 150 * time.Millisecond)
		}
		if err != nil {
			failed = true
			s.rec.mu.Lock()
			s.fail("object_store")
			s.rec.mu.Unlock()
			continue
		}
		if u.seg != nil {
			s.infoMu.Lock()
			s.info.Segments = append(s.info.Segments, *u.seg)
			s.info.Bytes += u.seg.Bytes
			s.infoMu.Unlock()
		}
		s.changed()
	}
}

// fail stops the stream: its recording ends with the reason, what was stored
// stays.
func (s *vstream) fail(reason string) {
	if s.state == vsFailed {
		return
	}
	s.state = vsFailed
	s.cur, s.prev = nil, nil
	s.infoMu.Lock()
	s.info.Failed = reason
	s.infoMu.Unlock()
	noteVideoRecFailure(reason)
	s.changed()
}

// finish stores the last fragment and ends the stream. A stream that ended
// without stop is replaced by a successor; its uploads finish in the
// background.
func (s *vstream) finish(stop bool) {
	if s.state == vsRunning {
		if s.prev != nil {
			s.push(*s.prev, 0)
			s.prev = nil
		}
		s.cut()
	} else if s.state == vsWaitKey {
		s.prev = nil
		s.cut()
	}
	if s.up != nil {
		close(s.up)
		s.up = nil
	}
	if s.state != vsFailed {
		s.state = vsEnded
	}
	s.infoMu.Lock()
	if s.info.Prefix != "" {
		s.info.Ended = true
	}
	s.infoMu.Unlock()
	if !stop && s.done != nil {
		done := s.done
		go func() {
			<-done
			s.changed()
		}()
		return
	}
	if !stop {
		s.changed()
	}
}

// wait blocks until the stream's uploads are done.
func (s *vstream) wait() {
	if s.done != nil {
		<-s.done
	}
	s.changed()
}
