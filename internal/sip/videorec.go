package sip

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/emiago/sipgo/sip"
)

// Video recording (clients-webrtc-mobile-video S-42). When a call is recorded
// and carries video, the video is recorded next to the audio: the relay's
// video tap feeds a media.VideoRecorder that depacketizes without decoding and
// stores fragments in the recordings bucket as it goes, and the stream rows
// (migration 00121) follow, so hello-control can finalize a stream whatever
// became of this node. The audio recording is unchanged; nothing is muxed into
// it. Both the node's switch and the extensions must allow it, and the video
// already stored must be under the quota. A takeover carries audio only, so a
// node that dies leaves the stream's row for hello-control's sweeper.

// VideoRecordingStore is the store of video stream rows (migration 00121,
// implemented over internal/store by hello-sip's wiring).
type VideoRecordingStore interface {
	// OpenVideoStream inserts the row of a stream and returns its id.
	OpenVideoStream(ctx context.Context, correlationID string, info media.VideoStreamInfo) (int64, error)
	// UpdateVideoStream brings the row up to date with the stream.
	UpdateVideoStream(ctx context.Context, id int64, info media.VideoStreamInfo) error
	// VideoUsageBytes is what video recordings hold in the object store.
	VideoUsageBytes(ctx context.Context) (int64, error)
}

// recordingNoticeVideo is the announcement played instead of recording-notice
// when the recording includes video; without it the plain notice plays.
const recordingNoticeVideo = "recording-notice-video"

// objectSink stores fragments as recording objects.
type objectSink struct{ o ObjectStore }

func (s objectSink) Put(ctx context.Context, key string, data []byte) error {
	return s.o.PutRecording(ctx, key, data)
}

// callVideoRec is a call's video recording (guarded by call.mu).
type callVideoRec struct {
	rec    *media.VideoRecorder
	writer *streamWriter
}

// startVideoRecording starts the call's video recording when the call is being
// recorded and carries video. It reports whether video is being recorded. It
// does nothing, and says why in the trace, when the policy forbids it.
func (c *call) startVideoRecording() bool {
	c.mu.Lock()
	rec, have := c.rec, c.vrec != nil
	c.mu.Unlock()
	if have {
		return true
	}
	if rec == nil || !rec.isActive() || !c.webVideoCarried() {
		return false
	}
	s := c.s
	if !s.cfg.RecordingVideo || s.deps.Objects == nil || s.deps.VideoRecordings == nil {
		c.addTrace("Video not recorded: the recording is audio only")
		return false
	}
	if why := c.videoRecordingDenied(); why != "" {
		c.addTrace("Video not recorded: " + why)
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.StateTimeout)
	used, err := s.deps.VideoRecordings.VideoUsageBytes(ctx)
	cancel()
	if err != nil {
		s.log.Warn("video recording quota unreadable", "error", err)
	} else if q := s.cfg.RecordingVideoQuotaBytes; q > 0 && used >= q {
		media.NoteVideoRecFailure("quota")
		c.addTrace("Video not recorded: the video recording quota is used up")
		return false
	}
	key, err := media.RecordingKey(c.callID, time.Now())
	if err != nil {
		c.addTrace("Video not recorded: " + err.Error())
		return false
	}
	r := c.anchorRelay()
	if r == nil {
		return false
	}
	rec.mu.Lock()
	epoch := rec.started
	rec.mu.Unlock()
	w := newStreamWriter(c)
	vr := media.NewVideoRecorder(media.VideoRecorderConfig{
		Prefix:       strings.TrimSuffix(key, ".wav"),
		Epoch:        epoch,
		Sink:         objectSink{s.deps.Objects},
		SegmentEvery: s.cfg.RecordingVideoSegment,
		SegmentBytes: s.cfg.RecordingVideoSegmentBytes,
		Keyframe: func(leg string) {
			if leg == "callee" {
				r.RequestKeyframe(legCallee)
			} else {
				r.RequestKeyframe(legCaller)
			}
		},
		Changed: w.note,
	})
	c.mu.Lock()
	if c.vrec != nil { // two triggers at once: the first one wins
		c.mu.Unlock()
		w.stop()
		return true
	}
	c.vrec = &callVideoRec{rec: vr, writer: w}
	c.mu.Unlock()
	r.OnVideo(func(leg, codec string, pkt *media.RTPPacket) {
		word := "caller"
		if leg == legCallee {
			word = "callee"
		}
		vr.Packet(word, codec, pkt)
	})
	// A stream that began before the recording gets its picture from a keyframe
	// at once.
	r.RequestKeyframe(legCaller)
	r.RequestKeyframe(legCallee)
	c.addTrace("Video is recorded next to the audio")
	c.signalRecording(true, true)
	return true
}

// videoRecordingDenied says why this call's video must not be recorded, "" when
// it may be: an extension of the call that keeps audio only.
func (c *call) videoRecordingDenied() string {
	snap := c.srtpSnapshot()
	if snap == nil {
		return ""
	}
	for _, num := range []string{c.callerNum, c.dialled} {
		if e, ok := snap.Extension(num); ok && e.NoRecordVideo {
			return "extension " + num + " keeps the audio only"
		}
	}
	return ""
}

