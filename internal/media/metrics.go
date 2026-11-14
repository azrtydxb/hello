// Media metrics (spec S-3, S-7): the RTP series of anchored sessions and
// the anchoring counters, registered on hello-sip's Prometheus registry.
package media

import (
	"sync/atomic"
	"time"

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
	// SRTPLegs is the number of call legs this node's anchor currently
	// encrypts with SRTP (SDES, phone-feature-parity S-15).
	SRTPLegs prometheus.Gauge
	// SRTPNegotiations counts leg negotiations by result: srtp (the leg is
	// encrypted), plain (crypto was not offered or the far end declined and
	// the policy allowed plain RTP), refused (the policy required SRTP and
	// the call was refused with 488).
	SRTPNegotiations *prometheus.CounterVec
	// DTLSHandshakes counts DTLS-SRTP handshakes by result (ok, timeout,
	// fingerprint, failed) and DTLSSeconds times the successful ones
	// (phone-feature-parity S-16).
	DTLSHandshakes *prometheus.CounterVec
	DTLSSeconds    prometheus.Histogram
	// ICEChecks counts the STUN binding requests ICE-lite received by
	// result (ok, bad_credentials, role_conflict).
	ICEChecks *prometheus.CounterVec
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
		SRTPLegs: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hello_srtp_legs",
			Help: "Call legs the anchor currently encrypts with SRTP.",
		}),
		SRTPNegotiations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_srtp_negotiations_total",
			Help: "SRTP leg negotiations by result (srtp, plain, refused).",
		}, []string{"result"}),
		DTLSHandshakes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_dtls_handshakes_total",
			Help: "DTLS-SRTP handshakes on the anchor by result (ok, timeout, fingerprint, failed).",
		}, []string{"result"}),
		DTLSSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "hello_dtls_handshake_seconds",
			Help:    "Duration of successful DTLS-SRTP handshakes.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 4, 8},
		}),
		ICEChecks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_ice_checks_total",
			Help: "STUN binding requests the ICE-lite agent received by result (ok, bad_credentials, role_conflict).",
		}, []string{"result"}),
		RecordingStorage: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hello_recording_storage_bytes",
			Help: "Recording audio bytes this node stored (cluster total lives in MinIO).",
		}),
	}
	reg.MustRegister(m.RTPSessions, m.AnchoredCalls, m.AnchorFailures,
		m.RTPPackets, m.RTPOctets, m.RTPLoss, m.RTPJitter, m.SRTPLegs, m.SRTPNegotiations,
		m.DTLSHandshakes, m.DTLSSeconds, m.ICEChecks, m.RecordingStorage)
	activeMetrics.Store(m)
	return m
}

// activeMetrics is the registered series the endpoint code counts into (the
// last Metrics made); nil in a process that never made one.
var activeMetrics atomic.Pointer[Metrics]

// noteDTLS counts one DTLS handshake.
func noteDTLS(result string, d time.Duration) {
	m := activeMetrics.Load()
	if m == nil {
		return
	}
	m.DTLSHandshakes.WithLabelValues(result).Inc()
	if result == "ok" {
		m.DTLSSeconds.Observe(d.Seconds())
	}
}

// noteICE counts one STUN check.
func noteICE(result string) {
	if m := activeMetrics.Load(); m != nil {
		m.ICEChecks.WithLabelValues(result).Inc()
	}
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
