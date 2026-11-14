package media

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

// Video session limit and series (S-41). The counters are process-wide atomics
// read by the metric functions, so they cost nothing on the packet path.
var videoGauge struct {
	sessions            atomic.Int64
	max                 atomic.Int64
	dropCap, dropOther  atomic.Uint64
	keyframes           atomic.Uint64
	negotiationFailures [5]atomic.Uint64
}

// DefaultVideoMaxSessions is HELLO_VIDEO_MAX_SESSIONS's default (S-41).
const DefaultVideoMaxSessions = 20

// SetVideoMaxSessions sets the per-node limit of calls that carry video
// (HELLO_VIDEO_MAX_SESSIONS); 0 or less means the default.
func SetVideoMaxSessions(n int) {
	if n <= 0 {
		n = DefaultVideoMaxSessions
	}
	videoGauge.max.Store(int64(n))
}

// AcquireVideoSession takes one slot of the node's video limit; false when the
// node already carries the most it may (the call then goes on as audio).
func AcquireVideoSession() bool {
	max := videoGauge.max.Load()
	if max == 0 {
		max = DefaultVideoMaxSessions
	}
	for {
		cur := videoGauge.sessions.Load()
		if cur >= max {
			return false
		}
		if videoGauge.sessions.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

// ReleaseVideoSession returns a slot taken by AcquireVideoSession.
func ReleaseVideoSession() {
	if videoGauge.sessions.Add(-1) < 0 {
		videoGauge.sessions.Store(0)
	}
}

// VideoSessions is how many calls carry video on this node.
func VideoSessions() int { return int(videoGauge.sessions.Load()) }

func noteVideoDrop(reason string) {
	if reason == "cap" {
		videoGauge.dropCap.Add(1)
		return
	}
	videoGauge.dropOther.Add(1)
}

func noteVideoKeyframeRequest() { videoGauge.keyframes.Add(1) }

// Why a video section was refused (hello_video_negotiation_failures_total).
const (
	VideoNoCodec = iota
	VideoNotAllowed
	VideoOverLimit
	VideoNoPeer
	VideoDisabled
)

var videoReasons = [...]string{"no_common_codec", "not_allowed", "over_limit", "peer_cannot", "disabled"}

// NoteVideoRefused counts a video section answered with port 0, by reason (one
// of the Video* constants).
func NoteVideoRefused(reason int) {
	if reason >= 0 && reason < len(videoGauge.negotiationFailures) {
		videoGauge.negotiationFailures[reason].Add(1)
	}
}

func registerVideoMetrics(reg prometheus.Registerer) {
	reg.MustRegister(
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "hello_video_sessions", Help: "Calls carrying video on this node.",
		}, func() float64 { return float64(videoGauge.sessions.Load()) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "hello_video_keyframe_requests_total", Help: "Picture loss and full intra requests the relay sent to video senders.",
		}, func() float64 { return float64(videoGauge.keyframes.Load()) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "hello_video_dropped_packets_total", Help: "Video packets the relay dropped above the per-call ceiling.", ConstLabels: prometheus.Labels{"reason": "cap"},
		}, func() float64 { return float64(videoGauge.dropCap.Load()) }),
	)
	for i, why := range videoReasons {
		reg.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "hello_video_negotiation_failures_total", Help: "Video sections answered with port 0, by reason.", ConstLabels: prometheus.Labels{"reason": why},
		}, func() float64 { return float64(videoGauge.negotiationFailures[i].Load()) }))
	}
}

// Video recording series (S-42).
var videoRecGauge struct {
	failures [3]atomic.Uint64
}

var videoRecReasons = [...]string{"object_store", "quota", "unsupported"}

func noteVideoRecFailure(reason string) {
	for i, r := range videoRecReasons {
		if r == reason {
			videoRecGauge.failures[i].Add(1)
			return
		}
	}
}

// NoteVideoRecFailure counts a video recording that did not start or stopped,
// by reason (object_store, quota, unsupported).
func NoteVideoRecFailure(reason string) { noteVideoRecFailure(reason) }

func registerVideoRecMetrics(reg prometheus.Registerer) {
	for i, why := range videoRecReasons {
		reg.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "hello_recording_video_failures_total", Help: "Video recordings that did not start or stopped early, by reason.", ConstLabels: prometheus.Labels{"reason": why},
		}, func() float64 { return float64(videoRecGauge.failures[i].Load()) }))
	}
}

// VideoRecFailures is how many video recordings failed for reason so far (tests
// and the status view).
func VideoRecFailures(reason string) uint64 {
	for i, r := range videoRecReasons {
		if r == reason {
			return videoRecGauge.failures[i].Load()
		}
	}
	return 0
}
