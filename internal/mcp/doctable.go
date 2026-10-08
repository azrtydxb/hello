package mcp

import (
	"fmt"
	"strings"

	"github.com/azrtydxb/hello/internal/apispec"
)

// DocTables is the tool and resource tables of docs/ai-access.md, in
// Markdown, generated from the API document exactly as the server derives
// its tools and resources (go run ./internal/mcp/cmd/doctable).
func DocTables(spec *apispec.Spec) (string, error) {
	ops := spec.Operations()
	tools, order, err := buildTools(ops)
	if err != nil {
		return "", err
	}
	byID := map[string]apispec.Operation{}
	for _, op := range ops {
		byID[op.ID] = op
	}
	var b strings.Builder
	b.WriteString("| Tool | Scope | Operation | What it does |\n|---|---|---|---|\n")
	for _, name := range order {
		op := tools[name].op
		fmt.Fprintf(&b, "| `%s` | %s | `%s %s` | %s |\n", name, op.Scope, op.Method, op.Path, cell(op.Summary))
	}
	b.WriteString("\n| Resource | Scope | What it holds |\n|---|---|---|\n")
	for _, set := range [][]resourceDef{resources, templates} {
		for _, d := range set {
			op, ok := byID[d.op]
			if !ok {
				return "", fmt.Errorf("mcp: resource %s reads unknown operation %s", d.uri, d.op)
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s |\n", d.uri, op.Scope, cell(d.desc))
		}
	}
	return b.String(), nil
}

// cell keeps s on one table row.
func cell(s string) string {
	return strings.ReplaceAll(strings.Join(strings.Fields(s), " "), "|", `\|`)
}
