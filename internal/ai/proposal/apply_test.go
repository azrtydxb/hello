package proposal_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/azrtydxb/hello/internal/ai/proposal"
)

// propose validates and stores a proposal made for the operator.
func (e *env) propose(source string, actions ...proposal.Action) string {
	e.t.Helper()
	uid := e.users["operator"]
	d := proposal.Draft{Source: source, Title: "Change", Rationale: "because", CreatedBy: &uid, Actions: actions}
	if err := e.val.Validate(context.Background(), e.ident(), &d); err != nil {
		e.t.Fatal(err)
	}
	pid, err := e.ps.Upsert(context.Background(), d)
	if err != nil {
		e.t.Fatal(err)
	}
	return pid
}

func (e *env) status(pid string) string {
	e.t.Helper()
	var st string
	if err := e.db.QueryRow(`SELECT status FROM ai_proposals WHERE id = $1`, pid).Scan(&st); err != nil {
		e.t.Fatal(err)
	}
	return st
}

func (e *env) extName(id string) string {
	e.t.Helper()
	return fmt.Sprint(e.must("admin", 200, "GET", "/api/v1/extensions/"+id, nil)["name"])
}

func (e *env) auditVia(action string) (actor string, via sql.NullString) {
	e.t.Helper()
	if err := e.db.QueryRow(`SELECT actor, via FROM audit_events WHERE action = $1 AND resource = 'extension' ORDER BY id DESC LIMIT 1`, action).Scan(&actor, &via); err != nil {
		e.t.Fatalf("audit %s: %v", action, err)
	}
	return actor, via
}

