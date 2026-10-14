package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/aifake"
	"github.com/azrtydxb/hello/internal/apispec"
)

// smokeBodies is realistic-size synthetic data for the read tools: regs
// registrations and extensions, six open findings.
func smokeBodies(regs int) map[string]string {
	now := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	var items []map[string]any
	for i := range regs {
		ext := fmt.Sprintf("%d", 100+i)
		items = append(items, map[string]any{
			"aor": "sip:" + ext + "-desk@pbx.example.lab", "extension": ext, "device": ext + "-desk",
			"contactUri": fmt.Sprintf("sip:%s-desk@10.20.%d.%d:5060;transport=udp;rinstance=a%07d", ext, i/200, i%200+10, i*7919),
			"source":     fmt.Sprintf("10.20.%d.%d:5060", i/200, i%200+10), "transport": "udp",
			"userAgent": "Yealink SIP-T54W 96.86.0.70", "path": []string{}, "receivedNode": fmt.Sprintf("hello-sip-%d", i%3),
			"expires": now.Add(time.Hour).Format(time.RFC3339), "updatedAt": now.Add(-time.Duration(i) * time.Minute).Format(time.RFC3339),
		})
	}
	regsBody, _ := json.Marshal(map[string]any{"items": items})
	var fs []map[string]any
	for i := range 6 {
		fs = append(fs, map[string]any{
			"id": fmt.Sprintf("8c7d%04d-1111-4222-8333-44445555%04d", i, i), "candidateId": fmt.Sprintf("device_unregistered:%d-desk", 120+i),
			"type": "device_unregistered", "subject": fmt.Sprintf("%d-desk", 120+i), "severity": []string{"warning", "critical", "info"}[i%3],
			"status": "open", "title": fmt.Sprintf("Device %d-desk has not registered for 7 days", 120+i),
			"evidence":    map[string]any{"lastRegistered": now.Add(-7 * 24 * time.Hour).Format(time.RFC3339), "extension": fmt.Sprintf("%d", 120+i)},
			"explanation": map[string]any{"summary": "The phone stopped registering.", "cause": "Powered off or unplugged.", "nextStep": "Check power and network."},
			"explained":   true, "rank": i + 1, "firstSeen": now.Add(-7 * 24 * time.Hour).Format(time.RFC3339), "lastSeen": now.Format(time.RFC3339),
			"occurrences": 40 + i, "severityHistory": []any{}, "acknowledgedBy": nil, "acknowledgedAt": nil, "dismissedBy": nil,
			"dismissedAt": nil, "dismissReason": nil, "resolvedAt": nil, "proposalId": nil,
		})
	}
	findBody, _ := json.Marshal(map[string]any{"items": fs, "healthScore": 3})
	var exts []map[string]any
	for i := range regs {
		exts = append(exts, map[string]any{"id": i + 1, "number": fmt.Sprintf("%d", 100+i), "name": fmt.Sprintf("Staff member %d", i),
			"createdAt": now.Format(time.RFC3339), "updatedAt": now.Format(time.RFC3339), "externalNumber": "", "dnd": false,
			"forwardAlways": "", "forwardBusy": "", "forwardNoAnswer": "", "voicemailEnabled": i%2 == 0, "recordDefault": false})
	}
	extBody, _ := json.Marshal(map[string]any{"items": exts})
	return map[string]string{
		"/api/v1/registrations": string(regsBody),
		"/api/v1/ai/findings":   string(findBody),
		"/api/v1/extensions":    string(extBody),
		"/api/v1/devices":       `{"items":[]}`,
		"/api/v1/trunks/status": `{"items":[{"id":1,"name":"carrier","state":"registered"}]}`,
		"/api/v1/calls":         `{"items":[]}`,
	}
}

