package prov

import "github.com/prometheus/client_golang/prometheus"

// Metrics are the provisioning metrics of spec S-17.
type Metrics struct {
	Requests      *prometheus.CounterVec // vendor, kind, result
	RenderSeconds prometheus.Histogram
	// Phones is set by hello-control from the inventory (never_fetched,
	// fetched, stale).
	Phones        *prometheus.GaugeVec
	RedirectOps   *prometheus.CounterVec // vendor, op, result
	FirmwareBytes *prometheus.CounterVec // vendor
	AuditDropped  prometheus.Counter
}

// NewMetrics registers the provisioning metrics on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_prov_requests_total", Help: "Provisioning requests by vendor, file kind and result.",
		}, []string{"vendor", "kind", "result"}),
		RenderSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "hello_prov_render_seconds", Help: "Time to render one provisioning file.",
			Buckets: []float64{.0005, .001, .0025, .005, .01, .02, .05, .1},
		}),
		Phones: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "hello_prov_phones", Help: "Phones by fetch state (never_fetched, fetched, stale).",
		}, []string{"state"}),
		RedirectOps: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_prov_redirect_ops_total", Help: "Vendor redirect-service operations by vendor, operation and result.",
		}, []string{"vendor", "op", "result"}),
		FirmwareBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_prov_firmware_bytes_total", Help: "Firmware bytes sent to phones.",
		}, []string{"vendor"}),
		AuditDropped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "hello_prov_audit_dropped_total", Help: "Fetch audit rows dropped because the queue was full or the database failed.",
		}),
	}
	reg.MustRegister(m.Requests, m.RenderSeconds, m.Phones, m.RedirectOps, m.FirmwareBytes, m.AuditDropped)
	return m
}

// RedirectOp counts one redirect-service operation (register, unregister,
// check, lookup) and its result (ok, failed, unsupported).
func (m *Metrics) RedirectOp(v Vendor, op, result string) {
	m.RedirectOps.WithLabelValues(string(v), op, result).Inc()
}

// SetPhones publishes the inventory's fetch states.
func (m *Metrics) SetPhones(neverFetched, fetched, stale int) {
	m.Phones.WithLabelValues("never_fetched").Set(float64(neverFetched))
	m.Phones.WithLabelValues("fetched").Set(float64(fetched))
	m.Phones.WithLabelValues("stale").Set(float64(stale))
}
