package ai

import (
	"strings"
	"testing"

	"github.com/azrtydxb/go-ai-sdk/provider"
)

// TestExtractField fails if the answer text is not read incrementally from
// partial structured output: through escapes cut in half, a reasoning
// block, a code fence, other properties first or nested, or a property that
// has not started.
func TestExtractField(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
		thinking        bool
	}{
		{"nothing yet", ``, "", false},
		{"object open", `{`, "", false},
		{"key partial", `{"ans`, "", false},
		{"value not started", `{"answer":`, "", false},
		{"value started", `{"answer": "`, "", false},
		{"value partial", `{"answer":"Trunk A is`, "Trunk A is", false},
		{"value complete", `{"answer":"Trunk A is up.","citations":[0]}`, "Trunk A is up.", false},
		{"escapes", `{"answer":"line one\nline \"two\"\\ é \/"}`, "line one\nline \"two\"\\ é /", false},
		{"cut after backslash", `{"answer":"a\`, "a", false},
		{"cut inside unicode escape", `{"answer":"a\u00`, "a", false},
		{"surrogate pair", `{"answer":"😀!"}`, "😀!", false},
		{"cut between surrogates", `{"answer":"x\ud83d`, "x", false},
		{"other property first", `{"citations":[0,1],"answer":"Done"}`, "Done", false},
		{"answer key inside a string", `{"proposal":{"rationale":"say \"answer\": no"},"answer":"yes"}`, "yes", false},
		{"nested answer ignored", `{"proposal":{"answer":"nested"},"citations":[]}`, "", false},
		{"fenced", "```json\n{\"answer\":\"fenced\"", "fenced", false},
		{"fence not complete", "```js", "", false},
		{"reasoning open", `<think>I should check {"answer":"no"`, "", true},
		{"reasoning closed", "<think>hm</think>\n{\"answer\":\"After\"", "After", false},
		{"not an object", `Sure, the answer is 3`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, thinking := extractField(tc.raw, "answer")
			if got != tc.want || thinking != tc.thinking {
				t.Errorf("extractField(%q) = %q, %v; want %q, %v", tc.raw, got, thinking, tc.want, tc.thinking)
			}
		})
	}
}

// TestFieldStreamByteAtATime fails if feeding the structured answer in
// one-byte deltas does not stream exactly the answer's text, once, in
// order.
func TestFieldStreamByteAtATime(t *testing.T) {
	const raw = `{"citations":[0],"answer":"Café \"A\"\nthe end é 😀","proposal":null}`
	var got strings.Builder
	var thinking, resets int
	fs := newFieldStream(&Stream{Field: "answer",
		Thinking: func() { thinking++ },
		Text:     func(d string) { got.WriteString(d) },
		Reset:    func() { resets++ },
	})
	for i := 0; i < len(raw); i++ {
		fs.text(raw[i : i+1])
	}
	if want := "Café \"A\"\nthe end é 😀"; got.String() != want {
		t.Errorf("streamed %q, want %q", got.String(), want)
	}
	if thinking != 0 || resets != 0 {
		t.Errorf("thinking %d, resets %d", thinking, resets)
	}
}

// TestFieldStreamSteps fails if reasoning is shown as text, a tool-calling
// step's text is kept, or a rejected answer is not voided.
func TestFieldStreamSteps(t *testing.T) {
	var got strings.Builder
	var thinking, resets int
	fs := newFieldStream(&Stream{Field: "answer",
		Thinking: func() { thinking++ },
		Text:     func(d string) { got.WriteString(d) },
		Reset:    func() { resets++; got.Reset() },
	})
	// A step that reasons, writes an answer-looking object, then calls a tool.
	fs.reasoning()
	fs.reasoning()
	fs.text(`{"answer":"draft`)
	fs.endStep(provider.FinishToolCalls)
	if thinking != 1 || resets != 1 || got.String() != "" {
		t.Errorf("after the tool step: thinking %d, resets %d, text %q", thinking, resets, got.String())
	}
	// The final step reasons inline, then answers.
	fs.text("<think>reasoning that mentions \"answer\"")
	fs.text("</think>{\"answer\":\"Final")
	if thinking != 2 || got.String() != "Final" {
		t.Errorf("final step: thinking %d, text %q", thinking, got.String())
	}
	fs.endStep(provider.FinishStop)
	// The answer was rejected: the retry starts over.
	fs.reset()
	if resets != 2 || got.String() != "" {
		t.Errorf("after the reset: resets %d, text %q", resets, got.String())
	}
	fs.text(`{"answer":"Second try"}`)
	if got.String() != "Second try" {
		t.Errorf("retry streamed %q", got.String())
	}
}
