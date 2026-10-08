package ai_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/go-ai-sdk/ai/aitest"
	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/aifake"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestAIMetrics (core half of spec S-25) fails if a call, a refused call,
// tokens or an agent run do not move their metric, or if the API key, a
// prompt, data or a response appears in a log line or a metric.
func TestAIMetrics(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	var logs bytes.Buffer
	cfg := aifake.Config()
	cfg.APIKey = "sk-NEVER-SHOWN-123"
	cfg.RequestsPerMinute = 1
	m := &aitest.MockModel{Responses: []*provider.Response{
		aifake.Text("RESPONSE-MARKER not json"), aifake.JSON(answer{Answer: "RESPONSE-MARKER"}),
	}, Caps: provider.Capabilities{NativeJSON: true}}
	s, err := ai.NewWith(cfg, aifake.NewStore(), nil, reg, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		ai.Options{Replica: "r", Model: m})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ai.Generate(ctx, s, ai.Call[answer]{Feature: "assistant", System: "SYSTEM-MARKER", Prompt: "PROMPT-MARKER",
		Data: map[string]string{"ua": "DATA-MARKER"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ai.Generate(ctx, s, ai.Call[answer]{Feature: "assistant", Prompt: "q", Background: true}); ai.CodeOf(err) != ai.CodeBusy {
		t.Fatalf("second start in the minute: %v", err)
	}
	a := &countAgent{name: "count", interval: time.Minute}
	s.Scheduler.Register(a)
	if _, err := s.Scheduler.RequestRun(ctx, "count", 0); err != nil {
		t.Fatal(err)
	}
	s.Scheduler.Tick(ctx)

	checks := []struct {
		name string
		got  float64
		want float64
	}{
		{"enabled", testutil.ToFloat64(s.Metrics().Enabled()), 1},
		{"calls ok", counter(t, reg, "hello_ai_calls_total", "feature", "assistant", "outcome", "ok"), 1},
		{"calls busy", counter(t, reg, "hello_ai_calls_total", "feature", "assistant", "outcome", "ai_busy"), 1},
		{"busy rate", counter(t, reg, "hello_ai_busy_total", "reason", "rate"), 1},
		{"input tokens", counter(t, reg, "hello_ai_tokens_total", "feature", "assistant", "kind", "input"), 20},
		{"output tokens", counter(t, reg, "hello_ai_tokens_total", "feature", "assistant", "kind", "output"), 10},
		{"agent runs", counter(t, reg, "hello_ai_agent_runs_total", "agent", "count", "outcome", "no_change"), 1},
		{"call seconds", float64(testutil.CollectAndCount(reg, "hello_ai_call_seconds")), 1},
		{"budget ratio", gaugeAbove0(t, reg, "hello_ai_budget_used_ratio"), 1},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	s.Metrics().ToolCall("listTrunks", "ok")
	s.Metrics().Proposal("assistant", "open")
	s.Metrics().Detector("trunk_down", time.Millisecond)
	s.Metrics().SetFindings(map[[3]string]int{{"trunk_down", "critical", "open"}: 2})
	if testutil.CollectAndCount(reg, "hello_ai_tool_calls_total", "hello_ai_proposals_total", "hello_ai_detector_seconds", "hello_ai_findings") != 4 {
		t.Error("the helper metrics did not move")
	}

	var exposed bytes.Buffer
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		exposed.WriteString(mf.String())
	}
	for _, secret := range []string{"sk-NEVER", "PROMPT-MARKER", "SYSTEM-MARKER", "DATA-MARKER", "RESPONSE-MARKER"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("a log line carries %s: %s", secret, logs.String())
		}
		if strings.Contains(exposed.String(), secret) {
			t.Errorf("a metric carries %s", secret)
		}
	}
	if !strings.Contains(logs.String(), "feature=assistant") || !strings.Contains(logs.String(), "error_code=ai_busy") {
		t.Errorf("logs lack feature or error code: %s", logs.String())
	}
}

func counter(t *testing.T, reg *prometheus.Registry, name string, labels ...string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
	metric:
		for _, m := range mf.GetMetric() {
			got := map[string]string{}
			for _, l := range m.GetLabel() {
				got[l.GetName()] = l.GetValue()
			}
			for i := 0; i+1 < len(labels); i += 2 {
				if got[labels[i]] != labels[i+1] {
					continue metric
				}
			}
			return m.GetCounter().GetValue()
		}
	}
	return 0
}

func gaugeAbove0(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name && len(mf.GetMetric()) == 1 && mf.GetMetric()[0].GetGauge().GetValue() > 0 {
			return 1
		}
	}
	return 0
}
