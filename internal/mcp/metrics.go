package mcp

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics are the hello_mcp_* series (spec S-20). Labels are bounded: the
// JSON-RPC method is one of the MCP methods or "other", and the tool is an
// operationId of the embedded document or "unknown".
type Metrics struct {
	// Requests counts POST /mcp requests by JSON-RPC method and result.
	Requests *prometheus.CounterVec
	// ToolCalls counts tool calls by tool and result.
	ToolCalls *prometheus.CounterVec
	// ToolSeconds is the duration of a tool call, replay included.
	ToolSeconds prometheus.Histogram
}

// NewMetrics creates the MCP series and registers them on reg (the
// internal/telemetry registry); a nil reg leaves them unregistered.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_mcp_requests_total",
			Help: "MCP requests by JSON-RPC method and result.",
		}, []string{"method", "result"}),
		ToolCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_mcp_tool_calls_total",
			Help: "MCP tool calls by tool and result.",
		}, []string{"tool", "result"}),
		ToolSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "hello_mcp_tool_call_seconds",
			Help:    "Duration of an MCP tool call, replay included.",
			Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
		}),
	}
	if reg != nil {
		reg.MustRegister(m.Requests, m.ToolCalls, m.ToolSeconds)
	}
	return m
}

// knownMethods are the JSON-RPC methods counted under their own name.
var knownMethods = map[string]bool{
	"initialize": true, "notifications/initialized": true, "server/discover": true,
	"ping": true, "tools/list": true, "tools/call": true,
	"resources/list": true, "resources/templates/list": true, "resources/read": true,
	"prompts/list": true, "prompts/get": true,
}

func methodLabel(m string) string {
	if knownMethods[m] {
		return m
	}
	return "other"
}

// resultLabel maps an HTTP status to the requests_total result.
func resultLabel(status int) string {
	switch {
	case status < 300:
		return "ok"
	case status == 401:
		return "unauthorized"
	case status == 403:
		return "forbidden"
	case status == 413:
		return "too_large"
	case status >= 500:
		return "error"
	}
	return "rejected"
}
