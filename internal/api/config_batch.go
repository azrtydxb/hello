package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/ai/proposal"
	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/azrtydxb/hello/internal/configbatch"
	"github.com/azrtydxb/hello/internal/flow"
	"github.com/azrtydxb/hello/internal/replay"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

// Configuration batches (spec flow-canvas S-13, S-14, S-21): many operations
// applied in one transaction. Each operation is replayed through this same
// handler with the caller's own credentials, so it is authorised, validated
// and audited exactly as a direct call; the store joins every write to the
// batch's transaction (store.RunBatch), checks the whole configuration once
// at the end and moves the revision once.

// maxBatchBody bounds a batch request: 200 operations of 64 KiB each is
// never reached, but a ring group with a hundred members is a few KiB.
const maxBatchBody = 4 << 20

// BatchService is the store the batch routes need; *store.Store implements
// it. Nil answers 503 on the batch routes.
type BatchService interface {
	RunBatch(ctx context.Context, dryRun bool, fn func(context.Context, *store.Batch) (store.BatchRecord, error)) (store.BatchOutcome, error)
	ListConfigBatches(ctx context.Context, before time.Time, limit int) ([]store.ConfigBatch, error)
	GetConfigBatch(ctx context.Context, id string) (store.ConfigBatch, error)
	NodeLayouts(ctx context.Context, node string) ([]store.NodeLayout, error)
	PlaceNode(ctx context.Context, actor, node string, ls []store.NodeLayout) error
	PruneLayout(ctx context.Context, live []string) (int, error)
}

// BatchMetrics are the hello_config_batch* series (spec flow-canvas).
type BatchMetrics struct {
	Batches *prometheus.CounterVec
	Ops     prometheus.Histogram
	// Traces counts flow traces (hello_flow_trace_total).
	Traces prometheus.Counter
}

// NewBatchMetrics creates the series and registers them on reg; a nil reg
// leaves them unregistered.
func NewBatchMetrics(reg prometheus.Registerer) *BatchMetrics {
	m := &BatchMetrics{
		Batches: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_config_batches_total",
			Help: "Configuration batches by result: applied, dry_run, stale, invalid or forbidden.",
		}, []string{"result"}),
		Ops: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "hello_config_batch_ops",
			Help:    "Operations per configuration batch.",
			Buckets: []float64{1, 2, 5, 10, 20, 50, 100, 200},
		}),
		Traces: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "hello_flow_trace_total",
			Help: "Call flow traces answered.",
		}),
	}
	if reg != nil {
		reg.MustRegister(m.Batches, m.Ops, m.Traces)
	}
	return m
}

func (m *BatchMetrics) count(result string, ops int) {
	if m == nil {
		return
	}
	m.Batches.WithLabelValues(result).Inc()
	m.Ops.Observe(float64(ops))
}

// batchSpec is the document's operations by id, loaded once.
var batchSpec = sync.OnceValues(func() (map[string]apispec.Operation, error) {
	spec, err := apispec.Load(openAPI)
	if err != nil {
		return nil, err
	}
	out := map[string]apispec.Operation{}
	for _, op := range spec.Operations() {
		out[op.ID] = op
	}
	return out, nil
})

// batchFail is a batch that did not apply, with the answer for it.
type batchFail struct {
	status    int
	code      string
	message   string
	problems  []configbatch.Problem
	conflicts []batchConflict
	// result is the metrics label.
	result string
}

func (f *batchFail) Error() string { return f.code + ": " + f.message }

type batchConflict struct {
	OpIndex int    `json:"opIndex"`
	Node    string `json:"node,omitempty"`
}

func writeBatchFail(w http.ResponseWriter, f *batchFail) {
	e := map[string]any{"code": f.code, "message": f.message}
	if len(f.problems) > 0 {
		e["errors"] = f.problems
	}
	if len(f.conflicts) > 0 {
		e["conflicts"] = f.conflicts
	}
	writeJSON(w, f.status, map[string]any{"error": e})
}

