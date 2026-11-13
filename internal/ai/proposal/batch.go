package proposal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/configbatch"
	"github.com/azrtydxb/hello/internal/flow"
	"github.com/azrtydxb/hello/internal/replay"
)

// A proposal may carry one configuration batch (spec flow-canvas S-19): the
// action applyConfigBatch, whose body is the batch. It is validated by
// recursion (every inner operation on the inner allowlist, no credential
// property, at most MaxBatchOps operations) and then by the batch's own dry
// run, replayed as the user with the agent's preview identity, so the model
// learns of every error the applier would get. Apply is the ordinary
// proposal apply: the batch is replayed with the applier's credentials and
// its baseRevision and expect make a moved configuration stale.

// BatchOp is the operation of a proposal action that carries a batch.
const BatchOp = "applyConfigBatch"

// MaxBatchOps is the most operations a proposal's batch carries.
const MaxBatchOps = 50

// Preview is what the dry run of a proposal's batch answered when the
// proposal was made: the per-operation results with their diffs, the flow
// warnings the batch introduces, and the change to the drawn graph the
// canvas overlays.
type Preview struct {
	BaseRevision int64           `json:"baseRevision"`
	Results      json.RawMessage `json:"results"`
	Warnings     json.RawMessage `json:"warnings"`
	FlowDiff     json.RawMessage `json:"flowDiff"`
}

// InnerAllowed reports whether an operation may be inside a proposal's
// batch: every batchable object write (configbatch.Ops), which covers every
// object type the canvas draws (decision 2 of the flow canvas).
func InnerAllowed(id string) bool {
	if _, refused := configbatch.Refused[id]; refused {
		return false
	}
	_, ok := configbatch.Ops[id]
	return ok
}

// batchBody is a proposal's batch as it is stored and applied.
type batchBody struct {
	BaseRevision int64                    `json:"baseRevision"`
	DryRun       bool                     `json:"dryRun,omitempty"`
	CanvasID     *int64                   `json:"canvasId,omitempty"`
	Ops          []configbatch.Op         `json:"ops"`
	Layout       []configbatch.LayoutNode `json:"layout,omitempty"`
}

// decodeBatch reads a batch body, unknown properties refused.
func decodeBatch(raw json.RawMessage) (batchBody, error) {
	var b batchBody
	if len(raw) == 0 {
		return b, errors.New("the operation needs a body: the batch")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return b, fmt.Errorf("the batch is not valid JSON of the batch request: %w", err)
	}
	return b, nil
}

// checkBatch is the recursive validation of a batch without running it.
func (v *SchemaValidator) checkBatch(b batchBody) error {
	if len(b.Ops) == 0 || len(b.Ops) > MaxBatchOps {
		return fmt.Errorf("a proposal's batch has 1 to %d operations, got %d", MaxBatchOps, len(b.Ops))
	}
	for i, o := range b.Ops {
		if !InnerAllowed(o.OperationID) {
			return fmt.Errorf("batch operation %d (%s) is not allowed in a proposal", i, o.OperationID)
		}
		if len(o.Body) > 0 {
			var body any
			if err := json.Unmarshal(o.Body, &body); err != nil {
				return fmt.Errorf("batch operation %d (%s): the body is not JSON", i, o.OperationID)
			}
			if hasCredential(body) {
				return fmt.Errorf("batch operation %d (%s) sets a credential property; a proposal never carries passwords, PINs or secrets", i, o.OperationID)
			}
		}
	}
	refused, problems := configbatch.Check(v.ops, configbatch.Request{Ops: b.Ops, Layout: b.Layout})
	if refused != nil {
		return fmt.Errorf("batch operation %d: %s", refused.OpIndex, refused.Message)
	}
	if len(problems) > 0 {
		return fmt.Errorf("the batch is not valid: %s", problemText(problems))
	}
	if len(b.Layout) > 0 && b.CanvasID == nil {
		return errors.New("the batch's layout needs a canvasId")
	}
	return nil
}

func problemText(ps []configbatch.Problem) string {
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		where := p.Path
		if p.OpIndex >= 0 && !strings.HasPrefix(where, "ops[") {
			where = fmt.Sprintf("operation %d %s", p.OpIndex, where)
		}
		parts = append(parts, strings.TrimSpace(where+": "+p.Message))
	}
	return strings.Join(parts, "; ")
}

// batch validates a batch action: the recursion, then baseRevision and the
// expected versions taken from the graph now, then the dry run. The action
// is rewritten to the batch that will be applied, with its preview.
func (v *SchemaValidator) batch(ctx context.Context, ident Identity, a *Action) error {
	if err := checkParams(v.ops[BatchOp], a.PathParams); err != nil {
		return err
	}
	b, err := decodeBatch(a.Body)
	if err != nil {
		return err
	}
	if err := v.checkBatch(b); err != nil {
		return err
	}
	g, err := v.graph(ctx, ident)
	if err != nil {
		return err
	}
	// The batch is based on the configuration the proposal is made at: if
	// it moves before the apply, the batch's own staleness check refuses
	// the operations whose target changed (409, the proposal goes stale).
	b.BaseRevision, b.DryRun = g.ConfigRevision, false
	nodes := make(map[string]flow.Node, len(g.Nodes))
	for _, n := range g.Nodes {
		nodes[n.ID] = n
	}
	a.Destructive, a.References = false, nil
	for i := range b.Ops {
		o := &b.Ops[i]
		info := configbatch.Ops[o.OperationID]
		id := o.PathParams[configbatch.IDParam]
		node := configbatch.NodeID(info, id)
		if info.Create || node == "" || strings.Contains(id, "${") {
			continue
		}
		n, there := nodes[node]
		if !there {
			return fmt.Errorf("batch operation %d (%s): %s does not exist", i, o.OperationID, node)
		}
		if o.Expect == "" {
			o.Expect = n.Version
		}
		if info.Delete {
			a.Destructive = true
			a.References = append(a.References, Reference{Kind: info.Node, ID: id, Name: n.Label,
				Detail: fmt.Sprintf("deleted by batch operation %d", i)})
		}
	}
	body, err := json.Marshal(b)
	if err != nil {
		return err
	}
	prev, err := v.dryRunBatch(ctx, ident, b)
	if err != nil {
		return err
	}
	a.Body, a.Before, a.After, a.Preview = body, nil, body, prev
	return nil
}

