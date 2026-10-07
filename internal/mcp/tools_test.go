package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/azrtydxb/hello/internal/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestToolsFromOpenAPI fails if any non-excluded operation has no tool or a
// tool has no operation, a tool's input or output schema differs from the
// operation's, annotations are wrong for the method, an excluded operation
// is listed, tools/list shows a write tool without write, or calling a tool
// beyond the caller's scope does not get the step-up 403 (spec S-14).
func TestToolsFromOpenAPI(t *testing.T) {
	t.Run("embedded document", func(t *testing.T) {
		ops := loadSpec(t).Operations()
		if len(ops) == 0 {
			t.Log("apispec has no operations yet (plan Task 2); the fixture below covers the generator")
		}
		checkTools(t, ops)
	})
	t.Run("fixture", func(t *testing.T) { checkTools(t, fixtureOps()) })

	ts, _ := newTestServer(t, echoAPI(), fixtureOps(), nil)
	listed := func(tok string) []string {
		res, err := connect(t, ts.URL, tok, "2025-11-25").ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, tl := range res.Tools {
			names = append(names, tl.Name)
		}
		return names
	}
	read, write, admin := listed(tokRead), listed(tokWrite), listed(tokAdmin)
	for _, op := range fixtureOps() {
		if op.Exclude != "" {
			if slices.Contains(admin, op.ID) {
				t.Errorf("excluded %s is listed", op.ID)
			}
			continue
		}
		for _, c := range []struct {
			who   string
			names []string
			has   auth.Scopes
		}{{"read", read, auth.Scopes{auth.ScopeRead}}, {"write", write, auth.Scopes{auth.ScopeWrite}}, {"admin", admin, auth.Scopes{auth.ScopeAdmin}}} {
			if want := op.Scope == "" || c.has.Has(op.Scope); slices.Contains(c.names, op.ID) != want {
				t.Errorf("%s: %s listed = %v, want %v", c.who, op.ID, !want, want)
			}
		}
	}

	t.Run("step-up", func(t *testing.T) {
		for _, c := range []struct{ tok, tool, scope string }{
			{tokRead, "createExtension", "write"},
			{tokRead, "deleteExtension", "write"},
			{tokWrite, "drainNode", "admin"},
		} {
			resp, _ := post(t, ts.URL, callBody(c.tool, map[string]any{"id": "1"}), map[string]string{"Authorization": "Bearer " + c.tok})
			ch := resp.Header.Get("WWW-Authenticate")
			if resp.StatusCode != http.StatusForbidden || !strings.Contains(ch, `error="insufficient_scope"`) ||
				!strings.Contains(ch, `scope="`+c.scope+`"`) || !strings.Contains(ch, `resource_metadata="`+testMetadata+`"`) {
				t.Errorf("%s calling %s = %d %q, want 403 insufficient_scope %s", c.tok, c.tool, resp.StatusCode, ch, c.scope)
			}
		}
		resp, body := post(t, ts.URL, callBody("listExtensions", map[string]any{}), map[string]string{"Authorization": "Bearer " + tokWrite})
		if resp.StatusCode != http.StatusOK || strings.Contains(body, `"error"`) && strings.Contains(body, "insufficient") {
			t.Errorf("write token calling a read tool = %d %s", resp.StatusCode, body)
		}
	})
	t.Run("excluded and unknown tools", func(t *testing.T) {
		for _, name := range []string{"login", "rotateDeviceSecret", "noSuchTool"} {
			_, body := post(t, ts.URL, callBody(name, map[string]any{}), map[string]string{"Authorization": "Bearer " + tokAdmin})
			var msg struct {
				Error *struct{ Code int } `json:"error"`
			}
			if err := json.Unmarshal([]byte(body), &msg); err != nil || msg.Error == nil || msg.Error.Code != -32602 {
				t.Errorf("calling %s = %s, want a JSON-RPC invalid-params error", name, body)
			}
		}
	})
}

// checkTools compares the generated tools with ops.
func checkTools(t *testing.T, ops []apispec.Operation) {
	t.Helper()
	tools, _, err := buildTools(ops)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]apispec.Operation{}
	for _, op := range ops {
		byID[op.ID] = op
		tl, ok := tools[op.ID]
		if op.Exclude != "" {
			if ok {
				t.Errorf("excluded operation %s has a tool", op.ID)
			}
			continue
		}
		if !ok {
			t.Errorf("operation %s has no tool", op.ID)
			continue
		}
		checkTool(t, op, tl.tool)
	}
	for name := range tools {
		if _, ok := byID[name]; !ok {
			t.Errorf("tool %s has no operation", name)
		}
	}
}

func checkTool(t *testing.T, op apispec.Operation, tl *sdk.Tool) {
	t.Helper()
	if tl.Name != op.ID || tl.Title != op.Summary || (op.Scope != "" && !strings.Contains(tl.Description, string(op.Scope))) {
		t.Errorf("%s: name %q title %q description %q", op.ID, tl.Name, tl.Title, tl.Description)
	}
	in := tl.InputSchema.(map[string]any)
	props := in["properties"].(map[string]any)
	required := in["required"].([]string)
	want := 0
	for _, p := range op.Params {
		if p.In != "path" && p.In != "query" {
			continue
		}
		want++
		got, ok := props[p.Name].(map[string]any)
		if !ok {
			t.Errorf("%s: parameter %s missing from the input schema", op.ID, p.Name)
			continue
		}
		if p.Schema != nil && got["type"] != p.Schema["type"] {
			t.Errorf("%s: parameter %s type %v, want %v", op.ID, p.Name, got["type"], p.Schema["type"])
		}
		if p.Description != "" && got["description"] != p.Description {
			t.Errorf("%s: parameter %s description %v", op.ID, p.Name, got["description"])
		}
		if (p.In == "path" || p.Required) != slices.Contains(required, p.Name) {
			t.Errorf("%s: parameter %s required = %v", op.ID, p.Name, slices.Contains(required, p.Name))
		}
	}
	if op.Body != nil && op.Body.JSON != nil {
		want++
		if !reflect.DeepEqual(props["body"], deepCopy(op.Body.JSON)) {
			t.Errorf("%s: body schema %v, want %v", op.ID, props["body"], op.Body.JSON)
		}
	}
	if len(props) != want {
		t.Errorf("%s: input schema has %d properties, want %d", op.ID, len(props), want)
	}
	if op.Output != nil && op.Output["type"] == "object" {
		out, ok := tl.OutputSchema.(map[string]any)
		if !ok {
			t.Errorf("%s: no output schema for an object 2xx body", op.ID)
		} else if len(op.Secrets) == 0 && !reflect.DeepEqual(out, deepCopy(op.Output)) {
			t.Errorf("%s: output schema differs from the operation's", op.ID)
		}
	} else if tl.OutputSchema != nil {
		t.Errorf("%s: output schema without an object 2xx body", op.ID)
	}
	a := tl.Annotations
	destructive := a.DestructiveHint != nil && *a.DestructiveHint
	if a.ReadOnlyHint != (op.Method == "GET") || destructive != (op.Method == "DELETE") ||
		a.IdempotentHint != (op.Method == "PUT" || op.Method == "DELETE") || a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Errorf("%s %s: annotations %+v", op.ID, op.Method, a)
	}
}
