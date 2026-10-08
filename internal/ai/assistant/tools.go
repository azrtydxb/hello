package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"sync"

	aisdk "github.com/azrtydxb/go-ai-sdk/ai"

	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/replay"
	"github.com/prometheus/client_golang/prometheus"
)

// assistantTools is the fixed list of read operations the model may call
// (spec S-6). Each must be a GET with x-hello-scope read that MCP exposes;
// New refuses a document where one is not. No write operation is ever a
// tool: a change is a proposal a human applies.
var assistantTools = []string{
	"listExtensions", "getExtension",
	"listDevices", "getDevice",
	"listRingGroups", "getRingGroup",
	"listFeatureCodes",
	"listTrunks", "listTrunkStatus", "getTrunk",
	"listOutboundRoutes", "getOutboundRoute",
	"listInboundRoutes", "getInboundRoute",
	"listRegistrations",
	"listCalls",
	"listCDRs", "countCDRs", "cdrConcurrency", "getCDR",
	"getCluster", "listClusterNodes",
	"getDeviceDiagnostics", "listAuthFailures",
	"listPhones", "getPhone", "listPhoneFetches",
	"listRecordings",
	"listVoicemailBoxes", "listVoicemailMessages",
	"listAIFindings", "getAIFinding",
	"listAIProposals", "getAIProposal",
}

// MaxResultBytes caps one tool result's indented JSON (spec S-6).
const MaxResultBytes = 16 << 10

// toolDef is one read tool: its operation and argument schema.
type toolDef struct {
	op     apispec.Operation
	schema json.RawMessage
	desc   string
}

// toolDefs builds the tools from the document, refusing an operation that
// is missing, not a GET, not scope read or not exposed to MCP.
func toolDefs(spec *apispec.Spec) ([]toolDef, error) {
	byID := map[string]apispec.Operation{}
	for _, op := range spec.Operations() {
		byID[op.ID] = op
	}
	defs := make([]toolDef, 0, len(assistantTools))
	for _, id := range assistantTools {
		op, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("assistant: tool %s is not in the document", id)
		}
		if err := readOnly(op); err != nil {
			return nil, err
		}
		schema, err := json.Marshal(inputSchema(op))
		if err != nil {
			return nil, err
		}
		desc := op.Description
		if desc == "" {
			desc = op.Summary
		}
		defs = append(defs, toolDef{op: op, schema: schema, desc: desc})
	}
	return defs, nil
}

// readOnly refuses an operation that is not a read the assistant may call.
func readOnly(op apispec.Operation) error {
	switch {
	case op.Method != http.MethodGet:
		return fmt.Errorf("assistant: tool %s is %s, not GET", op.ID, op.Method)
	case op.Scope != auth.ScopeRead:
		return fmt.Errorf("assistant: tool %s needs scope %q, not read", op.ID, op.Scope)
	case op.Exclude != "":
		return fmt.Errorf("assistant: tool %s is not exposed to MCP", op.ID)
	}
	return nil
}

// inputSchema is the tool's argument object exactly as MCP builds it
// (internal/mcp inputSchema): one property per path and query parameter,
// path parameters required. A GET has no body.
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
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             required,
		"additionalProperties": false,
	}
}

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

// errUserGone ends a task whose user no longer exists: a read answered 401.
var errUserGone = errors.New("the user this task reads as no longer exists")

// toolset is one message's tools: they share the call count and record
// what was called, never the results.
type toolset struct {
	api       http.Handler
	ident     auth.Agent
	dataBlock func(any) string
	metric    *prometheus.CounterVec
	hook      func(operation, result string)
	// abort ends the task (a read answered 401: the user was deleted).
	abort context.CancelCauseFunc

	mu    sync.Mutex
	calls []ToolCall
	tries int
}

// newToolset returns the read tools for one message, replayed as ident.
func (a *Assistant) newToolset(ident auth.Agent, abort context.CancelCauseFunc) (*toolset, []aisdk.Tool) {
	ts := &toolset{api: a.cfg.API, ident: ident, dataBlock: a.cfg.DataBlock, metric: a.toolCalls, hook: a.cfg.ToolCalls, abort: abort}
	tools := make([]aisdk.Tool, len(a.tools))
	for i, d := range a.tools {
		tools[i] = &readTool{def: d, ts: ts}
	}
	return ts, tools
}

// Calls returns the calls made so far.
func (ts *toolset) Calls() []ToolCall {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return append([]ToolCall(nil), ts.calls...)
}

// readTool is one read operation as a go-ai-sdk tool.
type readTool struct {
	def toolDef
	ts  *toolset
}

func (t *readTool) Name() string                             { return t.def.op.ID }
func (t *readTool) Description() string                      { return t.def.desc }
func (t *readTool) Schema() json.RawMessage                  { return t.def.schema }
func (t *readTool) Strict() bool                             { return false }
func (t *readTool) InputExamples() []json.RawMessage         { return nil }
func (t *readTool) InputCallbacks() aisdk.ToolInputCallbacks { return aisdk.ToolInputCallbacks{} }

// errCallLimit is what the model gets once the message's tool calls are
// spent.
var errCallLimit = fmt.Errorf("the limit of %d tool calls for this message is reached: give your final answer now from the data you have", MaxToolCalls)