// batchResponse is the answer to an applied or dry-run batch or revert.
type batchResponse struct {
	Applied      bool                 `json:"applied"`
	BatchID      string               `json:"batchId,omitempty"`
	BaseRevision int64                `json:"baseRevision"`
	Revision     int64                `json:"revision"`
	Results      []configbatch.Result `json:"results"`
	Warnings     []flow.Warning       `json:"warnings"`
	// Skipped lists what a revert could not put back.
	Skipped []string `json:"skipped,omitempty"`
}

// batchPlan is one batch to run.
type batchPlan struct {
	dryRun   bool
	base     int64
	ops      []configbatch.Op
	layout   []configbatch.LayoutNode
	canvasID *int64
	// revertOf is the batch being undone, for a revert; plan then builds
	// the operations inside the transaction, from the stored batch.
	revertOf string
	plan     func(ctx context.Context, br *batchRun) ([]configbatch.Op, *batchFail)
}

// batchRun is the state of one running batch.
type batchRun struct {
	s       *server
	r       *http.Request
	h       http.Handler
	spec    map[string]apispec.Operation
	header  http.Header
	actor   string
	b       *store.Batch
	refs    configbatch.Refs
	applied []configbatch.Applied
	nodes   []string // node of operation i, for error reports
	skipped []string
	revive  map[string]configbatch.Revive
}

func (s *server) batchesDisabled(w http.ResponseWriter) bool {
	if s.Batches == nil || s.self == nil || s.self.Load() == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "configuration batches are not available")
		return true
	}
	return false
}

// credentialHeaders is what an operation of a batch is replayed with: the
// caller's own credentials and nothing else.
func credentialHeaders(h http.Header) http.Header {
	out := http.Header{}
	for _, k := range []string{"Cookie", "Authorization"} {
		if vs := h.Values(k); len(vs) > 0 {
			out[k] = append([]string(nil), vs...)
		}
	}
	return out
}

func batchPath(pattern string, params map[string]string) string {
	for k, v := range params {
		pattern = strings.ReplaceAll(pattern, "{"+k+"}", url.PathEscape(v))
	}
	return pattern
}

// outerVia is the origin the batch row records: the OAuth client, or the
// via an AI proposal or the assistant set.
func outerVia(r *http.Request) string {
	a := actor(r)
	if a.ClientID != "" {
		return a.ClientID
	}
	return a.Via
}

// POST /api/v1/config/batch.
func (s *server) applyConfigBatch(w http.ResponseWriter, r *http.Request) {
	if s.batchesDisabled(w) {
		return
	}
	var req configbatch.Request
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBatchBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "the batch is too large")
			return
		}
		badRequest(w, "invalid JSON body: "+jsonProblem(err))
		return
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		badRequest(w, "invalid JSON body: trailing data")
		return
	}
	if req.BaseRevision == nil || *req.BaseRevision < 0 {
		badRequest(w, "baseRevision is required")
		return
	}
	if len(req.Ops) > configbatch.MaxOps {
		s.BatchMetrics.count("invalid", len(req.Ops))
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("a batch has at most %d operations", configbatch.MaxOps))
		return
	}
	spec, err := batchSpec()
	if err != nil {
		s.internal(w, "load the API document", err)
		return
	}
	refused, problems := configbatch.Check(spec, req)
	if refused != nil {
		s.BatchMetrics.count("invalid", len(req.Ops))
		writeBatchFail(w, &batchFail{status: http.StatusBadRequest, code: "op_not_batchable",
			message: fmt.Sprintf("operation %d: %s", refused.OpIndex, refused.Message), problems: []configbatch.Problem{*refused}})
		return
	}
	if len(req.Layout) > 0 && req.CanvasID == nil {
		problems = append(problems, configbatch.Problem{OpIndex: -1, Path: "canvasId", Message: "is required with layout"})
	}
	if len(req.Layout) > maxLayoutWrite {
		problems = append(problems, configbatch.Problem{OpIndex: -1, Path: "layout", Message: fmt.Sprintf("at most %d nodes", maxLayoutWrite)})
	}
	if len(problems) > 0 {
		s.BatchMetrics.count("invalid", len(req.Ops))
		writeBatchFail(w, &batchFail{status: http.StatusUnprocessableEntity, code: "validation_failed",
			message: "the batch is not valid: " + problems[0].Message, problems: problems})
		return
	}
	s.respondBatch(w, r, batchPlan{dryRun: req.DryRun, base: *req.BaseRevision, ops: req.Ops, layout: req.Layout, canvasID: req.CanvasID})
}

