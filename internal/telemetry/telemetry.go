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

// RedactURL replaces the password in a URL (e.g. a database DSN) with
// REDACTED. Input that does not parse as a URL is redacted entirely, since
// it may still carry a secret.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "REDACTED"
	}
	if _, ok := u.User.Password(); ok {
		u.User = url.UserPassword(u.User.Username(), "REDACTED")
	}
	q := u.Query()
	if q.Has("password") {
		q.Set("password", "REDACTED")
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// Metrics is a service's Prometheus registry plus the gauges every node has.
type Metrics struct {
	Registry  *prometheus.Registry
	NodeReady prometheus.Gauge
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
	reg.MustRegister(build, ready)
	return &Metrics{Registry: reg, NodeReady: ready}
}
