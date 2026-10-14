package chat

import (
	"context"
	"encoding/json"
	"fmt"
)

// Names of the events of a streamed answer (docs/ai-agent.md, "Streaming
// answers"). The terminal events are EventDone and EventError: exactly one
// of them ends every stream.
const (
	// EventStarted: the question is stored and the task answering it runs.
	EventStarted = "started"
	// EventThinking: the model is reasoning; no answer text yet.
	EventThinking = "thinking"
	// EventTool: the assistant is reading one more piece of PBX data.
	EventTool = "tool"
	// EventDelta: more of the answer text.
	EventDelta = "delta"
	// EventReset: the answer text sent so far is void; more follows.
	EventReset = "reset"
	// EventDone: the answer is stored; the payload is Done.
	EventDone = "done"
	// EventError: the task failed; the payload is ErrorEvent.
	EventError = "error"
)

// Event is one event of a streamed answer: its name and a payload that
// encodes as JSON (nil for an empty object).
type Event struct {
	Name string
	Data any
}

// Terminal reports whether the event ends the stream.
func (e Event) Terminal() bool { return e.Name == EventDone || e.Name == EventError }

// Started is the payload of EventStarted: what the non-streaming call
// answers 202 with.
type Started struct {
	TaskID    string `json:"taskId"`
	MessageID int64  `json:"messageId"`
}

// ToolEvent is the payload of EventTool. It names the operation and says in
// words what it reads; the arguments are never part of it.
type ToolEvent struct {
	OperationID string `json:"operationId"`
	Label       string `json:"label"`
}

// Delta is the payload of EventDelta.
type Delta struct {
	Text string `json:"text"`
}

// Usage is the tokens one answer cost.
type Usage struct {
	InputTokens     int64 `json:"inputTokens"`
	OutputTokens    int64 `json:"outputTokens"`
	ReasoningTokens int64 `json:"reasoningTokens"`
	Calls           int64 `json:"calls"`
}

// Done is the payload of EventDone: the stored assistant message exactly as
// getAISession lists it (its citations and proposal id included), and the
// ids the non-streaming call answers 202 with.
type Done struct {
	TaskID     string  `json:"taskId"`
	MessageID  int64   `json:"messageId"`
	Message    Message `json:"message"`
	ProposalID *string `json:"proposalId"`
	Usage      Usage   `json:"usage"`
}

// ErrorEvent is the payload of EventError: the error code a failed task
// carries (provider_error, timeout, invalid_output, ai_budget_exhausted,
// canceled, ...) and its message.
type ErrorEvent struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Streamer starts a message like Assistant.PostMessage and reports the
// answer as events (*assistant.Assistant implements it). A failure before
// the task starts is the returned error, as for PostMessage; afterwards the
// channel delivers EventStarted first and a terminal event last, then
// closes. Cancelling ctx stops the work.
type Streamer interface {
	PostMessageStream(ctx context.Context, u User, sessionID, content string) (<-chan Event, error)
}

// Frame is the event in the text/event-stream wire format.
func (e Event) Frame() ([]byte, error) {
	data := []byte("{}")
	if e.Data != nil {
		var err error
		if data, err = json.Marshal(e.Data); err != nil {
			return nil, err
		}
	}
	return fmt.Appendf(nil, "event: %s\ndata: %s\n\n", e.Name, data), nil
}
