package proposal_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/ai/proposal"
)

func batchAct(ops ...map[string]any) proposal.Action {
	return act(proposal.BatchOp, map[string]string{}, map[string]any{"ops": ops})
}

// TestBatchProposal (spec flow-canvas S-19, the database half of
// TestProposalValidation and TestProposalApply for batches) fails if
// validating a batch proposal commits anything, its preview lacks the
// graph change, a viewer's or an operator's admin-only batch is accepted,
// the apply does not run the batch in one transaction as the applier with
// via ai-proposal:<id>, or a batch whose target changed since applies.
func TestBatchProposal(t *testing.T) {
	e := newEnv(t)
	s := e.seed()
	create := map[string]any{"ref": "rg", "operationId": "createRingGroup", "body": map[string]any{
		"name": "support", "strategy": "sequential", "ringTimeout": 20,
		"members": []map[string]any{{"extensionId": json.Number(s.ext1), "position": 1}},
	}}
	route := map[string]any{"operationId": "createInboundRoute", "body": map[string]any{
		"name": "Support line", "didKind": "exact", "did": "0207654321", "destinationKind": "ring_group", "destination": "${rg.name}",
	}}

	t.Run("validation dry-runs and commits nothing", func(t *testing.T) {
		rev := e.revision()
		d := &proposal.Draft{Source: proposal.SourceAssistant, Title: "Support line", Actions: []proposal.Action{batchAct(create, route)}}
		if err := e.val.Validate(context.Background(), e.ident(), d); err != nil {
			t.Fatal(err)
		}
		if e.revision() != rev {
			t.Fatal("validation moved the configuration revision")
		}
		var n int
		if err := e.db.QueryRow(`SELECT count(*) FROM ring_groups WHERE name = 'support'`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("ring groups named support = %d %v: the dry run committed", n, err)
		}
		if err := e.db.QueryRow(`SELECT count(*) FROM config_batches`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("config_batches rows = %d %v: the dry run was stored", n, err)
		}
		p := d.Actions[0].Preview
		if p == nil || !strings.Contains(string(p.FlowDiff), `"label":"support"`) || p.BaseRevision != rev {
			t.Fatalf("preview = %+v", p)
		}
	})

	t.Run("roles: the user's own role decides", func(t *testing.T) {
		viewer := proposal.Identity{UserID: e.users["viewer"], TaskID: "task-v"}
		d := &proposal.Draft{Source: proposal.SourceAssistant, Title: "x", Actions: []proposal.Action{batchAct(create)}}
		if err := e.val.Validate(context.Background(), viewer, d); err == nil || !strings.Contains(err.Error(), "cannot make this change") {
			t.Errorf("a viewer's batch: err = %v", err)
		}
		paging := map[string]any{"operationId": "createPagingGroup", "body": map[string]any{"number": "7700", "name": "All"}}
		d = &proposal.Draft{Source: proposal.SourceAssistant, Title: "x", Actions: []proposal.Action{batchAct(paging)}}
		if err := e.val.Validate(context.Background(), e.ident(), d); err == nil || !strings.Contains(err.Error(), "403") {
			t.Errorf("an operator's admin-only batch: err = %v", err)
		}
	})

	t.Run("apply runs the batch as the applier", func(t *testing.T) {
		pid := e.propose("assistant", batchAct(create, route))
		e.must("viewer", 403, "POST", "/api/v1/ai/proposals/"+pid+"/apply", nil)
		rev := e.revision()
		got := e.must("operator", 200, "POST", "/api/v1/ai/proposals/"+pid+"/apply", nil)
		if got["status"] != "applied" {
			t.Fatalf("apply = %v", got)
		}
		if e.revision() != rev+1 {
			t.Errorf("revision %d -> %d, want one step for the whole batch", rev, e.revision())
		}
		var via, actor string
		if err := e.db.QueryRow(`SELECT via, actor FROM config_batches ORDER BY created_at DESC LIMIT 1`).Scan(&via, &actor); err != nil {
			t.Fatal(err)
		}
		if via != "ai-proposal:"+pid || !strings.HasPrefix(actor, "token:") {
			t.Errorf("batch row via %q actor %q, want ai-proposal:%s and the operator's token", via, actor, pid)
		}
		var n int
		if err := e.db.QueryRow(`SELECT count(*) FROM inbound_routes WHERE destination = 'support' AND destination_kind = 'ring_group'`).Scan(&n); err != nil || n != 1 {
			t.Errorf("inbound routes to support = %d %v", n, err)
		}
	})

	t.Run("stale when a target changed", func(t *testing.T) {
		rename := map[string]any{"operationId": "updateRingGroup", "pathParams": map[string]string{"id": s.group}, "body": map[string]any{"ringTimeout": 30}}
		pid := e.propose("assistant", batchAct(rename))
		e.must("admin", 200, "PATCH", "/api/v1/ring-groups/"+s.group, map[string]any{"ringTimeout": 45})
		r := e.do("operator", "POST", "/api/v1/ai/proposals/"+pid+"/apply", nil)
		if r.code != 409 {
			t.Fatalf("apply = %d %s, want 409", r.code, r.body)
		}
		if e.status(pid) != "stale" {
			t.Errorf("status = %s, want stale", e.status(pid))
		}
		g := e.must("admin", 200, "GET", "/api/v1/ring-groups/"+s.group, nil)
		if g["ringTimeout"] != float64(45) {
			t.Errorf("ringTimeout = %v: a stale batch applied", g["ringTimeout"])
		}
	})
}

