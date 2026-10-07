package oauth

import "github.com/prometheus/client_golang/prometheus"

// Metrics are the hello_oauth_* series of spec S-20. Labels are fixed
// words (grant types, failure reasons, fetch results), never a client id,
// token or code.
type Metrics struct {
	TokensIssued  *prometheus.CounterVec // grant
	TokenFailures *prometheus.CounterVec // reason
	CIMDFetches   *prometheus.CounterVec // result
}

// NewMetrics registers the OAuth metrics on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		TokensIssued: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_oauth_tokens_issued_total", Help: "OAuth access tokens issued by grant type.",
		}, []string{"grant"}),
		TokenFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_oauth_token_failures_total", Help: "Refused token endpoint requests by OAuth error.",
		}, []string{"reason"}),
		CIMDFetches: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_oauth_cimd_fetches_total", Help: "Client ID metadata document fetches by result.",
		}, []string{"result"}),
	}
	reg.MustRegister(m.TokensIssued, m.TokenFailures, m.CIMDFetches)
	return m
}

func (m *Metrics) issued(grant string) {
	if m != nil {
		m.TokensIssued.WithLabelValues(grant).Inc()
	}
}

func (m *Metrics) failed(reason string) {
	if m != nil {
		m.TokenFailures.WithLabelValues(reason).Inc()
	}
}

func (m *Metrics) fetched(result string) {
	if m != nil {
		m.CIMDFetches.WithLabelValues(result).Inc()
	}
}
