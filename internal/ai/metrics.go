package ai

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics are spec S-25's on the telemetry registry. Labels carry only
// features, outcomes, operation ids, agent and detector names, finding
// types and severities: never a prompt, response, tool result or key. The
// core moves its own; the assistant, proposal and detector packages call
// the methods below.
type Metrics struct {
	enabled     prometheus.Gauge
	calls       *prometheus.CounterVec
	callSeconds *prometheus.HistogramVec
	tokens      *prometheus.CounterVec
	budgetUsed  prometheus.Gauge
	busy        *prometheus.CounterVec
	toolCalls   *prometheus.CounterVec
	agentRuns   *prometheus.CounterVec
	findings    *prometheus.GaugeVec
	proposals   *prometheus.CounterVec
	detector    *prometheus.HistogramVec
}

func newMetrics(reg prometheus.Registerer) *Metrics {
	if reg == nil {
		reg = prometheus.NewRegistry()
	}
	m := &Metrics{
		enabled: prometheus.NewGauge(prometheus.GaugeOpts{Name: "hello_ai_enabled",
			Help: "1 when the in-product AI agent is on."}),
		calls: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "hello_ai_calls_total",
			Help: "Model calls (Generate) by feature and outcome (ok or an error code)."}, []string{"feature", "outcome"}),
		callSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "hello_ai_call_seconds",
			Help:    "Duration of a Generate call, retries included.",
			Buckets: []float64{0.5, 1, 2, 5, 10, 20, 40, 80, 160, 320}}, []string{"feature"}),
		tokens: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "hello_ai_tokens_total",
			Help: "Tokens by feature and kind (input, output, reasoning)."}, []string{"feature", "kind"}),
		budgetUsed: prometheus.NewGauge(prometheus.GaugeOpts{Name: "hello_ai_budget_used_ratio",
			Help: "Today's tokens over the daily budget, across replicas."}),
		busy: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "hello_ai_busy_total",
			Help: "Calls refused for lack of a slot or budget, by reason (concurrency, rate, budget, background_budget)."}, []string{"reason"}),
		toolCalls: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "hello_ai_tool_calls_total",
			Help: "Assistant tool calls by operation id and result."}, []string{"operation", "result"}),
		agentRuns: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "hello_ai_agent_runs_total",
			Help: "Background agent runs by agent and outcome."}, []string{"agent", "outcome"}),
		findings: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "hello_ai_findings",
			Help: "Findings by type, severity and status."}, []string{"type", "severity", "status"}),
		proposals: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "hello_ai_proposals_total",
			Help: "Proposal status changes by source kind and new status."}, []string{"source", "status"}),
		detector: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "hello_ai_detector_seconds",
			Help:    "Duration of one detector pass.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5}}, []string{"detector"}),
	}
	reg.MustRegister(m.enabled, m.calls, m.callSeconds, m.tokens, m.budgetUsed, m.busy,
		m.toolCalls, m.agentRuns, m.findings, m.proposals, m.detector)
	return m
}

// Enabled is the hello_ai_enabled gauge.
func (m *Metrics) Enabled() prometheus.Gauge { return m.enabled }

// ToolCall counts one assistant tool call; result is ok, error, denied or
// unknown_tool.
func (m *Metrics) ToolCall(operation, result string) {
	m.toolCalls.WithLabelValues(operation, result).Inc()
}

// SetFindings replaces the findings gauge with n per (type, severity,
// status).
func (m *Metrics) SetFindings(n map[[3]string]int) {
	m.findings.Reset()
	for k, v := range n {
		m.findings.WithLabelValues(k[0], k[1], k[2]).Set(float64(v))
	}
}

// Proposal counts a proposal reaching status; source is "assistant" or
// "finding" (the detector name stays out of the label set).
func (m *Metrics) Proposal(source, status string) {
	m.proposals.WithLabelValues(source, status).Inc()
}

// Detector records one detector pass.
func (m *Metrics) Detector(name string, d time.Duration) {
	m.detector.WithLabelValues(name).Observe(d.Seconds())
}