// TestProposalApply: apply replays each action as the applier with via
// ai-proposal:<id>, stops at the first failure, goes stale only when the
// revision moved and a target changed, lets one of two simultaneous appliers
// win, and dismiss and apply each write an audit row. Fails if a viewer can
// apply, a stale proposal sends anything, the via is missing, a failing
// second action undoes or hides the first, or a non-open proposal applies.
func TestProposalApply(t *testing.T) {
	e := newEnv(t)
	s := e.seed()
	ext1, ext2 := map[string]string{"id": s.ext1}, map[string]string{"id": s.ext2}
	rename := func(p map[string]string, name string) proposal.Action {
		return act("updateExtension", p, map[string]any{"name": name})
	}

	t.Run("applies as the applier with the via", func(t *testing.T) {
		pid := e.propose("assistant", rename(ext1, "Reception"), rename(ext2, "Billing"))
		e.must("viewer", 403, "POST", "/api/v1/ai/proposals/"+pid+"/apply", nil)
		if e.status(pid) != "open" || e.extName(s.ext1) != "Desk" {
			t.Fatal("a viewer's apply changed something")
		}
		got := e.must("operator", 200, "POST", "/api/v1/ai/proposals/"+pid+"/apply", nil)
		if got["status"] != "applied" || got["appliedBy"] == nil {
			t.Fatalf("apply = %v", got)
		}
		if e.extName(s.ext1) != "Reception" || e.extName(s.ext2) != "Billing" {
			t.Fatal("the actions were not applied")
		}
		actor, via := e.auditVia("update")
		toks, err := e.st.ListTokens(context.Background(), e.users["operator"])
		if err != nil || len(toks) != 1 {
			t.Fatalf("tokens = %v %v", toks, err)
		}
		if want := fmt.Sprintf("token:%d", toks[0].ID); actor != want || !via.Valid || via.String != "ai-proposal:"+pid {
			t.Errorf("config audit = %q %v, want the operator's token and via ai-proposal:%s", actor, via, pid)
		}
		var n int
		if err := e.db.QueryRow(`SELECT count(*) FROM audit_events WHERE resource = 'ai_proposal' AND resource_id = $1 AND actor LIKE 'token:%'`, pid).Scan(&n); err != nil || n != 1 {
			t.Errorf("proposal audit rows = %d %v", n, err)
		}
		// Not open any more.
		r := e.do("operator", "POST", "/api/v1/ai/proposals/"+pid+"/apply", nil)
		if r.code != 409 || r.json(t)["error"].(map[string]any)["code"] != "proposal_not_open" {
			t.Errorf("second apply = %d %s", r.code, r.body)
		}
		e.must("operator", 404, "POST", "/api/v1/ai/proposals/00000000-0000-0000-0000-000000000000/apply", nil)
		e.must("operator", 404, "POST", "/api/v1/ai/proposals/not-a-uuid/apply", nil)
	})

	t.Run("stale when a target changed", func(t *testing.T) {
		pid := e.propose("assistant", rename(ext1, "Lobby"))
		e.must("admin", 200, "PATCH", "/api/v1/extensions/"+s.ext1, map[string]any{"name": "Front desk"})
		r := e.do("operator", "POST", "/api/v1/ai/proposals/"+pid+"/apply", nil)
		if r.code != 409 || r.json(t)["error"].(map[string]any)["code"] != "proposal_stale" {
			t.Fatalf("apply = %d %s, want 409 proposal_stale", r.code, r.body)
		}
		if e.status(pid) != "stale" || e.extName(s.ext1) != "Front desk" {
			t.Fatalf("status = %s, name = %s: a stale proposal sent something", e.status(pid), e.extName(s.ext1))
		}
	})

	t.Run("applies when only other targets changed", func(t *testing.T) {
		pid := e.propose("assistant", rename(ext2, "Support"))
		e.must("admin", 201, "POST", "/api/v1/extensions", map[string]any{"number": "103", "name": "New"})
		got := e.must("operator", 200, "POST", "/api/v1/ai/proposals/"+pid+"/apply", nil)
		if got["status"] != "applied" || e.extName(s.ext2) != "Support" {
			t.Fatalf("apply = %v", got)
		}
	})

	t.Run("stops at the first failure", func(t *testing.T) {
		pid := e.propose("assistant",
			rename(ext1, "First"),
			act("updateRingGroup", map[string]string{"id": s.group}, map[string]any{"failureKind": "external", "failureTarget": ""}),
			rename(ext2, "Never"))
		got := e.must("operator", 200, "POST", "/api/v1/ai/proposals/"+pid+"/apply", nil)
		f, _ := got["failure"].(map[string]any)
		if got["status"] != "failed" || f == nil || f["index"] != float64(1) || f["status"] != float64(400) || fmt.Sprint(f["applied"]) != "[0]" {
			t.Fatalf("apply = %v, want failed at action 1 with action 0 applied", got)
		}
		if e.extName(s.ext1) != "First" || e.extName(s.ext2) == "Never" {
			t.Fatalf("names = %s %s", e.extName(s.ext1), e.extName(s.ext2))
		}
	})

	t.Run("a conflict is stale", func(t *testing.T) {
		pid := e.propose("assistant", act("updateExtension", ext2, map[string]any{"number": "101"}))
		r := e.do("operator", "POST", "/api/v1/ai/proposals/"+pid+"/apply", nil)
		if r.code != 409 || e.status(pid) != "stale" {
			t.Fatalf("apply = %d %s, status %s", r.code, r.body, e.status(pid))
		}
	})

	t.Run("one of two appliers wins", func(t *testing.T) {
		pid := e.propose("assistant", rename(ext1, "Race"))
		var wg sync.WaitGroup
		codes := make([]int, 2)
		for i := range codes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes[i] = e.do("operator", "POST", "/api/v1/ai/proposals/"+pid+"/apply", nil).code
			}()
		}
		wg.Wait()
		if (codes[0] != 200 || codes[1] != 409) && (codes[0] != 409 || codes[1] != 200) {
			t.Fatalf("codes = %v, want one 200 and one 409", codes)
		}
		if e.status(pid) != "applied" {
			t.Fatalf("status = %s", e.status(pid))
		}
	})

	t.Run("get shows current", func(t *testing.T) {
		pid := e.propose("assistant", rename(ext1, "Shown"))
		e.must("admin", 200, "PATCH", "/api/v1/extensions/"+s.ext1, map[string]any{"name": "Changed since"})
		got := e.must("viewer", 200, "GET", "/api/v1/ai/proposals/"+pid, nil)
		a := got["actions"].([]any)[0].(map[string]any)
		if a["before"].(map[string]any)["name"] == a["current"].(map[string]any)["name"] || a["current"].(map[string]any)["name"] != "Changed since" {
			t.Errorf("action = %v, want current to differ from before", a)
		}
		list := e.must("viewer", 200, "GET", "/api/v1/ai/proposals?status=open", nil)["items"].([]any)
		if len(list) == 0 {
			t.Error("the open list is empty")
		}
		e.must("viewer", 400, "GET", "/api/v1/ai/proposals?status=bogus", nil)
	})

	t.Run("dismiss", func(t *testing.T) {
		pid := e.propose("assistant", rename(ext1, "Dismissed"))
		e.must("viewer", 403, "POST", "/api/v1/ai/proposals/"+pid+"/dismiss", map[string]any{"reason": "wrong"})
		e.must("operator", 400, "POST", "/api/v1/ai/proposals/"+pid+"/dismiss", map[string]any{"reason": "meh"})
		e.must("operator", 400, "POST", "/api/v1/ai/proposals/"+pid+"/dismiss", map[string]any{})
		long := make([]byte, 501)
		for i := range long {
			long[i] = 'x'
		}
		e.must("operator", 400, "POST", "/api/v1/ai/proposals/"+pid+"/dismiss", map[string]any{"reason": "other", "text": string(long)})
		got := e.must("operator", 200, "POST", "/api/v1/ai/proposals/"+pid+"/dismiss", map[string]any{"reason": "other", "text": "no thanks"})
		if got["status"] != "dismissed" || got["dismissReason"] != "other" || got["dismissText"] != "no thanks" || got["dismissedBy"] == nil {
			t.Fatalf("dismiss = %v", got)
		}
		e.must("operator", 409, "POST", "/api/v1/ai/proposals/"+pid+"/dismiss", map[string]any{"reason": "wrong"})
		e.must("operator", 409, "POST", "/api/v1/ai/proposals/"+pid+"/apply", nil)
		var n int
		if err := e.db.QueryRow(`SELECT count(*) FROM audit_events WHERE resource = 'ai_proposal' AND resource_id = $1 AND action = 'dismiss'`, pid).Scan(&n); err != nil || n != 1 {
			t.Errorf("dismiss audit rows = %d %v", n, err)
		}
	})

	t.Run("store errors", func(t *testing.T) {
		ctx := context.Background()
		if _, err := e.ps.Apply(ctx, "00000000-0000-0000-0000-000000000000", http.Header{}); err == nil {
			t.Error("apply without an actor succeeded")
		}
		if err := e.ps.Dismiss(ctx, "00000000-0000-0000-0000-000000000000", 1, "wrong", ""); !errors.Is(err, proposal.ErrNotFound) {
			t.Errorf("dismiss of a missing proposal: %v", err)
		}
	})
}
