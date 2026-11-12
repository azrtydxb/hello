package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	aisdk "github.com/azrtydxb/go-ai-sdk/ai"

	"github.com/azrtydxb/hello/internal/ai/proposal"
	"github.com/azrtydxb/hello/internal/apispec"
)

// DescribeTool is the one tool that is not an API read: it answers the
// request schema of an operation a proposal may carry, so the model writes
// a valid body (most of all for the operations inside a configuration
// batch, spec flow-canvas S-19) instead of guessing and being refused. It
// reads only the embedded API document, never the PBX.
const DescribeTool = "describeOperation"

// describable is every operation a proposal may carry, directly or inside
// its batch, by id.
func describable(spec *apispec.Spec) map[string]apispec.Operation {
	out := map[string]apispec.Operation{}
	for _, op := range spec.Operations() {
		if proposal.Allowed(op.ID) || proposal.InnerAllowed(op.ID) {
			out[op.ID] = op
		}
	}
	return out
}

// describeTool is DescribeTool as a go-ai-sdk tool.
type describeTool struct {
	ops map[string]apispec.Operation
	ts  *toolset
}

func (t *describeTool) Name() string { return DescribeTool }

func (t *describeTool) Description() string {
	return "The request of an operation a proposal may carry, directly or inside an applyConfigBatch batch: method, path, path parameters and the JSON schema of the body. Call it before you propose an operation whose body you have not seen."
}

func (t *describeTool) Schema() json.RawMessage {
	ids := make([]string, 0, len(t.ops))
	for id := range t.ops {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	b, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"operationId": map[string]any{"type": "string", "enum": ids, "description": "The operation, such as createRingGroup."},
		},
		"required":             []string{"operationId"},
		"additionalProperties": false,
	})
	return b
}

func (t *describeTool) Strict() bool                             { return false }
func (t *describeTool) InputExamples() []json.RawMessage         { return nil }
func (t *describeTool) InputCallbacks() aisdk.ToolInputCallbacks { return aisdk.ToolInputCallbacks{} }

// Execute answers the operation's request from the document. It counts
// against the message's tool calls like a read.
func (t *describeTool) Execute(_ context.Context, args json.RawMessage) (any, error) {
	ts := t.ts
	ts.mu.Lock()
	if ts.tries >= MaxToolCalls {
		ts.mu.Unlock()
		ts.count(DescribeTool, "limit")
		return nil, errCallLimit
	}
	ts.tries++
	ts.mu.Unlock()
	if ts.onTool != nil {
		ts.onTool(DescribeTool)
	}
	call := ToolCall{OperationID: DescribeTool, Arguments: compactArgs(args)}
	record := func(result string) {
		ts.mu.Lock()
		ts.calls = append(ts.calls, call)
		ts.mu.Unlock()
		ts.count(DescribeTool, result)
	}
	var in struct {
		OperationID string `json:"operationId"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		record("error")
		return nil, fmt.Errorf("arguments must be {\"operationId\": ...}: %w", err)
	}
	op, ok := t.ops[in.OperationID]
	if !ok {
		record("error")
		return nil, fmt.Errorf("%q is not an operation a proposal may carry", in.OperationID)
	}
	var params []map[string]any
	for _, p := range op.Params {
		if p.In == "path" {
			params = append(params, map[string]any{"name": p.Name, "description": p.Description})
		}
	}
	out := map[string]any{
		"operationId": op.ID, "method": op.Method, "path": op.Path, "summary": op.Summary,
		"role": op.Role, "pathParams": params,
		"inProposal": proposal.Allowed(op.ID), "inBatch": proposal.InnerAllowed(op.ID),
	}
	if op.Body != nil && op.Body.JSON != nil {
		out["body"] = op.Body.JSON
	}
	if op.ID == proposal.BatchOp {
		out["note"] = fmt.Sprintf("Hello sets baseRevision and each op's expect itself; dryRun is ignored. At most %d ops, each one of the operations with inBatch true.", proposal.MaxBatchOps)
	}
	v, truncated := capResult(out)
	call.Status, call.Truncated = 200, truncated
	result := "ok"
	if truncated {
		result = "truncated"
	}
	record(result)
	return ts.dataBlock(v), nil
}

// describeNames is the operations DescribeTool answers, sorted, for tests.
func describeNames(ops map[string]apispec.Operation) []string {
	out := make([]string, 0, len(ops))
	for id := range ops {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}
