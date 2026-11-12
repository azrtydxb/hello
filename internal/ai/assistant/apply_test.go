package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	aisdk "github.com/azrtydxb/go-ai-sdk/ai"
	"github.com/azrtydxb/go-ai-sdk/provider"

	"github.com/azrtydxb/hello/internal/ai/proposal"
	"github.com/azrtydxb/hello/internal/auth"
)

// batchProposals is a proposal validator, store and agent applier for the
// write tool: it accepts one batch action, records what it is asked to
// apply and answers with outcome (the status the API's role check gives).
type batchProposals struct {
	mu      sync.Mutex
	stored  []proposal.Draft
	applied []agentApply
	outcome proposal.Proposal
}

type agentApply struct {
	id    string
	ident proposal.Identity
	via   string
}

func (b *batchProposals) Validate(_ context.Context, _ proposal.Identity, d *proposal.Draft) error {
	if len(d.Actions) != 1 || d.Actions[0].OperationID != proposal.BatchOp {
		return fmt.Errorf("one batch action only")
	}
	d.Actions[0].Preview = &proposal.Preview{BaseRevision: 4, FlowDiff: json.RawMessage(`{"added":[]}`)}
	return nil
}

func (b *batchProposals) Upsert(_ context.Context, d proposal.Draft) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stored = append(b.stored, d)
	return fmt.Sprintf("30000000-0000-0000-0000-%012d", len(b.stored)), nil
}

func (b *batchProposals) Apply(context.Context, string, http.Header) (proposal.Proposal, error) {
	return proposal.Proposal{}, fmt.Errorf("a person's apply is not the assistant's")
}

func (b *batchProposals) Dismiss(context.Context, string, int64, string, string) error {
	return fmt.Errorf("the assistant never dismisses")
}

func (b *batchProposals) ApplyAsAgent(_ context.Context, id string, ident proposal.Identity, via string) (proposal.Proposal, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.applied = append(b.applied, agentApply{id: id, ident: ident, via: via})
	p := b.outcome
	p.ID = id
	return p, nil
}

func toolNames(tools []aisdk.Tool) []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Name()
	}
	return out
}

const applyArgsJSON = `{"title":"Support line","rationale":"asked for","batch":{"canvasId":3,"ops":[{"operationId":"createRingGroup","body":{"name":"support","strategy":"sequential","ringTimeout":20,"members":[]}}]}}`

