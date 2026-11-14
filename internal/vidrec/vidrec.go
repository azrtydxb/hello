// Package vidrec is hello-control's side of video recording
// (clients-webrtc-mobile-video S-42): it finalizes the streams hello-sip
// recorded, ends the streams of a node that died, enforces the video retention
// and watches the storage the video uses. hello-sip stores an init segment and
// fragments as a call runs; here they become one file per stream with a seek
// index (sidx for MP4, Cues for WebM), the fragments are deleted, and the row
// says where the file is. Every pass is safe to run on every replica: finalizing
// claims a stream for ten minutes, and the sweeps run under an advisory lock.
package vidrec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

// Store is the part of the store the worker uses.
type Store interface {
	ClaimVideoStreams(ctx context.Context, n int) ([]store.VideoStream, error)
	FinishVideoStream(ctx context.Context, id int64, final string, bytes int64) error
	ReleaseVideoStream(ctx context.Context, id int64) error
	SweepVideoStreams(ctx context.Context, olderThan time.Duration) (int64, error)
	ExpiredVideoStreams(ctx context.Context, days, n int) ([]store.VideoStream, error)
	DeleteVideoStream(ctx context.Context, id int64) error
	VideoUsageBytes(ctx context.Context) (int64, error)
	GetRecordingSettings(ctx context.Context) (store.RecordingSettings, error)
	Audit(ctx context.Context, actor, action, resource, resourceID string) error
	TryLock(ctx context.Context, key string) (func(), bool, error)
}

// Objects is the recordings bucket.
type Objects interface {
	Open(ctx context.Context, object string) (io.ReadCloser, error)
	// Size is the size of an object, an error when it does not exist.
	Size(ctx context.Context, object string) (int64, error)
	Put(ctx context.Context, object string, r io.Reader, size int64) error
	Remove(ctx context.Context, object string) error
}

// Worker runs the video recording jobs.
type Worker struct {
	Store   Store
	Objects Objects
	Log     *slog.Logger
	// QuotaBytes is HELLO_RECORDING_VIDEO_QUOTA_GB in bytes (0 for none).
	QuotaBytes int64
	// Every is the pass interval (default 30 s); StaleAfter is how long a stream
	// that is still recording may go unwritten before it counts as dead (default
	// 10 minutes: fragments are stored every 10 s, so a live node never goes
	// that long).
	Every      time.Duration
	StaleAfter time.Duration
	Metrics    *Metrics

	lastPurge time.Time
	usage     atomic.Int64
}

// Usage is the video storage in use as of the last pass and the quota.
func (w *Worker) Usage() (used, quota int64) { return w.usage.Load(), w.QuotaBytes }

// Metrics are the worker's series.
type Metrics struct {
	Finalized *prometheus.CounterVec
	Purged    prometheus.Counter
}

// NewMetrics registers the series: hello_recording_video_bytes (what the video
// holds), hello_recording_video_quota_ratio (that over the quota, 0 without
// one), the finalizer's results and the purge's count.
func NewMetrics(reg prometheus.Registerer, usage func() (used int64, quota int64)) *Metrics {
	m := &Metrics{
		Finalized: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "hello_recording_video_finalized_total",
			Help: "Video streams the finalizer joined into one file, by result."}, []string{"result"}),
		Purged: prometheus.NewCounter(prometheus.CounterOpts{Name: "hello_recording_video_purged_total",
			Help: "Video streams removed by the retention job."}),
	}
	reg.MustRegister(m.Finalized, m.Purged,
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "hello_recording_video_bytes", Help: "Bytes the video recordings hold in the object store."},
			func() float64 { u, _ := usage(); return float64(u) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "hello_recording_video_quota_ratio", Help: "Video recording storage over its quota (0 without a quota); alert at 0.8."},
			func() float64 {
				u, q := usage()
				if q <= 0 {
					return 0
				}
				return float64(u) / float64(q)
			}))
	return m
}