// respondBatch runs a plan and answers it.
func (s *server) respondBatch(w http.ResponseWriter, r *http.Request, p batchPlan) {
	resp, fail, err := s.runBatch(r, p)
	switch {
	case err != nil:
		s.internal(w, "run a configuration batch", err)
	case fail != nil:
		s.BatchMetrics.count(fail.result, len(p.ops))
		writeBatchFail(w, fail)
	default:
		result := "applied"
		if p.dryRun {
			result = "dry_run"
		}
		s.BatchMetrics.count(result, len(resp.Results))
		writeJSON(w, http.StatusOK, resp)
	}
}

// runBatch runs the plan in one store batch. fail is the batch that cannot
// apply (the answer for the client); err is an infrastructure failure.
func (s *server) runBatch(r *http.Request, p batchPlan) (batchResponse, *batchFail, error) {
	spec, err := batchSpec()
	if err != nil {
		return batchResponse{}, nil, err
	}
	br := &batchRun{
		s: s, r: r, h: *s.self.Load(), spec: spec, header: credentialHeaders(r.Header),
		actor: actor(r).String(), refs: configbatch.Refs{}, revive: map[string]configbatch.Revive{},
	}
	var warnings []flow.Warning
	var ops []configbatch.Op
	base := p.base
	out, err := s.Batches.RunBatch(r.Context(), p.dryRun, func(ctx context.Context, b *store.Batch) (store.BatchRecord, error) {
		br.b = b
		if p.revertOf != "" {
			// A revert starts from the revision it finds under the lock.
			p.base = b.Revision()
			base = p.base
		}
		var fail *batchFail
		if p.plan != nil {
			if ops, fail = p.plan(ctx, br); fail != nil {
				return store.BatchRecord{}, fail
			}
		} else {
			ops = p.ops
		}
		before, err := br.graph(ctx)
		if err != nil {
			return store.BatchRecord{}, err
		}
		if fail := br.stale(ops, before, p.base); fail != nil {
			return store.BatchRecord{}, fail
		}
		for i, o := range ops {
			if fail := br.run(ctx, i, o); fail != nil {
				return store.BatchRecord{}, fail
			}
		}
		if fail := br.place(ctx, p); fail != nil {
			return store.BatchRecord{}, fail
		}
		after, err := br.graph(ctx)
		if err != nil {
			return store.BatchRecord{}, err
		}
		warnings = newWarnings(before.Warnings, after.Warnings)
		if !p.dryRun {
			live := make([]string, 0, len(after.Nodes))
			for _, n := range after.Nodes {
				live = append(live, n.ID)
			}
			if _, err := s.Batches.PruneLayout(ctx, live); err != nil {
				return store.BatchRecord{}, err
			}
		}
		return br.record(p)
	})
	var fail *batchFail
	var verr *store.BatchValidationError
	switch {
	case errors.As(err, &fail):
		return batchResponse{}, fail, nil
	case errors.As(err, &verr):
		return batchResponse{}, br.validationFail(verr), nil
	case err != nil:
		return batchResponse{}, nil, err
	}
	results := make([]configbatch.Result, len(br.applied))
	for i, a := range br.applied {
		results[i] = a.Result
	}
	if warnings == nil {
		warnings = []flow.Warning{}
	}
	return batchResponse{
		Applied: out.Applied, BatchID: out.ID, BaseRevision: base, Revision: out.Revision,
		Results: results, Warnings: warnings, Skipped: br.skipped,
	}, nil, nil
}

// record builds the config_batches row from what ran.
func (br *batchRun) record(p batchPlan) (store.BatchRecord, error) {
	ops := make([]configbatch.Op, len(br.applied))
	results := make([]configbatch.Result, len(br.applied))
	for i, a := range br.applied {
		ops[i], results[i] = configbatch.RedactOp(a.Op), a.Result
	}
	rec := store.BatchRecord{Actor: br.actor, Via: outerVia(br.r), BaseRevision: p.base, Reverts: p.revertOf}
	var err error
	if rec.Ops, err = json.Marshal(ops); err != nil {
		return rec, err
	}
	rec.Results, err = json.Marshal(results)
	return rec, err
}

