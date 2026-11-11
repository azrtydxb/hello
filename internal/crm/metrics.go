package crm

import (
	"context"
	"errors"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics are the CRM series (spec Observability). A nil *Metrics is a
// no-op.
type Metrics struct {
	Requests    *prometheus.CounterVec
	LookupTime  *prometheus.HistogramVec
	Logs        *prometheus.GaugeVec
	Connections *prometheus.GaugeVec
}

// NewMetrics registers hello_crm_requests_total, hello_crm_lookup_seconds,
// hello_crm_logs and hello_crm_connections on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "hello_crm_requests_total",
			Help: "Calls to a CRM, by connector, operation (lookup, log, update, owner, refresh) and result (ok, error, auth)."}, []string{"connector", "op", "result"}),
		LookupTime: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "hello_crm_lookup_seconds",
			Help: "Time of a CRM contact lookup.", Buckets: []float64{.05, .1, .25, .5, 1, 1.5, 3}}, []string{"connector"}),
		Logs: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "hello_crm_logs",
			Help: "CRM logging jobs by connector and status."}, []string{"connector", "status"}),
		Connections: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "hello_crm_connections",
			Help: "CRM connections by connector and status."}, []string{"connector", "status"}),
	}
	reg.MustRegister(m.Requests, m.LookupTime, m.Logs, m.Connections)
	return m
}

func (m *Metrics) request(connector, op string, err error) {
	if m == nil {
		return
	}
	res := "ok"
	if err != nil {
		res = "error"
		if errors.Is(err, ErrAuth) {
			res = "auth"
		}
	}
	m.Requests.WithLabelValues(connector, op, res).Inc()
}

func (m *Metrics) lookup(connector string, d time.Duration) {
	if m != nil {
		m.LookupTime.WithLabelValues(connector).Observe(d.Seconds())
	}
}

// Refresh reads the job and connection counts into the gauges.
func (m *Metrics) Refresh(ctx context.Context, r Repo) {
	if m == nil {
		return
	}
	stats, err := r.Stats(ctx)
	if err != nil {
		return
	}
	m.Logs.Reset()
	m.Connections.Reset()
	for _, s := range stats {
		if s.Kind == "logs" {
			m.Logs.WithLabelValues(s.Connector, s.Status).Set(float64(s.N))
		} else {
			m.Connections.WithLabelValues(s.Connector, s.Status).Set(float64(s.N))
		}
	}
}
