package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	aisdk "github.com/azrtydxb/go-ai-sdk/ai"
	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/aifake"
	"github.com/azrtydxb/hello/internal/ai/assistant"
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
	// HELLO_AI_THINKING is off unless the run sets it to on, as in hello-control.
	cfg.Thinking = os.Getenv("HELLO_AI_THINKING") == "on"
	t.Logf("HELLO_AI_THINKING on = %v", cfg.Thinking)
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

	t.Run("tools, then the split structured answer", func(t *testing.T) {
		// The assistant's own path: its system prompt and answer type, the
		// tool phase and the answer phase of Generate, and a validator
		// that, like the assistant's, only checks citations against the
		// calls made: it never tells the model to call a tool.
		run := smokeRun.Add(1)
		regs := &smokeTool{}
		findings := &smokeFindings{}
		tools := []aisdk.Tool{regs, findings}
		made := func() int { return int(regs.calls.Load() + findings.calls.Load()) }
		start := time.Now()
		got, usage, err := ai.Generate(ctx, svc, ai.Call[assistant.Answer]{
			Feature:  assistant.Feature,
			System:   assistant.SystemPrompt,
			Data:     map[string]any{"history": []any{}, "findings": []any{}},
			Prompt:   fmt.Sprintf("Which extensions are registered right now, and are there any open AI findings? (question %d)", run),
			Tools:    tools,
			MaxSteps: assistant.DefaultMaxSteps,
			Validate: func(_ context.Context, a assistant.Answer) error {
				for _, c := range a.Citations {
					if c < 0 || c >= made() {
						return fmt.Errorf("citation %d names no tool call: this message made %d", c, made())
					}
				}
				if strings.TrimSpace(a.Answer) == "" {
					return errors.New("answer is empty")
				}
				return nil
			},
		})
		latency := time.Since(start).Round(time.Millisecond)
		var aerr *ai.Error
		if errors.As(err, &aerr) && aerr.Code == ai.CodeToolsUnsupported {
			t.Fatalf("tools_unsupported: the endpoint rejects tool calling: %v", aerr)
		}
		called := "no"
		if made() > 0 {
			called = "yes"
		}
		t.Logf("KWSMOKE run=%d tool_called=%s calls=%d (listRegistrations=%d listAIFindings=%d) model_calls=%d reasoning_tokens=%d latency=%s",
			run, called, made(), regs.calls.Load(), findings.calls.Load(), usage.Calls, usage.ReasoningTokens, latency)
		if err != nil {
			cause := error(nil)
			if aerr != nil {
				cause = aerr.Err
			}
			t.Fatalf("assistant: %v (cause: %v; tool calls made: %d)", err, cause, made())
		}
		t.Logf("answer %q citations %v", got.Answer, got.Citations)
		if regs.calls.Load() == 0 {
			t.Errorf("the model never called listRegistrations")
		}
		// The validator kept every citation within the calls made.
		if len(got.Citations) == 0 {
			t.Errorf("the answer cites no tool call: %v", got.Citations)
		}
		if !strings.Contains(got.Answer, "2041") {
			t.Errorf("the answer does not name the registered extension 2041: %q", got.Answer)
		}
	})
}

// smokeRun numbers the subtest's runs so each prompt differs (fastllm
// may answer an identical request from a cache).
var smokeRun atomic.Int32

// smokeFindings is a fixed listAIFindings with no open findings.
type smokeFindings struct{ calls atomic.Int32 }

func (*smokeFindings) Name() string        { return "listAIFindings" }
func (*smokeFindings) Description() string { return "Lists the AI findings, open ones first." }
func (*smokeFindings) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}
func (*smokeFindings) Strict() bool                             { return false }
func (*smokeFindings) InputExamples() []json.RawMessage         { return nil }
func (*smokeFindings) InputCallbacks() aisdk.ToolInputCallbacks { return aisdk.ToolInputCallbacks{} }
func (t *smokeFindings) Execute(context.Context, json.RawMessage) (any, error) {
	t.calls.Add(1)
	return map[string]any{"items": []any{}}, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