// Execute replays the read as the user with scope read only, withholds
// x-hello-secret values, caps the result and hands it back inside a <data>
// block.
func (t *readTool) Execute(ctx context.Context, args json.RawMessage) (any, error) {
	ts := t.ts
	ts.mu.Lock()
	if ts.tries >= MaxToolCalls {
		ts.mu.Unlock()
		ts.count(t.def.op.ID, "limit")
		return nil, errCallLimit
	}
	ts.tries++
	ts.mu.Unlock()

	call := ToolCall{OperationID: t.def.op.ID, Arguments: compactArgs(args)}
	record := func() {
		ts.mu.Lock()
		ts.calls = append(ts.calls, call)
		ts.mu.Unlock()
		result := "error"
		switch {
		case call.Truncated:
			result = "truncated"
		case call.Status >= 200 && call.Status < 300:
			result = "ok"
		}
		ts.count(call.OperationID, result)
	}
	path, query, err := request(t.def.op, args)
	if err != nil {
		record()
		return nil, err
	}
	res, err := replay.Do(auth.WithAgent(ctx, ts.ident), ts.api, replay.Request{Method: http.MethodGet, Path: path, Query: query})
	if err != nil {
		record()
		return nil, err
	}
	if res.Fail != "" {
		record()
		return nil, fmt.Errorf("the read failed: %s", res.Fail)
	}
	call.Status = res.Status
	if res.Status == http.StatusUnauthorized {
		record()
		ts.abort(errUserGone)
		return nil, errUserGone
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(res.Body))
	dec.UseNumber()
	if len(res.Body) > 0 && dec.Decode(&v) != nil {
		v = map[string]any{"error": "the response is not JSON"}
	}
	if res.Status >= 300 {
		record()
		return ts.dataBlock(map[string]any{"status": res.Status, "response": v}), nil
	}
	replay.Redact(v, t.def.op.Secrets)
	v, call.Truncated = capResult(v)
	record()
	return ts.dataBlock(v), nil
}

// count adds one call to hello_ai_tool_calls_total.
func (ts *toolset) count(op, result string) {
	if ts.hook != nil {
		ts.hook(op, result)
	} else if ts.metric != nil {
		ts.metric.WithLabelValues(op, result).Inc()
	}
}

// compactArgs keeps a call's arguments for the stored summary, or nothing
// when they are not a JSON object.
func compactArgs(args json.RawMessage) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(args, &m) != nil || len(m) == 0 {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return b
}

// request maps a tool call's arguments to the operation's path and query,
// as MCP does: path parameters substituted, query parameters set, any
// other argument refused.
func request(op apispec.Operation, raw json.RawMessage) (string, url.Values, error) {
	args := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&args); err != nil {
			return "", nil, fmt.Errorf("arguments must be a JSON object: %w", err)
		}
	}
	known := map[string]bool{}
	path := op.Path
	query := url.Values{}
	for _, p := range op.Params {
		known[p.Name] = true
		v, ok := args[p.Name]
		switch p.In {
		case "path":
			if !ok || v == nil {
				return "", nil, fmt.Errorf("argument %q is required", p.Name)
			}
			path = strings.ReplaceAll(path, "{"+p.Name+"}", url.PathEscape(scalar(v)))
		case "query":
			if !ok || v == nil {
				continue
			}
			if list, isList := v.([]any); isList {
				for _, e := range list {
					query.Add(p.Name, scalar(e))
				}
				continue
			}
			query.Set(p.Name, scalar(v))
		}
	}
	for k := range maps.Keys(args) {
		if !known[k] {
			return "", nil, fmt.Errorf("unknown argument %q", k)
		}
	}
	return path, query, nil
}

func scalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		if x {
			return "true"
		}
		return "false"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// size is v's indented JSON length, the form DataBlock sends.
func size(v any) int {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return MaxResultBytes + 1
	}
	return len(b)
}

// capResult cuts a result to MaxResultBytes. A list (an array, or an
// object's largest array member such as "items") keeps as many leading
// elements as fit and gains "truncated": true and "total"; anything else
// that is too large is replaced by a note.
func capResult(v any) (any, bool) {
	if size(v) <= MaxResultBytes {
		return v, false
	}
	obj, isObj := v.(map[string]any)
	if list, ok := v.([]any); ok {
		obj, isObj = map[string]any{"items": list}, true
	}
	if isObj {
		key, list := largestArray(obj)
		if list != nil {
			out := maps.Clone(obj)
			out["truncated"] = true
			if _, has := out["total"]; !has {
				out["total"] = len(list)
			}
			// The largest prefix that fits, by binary search.
			lo, hi := 0, len(list)
			for lo < hi {
				mid := (lo + hi + 1) / 2
				out[key] = list[:mid]
				if size(out) <= MaxResultBytes {
					lo = mid
				} else {
					hi = mid - 1
				}
			}
			out[key] = list[:lo]
			if size(out) <= MaxResultBytes {
				return out, true
			}
		}
	}
	return map[string]any{
		"truncated": true,
		"bytes":     size(v),
		"note":      "the result is larger than 16 KiB; narrow the request (a filter, a limit or one item by id)",
	}, true
}

// largestArray is obj's array member with the longest JSON, or nil.
func largestArray(obj map[string]any) (string, []any) {
	var key string
	var best []any
	bestSize := -1
	for k, v := range obj {
		if a, ok := v.([]any); ok {
			if s := size(a); s > bestSize || (s == bestSize && k < key) {
				key, best, bestSize = k, a, s
			}
		}
	}
	return key, best
}

// ToolNames is the assistant's tool list, in order, for docs/ai-agent.md.
func ToolNames() []string { return append([]string(nil), assistantTools...) }