// stopVideoRecording ends the video recording: the open fragments are stored
// and the rows finished before it returns.
func (c *call) stopVideoRecording() {
	c.mu.Lock()
	v := c.vrec
	c.vrec = nil
	c.mu.Unlock()
	if v == nil {
		return
	}
	if r := c.anchorRelay(); r != nil {
		r.OnVideo(nil)
	}
	infos := v.rec.Stop()
	for _, in := range infos {
		v.writer.note(in)
	}
	v.writer.stop()
	var stored, failed int
	for _, in := range infos {
		stored += len(in.Segments)
		if in.Failed != "" {
			failed++
		}
	}
	if failed > 0 {
		c.addTrace("Video recording stopped: object store")
	}
	if stored > 0 {
		c.addTrace(fmt.Sprintf("Video stored (%d stream(s), %d fragment(s))", len(infos), stored))
	}
}

// videoRecordingActive reports whether the call's video is being recorded.
func (c *call) videoRecordingActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.vrec != nil
}

// streamWriter keeps the stream rows of a call current without ever holding up
// the media: the recorder says what changed, the latest state of each stream
// waits in a map, and one goroutine writes it.
type streamWriter struct {
	c *call

	mu      sync.Mutex
	pending map[string]media.VideoStreamInfo
	ids     map[string]int64
	wake    chan struct{}
	done    chan struct{}
	closed  bool
}

func newStreamWriter(c *call) *streamWriter {
	w := &streamWriter{c: c, pending: map[string]media.VideoStreamInfo{}, ids: map[string]int64{},
		wake: make(chan struct{}, 1), done: make(chan struct{})}
	go w.run()
	return w
}

func (w *streamWriter) note(in media.VideoStreamInfo) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.pending[in.Prefix] = in
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// stop writes what is left and ends the goroutine.
func (w *streamWriter) stop() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	w.mu.Unlock()
	close(w.wake)
	<-w.done
}

func (w *streamWriter) run() {
	defer close(w.done)
	for range w.wake {
		w.flush()
	}
	w.flush()
}

func (w *streamWriter) flush() {
	w.mu.Lock()
	batch := w.pending
	w.pending = map[string]media.VideoStreamInfo{}
	w.mu.Unlock()
	st := w.c.s.deps.VideoRecordings
	if st == nil {
		return
	}
	for prefix, in := range batch {
		ctx, cancel := context.WithTimeout(context.Background(), w.c.s.cfg.StateTimeout)
		id, ok := w.ids[prefix]
		var err error
		if !ok {
			id, err = st.OpenVideoStream(ctx, w.c.id, in)
			if err == nil {
				w.ids[prefix] = id
			}
		} else {
			err = st.UpdateVideoStream(ctx, id, in)
		}
		cancel()
		if err != nil {
			w.c.s.log.Warn("video stream row not written", "correlation_id", w.c.id, "error", err)
			// Keep the newest state for the next round unless a newer one came.
			w.mu.Lock()
			if _, newer := w.pending[prefix]; !newer && !w.closed {
				w.pending[prefix] = in
			}
			w.mu.Unlock()
		}
	}
}

// recordingInfoType is the content type of the INFO a web client gets when its
// call's recording changes: the badge of the softphone (S-42, S-43). Body lines
// are "recording: on|off" and "video: yes|no".
const recordingInfoType = "application/x-hello-recording"

// signalRecording tells the Hello clients on the call (browsers, which show a
// recording badge with a camera mark when video is in it) what the recording is
// now. Phones get nothing: they hear the notice. It runs off the call's path.
func (c *call) signalRecording(on, video bool) {
	r := c.anchorRelay()
	if r == nil {
		return
	}
	body := "recording: off\r\nvideo: no\r\n"
	if on {
		body = "recording: on\r\nvideo: no\r\n"
		if video {
			body = "recording: on\r\nvideo: yes\r\n"
		}
	}
	send := func(do func(context.Context, *sip.Request) (*sip.Response, error), target sip.Uri) {
		out := sip.NewRequest(sip.INFO, target)
		out.AppendHeader(sip.NewHeader("Content-Type", recordingInfoType))
		out.SetBody([]byte(body))
		ctx, cancel := context.WithTimeout(context.Background(), byeTimeout)
		defer cancel()
		if _, err := requireResponse(do(ctx, out)); err != nil {
			c.s.log.Debug("recording INFO to a client failed", "correlation_id", c.id, "error", err)
		}
	}
	if r.IsWebLeg(legCaller) && c.dss != nil {
		go func() {
			// The caller's dialog is confirmed once its 200 has gone out and been
			// acknowledged. WriteResponse records the response on the session
			// under answerWrite, so the INFO waits for a 2xx to be there (a call
			// that records from its answer reaches this before the 200 is sent).
			for range 100 {
				c.answerWrite.Lock()
				res := c.dss.InviteResponse
				c.answerWrite.Unlock()
				if res != nil && res.IsSuccess() {
					send(c.dss.Do, c.target())
					return
				}
				c.mu.Lock()
				over := c.ended
				c.mu.Unlock()
				if over {
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
		}()
	}
	if w := c.winnerLeg(); w != nil && r.IsWebLeg(legCallee) && w.session() != nil {
		go send(w.session().Do, w.target())
	}
}