func (w *Worker) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Run passes until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	every := w.Every
	if every <= 0 {
		every = 30 * time.Second
	}
	for {
		w.Pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// Pass is one round: end dead streams, finalize what has ended, purge what has
// expired (every ten minutes).
func (w *Worker) Pass(ctx context.Context) {
	stale := w.StaleAfter
	if stale <= 0 {
		stale = 10 * time.Minute
	}
	if unlock, ok, err := w.Store.TryLock(ctx, "hello:recording:video-sweep"); err == nil && ok {
		if n, err := w.Store.SweepVideoStreams(ctx, stale); err != nil {
			w.log().Warn("video recording: sweep", "error", err)
		} else if n > 0 {
			w.log().Info("video recording: ended streams of a node that stopped writing them", "count", n)
		}
		unlock()
	}
	for ctx.Err() == nil {
		streams, err := w.Store.ClaimVideoStreams(ctx, 4)
		if err != nil {
			w.log().Warn("video recording: claim", "error", err)
			break
		}
		if len(streams) == 0 {
			break
		}
		for _, s := range streams {
			w.finalize(ctx, s)
		}
	}
	if time.Since(w.lastPurge) >= 10*time.Minute {
		w.lastPurge = time.Now()
		w.Purge(ctx)
	}
	w.checkQuota(ctx)
}

// checkQuota warns at 80 percent of the quota.
func (w *Worker) checkQuota(ctx context.Context) {
	used, err := w.Store.VideoUsageBytes(ctx)
	if err != nil {
		return
	}
	w.usage.Store(used)
	if w.QuotaBytes <= 0 {
		return
	}
	switch {
	case used >= w.QuotaBytes:
		w.log().Error("video recording: the storage quota is used up; new video recordings do not start", "used_bytes", used, "quota_bytes", w.QuotaBytes)
	case used*10 >= w.QuotaBytes*8:
		w.log().Warn("video recording: the storage is at 80 percent of its quota", "used_bytes", used, "quota_bytes", w.QuotaBytes)
	}
}

func (w *Worker) result(r string) {
	if w.Metrics != nil {
		w.Metrics.Finalized.WithLabelValues(r).Inc()
	}
}

// finalize joins one stream. A fragment that is not there, or not the size the
// row says, ends the file at the fragment before it: what exists is kept.
func (w *Worker) finalize(ctx context.Context, s store.VideoStream) {
	log := w.log().With("stream", s.ID)
	frags, err := w.usable(ctx, s)
	if err != nil || len(frags) == 0 {
		log.Warn("video recording: nothing to finalize", "error", err)
		w.result("empty")
		_ = w.Store.ReleaseVideoStream(ctx, s.ID)
		return
	}
	init, err := w.read(ctx, s.InitObject())
	if err != nil {
		log.Warn("video recording: init segment unreadable", "error", err)
		w.result("failed")
		_ = w.Store.ReleaseVideoStream(ctx, s.ID)
		return
	}
	infos := make([]media.FragInfo, len(frags))
	var total uint64
	for i, f := range frags {
		ticks := f.Ticks
		if i+1 < len(frags) && frags[i+1].Base > f.Base {
			ticks = frags[i+1].Base - f.Base // the index is contiguous across a gap
		}
		infos[i] = media.FragInfo{Size: f.Bytes, Ticks: ticks, Key: f.Key, StartMs: f.StartMs}
		total += f.Ticks
	}
	var header []byte
	var write func(wr io.Writer) error
	open := func(i int) (io.ReadCloser, error) { return w.Objects.Open(ctx, frags[i].Object) }
	switch s.Container {
	case "mp4":
		header = media.MP4Header(init, infos)
		write = func(wr io.Writer) error { return media.WriteFinalMP4(wr, init, infos, open) }
	case "webm":
		durMs := float64(total) / 90
		header = media.WebMHeader(s.Width, s.Height, durMs, infos)
		write = func(wr io.Writer) error { return media.WriteFinalWebM(wr, s.Width, s.Height, durMs, infos, open) }
	default:
		w.result("failed")
		return
	}
	size := int64(len(header))
	for _, f := range frags {
		size += f.Bytes
	}
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(write(pw)) }()
	final := s.FinalObject()
	err = w.Objects.Put(ctx, final, pr, size)
	_ = pr.CloseWithError(err)
	if err != nil {
		log.Warn("video recording: joining the fragments failed", "error", err)
		w.result("failed")
		_ = w.Store.ReleaseVideoStream(ctx, s.ID)
		return
	}
	if got, err := w.Objects.Size(ctx, final); err != nil || got != size {
		log.Warn("video recording: the joined file is not the size expected", "want", size, "got", got, "error", err)
		_ = w.Objects.Remove(ctx, final)
		w.result("failed")
		_ = w.Store.ReleaseVideoStream(ctx, s.ID)
		return
	}
	if err := w.Store.FinishVideoStream(ctx, s.ID, final, size); err != nil {
		log.Warn("video recording: row not finished", "error", err)
		return
	}
	// The file is whole and recorded: the pieces go. A fragment that stays
	// behind costs space only; the purge removes it with the stream.
	for _, f := range frags {
		_ = w.Objects.Remove(ctx, f.Object)
	}
	_ = w.Objects.Remove(ctx, s.InitObject())
	w.result("ok")
}

