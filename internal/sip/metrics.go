package sip

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// Call results for hello_calls_total.
const (
	ResultAnswered    = "answered"
	ResultCancelled   = "cancelled"
	ResultBusy        = "busy"
	ResultNoAnswer    = "no_answer"
	ResultNotFound    = "not_found"
	ResultUnavailable = "unavailable"
	ResultFailed      = "failed"
)

// Metrics are the SIP node's Prometheus series.
type Metrics struct {
	// Registrations is the number of unexpired bindings in the cluster whose
	// last REGISTER was handled by this node (spec §8 receiving node).
	// Summing it across nodes gives the cluster's binding count.
	Registrations prometheus.Gauge
	// ActiveCalls is the number of calls whose dialogs this node owns.
	ActiveCalls prometheus.Gauge
	Calls       *prometheus.CounterVec // result
	Requests    *prometheus.CounterVec // method
	// Responses counts responses Hello sends (not the 100 Trying and CANCEL
	// handling the transaction layer generates on its own), by status code.
	Responses *prometheus.CounterVec // code
}

// NewMetrics registers the SIP series on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Registrations: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hello_sip_registrations",
			Help: "Unexpired registration bindings whose last REGISTER this node handled.",
		}),
		ActiveCalls: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hello_active_calls",
			Help: "Calls whose dialogs this node owns (ringing or connected).",
		}),
		Calls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_calls_total",
			Help: "Finished call attempts by result.",
		}, []string{"result"}),
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_sip_requests_total",
			Help: "SIP requests received, by method.",
		}, []string{"method"}),
		Responses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_sip_responses_total",
			Help: "SIP responses sent by Hello, by status code.",
		}, []string{"code"}),
	}
	reg.MustRegister(m.Registrations, m.ActiveCalls, m.Calls, m.Requests, m.Responses)
	return m
}

var knownMethods = map[string]bool{
	"REGISTER": true, "INVITE": true, "ACK": true, "BYE": true, "CANCEL": true,
	"OPTIONS": true, "UPDATE": true, "PRACK": true, "INFO": true, "MESSAGE": true,
	"SUBSCRIBE": true, "NOTIFY": true, "REFER": true, "PUBLISH": true,
}

// methodLabel bounds the label's cardinality.
func methodLabel(m string) string {
	if knownMethods[m] {
		return m
	}
	return "other"
}

func (m *Metrics) request(method string) { m.Requests.WithLabelValues(methodLabel(method)).Inc() }

func (m *Metrics) response(code int) { m.Responses.WithLabelValues(strconv.Itoa(code)).Inc() }
