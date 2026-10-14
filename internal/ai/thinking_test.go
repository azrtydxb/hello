package ai_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/aifake"
	"github.com/prometheus/client_golang/prometheus"
)

// bodyRecorder is an OpenAI-compatible endpoint that keeps every request
// body and answers {"answer":"ok"}, as one JSON reply or as an event stream.
type bodyRecorder struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (r *bodyRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	raw, _ := io.ReadAll(req.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	r.mu.Lock()
	r.bodies = append(r.bodies, m)
	r.mu.Unlock()
	if stream, _ := m["stream"].(bool); stream {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"id":"x","choices":[{"index":0,"delta":{"role":"assistant","content":"{\"answer\":\"ok\"}"}}]}`+"\n\n"+
			`data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`+"\n\n"+
			"data: [DONE]\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"id":"x","object":"chat.completion","choices":[{"index":0,"finish_reason":"stop",`+
		`"message":{"role":"assistant","content":"{\"answer\":\"ok\"}"}}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
}

// TestThinkingOption (spec S-4) fails if, with HELLO_AI_THINKING off (the
// default), a request to an openai provider lacks
// chat_template_kwargs.enable_thinking=false on either the GenerateText or
// the StreamText path, if it carries the field with thinking on or for the
// anthropic provider, or if reasoning_effort is ever sent.
func TestThinkingOption(t *testing.T) {
	type answer struct {
		Answer string `json:"answer"`
	}
	for _, tc := range []struct {
		name     string
		provider string
		thinking bool
		stream   bool
		want     bool // the request carries enable_thinking=false
	}{
		{"off generate", "openai", false, false, true},
		{"off stream", "openai", false, true, true},
		{"off default provider", "", false, false, true},
		{"on generate", "openai", true, false, false},
		{"on stream", "openai", true, true, false},
		{"anthropic off", "anthropic", false, false, false},
		{"anthropic off stream", "anthropic", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &bodyRecorder{}
			srv := httptest.NewServer(rec)
			t.Cleanup(srv.Close)
			cfg := cfgFor(srv.URL + "/v1")
			cfg.Provider, cfg.Thinking = tc.provider, tc.thinking
			cfg.StructuredOutput = "prompt"
			svc, err := ai.New(cfg, aifake.NewStore(), nil, prometheus.NewRegistry(), nil)
			if err != nil {
				t.Fatal(err)
			}
			call := ai.Call[answer]{Feature: "assistant", Prompt: "Is A up?"}
			if tc.stream {
				call.Stream = &ai.Stream{Field: "answer", Text: func(string) {}, Thinking: func() {}, Reset: func() {}}
			}
			_, _, err = ai.Generate(context.Background(), svc, call)
			if tc.provider != "anthropic" && err != nil {
				t.Fatal(err)
			}
			rec.mu.Lock()
			defer rec.mu.Unlock()
			if len(rec.bodies) == 0 {
				t.Fatal("no request reached the endpoint")
			}
			for _, b := range rec.bodies {
				kw, has := b["chat_template_kwargs"].(map[string]any)
				got := has && kw["enable_thinking"] == false
				if got != tc.want {
					t.Errorf("chat_template_kwargs = %v, want enable_thinking=false: %v", b["chat_template_kwargs"], tc.want)
				}
				if _, ok := b["reasoning_effort"]; ok {
					t.Error("reasoning_effort was sent")
				}
			}
		})
	}
}
