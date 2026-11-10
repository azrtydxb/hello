package chat

import "github.com/prometheus/client_golang/prometheus"

// Metrics are the chat listener's metrics (spec M-29a: every abuse control
// has one). Labels are small fixed sets; they never carry a visitor, a site
// key, an address or text.
type Metrics struct {
	Sessions        *prometheus.CounterVec // result
	Messages        *prometheus.CounterVec // result
	RateLimited     *prometheus.CounterVec // rule
	ChallengeRaised prometheus.Counter
	Rejected        *prometheus.CounterVec // reason: busy, disabled, bad_request
	Streams         prometheus.Gauge
}

// NewMetrics registers the metrics on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Sessions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_chat_sessions_total", Help: "Chat session calls by result.",
		}, []string{"result"}),
		Messages: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_chat_messages_total", Help: "Visitor messages by result.",
		}, []string{"result"}),
		RateLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_chat_rate_limited_total", Help: "Chat requests refused by a limit, by rule.",
		}, []string{"rule"}),
		ChallengeRaised: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "hello_chat_challenge_raised_total", Help: "Times a site's challenge difficulty was raised because of a flood.",
		}),
		Rejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_chat_rejected_total", Help: "Chat requests refused before any work, by reason.",
		}, []string{"reason"}),
		Streams: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hello_chat_streams", Help: "Open visitor event streams on this pod.",
		}),
	}
	reg.MustRegister(m.Sessions, m.Messages, m.RateLimited, m.ChallengeRaised, m.Rejected, m.Streams)
	return m
}
