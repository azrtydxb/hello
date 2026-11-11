package messaging

import "github.com/prometheus/client_golang/prometheus"

// Metrics are the inbox's metrics (spec messaging-channels, Interfaces).
// Labels never carry a number, a name or text.
type Metrics struct {
	// Inbound counts texts a trunk delivered to the gateway by result:
	// stored, duplicate, not_text (a text that is not plain text) or failed.
	Inbound *prometheus.CounterVec // provider, result
	// OptOut counts opt-outs and opt-ins Hello recorded, by source.
	OptOut *prometheus.CounterVec // source
	// Purged counts what retention deleted.
	Purged *prometheus.CounterVec // kind
	// UnassignedOldest is how long the longest-waiting unassigned
	// conversation has waited; set by hello-control.
	UnassignedOldest prometheus.Gauge
	// Unassigned is the size of the pool; set by hello-control.
	Unassigned prometheus.Gauge
}

// NewMetrics registers the metrics on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Inbound: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_msg_inbound_total", Help: "Texts delivered to the messaging inbox by provider and result.",
		}, []string{"provider", "result"}),
		OptOut: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_msg_optout_total", Help: "Opt-outs and opt-ins recorded, by source.",
		}, []string{"source"}),
		Purged: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_msg_purged_total", Help: "Rows retention deleted, by kind.",
		}, []string{"kind"}),
		UnassignedOldest: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hello_msg_unassigned_oldest_seconds", Help: "How long the longest-waiting unassigned conversation has waited.",
		}),
		Unassigned: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hello_msg_unassigned", Help: "Active conversations nobody has taken.",
		}),
	}
	reg.MustRegister(m.Inbound, m.OptOut, m.Purged, m.UnassignedOldest, m.Unassigned)
	return m
}
