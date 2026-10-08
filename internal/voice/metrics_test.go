package voice

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestVoiceMetrics (spec S-32) fails if a call report, an ack or a status
// read does not move its metric.
func TestVoiceMetrics(t *testing.T) {
	t.Parallel()
	src := newFakeSource()
	svc, _ := newTestService(t, src)
	ctx := context.Background()

	// hello_voice_calls_total moves with each first report, by agent and
	// outcome.
	src.next = true
	if err := svc.Report(ctx, CallReport{CorrelationID: "c1", AgentName: "support",
		Outcome: OutcomeAnswered}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Report(ctx, CallReport{CorrelationID: "c2", AgentName: "support",
		Outcome: OutcomeFailed}); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(svc.metrics.CallsTotal.WithLabelValues("support", OutcomeAnswered)); got != 1 {
		t.Fatalf("hello_voice_calls_total{answered} = %g, want 1", got)
	}
	if got := testutil.ToFloat64(svc.metrics.CallsTotal.WithLabelValues("support", OutcomeFailed)); got != 1 {
		t.Fatalf("hello_voice_calls_total{failed} = %g, want 1", got)
	}
	// A repeat report does not count twice (idempotent, spec S-21).
	src.next = false
	if err := svc.Report(ctx, CallReport{CorrelationID: "c1", AgentName: "support",
		Outcome: OutcomeAnswered}); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(svc.metrics.CallsTotal.WithLabelValues("support", OutcomeAnswered)); got != 1 {
		t.Fatalf("hello_voice_calls_total{answered} = %g after the repeat, want 1", got)
	}

	// hello_voice_runtime_last_seen_seconds is zero right after an ack and
	// tracks the age afterwards.
	now := time.Now()
	clock := now
	svc2, _ := newTestService(t, src, WithNow(func() time.Time { return clock }))
	if err := svc2.Ack(ctx, 7, Ack{Revision: 3}); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(svc2.metrics.LastSeen); got != 0 {
		t.Fatalf("last seen = %g after the ack, want 0", got)
	}
	clock = now.Add(90 * time.Second)
	if _, err := svc2.Status(ctx); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(svc2.metrics.LastSeen); got < 89 || got > 91 {
		t.Fatalf("last seen = %g, want about 90", got)
	}

	// hello_voice_runtime_revision_lag is the gap to the acked revision.
	if got := testutil.ToFloat64(svc2.metrics.RevisionLag); got != 0 {
		t.Fatalf("revision lag = %g, want 0", got)
	}
	src.state = RuntimeState{Revision: 5, AckRevision: 2, HasEnabledAgents: false,
		LastSeen: &now}
	if _, err := svc2.Status(ctx); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(svc2.metrics.RevisionLag); got != 3 {
		t.Fatalf("revision lag = %g, want 3", got)
	}
}
