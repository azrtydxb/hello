package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	aisdk "github.com/azrtydxb/go-ai-sdk/ai"

	"github.com/azrtydxb/hello/internal/ai/proposal"
	"github.com/azrtydxb/hello/internal/auth"
)

// The assistant may apply its own batch (flow canvas decision of
// 2026-11-10, which carries out decision 2 of the flow canvas spec): when
// the user turned on "Let AI apply changes" for a question, the model gets
// one write tool, ApplyTool. Its batch is validated exactly as a proposal's
// (inner allowlist, no credential, at most proposal.MaxBatchOps operations,
// baseRevision and expect from the graph, the dry run as the user), stored
// as a proposal so the canvas and the proposal page show what it did, and
// applied through POST /api/v1/config/batch as the user with their role
// (auth.Agent.Apply): a viewer is refused, an operator's admin-only
// operation too. The batch row, the proposal and its audit row carry via
// "ai:<session>" with the user as the actor, and the batch is reverted like
// any other. With the switch off (the default) the tool is not offered and
// a change stays a proposal a person applies.

// ApplyTool is the write tool's name: the batch operation it applies.
const ApplyTool = proposal.BatchOp

// AgentApplier applies a stored batch proposal as the assistant for the
// user (*proposal.DBStore implements it).
type AgentApplier interface {
	ApplyAsAgent(ctx context.Context, id string, ident proposal.Identity, via string) (proposal.Proposal, error)
}

// applyPrompt is added to the instructions of a question asked with the
// switch on. It names the tool and how its outcome is reported.
const applyPrompt = `For this question the operator turned on "Let AI apply changes", which replaces "You cannot change anything" above. To make a change they asked for, call applyConfigBatch with a title, a rationale and the batch ({"ops": [...]}, the body a proposal's applyConfigBatch action carries, with "canvasId" when the question came from a canvas). Hello validates and dry-runs it and then applies it in one transaction as the operator, with their role, audited as applied by the assistant on their behalf; they can revert it from the canvas history. Apply only what the operator asked for, in one batch, then say in the answer what changed and do not add a proposal for it. If the tool refuses the batch or says it went stale or failed, nothing changed: fix the batch and call it again, or explain why the change cannot be made.`

// applyTool is ApplyTool for one message.
type applyTool struct {
	ts        *toolset
	ident     auth.Agent
	sessionID string
	val       proposal.Validator
	store     proposal.Store
	applier   AgentApplier
}

// applyTool returns the write tool for a message whose user allowed it, or
// nil when proposals or the agent apply are not wired.
func (a *Assistant) applyTool(ts *toolset, ident auth.Agent, sessionID string) *applyTool {
	p := a.cfg.Proposals
	if p == nil || p.Validator == nil || p.Store == nil || p.Applier == nil {
		return nil
	}
	return &applyTool{ts: ts, ident: ident, sessionID: sessionID, val: p.Validator, store: p.Store, applier: p.Applier}
}

func (t *applyTool) Name() string { return ApplyTool }

func (t *applyTool) Description() string {
	return "Applies a configuration batch now, as the operator, audited as applied by the assistant on their behalf and revertible. Only when the operator asked for the change. The batch is validated and dry-run first; a refused, stale or failed batch changes nothing."
}

func (t *applyTool) Schema() json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title":     map[string]any{"type": "string", "description": "A short title of the change."},
			"rationale": map[string]any{"type": "string", "description": "Why the change does what the operator asked."},
			"batch": map[string]any{
				"type":        "object",
				"description": `The batch: {"ops": [{"ref"?, "operationId", "pathParams"?, "body"?}], "canvasId"?}. Hello sets baseRevision and each op's expect.`,
			},
		},
		"required":             []string{"title", "batch"},
		"additionalProperties": false,
	})
	return b
}

func (t *applyTool) Strict() bool                             { return false }
func (t *applyTool) InputExamples() []json.RawMessage         { return nil }
func (t *applyTool) InputCallbacks() aisdk.ToolInputCallbacks { return aisdk.ToolInputCallbacks{} }

// applyArgs is the tool's argument object.
type applyArgs struct {
	Title     string          `json:"title"`
	Rationale string          `json:"rationale"`
	Batch     json.RawMessage `json:"batch"`
}

