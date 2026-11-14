package router

import "github.com/prometheus/client_golang/prometheus"

// Metrics are the router's metrics (spec messaging-channels, Interfaces).
// Labels never carry a number, a name or text; the queue name is
// configuration.
type Metrics struct {
	// Assign counts what the router did: placed (a new conversation given
	// its route), assigned (a waiting conversation given to an agent), full
	// (an agent was at capacity when offered one), lost (another pod got
	// there first), returned (a conversation went back to the pool),
	// after_hours_reply (a closed-hours text was sent).
	Assign *prometheus.CounterVec // result
	// FirstResponse is the time from a conversation's start to the first
	// reply of an agent.
	FirstResponse prometheus.Histogram
	// QueueWaiting is how many conversations wait unassigned in a text queue.
	QueueWaiting *prometheus.GaugeVec // queue
	// LiveDown counts sweeps that could not read the agents' state and used
	// static members only.
	LiveDown prometheus.Counter
}

// NewMetrics registers the metrics on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Assign: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_msg_assign_total", Help: "Routing and assignment outcomes of the messaging inbox.",
		}, []string{"result"}),
		FirstResponse: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "hello_msg_first_response_seconds", Help: "Time from a conversation's start to an agent's first reply.",
			Buckets: []float64{15, 30, 60, 120, 300, 600, 1800, 3600, 14400, 86400},
		}),
		QueueWaiting: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "hello_msg_queue_text_waiting", Help: "Conversations waiting unassigned in a text queue.",
		}, []string{"queue"}),
		LiveDown: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "hello_msg_assign_live_down_total", Help: "Sweeps that could not read agent availability and used static members only.",
		}),
	}
	reg.MustRegister(m.Assign, m.FirstResponse, m.QueueWaiting, m.LiveDown)
	return m
}
