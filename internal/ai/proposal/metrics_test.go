package proposal_test

import (
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/aifake"
	"github.com/azrtydxb/hello/internal/ai/proposal"
	"github.com/azrtydxb/hello/internal/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestAIMetricsProposals (the proposal half of TestAIMetrics, spec S-25)
// wires the store's status hook to the service's metrics as hello-control
// does. It fails if storing, applying or dismissing a proposal does not
// move hello_ai_proposals_total for its source kind and new status.
func TestAIMetricsProposals(t *testing.T) {
	e := newEnv(t)
	s := e.seed()
	reg := prometheus.NewRegistry()
	svc, err := ai.New(config.AIAgent{}, aifake.NewStore(), nil, reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.ps.OnStatus = func(source string, st proposal.Status) {
		kind := source
		if strings.HasPrefix(source, "finding:") {
			kind = "finding"
		}
		svc.Metrics().Proposal(kind, string(st))
	}
	count := func(source, status string) float64 {
		t.Helper()
		mfs, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, mf := range mfs {
			if mf.GetName() != "hello_ai_proposals_total" {
				continue
			}
			for _, m := range mf.GetMetric() {
				l := map[string]string{}
				for _, p := range m.GetLabel() {
					l[p.GetName()] = p.GetValue()
				}
				if l["source"] == source && l["status"] == status {
					return m.GetCounter().GetValue()
				}
			}
		}
		return 0
	}
	rename := func(id, name string) proposal.Action {
		return act("updateExtension", map[string]string{"id": id}, map[string]any{"name": name})
	}

	applied := e.propose("assistant", rename(s.ext1, "Metered"))
	dismissed := e.propose("finding:trunk_down", rename(s.ext2, "Never"))
	if count("assistant", "open") != 1 || count("finding", "open") != 1 {
		t.Fatalf("stored proposals: open assistant %v, finding %v, want 1 each", count("assistant", "open"), count("finding", "open"))
	}
	e.must("operator", 200, "POST", "/api/v1/ai/proposals/"+applied+"/apply", nil)
	e.must("operator", 200, "POST", "/api/v1/ai/proposals/"+dismissed+"/dismiss", map[string]any{"reason": "wrong"})
	if count("assistant", "applied") != 1 {
		t.Errorf("hello_ai_proposals_total{assistant,applied} = %v after an apply, want 1", count("assistant", "applied"))
	}
	if count("finding", "dismissed") != 1 {
		t.Errorf("hello_ai_proposals_total{finding,dismissed} = %v after a dismiss, want 1", count("finding", "dismissed"))
	}
	if n := testutil.CollectAndCount(reg, "hello_ai_proposals_total"); n != 4 {
		t.Errorf("hello_ai_proposals_total has %d series, want 4", n)
	}
}