// errApplied refuses a second batch once one was applied for the message.
var errApplied = errors.New("a batch was already applied for this question: answer now, saying what changed")

// Execute validates the batch as a proposal, stores it and applies it as
// the user. It counts against the message's tool calls.
func (t *applyTool) Execute(ctx context.Context, args json.RawMessage) (any, error) {
	ts := t.ts
	ts.mu.Lock()
	switch {
	case ts.tries >= MaxToolCalls:
		ts.mu.Unlock()
		ts.count(ApplyTool, "limit")
		return nil, errCallLimit
	case ts.appliedDone:
		ts.mu.Unlock()
		ts.count(ApplyTool, "error")
		return nil, errApplied
	}
	ts.tries++
	ts.mu.Unlock()
	if ts.onTool != nil {
		ts.onTool(ApplyTool)
	}

	var in applyArgs
	call := ToolCall{OperationID: ApplyTool}
	record := func(result string) {
		ts.mu.Lock()
		ts.calls = append(ts.calls, call)
		ts.mu.Unlock()
		ts.count(ApplyTool, result)
	}
	if err := json.Unmarshal(args, &in); err != nil {
		record("error")
		return nil, fmt.Errorf(`arguments must be {"title", "rationale", "batch"}: %w`, err)
	}
	in.Title, in.Rationale = strings.TrimSpace(in.Title), strings.TrimSpace(in.Rationale)
	// The stored summary keeps the title, never the batch (the proposal
	// holds it).
	call.Arguments, _ = json.Marshal(map[string]string{"title": in.Title})
	switch {
	case in.Title == "" || len(in.Batch) == 0:
		record("error")
		return nil, errors.New("a title and a batch are required; nothing was applied")
	case markup.MatchString(in.Title) || markup.MatchString(in.Rationale):
		record("error")
		return nil, errors.New("title and rationale must be plain text: no Markdown links or images and no HTML; nothing was applied")
	}
	user := t.ident.UserID
	d := &proposal.Draft{
		Source: proposal.SourceAssistant, Title: in.Title, Rationale: in.Rationale,
		SessionID: &t.sessionID, CreatedBy: &user,
		Actions: []proposal.Action{{OperationID: proposal.BatchOp, PathParams: map[string]string{}, Body: in.Batch}},
	}
	if err := t.val.Validate(ctx, t.ident, d); err != nil {
		record("error")
		return nil, fmt.Errorf("the batch is invalid, nothing was applied: %w", err)
	}
	id, err := t.store.Upsert(ctx, *d)
	if errors.Is(err, proposal.ErrSuppressed) {
		record("error")
		return nil, errors.New("the operator dismissed this same change in the last 7 days, so it is not applied: tell them, and apply it only if they ask again in other words")
	}
	if err != nil {
		record("error")
		return nil, fmt.Errorf("the change could not be stored, nothing was applied: %w", err)
	}
	p, err := t.applier.ApplyAsAgent(ctx, id, t.ident, proposal.ViaAI+t.sessionID)
	if err != nil {
		record("error")
		return nil, fmt.Errorf("the change was not applied: %w", err)
	}
	ts.mu.Lock()
	ts.applied = id
	ts.appliedDone = p.Status == proposal.StatusApplied
	ts.mu.Unlock()
	out := map[string]any{"proposalId": id, "status": p.Status}
	switch p.Status {
	case proposal.StatusApplied:
		call.Status = http.StatusOK
		out["batchId"] = p.BatchID
		out["note"] = "Applied in one transaction as the operator, audited as applied by the assistant on their behalf; it can be reverted from the canvas history."
	case proposal.StatusStale:
		call.Status = http.StatusConflict
		out["failure"] = p.Failure
		out["note"] = "Nothing was applied: the configuration changed under the batch. Read it again and retry."
	default:
		call.Status = http.StatusUnprocessableEntity
		if p.Failure != nil && p.Failure.Status != 0 {
			call.Status = p.Failure.Status
		}
		out["failure"] = p.Failure
		out["note"] = "Nothing was applied."
		if call.Status == http.StatusForbidden {
			out["note"] = "Nothing was applied: the operator's role does not allow this change, so explain that instead."
		}
	}
	result := "ok"
	if p.Status != proposal.StatusApplied {
		result = "error"
	}
	record(result)
	return ts.dataBlock(out), nil
}
