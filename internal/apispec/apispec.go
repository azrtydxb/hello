// Package apispec reads hello-control's embedded OpenAPI document into typed
// operations once, so the conformance tests, the MCP tool generator and the
// skills test read the document the same way (spec ai-external-access S-2,
// S-4).
package apispec

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/azrtydxb/hello/internal/auth"
)

// Spec is a loaded OpenAPI document.
type Spec struct {
	doc map[string]any
	ops []Operation
	// responses and request hold, per operation id, every documented
	// response (status → JSON schema, nil without a JSON body) and every
	// request content type's schema, $refs inlined, for the conformance
	// validator.
	responses map[string]map[string]map[string]any
	request   map[string]map[string]map[string]any
}

// Operation is one documented operation with its Hello extensions.
type Operation struct {
	ID, Method, Path, Summary, Description string
	// Scope and Role are x-hello-scope and x-hello-role.
	Scope auth.Scope
	Role  auth.Role
	// Exclude is x-hello-mcp's exclusion reason; empty means the operation
	// is an MCP tool.
	Exclude string
	Params  []Param
	// Body is the request body, nil when the operation takes none.
	Body *Body
	// Output is the 2xx JSON schema with $refs inlined; nil without one.
	Output map[string]any
	// Secrets are the x-hello-secret property paths of the 2xx body,
	// JSON-pointer-like with * for array items ("/secret", "/items/*/url").
	Secrets []string
}

// Param is a path or query parameter.
type Param struct {
	Name, In, Description string
	Required              bool
	Schema                map[string]any
}

// Body is a request body.
type Body struct {
	ContentTypes []string
	// JSON is the application/json schema with $refs inlined; nil when the
	// body has no JSON form.
	JSON map[string]any
}

// MaxDepth is how many nested $refs Load inlines; a deeper reference
// keeps only its sibling keywords, so it accepts any value.
const MaxDepth = 8

// methods are the HTTP methods an OpenAPI path item can hold, in the order
// Operations lists them within a path.
var methods = []string{"get", "put", "post", "patch", "delete", "head", "options", "trace"}

// Load parses an OpenAPI 3 document into its operations: parameters (path
// and query; a path parameter is always required), the JSON request body
// and the 2xx JSON response with $refs inlined to MaxDepth, the x-hello-*
// extensions and the x-hello-secret property paths. Operations are sorted
// by path, then method.
func Load(data []byte) (*Spec, error) {
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("apispec: %w", err)
	}
	v, _ := doc["openapi"].(string)
	if !strings.HasPrefix(v, "3.") {
		return nil, errors.New("apispec: not an OpenAPI 3 document")
	}
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		return nil, errors.New("apispec: document has no paths")
	}
	s := &Spec{
		doc:       doc,
		responses: map[string]map[string]map[string]any{},
		request:   map[string]map[string]map[string]any{},
	}
	names := make([]string, 0, len(paths))
	for p := range paths {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, p := range names {
		item, _ := paths[p].(map[string]any)
		shared, _ := item["parameters"].([]any)
		for _, m := range methods {
			raw, ok := item[m].(map[string]any)
			if !ok {
				continue
			}
			op, err := s.operation(p, m, raw, shared)
			if err != nil {
				return nil, fmt.Errorf("apispec: %s %s: %w", strings.ToUpper(m), p, err)
			}
			if _, dup := s.responses[op.ID]; dup {
				return nil, fmt.Errorf("apispec: operationId %q used twice", op.ID)
			}
			s.responses[op.ID] = s.responseSchemas(raw)
			s.request[op.ID] = s.requestSchemas(raw)
			s.ops = append(s.ops, op)
		}
	}
	return s, nil
}

// Operations returns the document's operations.
func (s *Spec) Operations() []Operation { return s.ops }

// Responses returns an operation's documented responses: status code (or
// "default") → the application/json schema with $refs inlined, nil when
// that response has no JSON body. Nil for an unknown operation.
func (s *Spec) Responses(id string) map[string]map[string]any { return s.responses[id] }

// RequestContent returns an operation's request body schemas by content
// type, $refs inlined; empty when the operation documents no requestBody.
func (s *Spec) RequestContent(id string) map[string]map[string]any { return s.request[id] }

func (s *Spec) operation(path, method string, raw map[string]any, shared []any) (Operation, error) {
	op := Operation{
		Method:      strings.ToUpper(method),
		Path:        path,
		ID:          str(raw["operationId"]),
		Summary:     str(raw["summary"]),
		Description: str(raw["description"]),
	}
	if op.ID == "" {
		return op, errors.New("no operationId")
	}
	if v := str(raw["x-hello-scope"]); v != "" {
		sc, err := auth.ParseScopes(v)
		if err != nil || len(sc) != 1 {
			return op, fmt.Errorf("x-hello-scope %q is not one scope", v)
		}
		op.Scope = sc[0]
	}
	if v := str(raw["x-hello-role"]); v != "" {
		r, err := auth.ParseRole(v)
		if err != nil {
			return op, fmt.Errorf("x-hello-role: %w", err)
		}
		op.Role = r
	}
	exclude, err := mcpExclusion(raw["x-hello-mcp"])
	if err != nil {
		return op, err
	}
	op.Exclude = exclude
	params, err := s.params(shared, raw["parameters"])
	if err != nil {
		return op, err
	}
	op.Params = params
	if rb, ok := s.deref(raw["requestBody"]).(map[string]any); ok {
		content, _ := rb["content"].(map[string]any)
		b := &Body{}
		for ct, media := range content {
			b.ContentTypes = append(b.ContentTypes, ct)
			if ct == "application/json" {
				if m, ok := media.(map[string]any); ok {
					b.JSON = s.schema(m["schema"])
				}
			}
		}
		sort.Strings(b.ContentTypes)
		op.Body = b
	}
	op.Output = s.output(raw)
	op.Secrets = secretPaths(op.Output, "")
	return op, nil
}

