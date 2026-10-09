package voice

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/store"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestVoiceCallReport(t *testing.T) {
	t.Parallel()
	src := newFakeSource()
	svc, _ := newTestService(t, src)

	// A report before its CDR exists is stored (spec S-21); the fake has
	// no CDR either, and the join is the store's job.
	rep := CallReport{
		CorrelationID: "abc-123", AgentName: "support", Outcome: OutcomeAnswered,
		Summary: "reset a password", Transcript: "caller: ...\nagent: ...",
		ToolCalls: []ToolCall{{Tool: "crm.lookup", OK: true}},
		TokensIn:  120, TokensOut: 45,
	}
	src.next = true
	src.policy = AgentPolicy{RecordTranscript: true}
	if err := svc.Report(context.Background(), rep); err != nil {
		t.Fatal(err)
	}
	if len(src.saved) != 1 {
		t.Fatalf("%d reports saved, want 1", len(src.saved))
	}
	if got := src.saved[0].Transcript; got == "" {
		t.Fatal("transcript dropped while the agent records transcripts")
	}
	if src.saved[0].StartedAt != nil {
		t.Fatal("StartedAt must not be serialised into the report JSON")
	}
	if got := testutil.ToFloat64(
		svc.metrics.CallsTotal.WithLabelValues("support", OutcomeAnswered)); got != 1 {
		t.Fatalf("hello_voice_calls_total = %g, want 1", got)
	}

	// Idempotent: the repeat report does not overwrite the first one and
	// does not count twice (spec S-21).
	src.next = false
	rep2 := rep
	rep2.Summary = "a second, different summary"
	if err := svc.Report(context.Background(), rep2); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(
		svc.metrics.CallsTotal.WithLabelValues("support", OutcomeAnswered)); got != 1 {
		t.Fatalf("hello_voice_calls_total = %g after the repeat, want 1", got)
	}
}

func TestVoiceCallReportValidation(t *testing.T) {
	t.Parallel()
	src := newFakeSource()
	svc, _ := newTestService(t, src)
	src.next = true

	src.policyErr = store.ErrNotFound
	cases := []struct {
		name string
		rep  CallReport
		want error
	}{
		{"unknown agent", CallReport{CorrelationID: "c", AgentName: "ghost", Outcome: OutcomeAnswered}, ErrUnknownAgent},
		{"unknown agent (policy error)", CallReport{CorrelationID: "c", AgentName: "support", Outcome: OutcomeAnswered}, ErrUnknownAgent},
		{"no correlation id", CallReport{AgentName: "support", Outcome: OutcomeAnswered}, ErrBadReport},
		{"outcome", CallReport{CorrelationID: "c", AgentName: "support", Outcome: "perfect"}, ErrBadReport},
		{"summary", CallReport{CorrelationID: "c", AgentName: "support", Outcome: OutcomeAnswered,
			Summary: strings.Repeat("x", 2001)}, ErrBadReport},
		{"transcript size", CallReport{CorrelationID: "c", AgentName: "support", Outcome: OutcomeAnswered,
			Transcript: strings.Repeat("x", MaxTranscript+1)}, ErrReportTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := svc.Report(context.Background(), tc.rep); !errors.Is(err, tc.want) {
				t.Fatalf("Report = %v, want %v", err, tc.want)
			}
		})
	}

	// Transcript dropped when the agent does not record transcripts
	// (spec S-21, S-23). The policy error is back to nil.
	src.policyErr = nil
	src.policy = AgentPolicy{RecordTranscript: false}
	src.next = true
	rep := CallReport{CorrelationID: "c2", AgentName: "support", Outcome: OutcomeAnswered,
		Transcript: "spoken words"}
	if err := svc.Report(context.Background(), rep); err != nil {
		t.Fatal(err)
	}
	if got := src.saved[len(src.saved)-1].Transcript; got != "" {
		t.Fatalf("transcript stored without record_transcript: %q", got)
	}
}
