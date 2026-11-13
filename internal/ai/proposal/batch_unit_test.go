package proposal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/azrtydxb/hello/internal/auth"
)

// batchAPI answers the validator's two requests: the flow graph read and
// the batch dry run, recording the dry run's body and identity.
type batchAPI struct {
	mu       sync.Mutex
	status   int    // the dry run's status
	answer   string // the dry run's body
	dryRuns  []map[string]any
	previews []auth.Agent
}

func (f *batchAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/flow/graph":
		_, _ = io.WriteString(w, `{"configRevision": 41, "nodes": [
			{"id": "ringGroup:2", "kind": "ringGroup", "label": "sales", "summary": {}, "version": "v-rg2"},
			{"id": "inboundRoute:5", "kind": "inboundRoute", "label": "Main", "summary": {}, "version": "v-in5"}
		], "edges": [], "warnings": []}`)
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/config/batch":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		ag, _ := auth.AgentFrom(r.Context())
		f.mu.Lock()
		f.dryRuns = append(f.dryRuns, body)
		f.previews = append(f.previews, ag)
		status, answer := f.status, f.answer
		f.mu.Unlock()
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func batchValidator(t *testing.T, api http.Handler) *SchemaValidator {
	t.Helper()
	raw, err := os.ReadFile("../../api/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := apispec.Load(raw)
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewValidator(spec, api, nil)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func batchAction(t *testing.T, body any) Action {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return Action{OperationID: BatchOp, PathParams: map[string]string{}, Body: b}
}

const okDryRun = `{"applied": false, "baseRevision": 41, "revision": 41,
	"results": [{"operationId": "updateRingGroup", "status": "updated", "diff": []}],
	"warnings": [], "flowDiff": {"added": [], "changed": [{"id": "ringGroup:2", "kind": "ringGroup", "label": "Sales team", "summary": {}}],
	"removed": ["inboundRoute:5"], "edgesAdded": [], "edgesRemoved": []}}`

// TestBatchProposalValidation (spec flow-canvas S-19, the unit half of
// TestProposalValidation for batches) fails if a proposal's batch is stored
// with an inner operation outside the inner allowlist, a credential
// property, more than 50 operations, another action beside it, a target
// that does not exist, or a dry run that failed; if the dry run is not made
// as the user's preview agent with dryRun set; if the stored batch is a dry
// run or lacks the revision and versions it is based on; or if the preview
// is not kept.
func TestBatchProposalValidation(t *testing.T) {
	ident := Identity{UserID: 9, TaskID: "task-1"}
	ctx := context.Background()
	validate := func(api *batchAPI, actions ...Action) (*Draft, error) {
		d := &Draft{Source: SourceAssistant, Title: "t", Actions: actions}
		return d, batchValidator(t, api).Validate(ctx, ident, d)
	}
	good := map[string]any{"dryRun": true, "ops": []map[string]any{
		{"operationId": "updateRingGroup", "pathParams": map[string]string{"id": "2"}, "body": map[string]any{"name": "Sales team"}},
		{"operationId": "deleteInboundRoute", "pathParams": map[string]string{"id": "5"}},
	}}

	t.Run("a valid batch is dry-run as the preview agent and stored to apply", func(t *testing.T) {
		api := &batchAPI{answer: okDryRun}
		d, err := validate(api, batchAction(t, good))
		if err != nil {
			t.Fatal(err)
		}
		if len(api.dryRuns) != 1 {
			t.Fatalf("%d dry runs", len(api.dryRuns))
		}
		if p := api.previews[0]; !p.Preview || p.UserID != 9 || p.TaskID != "task-1" {
			t.Errorf("dry run identity = %+v, want the user's preview agent", p)
		}
		if dr := api.dryRuns[0]; dr["dryRun"] != true || dr["baseRevision"] != float64(41) {
			t.Errorf("dry run body = %v, want dryRun and baseRevision 41", dr)
		}
		a := d.Actions[0]
		var stored map[string]any
		if err := json.Unmarshal(a.Body, &stored); err != nil {
			t.Fatal(err)
		}
		if _, set := stored["dryRun"]; set {
			t.Errorf("the stored batch is a dry run: %s", a.Body)
		}
		if stored["baseRevision"] != float64(41) {
			t.Errorf("stored baseRevision = %v, want 41", stored["baseRevision"])
		}
		ops := stored["ops"].([]any)
		if e := ops[0].(map[string]any)["expect"]; e != "v-rg2" {
			t.Errorf("op 0 expect = %v, want the graph's version v-rg2", e)
		}
		if a.Preview == nil || !strings.Contains(string(a.Preview.FlowDiff), "Sales team") || a.Preview.BaseRevision != 41 {
			t.Errorf("preview = %+v", a.Preview)
		}
		if !a.Destructive || len(a.References) != 1 || a.References[0].ID != "5" || a.References[0].Name != "Main" {
			t.Errorf("destructive %v, references %+v: want the deleted inbound route", a.Destructive, a.References)
		}
	})

	t.Run("refused before the dry run", func(t *testing.T) {
		many := make([]map[string]any, MaxBatchOps+1)
		for i := range many {
			many[i] = map[string]any{"operationId": "updateRingGroup", "pathParams": map[string]string{"id": "2"}, "body": map[string]any{"name": "x"}}
		}
		cases := map[string]struct {
			actions []Action
			want    string
		}{
			"secret-returning create": {[]Action{batchAction(t, map[string]any{"ops": []map[string]any{
				{"operationId": "createDevice", "body": map[string]any{"username": "x"}}}})}, "not allowed"},
			"credential rotation": {[]Action{batchAction(t, map[string]any{"ops": []map[string]any{
				{"operationId": "rotateDeviceSecret", "pathParams": map[string]string{"id": "1"}}}})}, "not allowed"},
			"not batchable": {[]Action{batchAction(t, map[string]any{"ops": []map[string]any{
				{"operationId": "sendMessage", "body": map[string]any{"text": "x"}}}})}, "not allowed"},
			"credential property": {[]Action{batchAction(t, map[string]any{"ops": []map[string]any{
				{"operationId": "updateTrunk", "pathParams": map[string]string{"id": "1"}, "body": map[string]any{"password": "hunter2"}}}})}, "credential"},
			"nested credential": {[]Action{batchAction(t, map[string]any{"ops": []map[string]any{
				{"operationId": "createExtension", "body": map[string]any{"number": "200", "name": "x", "voicemail": map[string]any{"pin": "1234"}}}}})}, "credential"},
			"too many":         {[]Action{batchAction(t, map[string]any{"ops": many})}, "1 to 50"},
			"none":             {[]Action{batchAction(t, map[string]any{"ops": []any{}})}, "1 to 50"},
			"unknown property": {[]Action{batchAction(t, map[string]any{"ops": good["ops"], "apply": true})}, "not valid JSON"},
			"bad placeholder": {[]Action{batchAction(t, map[string]any{"ops": []map[string]any{
				{"operationId": "updateRingGroup", "pathParams": map[string]string{"id": "${nope.id}"}, "body": map[string]any{"name": "x"}}}})}, "no earlier operation"},
			"beside another action": {[]Action{batchAction(t, good),
				{OperationID: "updateExtension", PathParams: map[string]string{"id": "1"}, Body: json.RawMessage(`{"name":"x"}`)}}, "that one action"},
			"missing target": {[]Action{batchAction(t, map[string]any{"ops": []map[string]any{
				{"operationId": "updateRingGroup", "pathParams": map[string]string{"id": "77"}, "body": map[string]any{"name": "x"}}}})}, "does not exist"},
		}
		for name, c := range cases {
			api := &batchAPI{answer: okDryRun}
			_, err := validate(api, c.actions...)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%s: err = %v, want %q", name, err, c.want)
			}
			if len(api.dryRuns) != 0 {
				t.Errorf("%s: dry-run although refused", name)
			}
		}
	})

	t.Run("a refused dry run is the model's error", func(t *testing.T) {
		api := &batchAPI{status: http.StatusUnprocessableEntity, answer: `{"error": {"code": "validation_failed", "message": "the batch is not valid",
			"errors": [{"opIndex": 0, "path": "body.strategy", "message": "must be one of ring_all, sequential"}]}}`}
		if _, err := validate(api, batchAction(t, good)); err == nil || !strings.Contains(err.Error(), "must be one of ring_all") {
			t.Errorf("err = %v, want the problem of operation 0", err)
		}
		api = &batchAPI{status: http.StatusForbidden, answer: `{"error": {"code": "forbidden_role", "message": "operation 0: this needs the admin role"}}`}
		if _, err := validate(api, batchAction(t, good)); err == nil || !strings.Contains(err.Error(), "cannot make this change") {
			t.Errorf("err = %v, want the role refusal", err)
		}
	})

	t.Run("the fingerprint and targets ignore what the batch is based on", func(t *testing.T) {
		a := batchAction(t, map[string]any{"baseRevision": 3, "ops": []map[string]any{
			{"operationId": "updateRingGroup", "pathParams": map[string]string{"id": "2"}, "body": map[string]any{"name": "x"}, "expect": "old"}}})
		b := batchAction(t, map[string]any{"baseRevision": 9, "ops": []map[string]any{
			{"operationId": "updateRingGroup", "pathParams": map[string]string{"id": "2"}, "body": map[string]any{"name": "x"}, "expect": "new"}}})
		c := batchAction(t, map[string]any{"baseRevision": 9, "ops": []map[string]any{
			{"operationId": "updateRingGroup", "pathParams": map[string]string{"id": "2"}, "body": map[string]any{"name": "y"}}}})
		if Fingerprint("assistant", []Action{a}) != Fingerprint("assistant", []Action{b}) {
			t.Error("the same change at another revision has another fingerprint")
		}
		if Fingerprint("assistant", []Action{a}) == Fingerprint("assistant", []Action{c}) {
			t.Error("different changes share a fingerprint")
		}
		if got := Targets([]Action{a}); len(got) != 1 || got[0] != "RingGroup/2" {
			t.Errorf("targets = %v, want the ring group the batch changes", got)
		}
	})
}
