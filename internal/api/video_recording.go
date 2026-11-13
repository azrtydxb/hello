package api

// Video recordings (clients-webrtc-mobile-video S-42): the manifest of a
// recording's video streams, the streams themselves with Range support, and
// the retention setting. The audio stays what it is (recordings/{id}/audio);
// each video stream is its own file, and the web player slaves its clock to the
// audio's with the offsets the manifest gives. A stream hello-control has not
// finalized yet is served as its init segment and fragments joined, which is a
// valid progressive file without fast seeking. The same role and scope checks
// as the audio apply, and a download is audited.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/store"
)

type videoManifest struct {
	RecordingID   int64         `json:"recordingId"`
	CorrelationID string        `json:"correlationId"`
	DurationMs    int64         `json:"durationMs"`
	Streams       []videoStream `json:"streams"`
}

type videoStream struct {
	store.VideoStream
	// Finalized is true when the stream is one file with a seek index.
	Finalized bool `json:"finalized"`
	// MediaType is the Content-Type of the stream's file.
	MediaType string `json:"mediaType"`
}

func mediaType(container string) string {
	if container == "webm" {
		return "video/webm"
	}
	return "video/mp4"
}

// recordingManifest is GET /api/v1/recordings/{id}/manifest.
func (s *server) recordingManifest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rec, err := s.Store.GetRecording(r.Context(), id)
	if err != nil {
		s.configError(w, "recording", err)
		return
	}
	streams, err := s.Store.VideoStreamsOf(r.Context(), rec.CorrelationID)
	if err != nil {
		s.internal(w, "recording manifest", err)
		return
	}
	m := videoManifest{RecordingID: rec.ID, CorrelationID: rec.CorrelationID, DurationMs: rec.DurationMs, Streams: []videoStream{}}
	for _, v := range streams {
		m.Streams = append(m.Streams, videoStream{VideoStream: v, Finalized: v.State == "finalized" && v.Final != "", MediaType: mediaType(v.Container)})
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, m)
}

// recordingVideo is GET /api/v1/recordings/{id}/video/{stream}.
func (s *server) recordingVideo(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	sid, err := strconv.ParseInt(r.PathValue("stream"), 10, 64)
	if err != nil || sid <= 0 {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	download := ""
	switch r.URL.Query().Get("download") {
	case "", "0", "false":
	case "1", "true":
		download = "1"
	default:
		badRequest(w, "download must be true or false")
		return
	}
	rec, err := s.Store.GetRecording(r.Context(), id)
	if err != nil {
		s.configError(w, "recording", err)
		return
	}
	streams, err := s.Store.VideoStreamsOf(r.Context(), rec.CorrelationID)
	if err != nil {
		s.internal(w, "recording video", err)
		return
	}
	var st *store.VideoStream
	for i := range streams {
		if streams[i].ID == sid {
			st = &streams[i]
		}
	}
	if st == nil {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	if s.Objects == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "object storage is unavailable; try again shortly")
		return
	}
	var body io.ReadSeeker
	var size int64
	var closer func()
	var modTime time.Time
	if st.State == "finalized" && st.Final != "" {
		obj, err := s.Objects.OpenAudio(r.Context(), RecordingsBucket, st.Final)
		if err != nil {
			s.Log.Error("recording video: open", "error", err)
			writeError(w, http.StatusBadGateway, "upstream", "object storage is unavailable; try again shortly")
			return
		}
		body, size, modTime, closer = obj.Body, obj.Size, obj.ModTime, func() { _ = obj.Body.Close() }
	} else {
		cr, err := newConcatObjects(r.Context(), s.Objects, append([]string{st.InitObject()}, segmentObjects(st.SegmentList)...))
		if err != nil {
			s.Log.Error("recording video: open fragments", "error", err)
			writeError(w, http.StatusBadGateway, "upstream", "object storage is unavailable; try again shortly")
			return
		}
		body, size, modTime, closer = cr, cr.size, cr.modTime, func() { _ = cr.Close() }
	}
	defer closer()
	_ = size
	// One audit event per viewing, not per Range request of the player.
	if rng := r.Header.Get("Range"); rng == "" || rng == "bytes=0-" {
		if err := s.Store.Audit(r.Context(), actor(r).String(), "recording.video.download", "recording", fmt.Sprintf("%d/%d", id, sid)); err != nil {
			s.Log.Warn("recording video: audit", "error", err)
		}
	}
	h := w.Header()
	h.Set("Content-Type", mediaType(st.Container))
	h.Set("Cache-Control", "private, no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	if download != "" {
		h.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fmt.Sprintf("recording-%d-video-%d.%s", id, sid, st.Container)))
	}
	http.ServeContent(w, r, "", modTime, body)
}

