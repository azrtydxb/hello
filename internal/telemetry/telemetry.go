// Package telemetry owns Hello's structured logging, secret redaction, and
// Prometheus metrics.
package telemetry

import (
	"io"
	"log/slog"
	"net/url"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// NewLogger returns a JSON logger tagged with the service and node.
func NewLogger(w io.Writer, level slog.Level, service, nodeID string) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(h).With("service", service, "node_id", nodeID)
}

// RedactURL replaces the passwords in a URL (e.g. a database DSN) with
// REDACTED. Anything that is not a URL with a scheme — including a
// keyword/value DSN like "host=db password=x" — is redacted entirely, since
// it may still carry a secret.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return "REDACTED"
	}
	if _, ok := u.User.Password(); ok {
		u.User = url.UserPassword(u.User.Username(), "REDACTED")
	}
	q := u.Query()
	for _, k := range []string{"password", "sslpassword"} {
		if q.Has(k) {
			q.Set(k, "REDACTED")
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// Metrics is a service's Prometheus registry plus the gauges every node has.
type Metrics struct {
	Registry  *prometheus.Registry
	NodeReady prometheus.Gauge
	// DependencyUp is 1 per dependency that passed its last readiness check.
	DependencyUp *prometheus.GaugeVec
}

// NewMetrics builds a registry with Go/process collectors, hello_build_info
// and hello_node_ready.
func NewMetrics(service, version, commit string) *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	build := prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "hello_build_info",
		Help:        "Build information; always 1.",
		ConstLabels: prometheus.Labels{"service": service, "version": version, "commit": commit},
	})
	build.Set(1)
	ready := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "hello_node_ready",
		Help: "1 when the node passes its readiness checks and is not draining.",
	})
	up := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hello_dependency_up",
		Help: "1 when the dependency passed the last readiness check, else 0.",
	}, []string{"dependency"})
	reg.MustRegister(build, ready, up)
	return &Metrics{Registry: reg, NodeReady: ready, DependencyUp: up}
}
