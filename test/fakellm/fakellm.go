// Package fakellm is a scripted OpenAI-compatible endpoint for integration
// tests (plan ai-agent, Constraints): /v1/chat/completions answers each
// request from the reply queue of its feature (the "hello-feature: <name>"
// line Generate starts every system prompt with) and records the request.
// It runs in-process on httptest; nothing is deployed.
package fakellm

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// ToolCall is a scripted call of a tool.
type ToolCall struct {
	ID, Name string
	Args     any
}

// Reply is one scripted completion: text, or tool calls.
type Reply struct {
	Content   string
	ToolCalls []ToolCall
	// FinishReason defaults to "stop", or "tool_calls" with tool calls.
	FinishReason string
	// Tokens are the prompt and completion tokens reported (default 10, 5).
	PromptTokens, CompletionTokens int
	// Status, when set, answers with this HTTP status and an error body.
	Status int
}

// JSON is a reply whose content is v as JSON.
func JSON(v any) Reply {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return Reply{Content: string(b)}
}

// Request is a recorded request: its feature and decoded body.
type Request struct {
	Feature string
	Body    map[string]any
}

// Server is the fixture.
type Server struct {
	// URL is the base URL for HELLO_AI_BASE_URL (ending in /v1).
	URL string

	mu     sync.Mutex
	queues map[string][]Reply
	reqs   []Request
}

// New starts a fixture, closed when t ends.
func New(t testing.TB) *Server {
	return NewOn(t, "127.0.0.1:0")
}

// NewOn is New listening on addr, for a service in a container that reaches
// the test through the host's address (the lab's hello-control). URL still
// names the loopback address; the caller supplies the one the service uses.
func NewOn(t testing.TB, addr string) *Server {
	s := &Server{queues: map[string][]Reply{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.complete)
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatalf("fakellm: listen %s: %v", addr, err)
	}
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	s.URL = srv.URL + "/v1"
	return s
}

// Queue appends replies to feature's queue.
func (s *Server) Queue(feature string, replies ...Reply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queues[feature] = append(s.queues[feature], replies...)
}

// Requests returns the requests so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.reqs...)
}

// Pending is how many replies of feature are still queued.
func (s *Server) Pending(feature string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queues[feature])
}

// feature reads "hello-feature: <name>" from the system message.
func feature(body map[string]any) string {
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if mm["role"] != "system" {
			continue
		}
		text := ""
		switch c := mm["content"].(type) {
		case string:
			text = c
		case []any:
			for _, p := range c {
				if pm, ok := p.(map[string]any); ok {
					t, _ := pm["text"].(string)
					text += t
				}
			}
		}
		if rest, ok := strings.CutPrefix(text, "hello-feature: "); ok {
			name, _, _ := strings.Cut(rest, "\n")
			return name
		}
	}
	return ""
}

func (s *Server) complete(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f := feature(body)
	s.mu.Lock()
	s.reqs = append(s.reqs, Request{Feature: f, Body: body})
	q := s.queues[f]
	var reply Reply
	ok := len(q) > 0
	if ok {
		reply, s.queues[f] = q[0], q[1:]
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if !ok || reply.Status != 0 {
		status, msg := reply.Status, "scripted failure"
		if !ok {
			status, msg = http.StatusInternalServerError, fmt.Sprintf("no scripted reply for feature %q", f)
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "type": "fakellm"}})
		return
	}
	msg := map[string]any{"role": "assistant", "content": reply.Content}
	finish := reply.FinishReason
	if len(reply.ToolCalls) > 0 {
		var calls []map[string]any
		for _, c := range reply.ToolCalls {
			args, _ := json.Marshal(c.Args)
			calls = append(calls, map[string]any{"id": c.ID, "type": "function",
				"function": map[string]any{"name": c.Name, "arguments": string(args)}})
		}
		msg["tool_calls"] = calls
		if finish == "" {
			finish = "tool_calls"
		}
	}
	if finish == "" {
		finish = "stop"
	}
	pt, ct := reply.PromptTokens, reply.CompletionTokens
	if pt == 0 && ct == 0 {
		pt, ct = 10, 5
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "fake", "object": "chat.completion", "model": body["model"],
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
		"usage":   map[string]any{"prompt_tokens": pt, "completion_tokens": ct, "total_tokens": pt + ct},
	})
}
