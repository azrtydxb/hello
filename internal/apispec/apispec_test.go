package apispec

import (
	"os"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
)

// TestLoadShape fails if the embedded document does not load or a
// non-OpenAPI document does.
func TestLoadShape(t *testing.T) {
	// Read the file: importing internal/api would make a cycle, since the
	// API serves the proposal routes, which use this package.
	doc, err := os.ReadFile("../api/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(doc); err != nil {
		t.Fatalf("embedded document: %v", err)
	}
	for _, bad := range []string{`nope`, `{"swagger":"2.0","paths":{}}`, `{"openapi":"3.1.0"}`} {
		if _, err := Load([]byte(bad)); err == nil {
			t.Errorf("Load(%s) succeeded", bad)
		}
	}
}

// fixture is a small document covering what Load reads: shared and own
// parameters, a $ref parameter, a JSON body with a $ref, a multipart-only
// body, the lowest 2xx JSON response, x-hello-* extensions, secrets in an
// object and in array items, and a self-referencing schema.
const fixture = `{
  "openapi": "3.1.0",
  "paths": {
    "/things/{id}": {
      "parameters": [{"$ref": "#/components/parameters/id"}],
      "get": {
        "operationId": "getThing", "summary": "One thing", "description": "Reads a thing.",
        "x-hello-scope": "read", "x-hello-role": "viewer", "x-hello-mcp": true,
        "parameters": [{"name": "full", "in": "query", "description": "Everything.", "schema": {"type": "boolean"}},
                       {"name": "X-Trace", "in": "header", "schema": {"type": "string"}}],
        "responses": {
          "201": {"description": "never", "content": {"application/json": {"schema": {"type": "string"}}}},
          "200": {"description": "ok", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Thing"}}}},
          "404": {"$ref": "#/components/responses/NotFound"}
        }
      },
      "put": {
        "operationId": "putThing", "x-hello-scope": "secrets", "x-hello-role": "admin",
        "x-hello-mcp": {"exclude": "binary"},
        "requestBody": {"content": {
          "application/json": {"schema": {"$ref": "#/components/schemas/Thing"}},
          "multipart/form-data": {"schema": {"type": "object"}}}},
        "responses": {"204": {"description": "done"}}
      }
    },
    "/list": {
      "post": {
        "operationId": "listThings",
        "requestBody": {"content": {"text/csv": {"schema": {"type": "string"}}}},
        "responses": {"200": {"description": "ok", "content": {"application/json": {"schema": {
          "type": "object", "properties": {"items": {"type": "array", "items": {"$ref": "#/components/schemas/Thing"}}}}}}}}
      }
    }
  },
  "components": {
    "parameters": {"id": {"name": "id", "in": "path", "description": "Its id.", "schema": {"type": "integer"}}},
    "responses": {"NotFound": {"description": "nf", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Error"}}}}},
    "schemas": {
      "Error": {"type": "object", "properties": {"code": {"type": "string"}}},
      "Thing": {"type": "object", "properties": {
        "name": {"type": "string"},
        "secret": {"type": "string", "x-hello-secret": true},
        "next": {"$ref": "#/components/schemas/Thing", "description": "The next one."}}}
    }
  }
}`