// TestKwSmokeAssistant (spec ai-agent S-4, S-6) runs the assistant's
// message task against the deployed model with realistic-size tool results
// (HELLO_KW_SMOKE_REGS registrations, 60 by default), each question
// HELLO_KW_SMOKE_RUNS times, and fails unless every answer is valid. Run it from a pod on kw with
// HELLO_KW_SMOKE=1, the HELLO_AI_* settings and HELLO_KW_SMOKE_SPEC, the
// API document's path (internal/api/openapi.json by default).
func TestKwSmokeAssistant(t *testing.T) {
	if os.Getenv("HELLO_KW_SMOKE") != "1" {
		t.Skip("HELLO_KW_SMOKE=1 not set")
	}
	cfg := aifake.Config()
	cfg.Provider = "openai"
	cfg.BaseURL = os.Getenv("HELLO_AI_BASE_URL")
	cfg.Model = os.Getenv("HELLO_AI_MODEL")
	cfg.APIKey = os.Getenv("HELLO_AI_API_KEY")
	cfg.StructuredOutput = os.Getenv("HELLO_AI_STRUCTURED_OUTPUT")
	cfg.Timeout = 180 * time.Second
	cfg.Thinking = os.Getenv("HELLO_AI_THINKING") == "on"
	if cfg.BaseURL == "" || cfg.Model == "" {
		t.Fatal("HELLO_AI_BASE_URL and HELLO_AI_MODEL are required")
	}
	// The log shows each call and each rejected answer's class and reason.
	log := slog.New(slog.NewTextHandler(t.Output(), nil))
	svc, err := ai.New(cfg, aifake.NewStore(), nil, prometheus.NewRegistry(), log)
	if err != nil {
		t.Fatal(err)
	}
	regs, runs := 60, 1
	if v := os.Getenv("HELLO_KW_SMOKE_REGS"); v != "" {
		_, _ = fmt.Sscan(v, &regs)
	}
	if v := os.Getenv("HELLO_KW_SMOKE_RUNS"); v != "" {
		_, _ = fmt.Sscan(v, &runs)
	}
	questions := []string{
		"Which extensions are registered right now, and are there any open AI findings?",
		"Are any phones offline? List the open findings and which extensions are registered.",
		"How many devices are registered and on which nodes?",
		"Give me a health summary of the PBX: registrations, trunks and findings.",
		"Is extension 105 registered, and from which IP?",
	}
	var spec *apispec.Spec
	if p := os.Getenv("HELLO_KW_SMOKE_SPEC"); p != "" {
		spec = loadSpecAt(t, p)
	} else {
		spec = loadSpec(t)
	}
	valid, cited, total := 0, 0, 0
	for r := range runs {
		for qi, q := range questions {
			total++
			tasks := newFakeTasks()
			store := newMemStore(tasks)
			store.findings = []Finding{{ID: "8c7d0000-1111-4222-8333-444455550000", Type: "device_unregistered", Subject: "120-desk",
				Severity: "warning", Status: "open", Title: "Device 120-desk has not registered for 7 days", LastSeen: time.Now()}}
			a, err := New(Config{Store: store, Tasks: tasks, DataBlock: ai.DataBlock, API: &fakeAPI{bodies: smokeBodies(regs)}, Spec: spec,
				Generate: func(ctx context.Context, c ai.Call[Answer]) (Answer, ai.Usage, error) {
					return ai.Generate(ctx, svc, c)
				}})
			if err != nil {
				t.Fatal(err)
			}
			k := newSink(t.Context())
			go func() {
				for range k.ch {
				}
			}()
			t.Logf("=== run %d question %d: %s", r, qi, q)
			res, err := a.answer(t.Context(), 7, "00000000-0000-0000-0000-000000000001", fmt.Sprintf("task-%d-%d", r, qi),
				[]Message{{Role: RoleUser, Content: q}}, k)
			k.cancel()
			if err != nil {
				var e *ai.Error
				if errors.As(err, &e) && e.Err != nil {
					err = fmt.Errorf("%w (cause: %w)", err, e.Err)
				}
				t.Errorf("run %d question %d: %v", r, qi, err)
				continue
			}
			valid++
			if len(res.message.Citations) > 0 {
				cited++
			}
			t.Logf("RESULT OK: citations=%v calls=%d answer=%q", res.message.Citations, len(res.message.ToolCalls), res.message.Content)
		}
	}
	t.Logf("SUMMARY %d/%d valid, %d with citations", valid, total, cited)
}

func loadSpecAt(t *testing.T, path string) *apispec.Spec {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := apispec.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