// graph builds the flow graph, devices and phones included, from the batch's
// transaction.
func (br *batchRun) graph(ctx context.Context) (flow.Graph, error) {
	in, err := br.s.flowInput(ctx, true)
	if err != nil {
		return flow.Graph{}, err
	}
	return flow.Build(in, flow.Options{Devices: true})
}

func newWarnings(before, after []flow.Warning) []flow.Warning {
	had := map[flow.Warning]bool{}
	for _, w := range before {
		had[w] = true
	}
	var out []flow.Warning
	for _, w := range after {
		if !had[w] {
			out = append(out, w)
		}
	}
	return out
}

// stale refuses the batch when the configuration moved since the caller read
// it and an operation's target is no longer the version the caller based the
// operation on. An operation without expect, or one aimed at an object the
// batch itself creates, cannot overlap and is not checked.
func (br *batchRun) stale(ops []configbatch.Op, g flow.Graph, base int64) *batchFail {
	if base == br.b.Revision() {
		return nil
	}
	versions := make(map[string]string, len(g.Nodes))
	for _, n := range g.Nodes {
		versions[n.ID] = n.Version
	}
	var conflicts []batchConflict
	for i, o := range ops {
		info, ok := configbatch.Ops[o.OperationID]
		if !ok || o.Expect == "" || info.Create {
			continue
		}
		id := o.PathParams[configbatch.IDParam]
		node := configbatch.NodeID(info, id)
		if node == "" || strings.Contains(id, "${") {
			continue
		}
		if v, there := versions[node]; !there || v != o.Expect {
			conflicts = append(conflicts, batchConflict{OpIndex: i, Node: node})
		}
	}
	if len(conflicts) == 0 {
		return nil
	}
	return &batchFail{status: http.StatusConflict, code: "config_stale", result: "stale", conflicts: conflicts,
		message: fmt.Sprintf("the configuration moved from revision %d to %d and %d of the objects this batch changes changed with it; reload and try again",
			base, br.b.Revision(), len(conflicts))}
}

// get replays a GET with the caller's credentials. status is the HTTP status.
func (br *batchRun) get(ctx context.Context, path string) (body json.RawMessage, status int, fail *batchFail) {
	res, err := replay.Do(ctx, br.h, replay.Request{Method: http.MethodGet, Path: path, Header: br.header, Inherit: true, RemoteAddr: br.r.RemoteAddr})
	if err != nil {
		return nil, 0, &batchFail{status: http.StatusInternalServerError, code: "internal", result: "invalid", message: "internal error"}
	}
	if res.Fail != "" {
		return nil, res.Status, &batchFail{status: http.StatusInternalServerError, code: "internal", result: "invalid", message: "internal error"}
	}
	return res.Body, res.Status, nil
}

