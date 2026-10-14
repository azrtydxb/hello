package ai_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	aisdk "github.com/azrtydxb/go-ai-sdk/ai"
	"github.com/azrtydxb/go-ai-sdk/ai/aitest"
	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/aifake"
	"github.com/azrtydxb/hello/internal/config"
)

type streamRecorder struct {
	text     strings.Builder
	thinking int
	resets   int
}

func (r *streamRecorder) stream() *ai.Stream {
	return &ai.Stream{Field: "answer",
		Thinking: func() { r.thinking++ },
		Text:     func(d string) { r.text.WriteString(d) },
		Reset: func() {
			r.resets++
			r.text.Reset()
		}}
}

func streamService(t *testing.T, cfg config.AIAgent, streams ...[]provider.StreamPart) (*ai.Service, *aitest.MockModel) {
	t.Helper()
	s, m, _ := aifake.Service(t, cfg)
	m.Streams = streams
	return s, m
}

// TestGenerateStream (spec S-4) fails if a streamed call returns anything
// but what the same script returns without streaming, streams reasoning or
// the other properties as answer text, keeps the text of a tool-calling
// step or of a rejected answer, or does not work in both structured-output
// modes.
func TestGenerateStream(t *testing.T) {
	ctx := context.Background()
	final := answer{Answer: "Trunk \"A\" is up.\nAll good."}

	for _, mode := range []string{"json_schema", "prompt"} {
		t.Run(mode+": reasoning, answer deltas and the same result", func(t *testing.T) {
			cfg := aifake.Config()
			cfg.StructuredOutput = mode
			s, _ := streamService(t, cfg, aifake.StreamReasoning("Let me think.", aifake.StreamJSON(final, 3)))
			var rec streamRecorder
			got, u, err := ai.Generate(ctx, s, ai.Call[answer]{Feature: "assistant", Prompt: "Is A up?", Stream: rec.stream()})
			if err != nil || got != final {
				t.Fatalf("Generate = %+v, %v", got, err)
			}
			if rec.text.String() != final.Answer || rec.thinking != 1 || rec.resets != 0 {
				t.Errorf("streamed %q, thinking %d, resets %d", rec.text.String(), rec.thinking, rec.resets)
			}
			if strings.Contains(rec.text.String(), "think") {
				t.Errorf("reasoning reached the text: %q", rec.text.String())
			}
			if u.InputTokens != 10 || u.OutputTokens != 5 || u.Calls != 1 {
				t.Errorf("usage = %+v", u)
			}
		})
	}

	t.Run("inline think block is not text", func(t *testing.T) {
		s, _ := streamService(t, aifake.Config(),
			aifake.StreamText("<think>the \"answer\" is", " 3</think>", "```json\n", `{"answer":"Three"}`, "\n```"))
		var rec streamRecorder
		got, _, err := ai.Generate(ctx, s, ai.Call[answer]{Feature: "assistant", Prompt: "x", Stream: rec.stream()})
		if err != nil || got.Answer != "Three" || rec.text.String() != "Three" || rec.thinking != 1 {
			t.Fatalf("Generate = %+v, %v; streamed %q, thinking %d", got, err, rec.text.String(), rec.thinking)
		}
	})

	t.Run("the tool phase's text is never streamed", func(t *testing.T) {
		step1 := append([]provider.StreamPart{provider.TextDelta{Text: `{"answer":"Let me look`}},
			aifake.StreamToolCall("c1", "lookup", map[string]any{})...)
		s, _ := streamService(t, aifake.Config(), step1,
			aifake.StreamJSON(answer{Answer: "TOOL-PHASE"}, 5), aifake.StreamJSON(answer{Answer: "Looked up."}, 5))
		var rec streamRecorder
		calls := 0
		lookup := aisdk.NewTool("lookup", "Looks something up.", func(context.Context, struct{}) (any, error) {
			calls++
			return "found", nil
		})
		got, _, err := ai.Generate(ctx, s, ai.Call[answer]{Feature: "assistant", Prompt: "x", Stream: rec.stream(),
			Tools: []aisdk.Tool{lookup}})
		if err != nil || got.Answer != "Looked up." {
			t.Fatalf("Generate = %+v, %v", got, err)
		}
		if rec.text.String() != "Looked up." || rec.resets != 0 || calls != 1 {
			t.Errorf("streamed %q, resets %d, tool calls %d", rec.text.String(), rec.resets, calls)
		}
	})

	t.Run("a rejected answer is voided and asked again", func(t *testing.T) {
		s, _ := streamService(t, aifake.Config(),
			aifake.StreamJSON(answer{Answer: "bad"}, 4), aifake.StreamJSON(answer{Answer: "good"}, 4))
		var rec streamRecorder
		got, _, err := ai.Generate(ctx, s, ai.Call[answer]{Feature: "assistant", Prompt: "x", Stream: rec.stream(),
			Validate: func(_ context.Context, a answer) error {
				if a.Answer == "bad" {
					return errors.New("not good enough")
				}
				return nil
			}})
		if err != nil || got.Answer != "good" || rec.text.String() != "good" || rec.resets != 1 {
			t.Fatalf("Generate = %+v, %v; streamed %q, resets %d", got, err, rec.text.String(), rec.resets)
		}
	})

	t.Run("a stream that fails midway fails the call with a provider error", func(t *testing.T) {
		s, m := streamService(t, aifake.Config(), aifake.StreamJSON(final, 4)[:3])
		m.StreamErrs = []error{errors.New("connection reset")}
		var rec streamRecorder
		_, _, err := ai.Generate(ctx, s, ai.Call[answer]{Feature: "assistant", Prompt: "x", Stream: rec.stream()})
		if ai.CodeOf(err) != ai.CodeProviderError {
			t.Errorf("error = %v (%q)", err, ai.CodeOf(err))
		}
	})
}
