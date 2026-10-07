package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

const sentinel = "s3cr3t-value"

// TestRedactOperations fails if, for any operation with x-hello-secret
// properties (the document's and the fixture's, device create and a secret
// nested in a list included), a value at a secret path survives redaction
// or the output schema does not type it as the withheld string.
func TestRedactOperations(t *testing.T) {
	ops := append(loadSpec(t).Operations(), fixtureOps()...)
	n := 0
	for _, op := range ops {
		if len(op.Secrets) == 0 {
			continue
		}
		n++
		t.Run(op.ID, func(t *testing.T) {
			v := map[string]any{"keep": "visible"}
			for _, p := range op.Secrets {
				plant(v, strings.Split(strings.TrimPrefix(p, "/"), "/"))
			}
			redact(v, op.Secrets)
			b, _ := json.Marshal(v)
			if strings.Contains(string(b), sentinel) || !strings.Contains(string(b), "visible") {
				t.Fatalf("after redaction: %s", b)
			}
			if op.Output != nil && op.Output["type"] == "object" {
				out, err := outputSchema(op)
				if err != nil {
					t.Fatal(err)
				}
				for _, p := range op.Secrets {
					if s := schemaAt(out, p); s == nil || s["type"] != "string" || s["x-hello-secret"] != nil {
						t.Errorf("output schema at %s = %v, want the withheld string", p, s)
					}
				}
				if schemaAt(op.Output, op.Secrets[0])["x-hello-secret"] == nil && op.ID == "createDevice" {
					t.Error("outputSchema edited the operation's own schema")
				}
			}
		})
	}
	if n < 2 {
		t.Fatalf("only %d operations with secrets", n)
	}
}

// TestRedactWalk fails if the walk misses secrets in lists or nested
// objects, or touches values off the paths.
func TestRedactWalk(t *testing.T) {
	var v any
	_ = json.Unmarshal([]byte(`{"secret":"a","name":"secret","items":[{"url":"b","mac":"m"},{"url":"c"},{"mac":"n"}],
		"phone":{"admin":{"password":"d"}},"map":{"x":{"token":"e"},"y":{"token":"f"}},"null":null}`), &v)
	redact(v, []string{"/secret", "/items/*/url", "/phone/admin/password", "/map/*/token", "/missing/x", "/items/*/missing"})
	b, _ := json.Marshal(v)
	for _, leaked := range []string{`"a"`, `"b"`, `"c"`, `"d"`, `"e"`, `"f"`} {
		if strings.Contains(string(b), leaked) {
			t.Errorf("%s survived: %s", leaked, b)
		}
	}
	for _, kept := range []string{`"name":"secret"`, `"mac":"m"`, `"mac":"n"`, `"null":null`} {
		if !strings.Contains(string(b), kept) {
			t.Errorf("%s was changed: %s", kept, b)
		}
	}
	if strings.Contains(string(b), "missing") {
		t.Errorf("redaction created a path: %s", b)
	}
}

// plant puts the sentinel at segs, building objects and one-element lists.
func plant(v map[string]any, segs []string) {
	seg := unescape(segs[0])
	if len(segs) == 1 {
		v[seg] = sentinel
		return
	}
	if segs[1] == "*" {
		list, _ := v[seg].([]any)
		if len(list) == 0 {
			list = []any{map[string]any{}}
		}
		v[seg] = list
		if len(segs) == 2 {
			list[0] = sentinel
			return
		}
		plant(list[0].(map[string]any), segs[2:])
		return
	}
	child, _ := v[seg].(map[string]any)
	if child == nil {
		child = map[string]any{}
		v[seg] = child
	}
	plant(child, segs[1:])
}

// schemaAt returns the schema node at a secret path.
func schemaAt(s map[string]any, path string) map[string]any {
	node := s
	for _, seg := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if node == nil {
			return nil
		}
		if seg == "*" {
			node, _ = node["items"].(map[string]any)
			continue
		}
		props, _ := node["properties"].(map[string]any)
		node, _ = props[unescape(seg)].(map[string]any)
	}
	return node
}