// TestLoadOperations fails if Load drops or misreads an operation, a
// parameter (header parameters are not tool inputs; path parameters are
// always required), a body, the 2xx schema, an extension, a secret path or
// the depth limit on $ref inlining.
func TestLoadOperations(t *testing.T) {
	s, err := Load([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	ops := map[string]Operation{}
	var order []string
	for _, op := range s.Operations() {
		ops[op.ID] = op
		order = append(order, op.Method+" "+op.Path)
	}
	if got := strings.Join(order, ","); got != "POST /list,GET /things/{id},PUT /things/{id}" {
		t.Fatalf("operations = %s", got)
	}

	get := ops["getThing"]
	if get.Summary != "One thing" || get.Description != "Reads a thing." || get.Scope != auth.ScopeRead ||
		get.Role != auth.RoleViewer || get.Exclude != "" || get.Body != nil {
		t.Errorf("getThing = %+v", get)
	}
	if len(get.Params) != 2 || get.Params[0].Name != "id" || get.Params[0].In != "path" || !get.Params[0].Required ||
		get.Params[0].Description != "Its id." || get.Params[0].Schema["type"] != "integer" ||
		get.Params[1].Name != "full" || get.Params[1].Required {
		t.Errorf("getThing params = %+v", get.Params)
	}
	if get.Output["type"] != "object" {
		t.Errorf("getThing output = %v, want the 200 Thing", get.Output)
	}
	if got := strings.Join(get.Secrets, ","); got != "/next/next/next/next/next/next/next/secret,/next/next/next/next/next/next/secret,/next/next/next/next/next/secret,/next/next/next/next/secret,/next/next/next/secret,/next/next/secret,/next/secret,/secret" {
		t.Errorf("getThing secrets = %s", got)
	}
	// The self-reference stops at MaxDepth: Thing at depth 0 nests seven
	// more Things, and the eighth reference is the empty schema.
	deep := get.Output
	for range MaxDepth - 1 {
		deep, _ = deep["properties"].(map[string]any)["next"].(map[string]any)
	}
	last, _ := deep["properties"].(map[string]any)["next"].(map[string]any)
	if len(last) != 1 || last["description"] != "The next one." {
		t.Errorf("schema at the depth limit = %v, want only the sibling description", last)
	}

	put := ops["putThing"]
	if put.Scope != auth.ScopeSecrets || put.Role != auth.RoleAdmin || put.Exclude != "binary" ||
		put.Body == nil || strings.Join(put.Body.ContentTypes, ",") != "application/json,multipart/form-data" ||
		put.Body.JSON["type"] != "object" || put.Output != nil || put.Secrets != nil {
		t.Errorf("putThing = %+v", put)
	}
	if got := s.Responses("putThing"); len(got) != 1 || got["204"] != nil {
		t.Errorf("putThing responses = %v", got)
	}
	if got := s.Responses("getThing")["404"]; got["type"] != "object" {
		t.Errorf("getThing 404 = %v, want the inlined Error", got)
	}

	list := ops["listThings"]
	if list.Body == nil || list.Body.JSON != nil || list.Body.ContentTypes[0] != "text/csv" {
		t.Errorf("listThings body = %+v", list.Body)
	}
	if got := strings.Join(list.Secrets, ","); !strings.HasPrefix(got, "/items/*/next/") || !strings.HasSuffix(got, "/items/*/secret") {
		t.Errorf("listThings secrets = %s", got)
	}
	if got := s.RequestContent("listThings"); got["text/csv"]["type"] != "string" {
		t.Errorf("listThings request content = %v", got)
	}
}

// TestLoadRejects fails if Load accepts an operation it cannot describe.
func TestLoadRejects(t *testing.T) {
	op := func(body string) string {
		return `{"openapi":"3.1.0","paths":{"/a":{"get":` + body + `},"/b":{"get":{"operationId":"b"}}}}`
	}
	for name, doc := range map[string]string{
		"no operationId":    op(`{}`),
		"duplicate id":      op(`{"operationId":"b"}`),
		"unknown scope":     op(`{"operationId":"a","x-hello-scope":"root"}`),
		"two scopes":        op(`{"operationId":"a","x-hello-scope":"read write"}`),
		"unknown role":      op(`{"operationId":"a","x-hello-role":"owner"}`),
		"mcp false":         op(`{"operationId":"a","x-hello-mcp":false}`),
		"exclude no reason": op(`{"operationId":"a","x-hello-mcp":{}}`),
		"mcp string":        op(`{"operationId":"a","x-hello-mcp":"no"}`),
	} {
		if _, err := Load([]byte(doc)); err == nil {
			t.Errorf("%s: Load succeeded", name)
		}
	}
}
