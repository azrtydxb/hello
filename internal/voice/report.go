package voice

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/azrtydxb/hello/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics are the voice series of spec S-32 that the runtime owns:
// hello_voice_calls_total (from the report), the runtime's last seen and
// revision lag. Labels are fixed words, never a prompt, credential or
// transcript.
type Metrics struct {
	CallsTotal  *prometheus.CounterVec // agent, outcome
	LastSeen    prometheus.Gauge
	RevisionLag prometheus.Gauge
}

// NewMetrics registers the voice runtime metrics on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		CallsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_voice_calls_total",
			Help: "Calls to voice agents by agent and report outcome.",
		}, []string{"agent", "outcome"}),
		LastSeen: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hello_voice_runtime_last_seen_seconds",
			Help: "Seconds since talking-agent last acked; -1 when never.",
		}),
		RevisionLag: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hello_voice_runtime_revision_lag",
			Help: "How far the runtime's acked revision is behind the current one.",
		}),
	}
	if reg != nil {
		reg.MustRegister(m.CallsTotal, m.LastSeen, m.RevisionLag)
	}
	return m
}

func (m *Metrics) called(agent, outcome string) {
	if m != nil {
		m.CallsTotal.WithLabelValues(agent, outcome).Inc()
	}
}

func (m *Metrics) lastSeen(seconds float64) {
	if m != nil {
		m.LastSeen.Set(seconds)
	}
}

func (m *Metrics) lastSeenSeconds(at *time.Time, now time.Time) {
	if m == nil {
		return
	}
	if at == nil {
		m.LastSeen.Set(-1)
		return
	}
	m.LastSeen.Set(now.Sub(*at).Seconds())
}

func (m *Metrics) revisionLag(v float64) {
	if m != nil {
		m.RevisionLag.Set(v)
	}
}

// Report limits (spec S-21): a body above 128 KiB is refused 413, a
// transcript is at most 64 KiB and is dropped unless the agent records
// transcripts, and a summary is at most 2000 characters. A report is
// idempotent on its correlation id.
const (
	MaxReportBody      = 128 << 10
	MaxTranscript      = 64 << 10
	MaxSummary         = 2000
	MaxToolCallEntries = 200
	MaxCorrelation     = 128
)

// The outcomes of a call report, as the document's VoiceCallReport schema
// lists them.
const (
	OutcomeAnswered   = "answered"
	OutcomeUnanswered = "unanswered"
	OutcomeFailed     = "failed"
	OutcomeUnreported = "unreported"
)

// outcomes is the set of valid report outcomes.
var outcomes = map[string]bool{
	OutcomeAnswered:   true,
	OutcomeUnanswered: true,
	OutcomeFailed:     true,
	OutcomeUnreported: true,
}

// Errors Service.Report reports; match with errors.Is.
var (
	// ErrBadReport wraps every invalid report; the handler answers 400.
	ErrBadReport = errors.New("voice: invalid call report")
	// ErrReportTooLarge is a report past its size limits; the handler
	// answers 413.
	ErrReportTooLarge = errors.New("voice: call report too large")
)

// ToolCall is one tool call of a report.
type ToolCall struct {
	Tool      string         `json:"tool"`
	OK        bool           `json:"ok"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// CallReport is talking-agent's report for one call (spec S-21).
type CallReport struct {
	CorrelationID string     `json:"correlationId"`
	AgentName     string     `json:"agentName"`
	Outcome       string     `json:"outcome"`
	Summary       string     `json:"summary"`
	ToolCalls     []ToolCall `json:"toolCalls"`
	TokensIn      int64      `json:"tokensIn"`
	TokensOut     int64      `json:"tokensOut"`
	Transcript    string     `json:"transcript"`
	StartedAt     *time.Time `json:"-"`
	EndedAt       *time.Time `json:"-"`
}

// validate checks a report's fields. The transcript's size is not here: it
// is its own limit with its own error.
func (rep CallReport) validate() error {
	switch {
	case rep.CorrelationID == "":
		return fmt.Errorf("correlation id is required")
	case len(rep.CorrelationID) > MaxCorrelation:
		return fmt.Errorf("correlation id is over %d characters", MaxCorrelation)
	case rep.AgentName == "" || len(rep.AgentName) > 64:
		return fmt.Errorf("agent name is required and at most 64 characters")
	case !outcomes[rep.Outcome]:
		return fmt.Errorf("outcome %q is not one of answered, unanswered, failed, unreported", rep.Outcome)
	case len(rep.Summary) > MaxSummary:
		return fmt.Errorf("summary is over %d characters", MaxSummary)
	case len(rep.ToolCalls) > MaxToolCallEntries:
		return fmt.Errorf("%d tool calls, at most %d", len(rep.ToolCalls), MaxToolCallEntries)
	}
	for _, tc := range rep.ToolCalls {
		if tc.Tool == "" || len(tc.Tool) > 256 {
			return fmt.Errorf("tool call name %q", tc.Tool)
		}
	}
	return nil
}

// Report files a call report (spec S-21): it validates the sizes, drops the
// transcript unless the agent records transcripts, stores the report
// idempotently and counts the call. A report that arrives before hello-sip
// writes the CDR is stored on its correlation id until the join finds it;
// the store's INSERT ... ON CONFLICT DO NOTHING keeps a repeat report from
// overwriting the first one.
func (s *Service) Report(ctx context.Context, rep CallReport) error {
	if len(rep.Transcript) > MaxTranscript {
		return fmt.Errorf("%w: transcript is over %d KiB", ErrReportTooLarge, MaxTranscript>>10)
	}
	if err := rep.validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrBadReport, err)
	}
	policy, err := s.src.VoiceAgentPolicy(ctx, rep.AgentName)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrUnknownAgent
		}
		return err
	}
	if !policy.RecordTranscript {
		rep.Transcript = ""
	}
	saved, err := s.src.SaveVoiceCallReport(ctx, rep, s.now())
	if err != nil {
		return err
	}
	if saved {
		s.metrics.called(rep.AgentName, rep.Outcome)
	}
	return nil
}
