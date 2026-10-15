package ai_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	aisdk "github.com/azrtydxb/go-ai-sdk/ai"
	"github.com/azrtydxb/go-ai-sdk/ai/aitest"
	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/aifake"
	"github.com/prometheus/client_golang/prometheus"
)

// choice is a call's tool_choice mode, "" when none is sent.
func choice(c provider.Call) provider.ToolChoiceMode {
	if c.ToolChoice == nil {
		return ""
	}
	return c.ToolChoice.Mode
}

// toolNames are the names of the tools a call offers.
func toolNames(c provider.Call) []string {
	out := make([]string, 0, len(c.Tools))
	for _, t := range c.Tools {
		out = append(out, t.Name)
	}
	return out
}

// refusingModel is an endpoint without tool_choice "required": it answers
// such a request 400 and passes every other one to the mock.
type refusingModel struct {
	*aitest.MockModel
	refused int
}

func (m *refusingModel) Generate(ctx context.Context, c provider.Call) (*provider.Response, error) {
	if choice(c) == provider.ToolChoiceRequired {
		m.refused++
		return nil, &aisdk.APICallError{StatusCode: 400, Message: `"required" is not a supported tool_choice`}
	}
	return m.MockModel.Generate(ctx, c)
}

// TestGenerateRequireTool fails if a RequireTool call does not send
// tool_choice "required" on the tool phase's first step and "auto" on the
// later ones, sends any tool_choice to the answer phase or without
// RequireTool, does not offer NoDataTool, lists a NoDataTool call among the
// calls an answer may cite, does not nudge a tool phase that made no call,
// or keeps sending "required" to an endpoint that refuses it.
func TestGenerateRequireTool(t *testing.T) {
	ctx := context.Background()
	final := cited{Answer: "Extension 2041 is registered.", Citations: []int{0}}

	t.Run("the first step requires a tool, later steps are auto", func(t *testing.T) {
		for _, streamed := range []bool{false, true} {
			s, m, _ := aifake.Service(t, aifake.Config())
			if streamed {
				m.Streams = [][]provider.StreamPart{
					aifake.StreamToolCall("c1", "listRegistrations", map[string]any{}),
					aifake.StreamText("2041 is registered."),
					aifake.StreamJSON(final, 8),
				}
			} else {
				m.Responses = []*provider.Response{
					aifake.ToolCall("c1", "listRegistrations", map[string]any{}),
					aifake.Text("2041 is registered."),
					aifake.JSON(final),
				}
			}
			runs := 0
			call := ai.Call[cited]{Feature: "assistant", Prompt: "Is 2041 registered?", Tools: readTools(&runs, nil),
				RequireTool: true, Validate: citesMade(&runs)}
			if streamed {
				call.Stream = (&streamRecorder{}).stream()
			}
			got, _, err := ai.Generate(ctx, s, call)
			if err != nil || got.Answer != final.Answer || runs != 1 || len(m.Calls) != 3 {
				t.Fatalf("streamed %v: Generate = %+v, %v; runs %d, model calls %d", streamed, got, err, runs, len(m.Calls))
			}
			if c := choice(m.Calls[0]); c != provider.ToolChoiceRequired {
				t.Errorf("streamed %v: first step tool_choice %q", streamed, c)
			}
			if c := choice(m.Calls[1]); c != provider.ToolChoiceAuto {
				t.Errorf("streamed %v: second step tool_choice %q", streamed, c)
			}
			if names := toolNames(m.Calls[0]); len(names) != 3 || names[2] != ai.NoDataTool {
				t.Errorf("streamed %v: tool phase offers %v", streamed, names)
			}
			if !strings.Contains(texts(m.Calls[0], provider.RoleSystem), ai.NoDataTool) {
				t.Errorf("streamed %v: the tool phase's instructions do not name %s", streamed, ai.NoDataTool)
			}
			if last := m.Calls[2]; choice(last) != "" || len(last.Tools) != 0 {
				t.Errorf("streamed %v: answer phase tool_choice %q, %d tools", streamed, choice(last), len(last.Tools))
			}
		}
	})

	t.Run("a question that needs no data takes no_data_needed", func(t *testing.T) {
		s, m, _ := aifake.Service(t, aifake.Config(),
			aifake.ToolCall("n1", ai.NoDataTool, map[string]any{}),
			aifake.Text("I answer questions about your PBX from its live data."),
			aifake.JSON(cited{Answer: "I answer questions about your PBX.", Citations: []int{}}))
		runs := 0
		got, _, err := ai.Generate(ctx, s, ai.Call[cited]{Feature: "assistant", Prompt: "What can you do?",
			Tools: readTools(&runs, nil), RequireTool: true, Validate: citesMade(&runs)})
		if err != nil || got.Answer == "" || runs != 0 || len(m.Calls) != 3 {
			t.Fatalf("Generate = %+v, %v; runs %d, model calls %d", got, err, runs, len(m.Calls))
		}
		if choice(m.Calls[0]) != provider.ToolChoiceRequired || choice(m.Calls[1]) != provider.ToolChoiceAuto {
			t.Errorf("tool_choice %q then %q", choice(m.Calls[0]), choice(m.Calls[1]))
		}
		if !hasRole(m.Calls[1], provider.RoleTool) || !strings.Contains(texts(m.Calls[1], provider.RoleTool), "TOOLRESULT") {
			t.Error("no_data_needed got no result")
		}
		user := texts(m.Calls[2], provider.RoleUser)
		if strings.Contains(user, ai.NoDataTool) || strings.Contains(user, `"position"`) {
			t.Errorf("the answer request lists no_data_needed: %q", user)
		}
		if strings.Contains(texts(m.Calls[1], provider.RoleUser), "without calling a tool") {
			t.Error("a no_data_needed call was nudged")
		}
	})

	t.Run("a tool phase without any call is nudged once, not forced again", func(t *testing.T) {
		s, m, _ := aifake.Service(t, aifake.Config(),
			aifake.Text("Extension 2041 is registered."), // the endpoint ignored "required"
			aifake.ToolCall("c1", "listRegistrations", map[string]any{}),
			aifake.Text("2041 is registered."),
			aifake.JSON(final))
		runs := 0
		got, _, err := ai.Generate(ctx, s, ai.Call[cited]{Feature: "assistant", Prompt: "Is 2041 registered?",
			Tools: readTools(&runs, nil), RequireTool: true, Validate: citesMade(&runs)})
		if err != nil || got.Answer != final.Answer || runs != 1 || len(m.Calls) != 4 {
			t.Fatalf("Generate = %+v, %v; runs %d, model calls %d", got, err, runs, len(m.Calls))
		}
		if !strings.Contains(texts(m.Calls[1], provider.RoleUser), "without calling a tool") {
			t.Errorf("not nudged: %q", texts(m.Calls[1], provider.RoleUser))
		}
		for i, want := range []provider.ToolChoiceMode{provider.ToolChoiceRequired, provider.ToolChoiceAuto, provider.ToolChoiceAuto} {
			if c := choice(m.Calls[i]); c != want {
				t.Errorf("tool phase call %d tool_choice %q, want %q", i, c, want)
			}
		}
	})

	t.Run("an endpoint that refuses required is asked without it, then nudged", func(t *testing.T) {
		m := &refusingModel{MockModel: &aitest.MockModel{Caps: provider.Capabilities{NativeJSON: true}, Responses: []*provider.Response{
			aifake.ToolCall("c1", "listRegistrations", map[string]any{}),
			aifake.Text("2041 is registered."),
			aifake.JSON(final),
			aifake.ToolCall("c2", "listRegistrations", map[string]any{}),
			aifake.Text("2041 is registered."),
			aifake.JSON(final),
		}}}
		s, err := ai.NewWith(aifake.Config(), aifake.NewStore(), nil, prometheus.NewRegistry(), slog.New(slog.DiscardHandler),
			ai.Options{Replica: "test", Model: m})
		if err != nil {
			t.Fatal(err)
		}
		for i := range 2 {
			runs := 0
			got, _, err := ai.Generate(ctx, s, ai.Call[cited]{Feature: "assistant", Prompt: "Is 2041 registered?",
				Tools: readTools(&runs, nil), RequireTool: true, Validate: citesMade(&runs)})
			if err != nil || got.Answer != final.Answer || runs != 1 {
				t.Fatalf("message %d: Generate = %+v, %v; runs %d", i, got, err, runs)
			}
		}
		if m.refused != 1 {
			t.Errorf("required sent %d times to an endpoint that refused it once", m.refused)
		}
		for i, c := range m.Calls {
			if choice(c) == provider.ToolChoiceRequired {
				t.Errorf("call %d sent required past the refusal", i)
			}
		}
	})

	t.Run("without RequireTool no tool_choice and no extra tool", func(t *testing.T) {
		s, m, _ := aifake.Service(t, aifake.Config(),
			aifake.Text("Hello."),
			aifake.JSON(cited{Answer: "Hello.", Citations: []int{}}))
		runs := 0
		if _, _, err := ai.Generate(ctx, s, ai.Call[cited]{Feature: "assistant", Prompt: "hi",
			Tools: readTools(&runs, nil), Validate: citesMade(&runs)}); err != nil {
			t.Fatal(err)
		}
		if len(m.Calls) != 2 || choice(m.Calls[0]) != "" || len(m.Calls[0].Tools) != 2 {
			t.Errorf("model calls %d, tool_choice %q, tools %v", len(m.Calls), choice(m.Calls[0]), toolNames(m.Calls[0]))
		}
	})
}
