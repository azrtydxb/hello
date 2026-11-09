package proposal_test

import (
	"context"
	"encoding/json"
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
