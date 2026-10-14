package detect

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/aifake"
	"github.com/azrtydxb/hello/internal/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestAIMetricsFindings (the detector half of TestAIMetrics, spec S-25)
// runs the aiops agent with the service's metrics against PostgreSQL. It
// fails if a run does not observe hello_ai_detector_seconds for every
// detector, or a finding appearing or resolving does not move
// hello_ai_findings.
func TestAIMetricsFindings(t *testing.T) {
	env, live := testEnv(t)
	reg := prometheus.NewRegistry()
	svc, err := ai.New(config.AIAgent{}, aifake.NewStore(), nil, reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := t0
	env.Now = func() time.Time { return now }
	a := NewAIOps(env, Options{Interval: time.Minute, ExplainMinInterval: time.Minute, Metrics: svc.Metrics()})
	run := func() {
		t.Helper()
		if _, err := a.Run(context.Background()); err != nil && !errors.Is(err, ErrValkeyUnavailable) {
			t.Fatalf("run: %v", err)
		}
	}
	gauge := func() float64 {
		t.Helper()
		mfs, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, mf := range mfs {
			if mf.GetName() != "hello_ai_findings" {
				continue
			}
			for _, m := range mf.GetMetric() {
				l := map[string]string{}
				for _, p := range m.GetLabel() {
					l[p.GetName()] = p.GetValue()
				}
				if l["type"] == "trunk_down" && l["severity"] == "critical" && l["status"] == "open" {
					return m.GetGauge().GetValue()
				}
			}
		}
		return 0
	}

	// A trunk down for 5 minutes: one open critical trunk_down finding.
	id := addTrunk(t, env.DB, "carrier", "registration", 0, true)
	live.trunks[id] = trunkStatusFailed()
	exec(t, env.DB, `INSERT INTO ai_samples (kind, subject, at, value) VALUES ('trunk_state', 'carrier', $1, '{"down":true}')`, now.Add(-5*time.Minute))
	run()
	if got := gauge(); got != 1 {
		t.Fatalf("hello_ai_findings{trunk_down,critical,open} = %v after the finding appeared, want 1", got)
	}
	if n := testutil.CollectAndCount(reg, "hello_ai_detector_seconds"); n != len(Detectors()) {
		t.Errorf("hello_ai_detector_seconds has %d series, want one per detector (%d)", n, len(Detectors()))
	}

	// The trunk recovers and stays absent past 30 minutes: resolved, the
	// gauge drops.
	delete(live.trunks, id)
	exec(t, env.DB, `DELETE FROM trunks`)
	now = t0.Add(31 * time.Minute)
	run()
	if got := gauge(); got != 0 {
		t.Errorf("hello_ai_findings{trunk_down,critical,open} = %v after the finding resolved, want 0", got)
	}
}
