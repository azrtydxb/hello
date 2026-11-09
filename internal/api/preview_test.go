package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/flow"
)

// previewStore reads every agent as the operator user 5; every other store
// method panics (stubStore), which the test reads as "admitted".
type previewStore struct{ stubStore }

func (previewStore) UserActor(context.Context, int64) (auth.Actor, error) {
	return auth.Actor{UserID: 5, Username: "op", Role: auth.RoleOperator}, nil
}

// panicBatches is a batch store whose every method panics: a batch that
// reaches it was admitted.
type panicBatches struct{ BatchService }

// TestBatchPreviewGuard (spec flow-canvas S-19) fails if the agent identity
// that dry-runs an AI proposal's batch may write anything outside a dry-run
// batch: a direct object write, a batch that is not a dry run, a revert, or
// if a plain agent gains write scope. The dry run itself and reads must
// reach their handlers.
func TestBatchPreviewGuard(t *testing.T) {
	h := Handler(Config{Store: previewStore{}, Batches: panicBatches{}})
	call := func(ag auth.Agent, method, path, body string) (code int, out string) {
		t.Helper()
		defer func() {
			// A stub store method panicked: the request reached its handler.
			if r := recover(); r != nil {
				code, out = -1, "admitted"
			}
		}()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r = r.WithContext(auth.WithAgent(r.Context(), ag))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code, rec.Body.String()
	}
	preview := auth.Agent{UserID: 5, TaskID: "t", Preview: true}
	plain := auth.Agent{UserID: 5, TaskID: "t"}

	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/extensions", `{"number":"200","name":"x"}`},
		{http.MethodPatch, "/api/v1/ring-groups/1", `{"name":"x"}`},
		{http.MethodDelete, "/api/v1/routes/inbound/1", ``},
		{http.MethodPost, "/api/v1/config/batches/00000000-0000-4000-8000-000000000000/revert", `{"dryRun":true}`},
		{http.MethodPost, "/api/v1/config/batch", `{"baseRevision":1,"ops":[{"operationId":"deleteRingGroup","pathParams":{"id":"1"}}]}`},
	} {
		code, body := call(preview, c.method, c.path, c.body)
		if code != http.StatusForbidden || !strings.Contains(body, "insufficient_scope") {
			t.Errorf("preview %s %s = %d %s, want 403 insufficient_scope", c.method, c.path, code, body)
		}
		if code, body := call(plain, c.method, c.path, c.body); code != http.StatusForbidden {
			t.Errorf("plain agent %s %s = %d %s, want 403", c.method, c.path, code, body)
		}
	}
	// The dry run passes the guard and reaches the batch store.
	code, body := call(preview, http.MethodPost, "/api/v1/config/batch",
		`{"baseRevision":1,"dryRun":true,"ops":[{"operationId":"deleteRingGroup","pathParams":{"id":"1"}}]}`)
	if code != -1 {
		t.Errorf("preview dry run = %d %s, want it admitted", code, body)
	}
	if code, body := call(plain, http.MethodPost, "/api/v1/config/batch",
		`{"baseRevision":1,"dryRun":true,"ops":[]}`); code != http.StatusForbidden {
		t.Errorf("plain agent dry run = %d %s, want 403", code, body)
	}
	// Reads are the agent's reads.
	if code, _ := call(preview, http.MethodGet, "/api/v1/extensions", ""); code != -1 {
		t.Errorf("preview read = %d, want it admitted", code)
	}
}

// TestFlowGraphSummary (spec flow-canvas S-17) fails if fields=summary
// keeps a node's summary or an edge's details, or drops what an AI needs to
// follow the flow and to base a batch on it (ids, labels, versions, ports,
// owners).
func TestFlowGraphSummary(t *testing.T) {
	g := flow.Graph{ConfigRevision: 4,
		Nodes: []flow.Node{{ID: "ivr:1", Kind: flow.KindIVR, Label: "Main", Version: "v1", Summary: map[string]any{"greeting": "long text"}}},
		Edges: []flow.Edge{{ID: "e", Source: flow.Ref{Node: "ivr:1", Port: "key:1"}, Target: flow.Ref{Node: "ringGroup:2", Port: "in"},
			Owner: flow.Owner{Node: "ivr:1", Field: "options"}, Data: map[string]any{"key": "1"}}}}
	s := summarize(g)
	if len(s.Nodes[0].Summary) != 0 || s.Edges[0].Data != nil {
		t.Errorf("summary kept details: %+v", s)
	}
	if n := s.Nodes[0]; n.ID != "ivr:1" || n.Label != "Main" || n.Version != "v1" || s.Edges[0].Owner.Field != "options" || s.ConfigRevision != 4 {
		t.Errorf("summary dropped the flow: %+v", s)
	}
	if len(g.Nodes[0].Summary) == 0 {
		t.Error("summarize changed its input")
	}
}
