// Package doctable renders the tables of docs/ai-agent.md from the code:
// the assistant's tools, the proposal allowlist and the detector
// thresholds (go run ./internal/ai/cmd/doctable). TestDocsAIAgent fails
// when the document's copy differs.
package doctable

import (
	"fmt"
	"strings"

	"github.com/azrtydxb/hello/internal/ai/assistant"
	"github.com/azrtydxb/hello/internal/ai/detect"
	"github.com/azrtydxb/hello/internal/ai/proposal"
	"github.com/azrtydxb/hello/internal/apispec"
)

// Tables is the Markdown of the three tables.
func Tables(spec *apispec.Spec) (string, error) {
	byID := map[string]apispec.Operation{}
	for _, op := range spec.Operations() {
		byID[op.ID] = op
	}
	var b strings.Builder
	b.WriteString("| Tool | Operation | What it reads |\n|---|---|---|\n")
	for _, id := range assistant.ToolNames() {
		op, ok := byID[id]
		if !ok {
			return "", fmt.Errorf("doctable: tool %s is not in the document", id)
		}
		fmt.Fprintf(&b, "| `%s` | `%s %s` | %s |\n", id, op.Method, op.Path, cell(op.Summary))
	}
	b.WriteString("\n| Operation | Request | Role | Kind |\n|---|---|---|---|\n")
	for _, id := range proposal.Allowlist {
		op, ok := byID[id]
		if !ok {
			return "", fmt.Errorf("doctable: allowlisted operation %s is not in the document", id)
		}
		kind := "change"
		switch {
		case proposal.IsDelete(id):
			kind = "delete (marked, needs confirmation)"
		case strings.HasPrefix(id, "create"):
			kind = "create"
		}
		fmt.Fprintf(&b, "| `%s` | `%s %s` | %s | %s |\n", id, op.Method, op.Path, op.Role, kind)
	}
	b.WriteString("\n| Detector | Threshold | Value |\n|---|---|---|\n")
	for _, th := range detect.Thresholds() {
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", th.Detector, th.Name, th.Value)
	}
	return b.String(), nil
}

func cell(s string) string {
	return strings.ReplaceAll(strings.Join(strings.Fields(s), " "), "|", `\|`)
}