// mcpExclusion reads x-hello-mcp: absent or true is a tool, an object
// carries the exclusion reason.
func mcpExclusion(v any) (string, error) {
	switch m := v.(type) {
	case nil:
		return "", nil
	case bool:
		if !m {
			return "", errors.New(`x-hello-mcp is false; exclude with {"exclude": "<reason>"}`)
		}
		return "", nil
	case map[string]any:
		if r := str(m["exclude"]); r != "" {
			return r, nil
		}
		return "", errors.New("x-hello-mcp object without an exclude reason")
	default:
		return "", fmt.Errorf("x-hello-mcp is %T", m)
	}
}

// params merges path-item and operation parameters (the operation's win by
// name and location), keeping path and query parameters.
func (s *Spec) params(shared []any, own any) ([]Param, error) {
	list, _ := own.([]any)
	var out []Param
	index := map[string]int{}
	for _, raw := range append(slices.Clone(shared), list...) {
		m, ok := s.deref(raw).(map[string]any)
		if !ok {
			return nil, errors.New("parameter is not an object")
		}
		p := Param{
			Name:        str(m["name"]),
			In:          str(m["in"]),
			Description: str(m["description"]),
			Schema:      s.schema(m["schema"]),
		}
		if p.In != "path" && p.In != "query" {
			continue
		}
		p.Required = p.In == "path" || m["required"] == true
		key := p.In + "\x00" + p.Name
		if i, ok := index[key]; ok {
			out[i] = p
			continue
		}
		index[key] = len(out)
		out = append(out, p)
	}
	return out, nil
}

// output is the lowest 2xx response's JSON schema.
func (s *Spec) output(raw map[string]any) map[string]any {
	codes := s.responseSchemas(raw)
	var ok []string
	for c := range codes {
		if n, err := strconv.Atoi(c); err == nil && n >= 200 && n < 300 {
			ok = append(ok, c)
		}
	}
	sort.Strings(ok)
	for _, c := range ok {
		if codes[c] != nil {
			return codes[c]
		}
	}
	return nil
}

func (s *Spec) responseSchemas(raw map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	rs, _ := raw["responses"].(map[string]any)
	for code, r := range rs {
		out[code] = nil
		resp, _ := s.deref(r).(map[string]any)
		content, _ := resp["content"].(map[string]any)
		if media, ok := content["application/json"].(map[string]any); ok {
			out[code] = s.schema(media["schema"])
			if out[code] == nil {
				out[code] = map[string]any{}
			}
		}
	}
	return out
}

func (s *Spec) requestSchemas(raw map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	rb, _ := s.deref(raw["requestBody"]).(map[string]any)
	content, _ := rb["content"].(map[string]any)
	for ct, media := range content {
		m, _ := media.(map[string]any)
		out[ct] = s.schema(m["schema"])
		if out[ct] == nil {
			out[ct] = map[string]any{}
		}
	}
	return out
}

// deref resolves a top-level {"$ref": "#/..."} object (a parameter,
// response or request body), once.
func (s *Spec) deref(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	ref, ok := m["$ref"].(string)
	if !ok {
		return v
	}
	return s.pointer(ref)
}

// pointer resolves a local JSON pointer ("#/components/schemas/X").
func (s *Spec) pointer(ref string) any {
	if !strings.HasPrefix(ref, "#/") {
		return nil
	}
	var cur any = s.doc
	for _, part := range strings.Split(ref[2:], "/") {
		part = strings.NewReplacer("~1", "/", "~0", "~").Replace(part)
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[part]
	}
	return cur
}

// schema returns a deep copy of v with $refs inlined to MaxDepth; nil when
// v is not a schema object.
func (s *Spec) schema(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out, _ := s.inline(m, 0).(map[string]any)
	return out
}

func (s *Spec) inline(v any, depth int) any {
	switch t := v.(type) {
	case map[string]any:
		if ref, ok := t["$ref"].(string); ok {
			out := map[string]any{}
			var target map[string]any
			if depth < MaxDepth {
				target, _ = s.inline(s.pointer(ref), depth+1).(map[string]any)
			}
			for k, x := range target {
				out[k] = x
			}
			// Siblings of $ref (OpenAPI 3.1) refine the target.
			for k, x := range t {
				if k != "$ref" {
					out[k] = s.inline(x, depth)
				}
			}
			return out
		}
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = s.inline(x, depth)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = s.inline(x, depth)
		}
		return out
	default:
		return v
	}
}

// secretPaths lists the x-hello-secret properties of an inlined schema as
// JSON-pointer-like paths, * standing for any array item.
func secretPaths(schema map[string]any, at string) []string {
	if schema == nil {
		return nil
	}
	var out []string
	if props, ok := schema["properties"].(map[string]any); ok {
		names := make([]string, 0, len(props))
		for n := range props {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			p, _ := props[n].(map[string]any)
			path := at + "/" + n
			if p["x-hello-secret"] == true {
				out = append(out, path)
				continue
			}
			out = append(out, secretPaths(p, path)...)
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		out = append(out, secretPaths(items, at+"/*")...)
	}
	for _, k := range []string{"allOf", "anyOf", "oneOf"} {
		list, _ := schema[k].([]any)
		for _, x := range list {
			sub, _ := x.(map[string]any)
			for _, p := range secretPaths(sub, at) {
				if !slices.Contains(out, p) {
					out = append(out, p)
				}
			}
		}
	}
	return out
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
