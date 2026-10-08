package fakellm_test

import (
	"context"
	"testing"

	aisdk "github.com/azrtydxb/go-ai-sdk/ai"
	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/aifake"
	"github.com/azrtydxb/hello/test/fakellm"
)

// TestFakeLLM fails if the fixture does not drive the real provider path
// (provider.go, the privacy dialer on 127.0.0.1) through a tool call and a
// structured answer per feature, or does not record the requests.
func TestFakeLLM(t *testing.T) {
	llm := fakellm.New(t)
	cfg := aifake.Config()
	cfg.BaseURL = llm.URL
	s, err := ai.New(cfg, aifake.NewStore(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if on, reason := s.Enabled(); !on {
		t.Fatalf("off: %s", reason)
	}
	llm.Queue("assistant",
		fakellm.Reply{ToolCalls: []fakellm.ToolCall{{ID: "c1", Name: "listTrunks", Args: map[string]any{}}}},
		fakellm.JSON(map[string]any{"answer": "one trunk"}))
	llm.Queue("aiops_explain", fakellm.JSON(map[string]any{"answer": "explained"}))

	ran := 0
	tool := aisdk.NewTool("listTrunks", "List trunks", func(context.Context, struct{}) (any, error) {
		ran++
		return ai.DataBlock([]string{"carrier"}), nil
	})
	type out struct {
		Answer string `json:"answer"`
	}
	got, u, err := ai.Generate(context.Background(), s, ai.Call[out]{Feature: "assistant", Prompt: "trunks?", Tools: []aisdk.Tool{tool}})
	if err != nil || got.Answer != "one trunk" || ran != 1 || u.Calls != 2 {
		t.Fatalf("assistant = %+v, %+v, %v (tool ran %d)", got, u, err, ran)
	}
	got, _, err = ai.Generate(context.Background(), s, ai.Call[out]{Feature: "aiops_explain", Prompt: "explain", Background: true})
	if err != nil || got.Answer != "explained" {
		t.Fatalf("explain = %+v, %v", got, err)
	}
	reqs := llm.Requests()
	if len(reqs) != 3 || reqs[0].Feature != "assistant" || reqs[2].Feature != "aiops_explain" {
		t.Fatalf("requests = %+v", reqs)
	}
	if reqs[0].Body["response_format"] == nil || reqs[0].Body["tools"] == nil {
		t.Errorf("first request lacks response_format or tools: %v", reqs[0].Body)
	}
	if _, _, err := ai.Generate(context.Background(), s, ai.Call[out]{Feature: "unscripted", Prompt: "x"}); ai.CodeOf(err) != ai.CodeProviderError {
		t.Errorf("unscripted feature: %v", err)
	}
}