// TestAgentApply (flow canvas decision of 2026-11-10) fails if the
// assistant cannot apply its own batch proposal as an operator, if the
// batch, the proposal or its audit row do not say it was applied by the AI
// (via ai:<session>) on behalf of that user, if the applied batch cannot be
// reverted with the ordinary revert, if a viewer's agent apply changes
// anything, or if a proposal that is not one batch is applied this way.
func TestAgentApply(t *testing.T) {
	e := newEnv(t)
	s := e.seed()
	group := map[string]any{"operationId": "createRingGroup", "body": map[string]any{
		"name": "support", "strategy": "sequential", "ringTimeout": 20,
		"members": []map[string]any{{"extensionId": json.Number(s.ext1), "position": 1}},
	}}
	const via = proposal.ViaAI + "11111111-2222-4333-8444-555555555555"
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("a viewer's agent apply is refused and changes nothing", func(t *testing.T) {
		pid := e.propose("assistant", batchAct(group))
		rev := e.revision()
		viewer := proposal.Identity{UserID: e.users["viewer"], TaskID: "task-v"}
		p, err := e.ps.ApplyAsAgent(context.Background(), pid, viewer, via)
		if err != nil {
			t.Fatal(err)
		}
		if p.Status != proposal.StatusFailed || p.Failure == nil || p.Failure.Status != 403 {
			t.Fatalf("viewer agent apply = %s %+v, want failed with 403", p.Status, p.Failure)
		}
		if e.revision() != rev || count(`SELECT count(*) FROM ring_groups WHERE name = 'support'`) != 0 ||
			count(`SELECT count(*) FROM config_batches`) != 0 {
			t.Fatal("a viewer's agent apply changed the configuration")
		}
		if p.AppliedVia != nil || p.BatchID != nil {
			t.Errorf("a failed apply records applied_via %v batch %v", p.AppliedVia, p.BatchID)
		}
	})

	t.Run("an operator's agent apply is audited as the AI for the user and reverts", func(t *testing.T) {
		pid := e.propose("assistant", batchAct(group))
		op := proposal.Identity{UserID: e.users["operator"], TaskID: "task-o"}
		rev := e.revision()
		p, err := e.ps.ApplyAsAgent(context.Background(), pid, op, via)
		if err != nil {
			t.Fatal(err)
		}
		if p.Status != proposal.StatusApplied || p.AppliedBy == nil || *p.AppliedBy != e.users["operator"] {
			t.Fatalf("agent apply = %s by %v %+v", p.Status, p.AppliedBy, p.Failure)
		}
		if p.AppliedVia == nil || *p.AppliedVia != via || p.BatchID == nil {
			t.Fatalf("applied_via %v batch %v, want %s and the batch", p.AppliedVia, p.BatchID, via)
		}
		if e.revision() != rev+1 || count(`SELECT count(*) FROM ring_groups WHERE name = 'support'`) != 1 {
			t.Fatal("the batch did not apply in one step")
		}
		var bvia, actor string
		if err := e.db.QueryRow(`SELECT via, actor FROM config_batches WHERE id = $1`, *p.BatchID).Scan(&bvia, &actor); err != nil {
			t.Fatal(err)
		}
		if bvia != via || actor != "user:operator" {
			t.Errorf("batch row via %q actor %q, want %s and user:operator", bvia, actor, via)
		}
		var aactor, avia string
		if err := e.db.QueryRow(`SELECT actor, via FROM audit_events WHERE resource = 'ai_proposal' AND resource_id = $1 AND action = 'apply_applied'`, pid).Scan(&aactor, &avia); err != nil {
			t.Fatal(err)
		}
		if aactor != "user:operator" || avia != via {
			t.Errorf("proposal audit actor %q via %q, want user:operator and %s", aactor, avia, via)
		}
		if n := count(`SELECT count(*) FROM audit_events WHERE resource = 'ring_group' AND actor = 'user:operator' AND via = $1`, "batch:"+*p.BatchID); n != 1 {
			t.Errorf("ring group audit rows of the batch = %d, want 1", n)
		}
		got := e.must("viewer", 200, "GET", "/api/v1/ai/proposals/"+pid, nil)
		if got["appliedVia"] != via || got["batchId"] != *p.BatchID {
			t.Errorf("getAIProposal appliedVia %v batchId %v", got["appliedVia"], got["batchId"])
		}
		e.must("operator", 200, "POST", "/api/v1/config/batches/"+*p.BatchID+"/revert", map[string]any{})
		if count(`SELECT count(*) FROM ring_groups WHERE name = 'support'`) != 0 {
			t.Error("the revert left the ring group the AI created")
		}
	})

	t.Run("only a batch proposal", func(t *testing.T) {
		pid := e.propose("assistant", act("updateRingGroup", map[string]string{"id": s.group}, map[string]any{"ringTimeout": 33}))
		_, err := e.ps.ApplyAsAgent(context.Background(), pid, e.ident(), via)
		if !errors.Is(err, proposal.ErrNotBatch) {
			t.Fatalf("agent apply of a plain proposal: err = %v, want ErrNotBatch", err)
		}
		if e.status(pid) != "open" {
			t.Errorf("status = %s, want open", e.status(pid))
		}
	})
}
