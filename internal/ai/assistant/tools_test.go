package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/replay"
)

// TestAssistantTools (spec S-6) fails if a listed tool is missing from the
// document, is not GET, read and MCP-exposed, a tool result exceeds 16 KiB
// or holds an x-hello-secret value, a read is not replayed as the user's
// agent identity, or a message makes more than 8 steps or 16 tool calls.
func TestAssistantTools(t *testing.T) {
	spec := loadSpec(t)

	t.Run("every listed tool is a GET read MCP tool of the document", func(t *testing.T) {
		defs, err := toolDefs(spec)
		if err != nil {
			t.Fatal(err)
		}
		if len(defs) != len(assistantTools) {
			t.Fatalf("%d tools, want %d", len(defs), len(assistantTools))
		}
		for _, d := range defs {
			if d.op.Method != http.MethodGet || d.op.Scope != auth.ScopeRead || d.op.Exclude != "" {
				t.Errorf("%s: %s %s exclude=%q", d.op.ID, d.op.Method, d.op.Scope, d.op.Exclude)
			}
			var s map[string]any
			if err := json.Unmarshal(d.schema, &s); err != nil || s["type"] != "object" {
				t.Errorf("%s: schema %s", d.op.ID, d.schema)
			}
		}
	})

	t.Run("the read-only filter refuses writes, other scopes and MCP exclusions", func(t *testing.T) {
		for _, op := range []apispec.Operation{
			{ID: "createOutboundRoute", Method: http.MethodPost, Scope: auth.ScopeWrite},
			{ID: "deleteRingGroup", Method: http.MethodDelete, Scope: auth.ScopeWrite},
			{ID: "listTokens", Method: http.MethodGet, Scope: auth.ScopeAdmin},
			{ID: "exportCDRs", Method: http.MethodGet, Scope: auth.ScopeRead, Exclude: "large"},
			{ID: "sneaky", Method: http.MethodGet, Scope: auth.ScopeWrite},
			{ID: "testRoute", Method: http.MethodPost, Scope: auth.ScopeRead},
			{ID: "headThing", Method: http.MethodHead, Scope: auth.ScopeRead},
		} {
			if readOnly(op) == nil {
				t.Errorf("%s (%s %s) accepted as a tool", op.ID, op.Method, op.Scope)
			}
		}
		if err := readOnly(apispec.Operation{ID: "listCDRs", Method: http.MethodGet, Scope: auth.ScopeRead}); err != nil {
			t.Errorf("listCDRs refused: %v", err)
		}
		saved := assistantTools
		t.Cleanup(func() { assistantTools = saved })
		for _, bad := range []string{"createOutboundRoute", "exportCDRs", "noSuchOperation"} {
			assistantTools = append(append([]string(nil), saved...), bad)
			if _, err := toolDefs(spec); err == nil {
				t.Errorf("toolDefs accepted %s", bad)
			}
		}
		assistantTools = saved
	})

	t.Run("a read replays as the agent, withholds secrets and caps at 16 KiB", func(t *testing.T) {
		items := make([]map[string]any, 400)
		for i := range items {
			items[i] = map[string]any{"id": i, "secret": fmt.Sprintf("s3cret-%d", i), "note": strings.Repeat("x", 80)}
		}
		big, _ := json.Marshal(map[string]any{"items": items})
		api := &fakeAPI{bodies: map[string]string{"/api/v1/things": string(big)}}
		def := toolDef{op: apispec.Operation{ID: "listThings", Method: http.MethodGet, Path: "/api/v1/things", Scope: auth.ScopeRead,
			Secrets: []string{"/items/*/secret"}}, schema: json.RawMessage(`{"type":"object"}`)}
		a := &Assistant{cfg: Config{API: api, DataBlock: testDataBlock}, tools: []toolDef{def}}
		ident := auth.Agent{UserID: 7, TaskID: "t1"}
		ts, tools := a.newToolset(ident, func(error) {})
		out, err := tools[0].Execute(context.Background(), json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		block := out.(string)
		if strings.Contains(block, "s3cret") {
			t.Error("a secret reached the model")
		}
		if !strings.Contains(block, replay.Withheld) {
			t.Error("secrets not withheld")
		}
		body := strings.TrimSuffix(strings.TrimPrefix(block, "<data>\n"), "\n</data>")
		if len(body) > MaxResultBytes {
			t.Errorf("result is %d bytes, cap %d", len(body), MaxResultBytes)
		}
		var v struct {
			Items     []any `json:"items"`
			Truncated bool  `json:"truncated"`
			Total     int   `json:"total"`
		}
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			t.Fatal(err)
		}
		if !v.Truncated || v.Total != 400 || len(v.Items) == 0 || len(v.Items) >= 400 {
			t.Errorf("truncated=%v total=%d items=%d", v.Truncated, v.Total, len(v.Items))
		}
		calls := ts.Calls()
		if len(calls) != 1 || !calls[0].Truncated || calls[0].Status != 200 || calls[0].OperationID != "listThings" {
			t.Errorf("recorded %+v", calls)
		}
		seen := api.seen()
		if len(seen) != 1 || !seen[0].HasAgent || seen[0].Agent != ident || seen[0].Method != http.MethodGet {
			t.Errorf("replayed %+v", seen)
		}
		if seen[0].Header.Get("Authorization") != "" || seen[0].Header.Get("Cookie") != "" {
			t.Error("a read carried credentials; it must rely on the agent identity")
		}
	})

	t.Run("an oversized single object becomes a note", func(t *testing.T) {
		v, cut := capResult(map[string]any{"trace": strings.Repeat("y", 40<<10)})
		if !cut || size(v) > MaxResultBytes {
			t.Errorf("cut=%v size=%d", cut, size(v))
		}
		if _, cut := capResult(map[string]any{"small": 1}); cut {
			t.Error("a small result was cut")
		}
	})

	t.Run("a message makes at most 8 steps and 16 tool calls", func(t *testing.T) {
		var script []*provider.Response
		for i := range 12 {
			script = append(script, toolResp(
				call(fmt.Sprintf("a%d", i), "listExtensions", `{}`),
				call(fmt.Sprintf("b%d", i), "listCDRs", `{}`),
				call(fmt.Sprintf("c%d", i), "listTrunks", `{}`),
			))
		}
		r := newRig(t, script...)
		r.api.bodies["/api/v1/extensions"] = `{"items":[]}`
		r.api.bodies["/api/v1/cdrs"] = `{"items":[]}`
		r.api.bodies["/api/v1/trunks"] = `{"items":[]}`
		_, o := r.ask(t, "loop forever")
		if o.err == nil {
			t.Fatal("a message that never answers succeeded")
		}
		if n := len(r.api.seen()); n != MaxToolCalls {
			t.Errorf("%d reads replayed for 24 asked, want the limit %d", n, MaxToolCalls)
		}
		if len(r.calls) == 0 || r.calls[0].MaxSteps != DefaultMaxSteps {
			t.Fatalf("MaxSteps not passed to Generate")
		}
		// mockGenerate makes up to 3 attempts of at most MaxSteps steps.
		if n := len(r.model.RecordedCalls()); n < DefaultMaxSteps || n > 3*DefaultMaxSteps {
			t.Errorf("%d model steps, want the first attempt's %d and at most %d", n, DefaultMaxSteps, 3*DefaultMaxSteps)
		}
		if msgs := r.store.assistantMessages(); len(msgs) != 0 {
			t.Errorf("stored %d answers", len(msgs))
		}
		var limited float64
		for _, op := range []string{"listExtensions", "listCDRs", "listTrunks"} {
			limited += testutil.ToFloat64(r.a.toolCalls.WithLabelValues(op, "limit"))
		}
		if limited == 0 || testutil.ToFloat64(r.a.toolCalls.WithLabelValues("listCDRs", "ok")) == 0 {
			t.Error("hello_ai_tool_calls_total did not count the calls and the refused ones")
		}
	})
}
