// Media metrics (spec S-3, S-7): the RTP series of anchored sessions and
// the anchoring counters, registered on hello-sip's Prometheus registry.
package media

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics are the anchored-media Prometheus series.
type Metrics struct {
	// RTPSessions is the number of live anchored RTP sessions on this
	// node.
	RTPSessions prometheus.Gauge
	// AnchoredCalls is the number of calls whose media this node anchors
	// (voicemail included).
	AnchoredCalls prometheus.Gauge
	// AnchorFailures counts anchored sessions that fell back to direct
	// media: a relay panic, or no RTP port to bind (spec S-3, failure
	// modes).
	AnchorFailures prometheus.Counter
	// RTPPackets, RTPOctets and RTPLoss are the live sessions'
	// per-direction counters; the direction label is "a>b" (caller's leg
	// to callee's leg).
	RTPPackets *prometheus.CounterVec // direction
	RTPOctets  *prometheus.CounterVec // direction
	RTPLoss    *prometheus.CounterVec // direction
	// RTPJitter is the RFC 3550 interarrival jitter estimate per
	// direction, observed once per stats interval per live session.
	RTPJitter *prometheus.HistogramVec
	// RecordingStorage is what this node stored (the cluster total lives
	// in MinIO, like voicemail's).
	RecordingStorage prometheus.Gauge
}

// NewMetrics registers the media series on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		RTPSessions: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hello_rtp_sessions",
			Help: "Live anchored RTP sessions on this node.",
		}),
		AnchoredCalls: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hello_media_anchored_calls",
			Help: "Calls whose media this node anchors (voicemail included).",
		}),
		AnchorFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "hello_media_anchor_failures_total",
			Help: "Anchored sessions that fell back to direct media (relay panic, port exhaustion).",
		}),
		RTPPackets: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "hello_rtp_packets_total",
				Help: "RTP packets relayed by anchored sessions, by direction.",
			},
			[]string{"direction"},
		),
		RTPOctets: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "hello_rtp_octets_total",
				Help: "RTP payload octets relayed by anchored sessions, by direction.",
			},
			[]string{"direction"},
		),
		RTPLoss: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "hello_rtp_loss_total",
				Help: "RTP sequence gaps counted as lost packets, by direction.",
			},
			[]string{"direction"},
		),
		RTPJitter: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "hello_rtp_jitter_ms",
				Help:    "RFC 3550 interarrival jitter estimate in ms, by direction.",
				Buckets: []float64{0.5, 1, 2, 5, 10, 20, 50, 100},
			},
			[]string{"direction"},
		),
		RecordingStorage: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hello_recording_storage_bytes",
			Help: "Recording audio bytes this node stored (cluster total lives in MinIO).",
		}),
	}
	reg.MustRegister(m.RTPSessions, m.AnchoredCalls, m.AnchorFailures,
		m.RTPPackets, m.RTPOctets, m.RTPLoss, m.RTPJitter, m.RecordingStorage)
	return m
}

// ObserveStats feeds one relay's snapshot into the RTP series; it runs on
// the relay's ticker and at close.
func (m *Metrics) ObserveStats(st RelayStats) {
	for dir, d := range st.Directions {
		m.RTPPackets.WithLabelValues(dir).Add(float64(d.Packets))
		m.RTPOctets.WithLabelValues(dir).Add(float64(d.Octets))
		m.RTPLoss.WithLabelValues(dir).Add(float64(d.Lost))
		m.RTPJitter.WithLabelValues(dir).Observe(d.JitterMs)
	}
}

// NoteStart/NoteEnd move the session and anchored-call gauges.
func (m *Metrics) NoteStart() { m.RTPSessions.Inc(); m.AnchoredCalls.Inc() }
func (m *Metrics) NoteEnd()   { m.RTPSessions.Dec(); m.AnchoredCalls.Dec() }