// run replays operation i and records what it did.
func (br *batchRun) run(ctx context.Context, i int, o configbatch.Op) *batchFail {
	info, op, err := configbatch.Lookup(br.spec, o.OperationID)
	if err != nil {
		return &batchFail{status: http.StatusBadRequest, code: "op_not_batchable", result: "invalid", message: err.Error()}
	}
	invalid := func(path, format string, a ...any) *batchFail {
		msg := fmt.Sprintf(format, a...)
		return &batchFail{status: http.StatusUnprocessableEntity, code: "validation_failed", result: "invalid",
			message:  fmt.Sprintf("operation %d (%s): %s", i, o.OperationID, msg),
			problems: []configbatch.Problem{{OpIndex: i, Path: path, Message: msg}}}
	}
	params := make(map[string]string, len(o.PathParams))
	for k, v := range o.PathParams {
		rv, err := br.refs.String(v)
		if err != nil {
			return invalid("pathParams."+k, "%v", err)
		}
		if !configbatch.ValidParam(rv) {
			return invalid("pathParams."+k, "must be 1-64 of A-Z a-z 0-9 . _ -")
		}
		params[k] = rv
	}
	body, err := br.refs.Body(o.Body)
	if err != nil {
		return invalid("body", "%v", err)
	}
	resolved := configbatch.Op{Ref: o.Ref, OperationID: o.OperationID, PathParams: params, Body: body, Expect: o.Expect}
	id := params[configbatch.IDParam]
	node := configbatch.NodeID(info, id)
	br.b.SetOp(i)
	br.nodes = append(br.nodes, node)

	var before json.RawMessage
	if !info.Create {
		if path, ok := configbatch.ReadPath(br.spec, resolved, id); ok {
			b, status, fail := br.get(ctx, path)
			if fail != nil {
				return fail
			}
			if status == http.StatusNotFound {
				return invalid("pathParams.id", "the target does not exist")
			}
			if status != http.StatusOK {
				return br.innerFail(i, o.OperationID, node, path, status, b)
			}
			before = bytes.TrimSpace(b)
		}
	}
	var layout []store.NodeLayout
	if info.Delete && node != "" {
		if layout, err = br.s.Batches.NodeLayouts(ctx, node); err != nil {
			return &batchFail{status: http.StatusInternalServerError, code: "internal", result: "invalid", message: "internal error"}
		}
	}
	path := batchPath(op.Path, params)
	res, err := replay.Do(ctx, br.h, replay.Request{Method: op.Method, Path: path, Body: body, Header: br.header, Inherit: true, RemoteAddr: br.r.RemoteAddr})
	if err != nil {
		return &batchFail{status: http.StatusInternalServerError, code: "internal", result: "invalid", message: "internal error"}
	}
	if res.Fail != "" {
		return &batchFail{status: http.StatusInternalServerError, code: res.Fail, result: "invalid",
			message: fmt.Sprintf("operation %d (%s): the request did not complete", i, o.OperationID)}
	}
	if res.Status < 200 || res.Status > 299 {
		return br.innerFail(i, o.OperationID, node, path, res.Status, res.Body)
	}
	if err := br.refs.Set(o.Ref, res.Body); o.Ref != "" && err != nil {
		return invalid("ref", "%v", err)
	}
	if info.Create {
		var created struct {
			ID json.Number `json:"id"`
		}
		d := json.NewDecoder(bytes.NewReader(res.Body))
		d.UseNumber()
		if err := d.Decode(&created); err != nil || created.ID.String() == "" {
			return &batchFail{status: http.StatusInternalServerError, code: "internal", result: "invalid", message: "internal error"}
		}
		id = created.ID.String()
		node = configbatch.NodeID(info, id)
		br.nodes[i] = node
	}
	var after json.RawMessage
	if !info.Delete {
		if rp, ok := configbatch.ReadPath(br.spec, resolved, id); ok {
			b, status, fail := br.get(ctx, rp)
			if fail != nil {
				return fail
			}
			if status != http.StatusOK {
				return br.innerFail(i, o.OperationID, node, rp, status, b)
			}
			after = bytes.TrimSpace(b)
		} else {
			after = bytes.TrimSpace(res.Body)
		}
	}
	diff, err := proposal.Diff(before, after)
	if err != nil {
		return &batchFail{status: http.StatusInternalServerError, code: "internal", result: "invalid", message: "internal error"}
	}
	if diff == nil {
		diff = []proposal.Change{}
	}
	status := "updated"
	switch {
	case info.Create:
		status = "created"
	case info.Delete:
		status = "deleted"
	case len(diff) == 0:
		status = "unchanged"
	}
	if info.Node == "" && !info.Create {
		id = ""
	}
	br.applied = append(br.applied, configbatch.Applied{Op: resolved, Result: configbatch.Result{
		Ref: o.Ref, OperationID: o.OperationID, Status: status, ID: id, Node: node,
		Before: before, After: after, Diff: diff, Layout: layout,
	}})
	return nil
}

