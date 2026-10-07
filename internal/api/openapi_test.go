package api

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/azrtydxb/hello/internal/auth"
)

func loadSpec(t *testing.T) *apispec.Spec {
	t.Helper()
	s, err := apispec.Load(openAPI)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// pendingRoutes are the route rows whose handler is still s.pending: their
// stream documents them when it lands the handler (plan ai-external-access,
// Shared contracts), so until then they are not required in the document.
func pendingRoutes() map[string]bool {
	s := &server{}
	pending := reflect.ValueOf(s.pending).Pointer()
	out := map[string]bool{}
	for _, r := range s.routes() {
		if reflect.ValueOf(r.H).Pointer() == pending {
			out[r.Method+" "+r.Pattern] = true
		}
	}
	return out
}

// TestRoutesMatchOpenAPI fails if a route in the table has no operation in
// openapi.json, an operation has no route, or a route's scope or minimum
// role differs from the operation's x-hello-scope or x-hello-role (spec
// ai-external-access S-1, S-23).
func TestRoutesMatchOpenAPI(t *testing.T) {
	ops := map[string]apispec.Operation{}
	for _, op := range loadSpec(t).Operations() {
		ops[op.Method+" "+op.Path] = op
	}
	pending := pendingRoutes()
	routed := map[string]bool{}
	for _, r := range Routes() {
		key := r.Method + " " + r.Pattern
		routed[key] = true
		op, ok := ops[key]
		switch {
		case !ok && pending[key]:
			continue
		case !ok:
			t.Errorf("%s is routed but not documented", key)
			continue
		}
		if op.Scope != r.Scope {
			t.Errorf("%s (%s): x-hello-scope %q, route scope %q", key, op.ID, op.Scope, r.Scope)
		}
		if op.Role != r.Role {
			t.Errorf("%s (%s): x-hello-role %q, route role %q", key, op.ID, op.Role, r.Role)
		}
	}
	for key, op := range ops {
		if !routed[key] {
			t.Errorf("%s (%s) is documented but not routed", key, op.ID)
		}
	}
	if len(ops) < 100 {
		t.Fatalf("only %d operations documented", len(ops))
	}
}

// sentence is at least one full sentence: a capital, words, a full stop.
var sentence = regexp.MustCompile(`^[A-Z][^.]*\s\S+[^.]*\.(\s|$)`)

// secretName is a response property name that carries a credential.
var secretName = regexp.MustCompile(`(?i)(secret|token|password|provisioningurl)$`)

// TestOpenAPIForTools fails if an operation lacks what the MCP tool
// generator needs (spec S-4): a summary, a description of at least one
// sentence, a description on every parameter and top-level request body
// property, x-hello-scope (x-hello-role) on every non-public operation,
// x-hello-mcp, an exclusion on every secrets-scoped or upload-only
// operation; or if a string response property named like a credential
// lacks x-hello-secret.
func TestOpenAPIForTools(t *testing.T) {
	spec := loadSpec(t)
	var raw struct {
		Paths map[string]map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(openAPI, &raw); err != nil {
		t.Fatal(err)
	}
	public := map[string]bool{}
	for _, r := range Routes() {
		public[r.Method+" "+r.Pattern] = r.Public
	}
	for _, op := range spec.Operations() {
		name := op.ID
		if op.Summary == "" {
			t.Errorf("%s: no summary", name)
		}
		if !sentence.MatchString(op.Description) {
			t.Errorf("%s: description %q does not start with a full sentence", name, op.Description)
		}
		for _, p := range op.Params {
			if strings.TrimSpace(p.Description) == "" {
				t.Errorf("%s: parameter %s has no description", name, p.Name)
			}
		}
		for ct, schema := range spec.RequestContent(op.ID) {
			props, _ := schema["properties"].(map[string]any)
			for prop, v := range props {
				if d, _ := v.(map[string]any)["description"].(string); strings.TrimSpace(d) == "" {
					t.Errorf("%s: %s body property %s has no description", name, ct, prop)
				}
			}
		}
		if public[op.Method+" "+op.Path] {
			if op.Scope != "" || op.Role != "" {
				t.Errorf("%s: public operation with x-hello-scope %q x-hello-role %q", name, op.Scope, op.Role)
			}
		} else if op.Scope == "" || op.Role == "" {
			t.Errorf("%s: x-hello-scope %q x-hello-role %q", name, op.Scope, op.Role)
		}
		if _, ok := raw.Paths[op.Path][strings.ToLower(op.Method)]["x-hello-mcp"]; !ok {
			t.Errorf("%s: no x-hello-mcp", name)
		}
		if op.Scope == auth.ScopeSecrets && op.Exclude == "" {
			t.Errorf("%s: a secrets-scoped operation is an MCP tool", name)
		}
		if op.Body != nil && op.Body.JSON == nil && op.Exclude == "" {
			t.Errorf("%s: a body without a JSON form (%v) is an MCP tool", name, op.Body.ContentTypes)
		}
		for status, schema := range spec.Responses(op.ID) {
			if strings.HasPrefix(status, "2") {
				checkSecrets(t, name+" "+status, schema, "")
			}
		}
	}
}

// checkSecrets walks an inlined schema for string properties named like a
// credential without x-hello-secret.
func checkSecrets(t *testing.T, where string, schema map[string]any, at string) {
	t.Helper()
	props, _ := schema["properties"].(map[string]any)
	for name, v := range props {
		p, _ := v.(map[string]any)
		if secretName.MatchString(name) && p["type"] == "string" && p["x-hello-secret"] != true {
			t.Errorf("%s: %s/%s carries a credential without x-hello-secret", where, at, name)
		}
		checkSecrets(t, where, p, at+"/"+name)
	}
	if items, ok := schema["items"].(map[string]any); ok {
		checkSecrets(t, where, items, at+"/*")
	}
	for _, k := range []string{"allOf", "anyOf", "oneOf"} {
		list, _ := schema[k].([]any)
		for _, x := range list {
			sub, _ := x.(map[string]any)
			checkSecrets(t, where, sub, at)
		}
	}
}
