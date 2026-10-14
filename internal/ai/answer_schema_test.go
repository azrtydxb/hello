package ai_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/azrtydxb/go-ai-sdk/ai/aitest"
	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/aifake"
	"github.com/azrtydxb/hello/internal/ai/assistant"
	"github.com/azrtydxb/hello/internal/ai/detect"
)

// badTag has the jsonschema tag form jsonschema-go refuses ("WORD=..."),
// which assistant.Answer had: no schema can be built for it.
type badTag struct {
	Answer string `json:"answer" jsonschema:"description=The answer."`
}

// syncBuffer is a log sink safe for the service's goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// loggedService is aifake.Service with its log captured.
func loggedService(t *testing.T, mode string, responses ...*provider.Response) (*ai.Service, *aitest.MockModel, *syncBuffer) {
	t.Helper()
	cfg := aifake.Config()
	cfg.StructuredOutput = mode
	m := &aitest.MockModel{Responses: responses, Caps: provider.Capabilities{NativeJSON: true}}
	buf := &syncBuffer{}
	s, err := ai.NewWith(cfg, aifake.NewStore(), nil, prometheus.NewRegistry(),
		slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), ai.Options{Replica: "test", Model: m})
	if err != nil {
		t.Fatal(err)
	}
	return s, m, buf
}

// TestAnswerTypesHaveSchemas fails if an answer type Hello generates has
// no JSON schema: in the prompt mode the model is then never told the
// answer's properties (the assistant answered {"citations":[0,1]} with no
// "answer" and failed invalid_output after 3 attempts).
func TestAnswerTypesHaveSchemas(t *testing.T) {
	if err := ai.CheckSchema[assistant.Answer](); err != nil {
		t.Error(err)
	}
	if err := ai.CheckSchema[detect.Explanations](); err != nil {
		t.Error(err)
	}
	if err := ai.CheckSchema[badTag](); err == nil {
		t.Error("CheckSchema accepts a type jsonschema-go cannot describe")
	}
}

// TestAnswerPhaseSchema (spec S-4) fails if the prompt mode's answer phase
// does not show the model the answer's properties, if an object without
// "answer" (what the model on kw returned on 18k tokens of tool results)
// is accepted or rejected without naming the property, if a rejection is
// not logged at WARN with its class and reason, if that log line carries
// the model's or the user's text, or if a type without a schema reaches
// the model at all.
func TestAnswerPhaseSchema(t *testing.T) {
	ctx := context.Background()

	t.Run("prompt mode: the schema names answer; a citations-only object is rejected and logged", func(t *testing.T) {
		s, m, logs := loggedService(t, "prompt",
			aifake.ToolCall("c1", "listRegistrations", map[string]any{}),
			aifake.ToolCall("c2", "listAIFindings", map[string]any{}),
			aifake.Text("SECRET-PROSE: extension 2041 is registered; no findings are open."),
			aifake.Text(`{"citations":[0,1]}`),
			aifake.Text(`{"answer":"Extension 2041 is registered; no findings are open.","citations":[0,1]}`))
		runs := 0
		got, _, err := ai.Generate(ctx, s, ai.Call[assistant.Answer]{Feature: "assistant", System: "Answer about the PBX.",
			Prompt: "USER-QUESTION: which extensions are registered?", Tools: readTools(&runs, nil)})
		if err != nil || got.Answer == "" || len(got.Citations) != 2 {
			t.Fatalf("Generate = %+v, %v", got, err)
		}
		if len(m.Calls) != 5 {
			t.Fatalf("model calls %d, want 5", len(m.Calls))
		}
		first := m.Calls[3]
		for _, part := range []string{texts(first, provider.RoleSystem), texts(first, provider.RoleUser)} {
			if !strings.Contains(part, `"answer"`) || !strings.Contains(part, `"required":["answer","citations"]`) {
				t.Errorf("the answer phase does not show the answer's schema: %q", part)
			}
		}
		if !strings.Contains(texts(first, provider.RoleUser), "not only the citations") {
			t.Errorf("the answer request does not ask for the whole answer: %q", texts(first, provider.RoleUser))
		}
		retry := texts(m.Calls[4], provider.RoleUser)
		if !strings.Contains(retry, "Your answer was rejected") || !strings.Contains(retry, "answer") {
			t.Errorf("the retry does not say what was wrong: %q", retry)
		}
		out := logs.String()
		if !strings.Contains(out, "level=WARN") || !strings.Contains(out, `msg="ai: answer rejected"`) ||
			!strings.Contains(out, "attempt=1") || !strings.Contains(out, "class=schema") || !strings.Contains(out, "reason=schema") {
			t.Errorf("the rejection is not logged with its class: %s", out)
		}
		for _, leak := range []string{"SECRET-PROSE", "USER-QUESTION", "citations", "2041"} {
			if strings.Contains(out, leak) {
				t.Errorf("the log carries %q: %s", leak, out)
			}
		}
	})

	t.Run("a validator's reason is logged", func(t *testing.T) {
		s, _, logs := loggedService(t, "prompt",
			aifake.Text(`{"answer":"Done.","citations":[]}`),
			aifake.Text(`{"answer":"Done.","citations":[]}`))
		calls := 0
		_, _, err := ai.Generate(ctx, s, ai.Call[assistant.Answer]{Feature: "assistant", Prompt: "q",
			Validate: func(context.Context, assistant.Answer) error {
				calls++
				if calls == 1 {
					return ai.Reject("citation_out_of_range", errors.New("citation 4 names no tool call"))
				}
				return nil
			}})
		if err != nil {
			t.Fatal(err)
		}
		if out := logs.String(); !strings.Contains(out, "class=validator") || !strings.Contains(out, "reason=citation_out_of_range") ||
			strings.Contains(out, "names no tool call") {
			t.Errorf("validator rejection log: %s", out)
		}
	})

	t.Run("an answer type without a schema fails before any model call", func(t *testing.T) {
		for _, mode := range []string{"prompt", "json_schema"} {
			s, m, logs := loggedService(t, mode, aifake.Text(`{"answer":"x"}`))
			_, _, err := ai.Generate(ctx, s, ai.Call[badTag]{Feature: "assistant", Prompt: "q"})
			if ai.CodeOf(err) != ai.CodeInvalidOutput || len(m.Calls) != 0 {
				t.Errorf("%s: err %v after %d model calls", mode, err, len(m.Calls))
			}
			if !strings.Contains(logs.String(), "level=ERROR") {
				t.Errorf("%s: no error log: %s", mode, logs.String())
			}
		}
	})
}
