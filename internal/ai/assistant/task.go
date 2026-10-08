package assistant

import (
	"context"
	"errors"
	"strings"
	"time"

	aisdk "github.com/azrtydxb/go-ai-sdk/ai"

	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/proposal"
	"github.com/azrtydxb/hello/internal/auth"
)

// Feature names the assistant's model calls.
const Feature = "assistant"

// findingsLimit bounds the findings summary the model sees.
const findingsLimit = 20

// systemPrompt is the assistant's instructions; Generate prepends the
// feature line and the untrusted-data notice. Instructions live only here
// and in the operator's current message: history, findings and tool
// results arrive as data.
const systemPrompt = `You are the assistant inside Hello, a SIP PBX, answering an operator's questions about their PBX.
Read what you need with the tools. They are read-only and answer with what the operator may see; results arrive inside <data> blocks.
Ground the answer in tool results. List in "citations" the zero-based positions, in the order you made them in this message, of the tool calls the answer relies on. If the data does not answer the question, say so.
Write the answer as plain text with line breaks. Never use Markdown links, Markdown images or HTML.
You cannot change anything. Only when the operator asks for a change, add one "proposal": allowlisted operations (operation id, path parameters and body) that a human reviews as a diff and applies. Never put a password or other secret in a proposal.
The earlier messages of this conversation are under "history" in the data, and the PBX's open findings under "findings".`

// historyEntry is a prior message as the model sees it (data, never
// instructions).
type historyEntry struct {
	Role    string    `json:"role"`
	Content string    `json:"content"`
	At      time.Time `json:"at"`
}

// answer is the message task: one Generate with the read tools, the answer
// validated against the calls made, its proposal validated and stored, and
// the assistant message stored with the calls' summaries.
func (a *Assistant) answer(ctx context.Context, userID int64, sessionID, taskID string, history []Message) (any, error) {
	if len(history) == 0 {
		return nil, errors.New("assistant: no message to answer")
	}
	ident := auth.Agent{UserID: userID, TaskID: taskID}
	ctx, abort := context.WithCancelCause(ctx)
	defer abort(nil)
	ts, tools := a.newToolset(ident, abort)

	current := history[len(history)-1]
	prior := make([]historyEntry, 0, len(history)-1)
	for _, m := range history[:len(history)-1] {
		prior = append(prior, historyEntry{Role: m.Role, Content: m.Content, At: m.CreatedAt})
	}
	findings, err := a.cfg.Store.OpenAIFindings(ctx, findingsLimit)
	if err != nil {
		a.cfg.Log.Warn("assistant: findings summary unavailable", "task", taskID, "error", err)
		findings = nil
	}
	if findings == nil {
		findings = []Finding{}
	}

	var validated *proposal.Draft
	started := time.Now()
	ans, usage, err := a.cfg.Generate(ctx, ai.Call[Answer]{
		Feature:  Feature,
		System:   systemPrompt,
		Data:     map[string]any{"history": prior, "findings": findings},
		Prompt:   current.Content,
		Tools:    tools,
		MaxSteps: a.cfg.MaxSteps,
		Validate: a.validator(ident, sessionID, ts, &validated),
	})
	if cause := context.Cause(ctx); errors.Is(cause, errUserGone) {
		return nil, errUserGone
	}
	if err != nil {
		err = classify(err)
		a.cfg.Log.Info("assistant: message failed", "feature", Feature, "task", taskID,
			"code", ai.CodeOf(err), "tool_calls", len(ts.Calls()), "duration", time.Since(started))
		return nil, err
	}

	var proposalID *string
	if validated != nil {
		id, err := a.cfg.Proposals.Store.Upsert(ctx, *validated)
		switch {
		case errors.Is(err, proposal.ErrSuppressed):
			// An equal proposal was dismissed recently: not made again.
		case err != nil:
			return nil, err
		default:
			proposalID = &id
		}
	}
	calls := ts.Calls()
	citations := ans.Citations
	if citations == nil {
		citations = []int{}
	}
	msgID, err := a.cfg.Store.AddAIMessage(ctx, Message{
		SessionID:  sessionID,
		Role:       RoleAssistant,
		Content:    ans.Answer,
		ToolCalls:  calls,
		Citations:  citations,
		ProposalID: proposalID,
		TaskID:     &taskID,
	})
	if err != nil {
		return nil, err
	}
	a.cfg.Log.Info("assistant: message answered", "feature", Feature, "task", taskID,
		"tokens", usage.Total(), "tool_calls", len(calls), "proposal", proposalID != nil, "duration", time.Since(started))
	return map[string]any{"messageId": msgID, "proposalId": proposalID}, nil
}

// classify turns an endpoint's refusal of tool calling (a 400 that names
// tools) into tools_unsupported with the provider's message, so the status
// page says what to fix (spec failure modes); every other error is kept.
func classify(err error) error {
	if code := ai.CodeOf(err); code != "" && code != ai.CodeProviderError {
		return err
	}
	var api *aisdk.APICallError
	if !errors.As(err, &api) || api.StatusCode != 400 {
		return err
	}
	msg := api.Message
	if msg == "" {
		msg = api.ResponseBody
	}
	if !strings.Contains(strings.ToLower(msg+" "+api.ResponseBody), "tool") {
		return err
	}
	if r := []rune(msg); len(r) > 300 {
		msg = string(r[:300]) + "…"
	}
	return &ai.Error{Code: ai.CodeToolsUnsupported, Message: "the endpoint rejects tool calling: " + msg, Err: err}
}
