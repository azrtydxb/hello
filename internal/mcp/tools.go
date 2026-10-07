package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/azrtydxb/hello/internal/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// tool is one generated tool and the operation it replays.
type tool struct {
	op   apispec.Operation
	tool *sdk.Tool
}

// buildTools derives one tool per non-excluded operation (spec S-14),
// failing loudly on an operation that cannot become a tool.
func buildTools(ops []apispec.Operation) (map[string]*tool, []string, error) {
	tools := map[string]*tool{}
	var order []string
	for _, op := range ops {
		if op.Exclude != "" {
			continue
		}
		if op.ID == "" {
			return nil, nil, fmt.Errorf("mcp: %s %s has no operationId", op.Method, op.Path)
		}
		if _, dup := tools[op.ID]; dup {
			return nil, nil, fmt.Errorf("mcp: duplicate operationId %q", op.ID)
		}
		t, err := toolFor(op)
		if err != nil {
			return nil, nil, err
		}
		tools[op.ID] = &tool{op: op, tool: t}
		order = append(order, op.ID)
	}
	return tools, order, nil
}

// toolFor is the MCP tool of op: name, title, description with the scope it
// needs, input and output schemas and annotations by method.
func toolFor(op apispec.Operation) (*sdk.Tool, error) {
	desc := op.Description
	if desc == "" {
		desc = op.Summary
	}
	if op.Scope != "" {
		desc += fmt.Sprintf("\n\nNeeds scope %q.", op.Scope)
	}
	out, err := outputSchema(op)
	if err != nil {
		return nil, err
	}
	t := &sdk.Tool{
		Name:        op.ID,
		Title:       op.Summary,
		Description: desc,
		InputSchema: inputSchema(op),
		Annotations: annotations(op.Method, op.Summary),
	}
	if out != nil {
		t.OutputSchema = out
	}
	return t, nil
}

// inputSchema is an object with one property per path and query parameter
// (path parameters required) and, with a JSON request body, a "body"
// property holding its schema.
func inputSchema(op apispec.Operation) map[string]any {
	props := map[string]any{}
	required := []string{}
	for _, p := range op.Params {
		if p.In != "path" && p.In != "query" {
			continue
		}
		s := deepCopy(p.Schema)
		if s == nil {
			s = map[string]any{"type": "string"}
		}
		if p.Description != "" {
			s["description"] = p.Description
		}
		props[p.Name] = s
		if p.In == "path" || p.Required {
			required = append(required, p.Name)
		}
	}
	if op.Body != nil && op.Body.JSON != nil {
		props["body"] = deepCopy(op.Body.JSON)
	}
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             required,
		"additionalProperties": false,
	}
}

// outputSchema is the 2xx JSON schema when it is an object, with its
// x-hello-secret properties typed as the withheld string; nil otherwise.
func outputSchema(op apispec.Operation) (map[string]any, error) {
	if op.Output == nil || op.Output["type"] != "object" {
		return nil, nil
	}
	s := deepCopy(op.Output)
	if s == nil {
		return nil, fmt.Errorf("mcp: %s: output schema is not JSON", op.ID)
	}
	withholdSchema(s, op.Secrets)
	return s, nil
}

// annotations are the hints of spec S-14: readOnly for GET, destructive for
// DELETE, idempotent for PUT and DELETE, never open-world.
func annotations(method, title string) *sdk.ToolAnnotations {
	f := false
	a := &sdk.ToolAnnotations{Title: title, OpenWorldHint: &f}
	switch method {
	case http.MethodGet:
		a.ReadOnlyHint = true
	case http.MethodDelete:
		t := true
		a.DestructiveHint = &t
		a.IdempotentHint = true
	case http.MethodPut:
		a.DestructiveHint = &f
		a.IdempotentHint = true
	default:
		a.DestructiveHint = &f
	}
	return a
}

// allowed reports whether scopes allow an operation needing need; a public
// operation (no scope) is allowed to everyone.
func allowed(scopes auth.Scopes, need auth.Scope) bool {
	return need == "" || scopes.Has(need)
}

// deepCopy copies a decoded JSON schema so per-tool edits never touch the
// shared document.
func deepCopy(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	return out
}