// graph reads the whole flow graph, devices and phones included, as the
// agent: the revision and the node versions the batch is based on.
func (v *SchemaValidator) graph(ctx context.Context, ident Identity) (flow.Graph, error) {
	h := v.api.Load()
	if h == nil {
		return flow.Graph{}, errors.New("the API is not wired")
	}
	res, err := replay.Do(auth.WithAgent(ctx, ident), *h, replay.Request{Method: http.MethodGet, Path: "/api/v1/flow/graph",
		Query: url.Values{"include": {"devices"}}})
	switch {
	case err != nil:
		return flow.Graph{}, fmt.Errorf("read the flow graph: %w", err)
	case res.Fail != "":
		return flow.Graph{}, fmt.Errorf("read the flow graph: %s", res.Fail)
	case res.Status != http.StatusOK:
		return flow.Graph{}, fmt.Errorf("read the flow graph: status %d", res.Status)
	}
	var g flow.Graph
	if err := json.Unmarshal(res.Body, &g); err != nil {
		return flow.Graph{}, fmt.Errorf("read the flow graph: %w", err)
	}
	return g, nil
}

// dryRunBatch runs the batch with dryRun as the user's agent in preview
// mode: every operation is replayed with the user's role, validated and
// checked as on apply, and rolled back. A refusal is the error the model is
// asked to fix.
func (v *SchemaValidator) dryRunBatch(ctx context.Context, ident Identity, b batchBody) (*Preview, error) {
	h := v.api.Load()
	if h == nil {
		return nil, errors.New("the API is not wired")
	}
	b.DryRun = true
	body, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	preview := ident
	preview.Preview = true
	res, err := replay.Do(auth.WithAgent(ctx, preview), *h, replay.Request{Method: http.MethodPost, Path: "/api/v1/config/batch",
		Body: body, Header: http.Header{"Content-Type": {"application/json"}}})
	switch {
	case err != nil:
		return nil, fmt.Errorf("batch dry run: %w", err)
	case res.Fail != "":
		return nil, fmt.Errorf("batch dry run: %s", res.Fail)
	case res.Status != http.StatusOK:
		return nil, dryRunError(res.Status, res.Body)
	}
	var out struct {
		BaseRevision int64           `json:"baseRevision"`
		Results      json.RawMessage `json:"results"`
		Warnings     json.RawMessage `json:"warnings"`
		FlowDiff     json.RawMessage `json:"flowDiff"`
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		return nil, fmt.Errorf("batch dry run: %w", err)
	}
	// What the model and every reader of the proposal sees: secret values
	// a result might hold are withheld, as in every batch result.
	return &Preview{BaseRevision: out.BaseRevision, Results: configbatch.Redact(out.Results),
		Warnings: out.Warnings, FlowDiff: out.FlowDiff}, nil
}

// dryRunError turns a refused dry run into the message the model reads.
func dryRunError(status int, body []byte) error {
	var e struct {
		Error struct {
			Code    string                `json:"code"`
			Message string                `json:"message"`
			Errors  []configbatch.Problem `json:"errors"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	msg := fmt.Sprintf("the batch dry run was refused (%d %s): %s", status, e.Error.Code, e.Error.Message)
	if status == http.StatusForbidden {
		msg += "; the user cannot make this change, so answer without a proposal"
	}
	if len(e.Error.Errors) > 0 {
		msg += "; " + problemText(e.Error.Errors)
	}
	return errors.New(msg)
}

// batchCore is a batch body as the fingerprint sees it: what it changes,
// not the revision or versions it was based on.
func batchCore(raw json.RawMessage) any {
	b, err := decodeBatch(raw)
	if err != nil {
		v, _ := decode(raw)
		return v
	}
	ops := make([]map[string]any, len(b.Ops))
	for i, o := range b.Ops {
		body, _ := decode(o.Body)
		ops[i] = map[string]any{"ref": o.Ref, "operationId": o.OperationID, "pathParams": o.PathParams, "body": body}
	}
	return map[string]any{"canvasId": b.CanvasID, "ops": ops, "layout": b.Layout}
}

// batchTargets are the existing objects a batch changes, named as Targets
// names an action's.
func batchTargets(raw json.RawMessage) []string {
	b, err := decodeBatch(raw)
	if err != nil {
		return nil
	}
	var out []string
	for _, o := range b.Ops {
		if t, ok := target(o.OperationID, o.PathParams); ok {
			out = append(out, t)
		}
	}
	return out
}
