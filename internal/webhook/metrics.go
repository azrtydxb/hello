package webhook

import "github.com/prometheus/client_golang/prometheus"

// Metrics are the webhook series (spec integrations, Observability).
type Metrics struct {
	Deliveries *prometheus.CounterVec
	Seconds    prometheus.Histogram
	Pending    prometheus.Gauge
	Dead       prometheus.Gauge
	Disabled   prometheus.Gauge
	Dropped    prometheus.Counter
}

// NewMetrics registers hello_webhook_deliveries_total{result},
// hello_webhook_delivery_seconds, hello_webhook_pending, hello_webhook_dead,
// hello_webhook_endpoints_disabled and hello_webhook_dead_dropped_total on
// reg. result is succeeded, retried, dead or gone, one per attempt.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Deliveries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_webhook_deliveries_total", Help: "Webhook delivery attempts, by result (succeeded, retried, dead, gone)."}, []string{"result"}),
		Seconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "hello_webhook_delivery_seconds", Help: "Duration of one webhook delivery attempt.",
			Buckets: []float64{.05, .1, .25, .5, 1, 2.5, 5, 10}}),
		Pending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hello_webhook_pending", Help: "Webhook deliveries waiting for a first attempt or a retry."}),
		Dead: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hello_webhook_dead", Help: "Webhook deliveries in the dead-letter state."}),
		Disabled: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hello_webhook_endpoints_disabled", Help: "Webhook endpoints that are switched off."}),
		Dropped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "hello_webhook_dead_dropped_total", Help: "Dead letters dropped because an endpoint held more than the cap."}),
	}
	reg.MustRegister(m.Deliveries, m.Seconds, m.Pending, m.Dead, m.Disabled, m.Dropped)
	return m
}

func (m *Metrics) result(o Outcome) {
	if m == nil {
		return
	}
	switch {
	case o == OutcomeSuccess:
		m.Deliveries.WithLabelValues("succeeded").Inc()
	case o == OutcomeRetry:
		m.Deliveries.WithLabelValues("retried").Inc()
	case o == OutcomeGone:
		m.Deliveries.WithLabelValues("gone").Inc()
	default:
		m.Deliveries.WithLabelValues("dead").Inc()
	}
}