func segmentObjects(segs []store.VideoSegment) []string {
	out := make([]string, len(segs))
	for i, s := range segs {
		out[i] = s.Object
	}
	return out
}

// concatObjects reads several objects as one, seekable, so a stream that is
// not finalized yet can be served with Range support.
type concatObjects struct {
	ctx     context.Context
	objs    Objects
	names   []string
	sizes   []int64
	size    int64
	modTime time.Time
	pos     int64
	cur     int
	rc      io.ReadSeekCloser
}

func newConcatObjects(ctx context.Context, objs Objects, names []string) (*concatObjects, error) {
	c := &concatObjects{ctx: ctx, objs: objs, cur: -1}
	for _, n := range names {
		o, err := objs.OpenAudio(ctx, RecordingsBucket, n)
		if err != nil {
			if len(c.names) > 0 {
				break // a fragment that is not there ends the file early
			}
			return nil, err
		}
		_ = o.Body.Close()
		c.names = append(c.names, n)
		c.sizes = append(c.sizes, o.Size)
		c.size += o.Size
		if o.ModTime.After(c.modTime) {
			c.modTime = o.ModTime
		}
	}
	if len(c.names) == 0 {
		return nil, errors.New("api: no video objects")
	}
	return c, nil
}

func (c *concatObjects) Seek(off int64, whence int) (int64, error) {
	var np int64
	switch whence {
	case io.SeekStart:
		np = off
	case io.SeekCurrent:
		np = c.pos + off
	case io.SeekEnd:
		np = c.size + off
	default:
		return 0, errors.New("api: bad whence")
	}
	if np < 0 {
		return 0, errors.New("api: negative seek")
	}
	c.pos = np
	return np, nil
}

func (c *concatObjects) Read(p []byte) (int, error) {
	if c.pos >= c.size {
		return 0, io.EOF
	}
	idx, start := 0, int64(0)
	for i, n := range c.sizes {
		if c.pos < start+n {
			idx = i
			break
		}
		start += n
	}
	if c.cur != idx || c.rc == nil {
		if c.rc != nil {
			_ = c.rc.Close()
			c.rc = nil
		}
		o, err := c.objs.OpenAudio(c.ctx, RecordingsBucket, c.names[idx])
		if err != nil {
			return 0, err
		}
		c.rc, c.cur = o.Body, idx
	}
	if _, err := c.rc.Seek(c.pos-start, io.SeekStart); err != nil {
		return 0, err
	}
	if rest := c.sizes[idx] - (c.pos - start); int64(len(p)) > rest {
		p = p[:rest]
	}
	n, err := c.rc.Read(p)
	c.pos += int64(n)
	if errors.Is(err, io.EOF) && c.pos < c.size {
		err = nil
	}
	return n, err
}

func (c *concatObjects) Close() error {
	if c.rc != nil {
		return c.rc.Close()
	}
	return nil
}

// Settings.

type recordingSettingsBody struct {
	VideoRetentionDays *int `json:"videoRetentionDays"`
}

func (s *server) getRecordingSettings(w http.ResponseWriter, r *http.Request) {
	set, err := s.Store.GetRecordingSettings(r.Context())
	if err != nil {
		s.internal(w, "recording settings", err)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

func (s *server) putRecordingSettings(w http.ResponseWriter, r *http.Request) {
	var in recordingSettingsBody
	if !decode(w, r, &in) {
		return
	}
	if in.VideoRetentionDays == nil {
		badRequest(w, "videoRetentionDays is required")
		return
	}
	if d := *in.VideoRetentionDays; d < 1 || d > 3650 {
		badRequest(w, "videoRetentionDays must be between 1 and 3650")
		return
	}
	set, err := s.Store.SetVideoRetention(r.Context(), actor(r).String(), *in.VideoRetentionDays)
	if err != nil {
		s.configError(w, "recording settings", err)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

// videoStreamObjects is every object a stream may have.
func videoStreamObjects(v store.VideoStream) []string {
	out := []string{v.InitObject(), v.FinalObject()}
	for _, f := range v.SegmentList {
		out = append(out, f.Object)
	}
	return out
}