// TestAssistantApply (flow canvas decision of 2026-11-10) fails if the
// assistant gets a write tool for a question asked without "Let AI apply
// changes" (ask first is the default), if with it the batch is not
// validated, stored and applied as the asking user with via ai:<session>,
// if the answer does not link what was applied, if a refused apply (a
// viewer's 403) is reported as applied, or if a second batch or a proposal
// follows an applied one.
func TestAssistantApply(t *testing.T) {
	canvas := int64(3)
	setup := func(t *testing.T, outcome proposal.Proposal, responses ...any) (*rig, *batchProposals) {
		t.Helper()
		r := newRig(t, scripted(responses)...)
		bp := &batchProposals{outcome: outcome}
		r.a.cfg.Proposals = &Proposals{Validator: bp, Store: bp, Applier: bp}
		return r, bp
	}
	post := func(t *testing.T, r *rig, mc *Context) (Session, taskOutcome) {
		t.Helper()
		u := User{ID: 7}
		s, err := r.a.CreateSession(t.Context(), u, "")
		if err != nil {
			t.Fatal(err)
		}
		_, task, err := r.a.PostMessage(t.Context(), u, s.ID, "Add a support ring group", mc)
		if err != nil {
			t.Fatal(err)
		}
		return s, r.tasks.outcome(t, task)
	}
	batchID := "40000000-0000-0000-0000-000000000001"
	applied := proposal.Proposal{Status: proposal.StatusApplied, BatchID: &batchID}

	t.Run("ask first: no write tool without the switch", func(t *testing.T) {
		r, bp := setup(t, applied, Answer{Answer: "Here is a proposal.", Citations: []int{}})
		if _, o := post(t, r, &Context{CanvasID: &canvas}); o.err != nil {
			t.Fatal(o.err)
		}
		if len(r.calls) == 0 {
			t.Fatal("the model was not called")
		}
		if slices.Contains(toolNames(r.calls[0].Tools), ApplyTool) {
			t.Errorf("the write tool was offered without the switch: %v", toolNames(r.calls[0].Tools))
		}
		if strings.Contains(r.calls[0].System, "Let AI apply changes") {
			t.Error("the apply instructions were sent without the switch")
		}
		if len(bp.applied) != 0 {
			t.Errorf("applied without the switch: %+v", bp.applied)
		}
	})

	t.Run("the AI applies its batch as the user", func(t *testing.T) {
		r, bp := setup(t, applied,
			call("c1", ApplyTool, applyArgsJSON),
			Answer{Answer: "I created the support ring group.", Citations: []int{0}},
		)
		s, o := post(t, r, &Context{CanvasID: &canvas, AllowApply: true})
		if o.err != nil {
			t.Fatal(o.err)
		}
		if !slices.Contains(toolNames(r.calls[0].Tools), ApplyTool) || !strings.Contains(r.calls[0].System, "Let AI apply changes") {
			t.Fatalf("the write tool or its instructions are missing: %v", toolNames(r.calls[0].Tools))
		}
		if len(bp.stored) != 1 || len(bp.applied) != 1 {
			t.Fatalf("stored %d, applied %d; want the batch stored and applied once", len(bp.stored), len(bp.applied))
		}
		d, ap := bp.stored[0], bp.applied[0]
		if d.Actions[0].OperationID != proposal.BatchOp || !strings.Contains(string(d.Actions[0].Body), "createRingGroup") ||
			d.SessionID == nil || *d.SessionID != s.ID || d.Title != "Support line" {
			t.Errorf("stored draft = %+v", d)
		}
		if ap.via != proposal.ViaAI+s.ID || ap.ident.UserID != 7 || ap.ident.TaskID == "" || ap.id == "" {
			t.Errorf("applied as %+v, want user 7 with via ai:%s", ap, s.ID)
		}
		msgs := r.store.assistantMessages()
		if len(msgs) != 1 || msgs[0].ProposalID == nil || *msgs[0].ProposalID != ap.id {
			t.Fatalf("the answer does not link the applied proposal: %+v", msgs)
		}
		if c := msgs[0].ToolCalls; len(c) != 1 || c[0].OperationID != ApplyTool || c[0].Status != http.StatusOK ||
			strings.Contains(string(c[0].Arguments), "createRingGroup") {
			t.Errorf("tool calls = %+v, want the apply with status 200 and only its title", c)
		}
		if res, _ := o.result.(map[string]any); res["proposalId"] == nil {
			t.Errorf("task result = %v, want the proposal id", o.result)
		}
	})

	t.Run("a refused apply is not reported as applied", func(t *testing.T) {
		refused := proposal.Proposal{Status: proposal.StatusFailed,
			Failure: &proposal.Failure{Index: 0, Status: http.StatusForbidden, Code: "forbidden_role", Message: "needs operator"}}
		r, bp := setup(t, refused,
			call("c1", ApplyTool, applyArgsJSON),
			Answer{Answer: "Your role cannot make this change.", Citations: []int{0}},
		)
		if _, o := post(t, r, &Context{AllowApply: true}); o.err != nil {
			t.Fatal(o.err)
		}
		if len(bp.applied) != 1 {
			t.Fatalf("applied %d times", len(bp.applied))
		}
		msgs := r.store.assistantMessages()
		if c := msgs[0].ToolCalls; len(c) != 1 || c[0].Status != http.StatusForbidden {
			t.Errorf("tool calls = %+v, want status 403", c)
		}
		// What the model reads: nothing was applied, and why.
		ts, _ := r.a.newToolset(auth.Agent{UserID: 7, TaskID: "t"}, func(error) {})
		out, err := r.a.applyTool(ts, auth.Agent{UserID: 7, TaskID: "t"}, "s").Execute(t.Context(), json.RawMessage(applyArgsJSON))
		if err != nil {
			t.Fatal(err)
		}
		if s, _ := out.(string); !strings.Contains(s, "role does not allow") || !strings.Contains(s, `"status": "failed"`) {
			t.Errorf("tool result = %v, want the refusal", out)
		}
		if ts.didApply() {
			t.Error("a refused batch counts as applied")
		}
	})

	t.Run("one applied batch per question, and no proposal after it", func(t *testing.T) {
		r, bp := setup(t, applied,
			call("c1", ApplyTool, applyArgsJSON),
			call("c2", ApplyTool, applyArgsJSON),
			Answer{Answer: "Done.", Citations: []int{0}, Proposal: &ProposalOut{Title: "Again", Actions: []ActionOut{{OperationID: "updateOutboundRoute"}}}},
			Answer{Answer: "Done.", Citations: []int{0}},
		)
		if _, o := post(t, r, &Context{AllowApply: true}); o.err != nil {
			t.Fatal(o.err)
		}
		if len(bp.applied) != 1 {
			t.Errorf("applied %d batches, want 1", len(bp.applied))
		}
		if len(bp.stored) != 1 {
			t.Errorf("stored %d proposals, want only the applied batch", len(bp.stored))
		}
	})
}

// scripted turns tool calls and answers into the mock model's responses:
// one response per tool call, one per answer.
func scripted(items []any) []*provider.Response {
	out := make([]*provider.Response, 0, len(items))
	for _, it := range items {
		switch v := it.(type) {
		case provider.ToolCallPart:
			out = append(out, toolResp(v))
		default:
			out = append(out, textResp(v))
		}
	}
	return out
}
