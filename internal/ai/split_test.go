package ai_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	aisdk "github.com/azrtydxb/go-ai-sdk/ai"
	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/aifake"
)

// cited is an answer with citations, as the assistant's.
type cited struct {
	Answer    string `json:"answer"`
	Citations []int  `json:"citations"`
}

// readTools are two read tools that count their runs and log them to order.
func readTools(runs *int, order *[]string) []aisdk.Tool {
	mk := func(name, result string) aisdk.Tool {
		return aisdk.NewTool(name, "Reads "+name+".", func(context.Context, struct{}) (any, error) {
			*runs++
			if order != nil {
				*order = append(*order, "tool:"+name)
			}
			return ai.DataBlock(result), nil
		})
	}
	return []aisdk.Tool{mk("listRegistrations", "2041 at 10.0.0.7"), mk("listAIFindings", "none open")}
}

// citesMade validates citations against the tool runs so far.
func citesMade(runs *int) func(context.Context, cited) error {
	return func(_ context.Context, c cited) error {
		for _, i := range c.Citations {
			if i < 0 || i >= *runs {
				return fmt.Errorf("citation %d names no tool call", i)
			}
		}
		return nil
	}
}

func hasRole(c provider.Call, role provider.Role) bool {
	for _, m := range c.Messages {
		if m.Role == role {
			return true
		}
	}
	return false
}

