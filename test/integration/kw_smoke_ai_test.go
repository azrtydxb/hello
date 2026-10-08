package integration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	aisdk "github.com/azrtydxb/go-ai-sdk/ai"
	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/aifake"
	"github.com/azrtydxb/hello/internal/ai/detect"
	"github.com/prometheus/client_golang/prometheus"
)

// smokeTool is a fixed read tool with no arguments.
type smokeTool struct{ calls atomic.Int32 }

func (*smokeTool) Name() string { return "listRegistrations" }
func (*smokeTool) Description() string {
	return "Lists the registered phones: extension number and contact."
}
func (*smokeTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}
func (*smokeTool) Strict() bool                             { return false }
func (*smokeTool) InputExamples() []json.RawMessage         { return nil }
func (*smokeTool) InputCallbacks() aisdk.ToolInputCallbacks { return aisdk.ToolInputCallbacks{} }
func (t *smokeTool) Execute(context.Context, json.RawMessage) (any, error) {
	t.calls.Add(1)
	return map[string]any{"items": []map[string]string{{"extension": "2041", "contact": "sip:2041@10.0.0.7:5060"}}}, nil
}

type smokeAnswer struct {
	Answer    string `json:"answer" jsonschema:"description=The answer as plain text."`
	Citations []int  `json:"citations" jsonschema:"description=Zero-based positions of the tool calls relied on."`
}

// TestKwSmokeAI (spec ai-agent S-2, S-27) drives the deployed settings
// (HELLO_AI_PROVIDER, _BASE_URL, _MODEL, _API_KEY, _STRUCTURED_OUTPUT,
// _TIMEOUT) against the real model on kw, through the same Generate the
// agent uses: a structured explanation, then a tool call with a final
// structured answer. It fails with tools_unsupported when the endpoint
// rejects tool calling. Run it from a pod on kw with HELLO_KW_SMOKE=1.
func TestKwSmokeAI(t *testing.T) {
	if os.Getenv("HELLO_KW_SMOKE") != "1" {
		t.Skip("HELLO_KW_SMOKE=1 not set")
	}
	cfg := aifake.Config()
	cfg.Provider = envOr("HELLO_AI_PROVIDER", "openai")
	cfg.BaseURL = os.Getenv("HELLO_AI_BASE_URL")
	cfg.Model = os.Getenv("HELLO_AI_MODEL")
	cfg.APIKey = os.Getenv("HELLO_AI_API_KEY")
	cfg.StructuredOutput = envOr("HELLO_AI_STRUCTURED_OUTPUT", "json_schema")
	cfg.Timeout = 180 * time.Second
	if cfg.BaseURL == "" || cfg.Model == "" {
		t.Fatal("HELLO_AI_BASE_URL and HELLO_AI_MODEL are required")
	}
	svc, err := ai.New(cfg, aifake.NewStore(), nil, prometheus.NewRegistry(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if on, reason := svc.Enabled(); !on {
		t.Fatalf("AI is off: %s", reason)
	}
	ctx := t.Context()

	t.Run("structured explanation", func(t *testing.T) {
		start := time.Now()
		got, usage, err := ai.Generate(ctx, svc, ai.Call[detect.Explanations]{
			Feature: "aiops_explain", Background: true,
			System: "You are the operations analyst of a small business PBX. For each finding give a short summary, the likely cause and one next step, and a rank (1 is most urgent). Use the finding's id exactly.",
			Data:   map[string]any{"findings": []map[string]any{{"id": "auth_bruteforce:203.0.113.9", "type": "auth_bruteforce", "severity": "warning", "title": "REGISTER flood from 203.0.113.9", "evidence": map[string]any{"failures": 42, "usernames": 9}}}},
			Prompt: "Explain the findings.",
			Validate: func(_ context.Context, e detect.Explanations) error {
				if len(e.Findings) != 1 || e.Findings[0].ID != "auth_bruteforce:203.0.113.9" || e.Findings[0].Summary == "" || e.Findings[0].Rank != 1 {
					return errors.New("answer exactly one finding with id auth_bruteforce:203.0.113.9, a summary and rank 1")
				}
				return nil
			},
		})
		if err != nil {
			t.Fatalf("explanation: %v", err)
		}
		t.Logf("explanation in %s, %+v tokens: %q", time.Since(start).Round(time.Millisecond), usage, got.Findings[0].Summary)
	})

	t.Run("tool call and final structured answer", func(t *testing.T) {
		tool := &smokeTool{}
		start := time.Now()
		got, usage, err := ai.Generate(ctx, svc, ai.Call[smokeAnswer]{
			Feature: "assistant",
			System:  "You are the assistant of a small business PBX. Answer from the tools, never from memory. Plain text only. Cite the positions of the tool calls you relied on.",
			Prompt:  "Which extensions are registered right now?",
			Tools:   []aisdk.Tool{tool},
			Validate: func(_ context.Context, a smokeAnswer) error {
				t.Logf("decoded answer %q citations %v after %d tool calls", a.Answer, a.Citations, tool.calls.Load())
				if tool.calls.Load() == 0 {
					return errors.New("call the tool listRegistrations before you answer")
				}
				if !strings.Contains(a.Answer, "2041") {
					return errors.New("the answer must name the registered extension from the tool result")
				}
				return nil
			},
		})
		var aerr *ai.Error
		if errors.As(err, &aerr) && aerr.Code == ai.CodeToolsUnsupported {
			t.Fatalf("tools_unsupported: the endpoint rejects tool calling: %v", aerr)
		}
		if err != nil {
			cause := error(nil)
			if aerr != nil {
				cause = aerr.Err
			}
			t.Fatalf("assistant: %v (cause: %v; tool calls made: %d)", err, cause, tool.calls.Load())
		}
		t.Logf("tool calls %d, answered in %s, %+v tokens: %q citations %v", tool.calls.Load(), time.Since(start).Round(time.Millisecond), usage, got.Answer, got.Citations)
	})
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
