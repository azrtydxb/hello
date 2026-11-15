package scim

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics counts SCIM requests (hello_scim_requests_total) by method,
// resource and status. Labels never carry a token, an id or a name.
type Metrics struct {
	Requests *prometheus.CounterVec
}

// NewMetrics registers the series on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_scim_requests_total",
		Help: "SCIM requests by method, resource (Users, Groups, discovery, other) and HTTP status.",
	}, []string{"method", "resource", "status"})}
	reg.MustRegister(m.Requests)
	return m
}

func (m *Metrics) request(method, resource string, status int) {
	if m == nil {
		return
	}
	m.Requests.WithLabelValues(method, resource, strconv.Itoa(status)).Inc()
}
