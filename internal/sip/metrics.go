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

	// Trunks (spec S-14). Status, registration, latency and active calls
	// come from the shared trunk state, so every node reports the same
	// values; TrunkCalls counts this node's trunk attempts by result.
	TrunkStatus         *prometheus.GaugeVec   // trunk, destination: 1 up, 0 down
	TrunkRegistered     *prometheus.GaugeVec   // trunk
	TrunkOptionsLatency *prometheus.GaugeVec   // trunk, destination: last OPTIONS round trip
	TrunkCalls          *prometheus.CounterVec // trunk, result
	TrunkActiveCalls    *prometheus.GaugeVec   // trunk: cluster-wide
	TrunkSlotOvercommit *prometheus.CounterVec // trunk: lost slots re-added over max_calls
	RouteDecision       prometheus.Histogram
}

// Trunk attempt results for hello_trunk_calls_total.
const (
	TrunkAnswered  = "answered"  // the trunk answered the call
	TrunkFailover  = "failover"  // a failover code or no answer: the next one was tried
	TrunkRejected  = "rejected"  // a final response that is not failed over (e.g. 486)
	TrunkCancelled = "cancelled" // the caller hung up during the attempt
	TrunkFull      = "full"      // skipped: concurrency limit reached
)

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
	m.TrunkStatus = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hello_trunk_status", Help: "Trunk destination health from OPTIONS: 1 up, 0 down.",
	}, []string{"trunk", "destination"})
	m.TrunkRegistered = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hello_trunk_registered", Help: "1 when the trunk's registration is active.",
	}, []string{"trunk"})
	m.TrunkOptionsLatency = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hello_trunk_options_latency_seconds", Help: "Round trip of the last OPTIONS to the destination.",
	}, []string{"trunk", "destination"})
	m.TrunkCalls = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_trunk_calls_total", Help: "Trunk call attempts by result.",
	}, []string{"trunk", "result"})
	m.TrunkActiveCalls = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hello_trunk_active_calls", Help: "Calls holding a slot on the trunk, cluster-wide.",
	}, []string{"trunk"})
	m.TrunkSlotOvercommit = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_trunk_slot_overcommit_total",
		Help: "Established calls whose lost trunk slot was re-added although the trunk was full.",
	}, []string{"trunk"})
	m.RouteDecision = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "hello_route_decision_seconds", Help: "Time to take a routing decision.",
		Buckets: []float64{.00001, .00005, .0001, .00025, .0005, .001, .0025, .005, .01},
	})
	reg.MustRegister(m.Registrations, m.ActiveCalls, m.Calls, m.Requests, m.Responses,
		m.TrunkStatus, m.TrunkRegistered, m.TrunkOptionsLatency, m.TrunkCalls, m.TrunkActiveCalls, m.TrunkSlotOvercommit, m.RouteDecision)
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