// usable is the stream's fragments up to the first that is missing or wrong.
func (w *Worker) usable(ctx context.Context, s store.VideoStream) ([]store.VideoSegment, error) {
	var out []store.VideoSegment
	for _, f := range s.SegmentList {
		n, err := w.Objects.Size(ctx, f.Object)
		if err != nil || n != f.Bytes {
			break
		}
		out = append(out, f)
	}
	return out, nil
}

func (w *Worker) read(ctx context.Context, object string) ([]byte, error) {
	rc, err := w.Objects.Open(ctx, object)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	var b bytes.Buffer
	if _, err := io.Copy(&b, io.LimitReader(rc, 1<<20)); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Purge removes the streams that ended more than the retention ago: objects
// first, then rows. The audit event carries a count only.
func (w *Worker) Purge(ctx context.Context) int {
	unlock, ok, err := w.Store.TryLock(ctx, "hello:recording:video-purge")
	if err != nil || !ok {
		return 0
	}
	defer unlock()
	set, err := w.Store.GetRecordingSettings(ctx)
	if err != nil {
		w.log().Warn("video recording: retention unreadable", "error", err)
		return 0
	}
	removed := 0
	for ctx.Err() == nil {
		streams, err := w.Store.ExpiredVideoStreams(ctx, set.VideoRetentionDays, 50)
		if err != nil {
			w.log().Warn("video recording: list expired streams", "error", err)
			break
		}
		gone := 0
		for _, s := range streams {
			if err := w.RemoveObjects(ctx, s); err != nil {
				w.log().Warn("video recording: remove objects", "stream", s.ID, "error", err)
				continue // the row stays until its objects are gone
			}
			if err := w.Store.DeleteVideoStream(ctx, s.ID); err != nil {
				w.log().Warn("video recording: delete row", "stream", s.ID, "error", err)
				continue
			}
			gone++
		}
		removed += gone
		if gone == 0 || len(streams) < 50 {
			break
		}
	}
	if removed > 0 {
		if w.Metrics != nil {
			w.Metrics.Purged.Add(float64(removed))
		}
		if err := w.Store.Audit(ctx, "system", "recording.video.purge", "recording_streams", fmt.Sprint(removed)); err != nil {
			w.log().Warn("video recording: audit", "error", err)
		}
	}
	return removed
}

// RemoveObjects deletes every object of a stream; an object that is already
// gone is not an error (Objects.Remove ignores it).
func (w *Worker) RemoveObjects(ctx context.Context, s store.VideoStream) error {
	var errs []error
	for _, o := range StreamObjects(s) {
		if err := w.Objects.Remove(ctx, o); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// StreamObjects lists every object a stream may have: the init segment, the
// fragments, the finalized file.
func StreamObjects(s store.VideoStream) []string {
	out := []string{s.InitObject(), s.FinalObject()}
	if s.Final != "" && s.Final != s.FinalObject() {
		out = append(out, s.Final)
	}
	for _, f := range s.SegmentList {
		if strings.HasPrefix(f.Object, "rec/") {
			out = append(out, f.Object)
		}
	}
	return out
}
