package sso

import "github.com/prometheus/client_golang/prometheus"

// Metrics are the sign-in counters (spec Observability).
type Metrics struct {
	OIDCLogins *prometheus.CounterVec
	BindLogins *prometheus.CounterVec
}

// NewMetrics registers the counters on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		OIDCLogins: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_oidc_logins_total", Help: "OIDC sign-ins by provider and result.",
		}, []string{"provider", "result"}),
		BindLogins: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_ldap_bind_logins_total", Help: "LDAP bind logins by result.",
		}, []string{"result"}),
	}
	if reg != nil {
		reg.MustRegister(m.OIDCLogins, m.BindLogins)
	}
	return m
}
