package proposal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/replay"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/google/jsonschema-go/jsonschema"
)

// RoutingReader loads the routing configuration for the route dry run;
// *store.Store implements it.
type RoutingReader interface {
	RoutingConfig(ctx context.Context) (store.RoutingSnapshot, error)
}

// SchemaValidator is the Validator: allowlist, request schema, credential
// properties, existing targets, route dry run (spec S-12).
type SchemaValidator struct {
	ops    map[string]apispec.Operation
	gets   map[string]apispec.Operation // GET operation by path
	routes RoutingReader
	api    atomic.Pointer[http.Handler]

	mu      sync.Mutex
	schemas map[string]*jsonschema.Resolved
}

var _ Validator = (*SchemaValidator)(nil)

// NewValidator checks the document against the allowlist (CheckDocument).
// api is the handler reads are replayed through; it may be nil here and set
// with SetHandler once the API exists.
func NewValidator(spec *apispec.Spec, api http.Handler, routes RoutingReader) (*SchemaValidator, error) {
	if err := CheckDocument(spec); err != nil {
		return nil, err
	}
	v := &SchemaValidator{
		ops:     map[string]apispec.Operation{},
		gets:    map[string]apispec.Operation{},
		routes:  routes,
		schemas: map[string]*jsonschema.Resolved{},
	}
	for _, op := range spec.Operations() {
		v.ops[op.ID] = op
		if op.Method == http.MethodGet {
			v.gets[op.Path] = op
		}
	}
	if api != nil {
		v.SetHandler(api)
	}
	return v, nil
}

// SetHandler sets the API handler reads are replayed through.
func (v *SchemaValidator) SetHandler(h http.Handler) { v.api.Store(&h) }

