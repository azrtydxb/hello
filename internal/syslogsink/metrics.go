package syslogsink

import "github.com/prometheus/client_golang/prometheus"

// Metrics are the sink's counters.
type Metrics struct {
	// Received counts lines accepted for processing (parsed, not empty),
	// and not empty.
	Received prometheus.Counter
	// Dropped counts lines refused by the per-phone rate cap.
	Dropped prometheus.Counter
	// Unmatched counts lines whose source address no phone owns (kept under
	// the address).
	Unmatched prometheus.Counter
}

// NewMetrics registers the sink's metrics on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Received: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "hello_syslog_lines_received_total", Help: "Syslog lines received from phones and other senders, after parsing.",
		}),
		Dropped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "hello_syslog_lines_dropped_total", Help: "Syslog lines dropped by the per-phone rate cap.",
		}),
		Unmatched: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "hello_syslog_lines_unmatched_total", Help: "Syslog lines from an address no phone owns, kept under that address.",
		}),
	}
	reg.MustRegister(m.Received, m.Dropped, m.Unmatched)
	return m
}