// TestGenerateSplit (spec S-4) fails if a call with tools asks for JSON or
// sends a response format while tools are offered, offers tools in the
// answer phase, does not list the calls made for citations, loses a tool
// result before the answer, nudges a model that describes a tool call more
// than once or not at all, nudges past the step budget, or does not apply
// the structured-output mode and the validation retries to the answer
// phase.
func TestGenerateSplit(t *testing.T) {
	ctx := context.Background()

	for _, mode := range []string{"json_schema", "prompt"} {
		t.Run(mode+": tools, plain text, then the cited structured answer", func(t *testing.T) {
			cfg := aifake.Config()
			cfg.StructuredOutput = mode
			final := cited{Answer: "Extension 2041 is registered; no findings are open.", Citations: []int{0, 1}}
			s, m, _ := aifake.Service(t, cfg,
				aifake.ToolCall("c1", "listRegistrations", map[string]any{}),
				aifake.ToolCall("c2", "listAIFindings", map[string]any{}),
				aifake.Text("Extension 2041 is registered and no findings are open."),
				aifake.JSON(final))
			runs := 0
			got, u, err := ai.Generate(ctx, s, ai.Call[cited]{Feature: "assistant", System: "Answer about the PBX.",
				Prompt: "Which extensions are registered, and are there open findings?", Tools: readTools(&runs, nil),
				Validate: citesMade(&runs)})
			if err != nil || got.Answer != final.Answer || len(got.Citations) != 2 {
				t.Fatalf("Generate = %+v, %v", got, err)
			}
			if runs != 2 || len(m.Calls) != 4 || u.Calls != 4 {
				t.Fatalf("tool runs %d, model calls %d, usage %+v", runs, len(m.Calls), u)
			}
			for i, c := range m.Calls[:3] {
				sys := texts(c, provider.RoleSystem)
				if len(c.Tools) != 2 || c.ResponseFormat != nil || strings.Contains(sys, "JSON schema") || strings.Contains(sys, "JSON object") {
					t.Errorf("tool phase call %d: %d tools, format %+v, system %q", i, len(c.Tools), c.ResponseFormat, sys)
				}
				if !strings.Contains(sys, "Answer about the PBX.") || !strings.Contains(sys, "plain text") {
					t.Errorf("tool phase call %d system %q", i, sys)
				}
			}
			last := m.Calls[3]
			if len(last.Tools) != 0 {
				t.Errorf("the answer phase offers %d tools", len(last.Tools))
			}
			sys := texts(last, provider.RoleSystem)
			switch mode {
			case "json_schema":
				if last.ResponseFormat == nil || last.ResponseFormat.Type != "json" || strings.Contains(sys, "JSON schema") {
					t.Errorf("json_schema answer phase: format %+v, system %q", last.ResponseFormat, sys)
				}
			case "prompt":
				if last.ResponseFormat != nil || !strings.Contains(sys, "JSON schema") || !strings.Contains(sys, `"citations"`) {
					t.Errorf("prompt answer phase: format %+v, system %q", last.ResponseFormat, sys)
				}
			}
			user := texts(last, provider.RoleUser)
			if !strings.Contains(user, "Now write the final answer") ||
				!strings.Contains(user, `"position": 0`) || !strings.Contains(user, `"tool": "listRegistrations"`) ||
				!strings.Contains(user, `"position": 1`) || !strings.Contains(user, `"tool": "listAIFindings"`) {
				t.Errorf("answer request %q", user)
			}
			if !hasRole(last, provider.RoleTool) || !strings.Contains(texts(last, provider.RoleAssistant), "2041 is registered") {
				t.Error("the answer phase lacks the tool results or the tool phase's text")
			}
		})
	}

	t.Run("ToolLog is the list of calls the answer phase shows", func(t *testing.T) {
		s, m, _ := aifake.Service(t, aifake.Config(),
			aifake.ToolCall("c1", "listRegistrations", map[string]any{}),
			aifake.Text("Found it."),
			aifake.JSON(cited{Answer: "Found.", Citations: []int{0}}))
		runs := 0
		_, _, err := ai.Generate(ctx, s, ai.Call[cited]{Feature: "assistant", Prompt: "q", Tools: readTools(&runs, nil),
			ToolLog: func() any { return []string{"LOGGED-CALL-0"} }})
		if err != nil {
			t.Fatal(err)
		}
		if u := texts(m.Calls[2], provider.RoleUser); !strings.Contains(u, "LOGGED-CALL-0") || strings.Contains(u, `"tool": "listRegistrations"`) {
			t.Errorf("answer request %q", u)
		}
	})

	for name, described := range map[string]string{
		"an announced lookup": "I need to check the current SIP registrations. Now I will check the current SIP registrations.",
		"a tool_call tag":     "<tool_call>\n{\"name\": \"listRegistrations\", \"arguments\": {}}\n</tool_call>",
		"tool-call JSON":      "```json\n{\"name\": \"listRegistrations\", \"arguments\": {}}\n```",
		"a call in text":      "Calling listRegistrations() for you.",
	} {
		t.Run(name+" gets one nudge, then the tool is called", func(t *testing.T) {
			s, m, _ := aifake.Service(t, aifake.Config(),
				aifake.Text(described),
				aifake.ToolCall("c1", "listRegistrations", map[string]any{}),
				aifake.Text("2041 is registered."),
				aifake.JSON(cited{Answer: "2041 is registered.", Citations: []int{0}}))
			runs := 0
			got, _, err := ai.Generate(ctx, s, ai.Call[cited]{Feature: "assistant", Prompt: "Who is registered?",
				Tools: readTools(&runs, nil), Validate: citesMade(&runs)})
			if err != nil || runs != 1 || len(m.Calls) != 4 || len(got.Citations) != 1 {
				t.Fatalf("Generate = %+v, %v; tool runs %d, model calls %d", got, err, runs, len(m.Calls))
			}
			if u := texts(m.Calls[1], provider.RoleUser); !strings.Contains(u, "described a tool call") || len(m.Calls[1].Tools) == 0 {
				t.Errorf("second call is not the nudge with tools: %q", u)
			}
		})
	}

	t.Run("only one nudge, then the answer phase", func(t *testing.T) {
		s, m, _ := aifake.Service(t, aifake.Config(),
			aifake.Text("Let me check the registrations."),
			aifake.Text("Let me check the registrations again."),
			aifake.JSON(cited{Answer: "I could not read the registrations.", Citations: []int{}}))
		runs := 0
		got, _, err := ai.Generate(ctx, s, ai.Call[cited]{Feature: "assistant", Prompt: "q", Tools: readTools(&runs, nil)})
		if err != nil || len(m.Calls) != 3 || runs != 0 || got.Answer == "" {
			t.Fatalf("Generate = %+v, %v after %d calls", got, err, len(m.Calls))
		}
		if len(m.Calls[2].Tools) != 0 || strings.Count(texts(m.Calls[2], provider.RoleUser), "described a tool call") != 1 {
			t.Errorf("third call: %d tools, user %q", len(m.Calls[2].Tools), texts(m.Calls[2], provider.RoleUser))
		}
	})

	t.Run("plain text is not nudged", func(t *testing.T) {
		s, m, _ := aifake.Service(t, aifake.Config(),
			aifake.Text("SIP is the Session Initiation Protocol."),
			aifake.JSON(cited{Answer: "SIP is the Session Initiation Protocol.", Citations: []int{}}))
		runs := 0
		if _, _, err := ai.Generate(ctx, s, ai.Call[cited]{Feature: "assistant", Prompt: "What is SIP?", Tools: readTools(&runs, nil)}); err != nil {
			t.Fatal(err)
		}
		if len(m.Calls) != 2 {
			t.Errorf("model calls %d, want 2", len(m.Calls))
		}
	})

	t.Run("no nudge once the steps are spent", func(t *testing.T) {
		s, m, _ := aifake.Service(t, aifake.Config(),
			aifake.Text("Let me check the registrations."),
			aifake.JSON(cited{Answer: "Unknown.", Citations: []int{}}))
		runs := 0
		if _, _, err := ai.Generate(ctx, s, ai.Call[cited]{Feature: "assistant", Prompt: "q", Tools: readTools(&runs, nil), MaxSteps: 1}); err != nil {
			t.Fatal(err)
		}
		if len(m.Calls) != 2 || len(m.Calls[1].Tools) != 0 {
			t.Errorf("model calls %d", len(m.Calls))
		}
	})

	t.Run("the answer phase's validation attempts are bounded", func(t *testing.T) {
		s, m, _ := aifake.Service(t, aifake.Config(),
			aifake.ToolCall("c1", "listRegistrations", map[string]any{}),
			aifake.Text("Found one."),
			aifake.JSON(cited{Answer: "a", Citations: []int{4}}),
			aifake.JSON(cited{Answer: "b", Citations: []int{4}}),
			aifake.JSON(cited{Answer: "c", Citations: []int{4}}),
			aifake.JSON(cited{Answer: "never asked", Citations: []int{0}}))
		runs := 0
		_, u, err := ai.Generate(ctx, s, ai.Call[cited]{Feature: "assistant", Prompt: "q", Tools: readTools(&runs, nil), Validate: citesMade(&runs)})
		if ai.CodeOf(err) != ai.CodeInvalidOutput || len(m.Calls) != 5 || u.Calls != 5 {
			t.Fatalf("err %v after %d calls (usage %+v)", err, len(m.Calls), u)
		}
		for i, c := range m.Calls[2:] {
			if len(c.Tools) != 0 {
				t.Errorf("answer attempt %d offers tools", i)
			}
		}
		if !strings.Contains(texts(m.Calls[4], provider.RoleUser), "citation 4 names no tool call") {
			t.Error("the validation error did not go back")
		}
	})

	t.Run("a tool phase failure fails the call", func(t *testing.T) {
		s, m, _ := aifake.Service(t, aifake.Config())
		m.Err = &aisdk.APICallError{StatusCode: 400, Message: "tools are not supported"}
		runs := 0
		_, _, err := ai.Generate(ctx, s, ai.Call[cited]{Feature: "assistant", Prompt: "q", Tools: readTools(&runs, nil)})
		if ai.CodeOf(err) != ai.CodeToolsUnsupported {
			t.Errorf("err = %v", err)
		}
	})

	for _, mode := range []string{"json_schema", "prompt"} {
		t.Run(mode+": streamed, tool events then answer deltas, no reset", func(t *testing.T) {
			cfg := aifake.Config()
			cfg.StructuredOutput = mode
			final := cited{Answer: "Extension 2041 is registered.\nNo findings.", Citations: []int{0}}
			s, _ := streamService(t, cfg,
				aifake.StreamText("Now I will check the registrations."),
				aifake.StreamToolCall("c1", "listRegistrations", map[string]any{}),
				aifake.StreamText(`{"answer":"TOOL-PHASE`, ` prose"}`),
				aifake.StreamJSON(final, 4))
			var order []string
			resets, thinking := 0, 0
			var text strings.Builder
			runs := 0
			got, _, err := ai.Generate(ctx, s, ai.Call[cited]{Feature: "assistant", Prompt: "Who is registered?",
				Tools: readTools(&runs, &order), Validate: citesMade(&runs),
				Stream: &ai.Stream{Field: "answer",
					Thinking: func() { thinking++ },
					Text: func(d string) {
						text.WriteString(d)
						if len(order) == 0 || order[len(order)-1] != "delta" {
							order = append(order, "delta")
						}
					},
					Reset: func() { resets++ }}})
			if err != nil || got.Answer != final.Answer {
				t.Fatalf("Generate = %+v, %v", got, err)
			}
			if text.String() != final.Answer || resets != 0 || runs != 1 {
				t.Errorf("streamed %q, resets %d, tool runs %d", text.String(), resets, runs)
			}
			if strings.Join(order, ",") != "tool:listRegistrations,delta" {
				t.Errorf("order %v", order)
			}
		})
	}
}

// TestGenerateNoTools (spec S-4) fails if a call without tools, such as a
// detector explanation, makes more than its single structured call.
func TestGenerateNoTools(t *testing.T) {
	s, m, _ := aifake.Service(t, aifake.Config(), aifake.JSON(answer{Answer: "explained"}))
	got, _, err := ai.Generate(context.Background(), s, ai.Call[answer]{Feature: "aiops_explain", Prompt: "Explain."})
	if err != nil || got.Answer != "explained" || len(m.Calls) != 1 || m.Calls[0].ResponseFormat == nil {
		t.Fatalf("Generate = %+v, %v after %d calls", got, err, len(m.Calls))
	}
	if strings.Contains(texts(m.Calls[0], provider.RoleSystem), "plain text") {
		t.Error("a call without tools got the tool phase's instruction")
	}
}