var paramValue = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}$`)

// Validate checks the draft and fills each action's Before, After,
// Destructive and References. A draft that fails is never stored.
func (v *SchemaValidator) Validate(ctx context.Context, ident Identity, d *Draft) error {
	if strings.TrimSpace(d.Title) == "" {
		return errors.New("the proposal needs a title")
	}
	if len(d.Actions) == 0 || len(d.Actions) > MaxActions {
		return fmt.Errorf("a proposal has 1 to %d actions, got %d", MaxActions, len(d.Actions))
	}
	for i := range d.Actions {
		if err := v.action(ctx, ident, &d.Actions[i]); err != nil {
			return fmt.Errorf("action %d (%s): %w", i, d.Actions[i].OperationID, err)
		}
	}
	return v.dryRun(ctx, d.Actions)
}

func (v *SchemaValidator) action(ctx context.Context, ident Identity, a *Action) error {
	if !Allowed(a.OperationID) {
		return errors.New("the operation is not allowed in a proposal")
	}
	op, ok := v.ops[a.OperationID]
	if !ok {
		return errors.New("the operation is not in the API document")
	}
	if err := checkParams(op, a.PathParams); err != nil {
		return err
	}
	body, err := v.checkBody(op, a.Body)
	if err != nil {
		return err
	}
	if hasCredential(body) {
		return errors.New("the body sets a credential property; a proposal never carries passwords, PINs or secrets")
	}
	a.Before, a.After, a.Destructive, a.References = nil, nil, IsDelete(op.ID), nil
	if op.Method != http.MethodPost {
		get, ok := v.gets[op.Path]
		if !ok {
			return errors.New("the target cannot be read: the operation has no GET")
		}
		before, err := v.read(ctx, ident, get, a.PathParams)
		if err != nil {
			return err
		}
		a.Before = before
	}
	switch op.Method {
	case http.MethodPut, http.MethodPost:
		a.After = bytesOrNil(a.Body)
	case http.MethodPatch:
		if a.After, err = mergePatch(a.Before, a.Body); err != nil {
			return fmt.Errorf("merge: %w", err)
		}
	}
	if a.Destructive {
		if a.References, err = v.references(ctx, ident, op.ID, a.Before); err != nil {
			return err
		}
	}
	return nil
}

func bytesOrNil(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	return b
}

// checkParams requires exactly the operation's path parameters, each a
// plain token, so a value cannot add path segments or a query.
func checkParams(op apispec.Operation, got map[string]string) error {
	want := 0
	for _, p := range op.Params {
		if p.In != "path" {
			continue
		}
		want++
		val, ok := got[p.Name]
		if !ok || !paramValue.MatchString(val) {
			return fmt.Errorf("path parameter %q is missing or not a plain identifier", p.Name)
		}
	}
	if len(got) != want {
		return errors.New("unknown path parameters")
	}
	return nil
}

// checkBody validates the body against the request schema, unknown
// properties rejected, and returns it decoded.
func (v *SchemaValidator) checkBody(op apispec.Operation, raw json.RawMessage) (any, error) {
	if op.Body == nil || op.Body.JSON == nil {
		if len(raw) > 0 && string(raw) != "null" {
			return nil, errors.New("the operation takes no body")
		}
		return nil, nil
	}
	if len(raw) == 0 {
		return nil, errors.New("the operation needs a body")
	}
	var body any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("body is not JSON: %w", err)
	}
	rs, err := v.resolved(op)
	if err != nil {
		return nil, err
	}
	if err := rs.Validate(body); err != nil {
		return nil, fmt.Errorf("body does not match the request schema: %w", err)
	}
	return body, nil
}

func (v *SchemaValidator) resolved(op apispec.Operation) (*jsonschema.Resolved, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if rs, ok := v.schemas[op.ID]; ok {
		return rs, nil
	}
	raw, err := json.Marshal(op.Body.JSON)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	closeObjects(generic)
	if raw, err = json.Marshal(generic); err != nil {
		return nil, err
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("request schema: %w", err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("request schema: %w", err)
	}
	v.schemas[op.ID] = rs
	return rs, nil
}

// closeObjects rejects unknown properties: every object schema that lists
// properties and says nothing about additional ones gets
// additionalProperties false.
func closeObjects(n any) {
	switch t := n.(type) {
	case map[string]any:
		if _, has := t["properties"]; has {
			if _, set := t["additionalProperties"]; !set {
				t["additionalProperties"] = false
			}
		}
		for _, c := range t {
			closeObjects(c)
		}
	case []any:
		for _, c := range t {
			closeObjects(c)
		}
	}
}

// hasCredential reports a property named password, pin or secret, or ending
// in Password, anywhere in the body (spec S-12).
func hasCredential(n any) bool {
	switch t := n.(type) {
	case map[string]any:
		for k, c := range t {
			l := strings.ToLower(k)
			if l == "password" || l == "pin" || l == "secret" || strings.HasSuffix(l, "password") || hasCredential(c) {
				return true
			}
		}
	case []any:
		for _, c := range t {
			if hasCredential(c) {
				return true
			}
		}
	}
	return false
}

// pathFor fills an operation's path template with the parameters.
func pathFor(path string, params map[string]string) string {
	for k, val := range params {
		path = strings.ReplaceAll(path, "{"+k+"}", url.PathEscape(val))
	}
	return path
}

// read replays a GET as the agent and returns its body with secrets
// withheld. A non-2xx is the validation error: the target does not exist.
func (v *SchemaValidator) read(ctx context.Context, ident Identity, get apispec.Operation, params map[string]string) (json.RawMessage, error) {
	h := v.api.Load()
	if h == nil {
		return nil, errors.New("the API is not wired")
	}
	res, err := replay.Do(auth.WithAgent(ctx, ident), *h, replay.Request{Method: http.MethodGet, Path: pathFor(get.Path, params)})
	if err != nil {
		return nil, fmt.Errorf("read target: %w", err)
	}
	switch {
	case res.Fail != "":
		return nil, fmt.Errorf("read target: %s", res.Fail)
	case res.Status == http.StatusNotFound:
		return nil, errors.New("the target does not exist")
	case res.Status < 200 || res.Status > 299:
		return nil, fmt.Errorf("read target: status %d", res.Status)
	}
	var doc any
	if err := json.Unmarshal(res.Body, &doc); err != nil {
		return nil, fmt.Errorf("read target: %w", err)
	}
	replay.Redact(doc, get.Secrets)
	return json.Marshal(doc)
}

// Current reads each action's target now, with the headers given (the
// caller's Cookie and Authorization, or an agent identity in ctx), null for
// a create. Reads that fail give a nil entry.
func (v *SchemaValidator) Current(ctx context.Context, header http.Header, actions []Action) []json.RawMessage {
	return currentState(ctx, v.api.Load(), v.ops, v.gets, header, actions)
}

func currentState(ctx context.Context, h *http.Handler, ops, gets map[string]apispec.Operation, header http.Header, actions []Action) []json.RawMessage {
	out := make([]json.RawMessage, len(actions))
	if h == nil {
		return out
	}
	for i, a := range actions {
		op := ops[a.OperationID]
		get, ok := gets[op.Path]
		if !ok || op.Method == http.MethodPost {
			continue
		}
		res, err := replay.Do(ctx, *h, replay.Request{Method: http.MethodGet, Path: pathFor(get.Path, a.PathParams), Header: pick(header)})
		if err != nil || res.Fail != "" || res.Status < 200 || res.Status > 299 {
			continue
		}
		var doc any
		if json.Unmarshal(res.Body, &doc) != nil {
			continue
		}
		replay.Redact(doc, get.Secrets)
		out[i], _ = json.Marshal(doc)
	}
	return out
}

// pick copies the only headers a proposal replay carries: the applier's
// Cookie and Authorization.
func pick(h http.Header) http.Header {
	out := http.Header{}
	for _, k := range []string{"Cookie", "Authorization"} {
		if vs := h.Values(k); len(vs) > 0 {
			out[k] = append([]string(nil), vs...)
		}
	}
	return out
}

// references lists what a delete takes with it or leaves dangling: for a
// ring group its members and the inbound routes sending calls to it, for an
// outbound route its trunks and source extensions, for an inbound route its
// trunk and destination.
func (v *SchemaValidator) references(ctx context.Context, ident Identity, id string, before json.RawMessage) ([]Reference, error) {
	var t map[string]any
	if err := json.Unmarshal(before, &t); err != nil {
		return nil, fmt.Errorf("read target: %w", err)
	}
	var refs []Reference
	switch id {
	case "deleteRingGroup":
		name, gid := str(t["name"]), num(t["id"])
		for _, m := range list(t["members"]) {
			refs = append(refs, Reference{Kind: "extension", ID: num(m["extensionId"]), Name: str(m["number"]), Detail: "member of the group"})
		}
		in, err := v.items(ctx, ident, "/api/v1/routes/inbound")
		if err != nil {
			return nil, err
		}
		for _, r := range in {
			if d := str(r["destination"]); d != "" && (d == name || d == gid) {
				refs = append(refs, Reference{Kind: "inbound_route", ID: num(r["id"]), Name: str(r["name"]), Detail: "sends calls to the group (" + str(r["destinationKind"]) + ")"})
			}
		}
	case "deleteOutboundRoute":
		for _, tr := range list0(t["trunks"]) {
			refs = append(refs, Reference{Kind: "trunk", ID: tr, Name: "trunk " + tr, Detail: "reached through the route"})
		}
		for _, e := range list0(t["sourceExtensions"]) {
			refs = append(refs, Reference{Kind: "extension", ID: e, Name: e, Detail: "may dial through the route"})
		}
		if b, _ := t["emergency"].(bool); b {
			refs = append(refs, Reference{Kind: "emergency", ID: num(t["id"]), Name: str(t["name"]), Detail: "an emergency route"})
		}
	case "deleteInboundRoute":
		if tr := num(t["trunkId"]); tr != "" {
			refs = append(refs, Reference{Kind: "trunk", ID: tr, Name: "trunk " + tr, Detail: "calls arrive on it"})
		}
		if d := str(t["destination"]); d != "" {
			refs = append(refs, Reference{Kind: str(t["destinationKind"]), ID: d, Name: d, Detail: "where the route delivers calls"})
		}
	}
	return refs, nil
}

func (v *SchemaValidator) items(ctx context.Context, ident Identity, path string) ([]map[string]any, error) {
	body, err := v.read(ctx, ident, apispec.Operation{Path: path}, nil)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Items []map[string]any `json:"items"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return nil, nil
	}
	return doc.Items, nil
}

func str(v any) string { s, _ := v.(string); return s }

func num(v any) string {
	switch t := v.(type) {
	case float64:
		return strconv.FormatInt(int64(t), 10)
	case string:
		return t
	}
	return ""
}

func list(v any) []map[string]any {
	var out []map[string]any
	a, _ := v.([]any)
	for _, e := range a {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func list0(v any) []string {
	var out []string
	a, _ := v.([]any)
	for _, e := range a {
		if s := num(e); s != "" {
			out = append(out, s)
		}
	}
	return out
}