// innerFail turns the answer of a replayed operation that was not a success
// into the answer for the batch: 403 stays 403 (naming the operation), a
// validation failure of the operation is 422 validation_failed with the
// operation's index, anything else is reported as it came.
func (br *batchRun) innerFail(i int, opID, node, path string, status int, body []byte) *batchFail {
	var e struct {
		Error struct {
			Code    string                           `json:"code"`
			Message string                           `json:"message"`
			Fields  []struct{ Path, Message string } `json:"fields"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	msg := e.Error.Message
	if msg == "" {
		msg = http.StatusText(status)
	}
	head := fmt.Sprintf("operation %d (%s): ", i, opID)
	switch {
	case status == http.StatusForbidden || status == http.StatusUnauthorized:
		code := e.Error.Code
		if code == "" {
			code = "forbidden_role"
		}
		return &batchFail{status: status, code: code, result: "forbidden", message: head + msg}
	case status >= 500:
		br.s.Log.Error("batch operation failed", "operation", opID, "path", path, "status", status)
		return &batchFail{status: status, code: "internal", result: "invalid", message: head + "failed"}
	}
	f := &batchFail{status: http.StatusUnprocessableEntity, code: "validation_failed", result: "invalid", message: head + msg}
	for _, fe := range e.Error.Fields {
		f.problems = append(f.problems, configbatch.Problem{OpIndex: i, Node: node, Path: fe.Path, Message: fe.Message})
	}
	if len(f.problems) == 0 {
		f.problems = []configbatch.Problem{{OpIndex: i, Node: node, Path: "", Message: msg}}
	}
	return f
}

// validationFail answers the errors the batch's final whole-configuration
// check found, naming the operation that made each one.
func (br *batchRun) validationFail(v *store.BatchValidationError) *batchFail {
	f := &batchFail{status: http.StatusUnprocessableEntity, code: "validation_failed", result: "invalid"}
	for _, fe := range v.Fields {
		p := configbatch.Problem{OpIndex: fe.OpIndex, Path: fe.Path, Message: fe.Message}
		if fe.OpIndex >= 0 && fe.OpIndex < len(br.nodes) {
			p.Node = br.nodes[fe.OpIndex]
		}
		f.problems = append(f.problems, p)
	}
	f.message = "the configuration after the batch is not valid"
	if len(f.problems) > 0 {
		f.message += ": " + f.problems[0].Path + ": " + f.problems[0].Message
	}
	return f
}

// place saves the positions of the new nodes, and for a revert moves the
// layout of a deleted node onto the node made again.
func (br *batchRun) place(ctx context.Context, p batchPlan) *batchFail {
	if p.dryRun {
		return nil
	}
	internal := &batchFail{status: http.StatusInternalServerError, code: "internal", result: "invalid", message: "internal error"}
	for ref, rv := range br.revive {
		for _, a := range br.applied {
			if a.Result.Ref != ref || len(rv.Layout) == 0 {
				continue
			}
			if err := br.s.Batches.PlaceNode(ctx, br.actor, rv.Node+":"+a.Result.ID, rv.Layout); err != nil && !errors.Is(err, store.ErrNotFound) {
				return internal
			}
		}
	}
	if len(p.layout) == 0 {
		return nil
	}
	kinds := map[string]bool{}
	for _, n := range flow.Get().Nodes {
		kinds[n.Kind] = true
	}
	for i, l := range p.layout {
		node, err := br.refs.String(l.Node)
		path := "layout[" + strconv.Itoa(i) + "]"
		bad := func(format string, a ...any) *batchFail {
			msg := fmt.Sprintf(format, a...)
			return &batchFail{status: http.StatusUnprocessableEntity, code: "validation_failed", result: "invalid",
				message: path + ": " + msg, problems: []configbatch.Problem{{OpIndex: -1, Path: path, Message: msg}}}
		}
		if err != nil {
			return bad("%v", err)
		}
		m := flowNodeIDRe.FindStringSubmatch(node)
		if m == nil || !kinds[m[1]] {
			return bad("node must be <kind>:<id> of a catalogue node kind")
		}
		if math.Abs(l.X) > maxLayoutCoord || math.Abs(l.Y) > maxLayoutCoord {
			return bad("x and y must be within ±%d", maxLayoutCoord)
		}
		if err := br.s.Batches.PlaceNode(ctx, br.actor, node, []store.NodeLayout{{CanvasID: *p.canvasID, X: l.X, Y: l.Y}}); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return bad("the canvas does not exist")
			}
			return internal
		}
	}
	return nil
}

// GET /api/v1/config/batches.
func (s *server) listConfigBatches(w http.ResponseWriter, r *http.Request) {
	if s.batchesDisabled(w) {
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			badRequest(w, "limit must be 1-200")
			return
		}
		limit = n
	}
	var before time.Time
	if v := r.URL.Query().Get("before"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			badRequest(w, "before must be an RFC 3339 time")
			return
		}
		before = t
	}
	bs, err := s.Batches.ListConfigBatches(r.Context(), before, limit)
	if err != nil {
		s.internal(w, "list configuration batches", err)
		return
	}
	writeJSON(w, http.StatusOK, items(bs))
}

// GET /api/v1/config/batches/{id}.
func (s *server) getConfigBatch(w http.ResponseWriter, r *http.Request) {
	if s.batchesDisabled(w) {
		return
	}
	b, err := s.Batches.GetConfigBatch(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, "configuration batch", err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// POST /api/v1/config/batches/{id}/revert.
func (s *server) revertConfigBatch(w http.ResponseWriter, r *http.Request) {
	if s.batchesDisabled(w) {
		return
	}
	var in struct {
		DryRun bool `json:"dryRun"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	cb, err := s.Batches.GetConfigBatch(r.Context(), id)
	if err != nil {
		s.storeError(w, "configuration batch", err)
		return
	}
	p := batchPlan{dryRun: in.DryRun, revertOf: cb.ID}
	p.plan = func(ctx context.Context, br *batchRun) ([]configbatch.Op, *batchFail) {
		return br.planRevert(ctx, cb.ID)
	}
	s.respondBatch(w, r, p)
}

// planRevert builds, inside the batch's transaction, the operations that undo
// the stored batch. It refuses a batch that was already reverted and one
// whose objects changed since it was applied.
func (br *batchRun) planRevert(ctx context.Context, id string) ([]configbatch.Op, *batchFail) {
	fail := func(status int, code, result, format string, a ...any) *batchFail {
		return &batchFail{status: status, code: code, result: result, message: fmt.Sprintf(format, a...)}
	}
	cb, err := br.s.Batches.GetConfigBatch(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fail(http.StatusNotFound, "not_found", "invalid", "configuration batch: not found")
	}
	if err != nil {
		return nil, fail(http.StatusInternalServerError, "internal", "invalid", "internal error")
	}
	if cb.RevertedBy != "" {
		return nil, fail(http.StatusConflict, "conflict", "invalid", "the batch was already reverted by batch %s", cb.RevertedBy)
	}
	var ops []configbatch.Op
	var results []configbatch.Result
	if json.Unmarshal(cb.Ops, &ops) != nil || json.Unmarshal(cb.Results, &results) != nil || len(ops) != len(results) {
		return nil, fail(http.StatusInternalServerError, "internal", "invalid", "internal error")
	}
	applied := make([]configbatch.Applied, len(ops))
	for i := range ops {
		applied[i] = configbatch.Applied{Op: ops[i], Result: results[i]}
	}
	var conflicts []batchConflict
	for _, c := range configbatch.StaleChecks(br.spec, applied) {
		body, status, f := br.get(ctx, c.Read)
		if f != nil {
			return nil, f
		}
		if status != http.StatusOK || !c.Same(body) {
			conflicts = append(conflicts, batchConflict{OpIndex: c.OpIndex, Node: c.Node})
		}
	}
	if len(conflicts) > 0 {
		return nil, &batchFail{status: http.StatusConflict, code: "config_stale", result: "stale", conflicts: conflicts,
			message: fmt.Sprintf("%d of the objects this batch changed have changed since; revert them by hand or reload", len(conflicts))}
	}
	inv, err := configbatch.BuildInverse(br.spec, applied)
	if err != nil {
		return nil, fail(http.StatusUnprocessableEntity, "validation_failed", "invalid", "%v", err)
	}
	br.revive, br.skipped = inv.Revive, inv.Skipped
	return inv.Ops, nil
}
