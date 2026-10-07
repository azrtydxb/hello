// Package apispec reads hello-control's embedded OpenAPI document into typed
// operations once, so the conformance tests, the MCP tool generator and the
// skills test read the document the same way (spec ai-external-access S-2,
// S-4).
package apispec

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/azrtydxb/hello/internal/auth"
)

// Spec is a loaded OpenAPI document.
type Spec struct {
	doc map[string]any
	ops []Operation
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

// Load parses an OpenAPI 3 document.
//
// Contract only: the operations are filled by plan ai-external-access
// Task 2; until then Load checks the document's shape and Operations is
// empty.
func Load(data []byte) (*Spec, error) {
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("apispec: %w", err)
	}
	v, _ := doc["openapi"].(string)
	if !strings.HasPrefix(v, "3.") {
		return nil, errors.New("apispec: not an OpenAPI 3 document")
	}
	if _, ok := doc["paths"].(map[string]any); !ok {
		return nil, errors.New("apispec: document has no paths")
	}
	return &Spec{doc: doc}, nil
}

// Operations returns the document's operations.
func (s *Spec) Operations() []Operation { return s.ops }
