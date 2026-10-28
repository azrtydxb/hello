package configbatch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/azrtydxb/hello/internal/ai/proposal"
	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/azrtydxb/hello/internal/replay"
	"github.com/azrtydxb/hello/internal/store"
)

// Result is what one operation of an applied batch did. Before and After are
// the object as the API returns it (null for a create's before and a
// delete's after); Layout is where a deleted node sat on each canvas, so a
// revert can put the re-created node back.
type Result struct {
	Ref         string             `json:"ref,omitempty"`
	OperationID string             `json:"operationId"`
	Status      string             `json:"status"`
	ID          string             `json:"id,omitempty"`
	Node        string             `json:"node,omitempty"`
	Before      json.RawMessage    `json:"before"`
	After       json.RawMessage    `json:"after"`
	Diff        []proposal.Change  `json:"diff"`
	Layout      []store.NodeLayout `json:"layout,omitempty"`
}

// Applied is an operation as it ran (placeholders filled in) with its
// result.
type Applied struct {
	Op     Op
	Result Result
}

// Revive says where a node an inverse create re-makes sat before it was
// deleted, so its layout moves to the new id.
type Revive struct {
	Node   string
	Layout []store.NodeLayout
}

// Inverse is the batch that undoes an applied one.
type Inverse struct {
	// Ops run in this order: the applied operations backwards.
	Ops []Op
	// Revive is keyed by the ref of the inverse create.
	Revive map[string]Revive
	// Skipped lists what the revert cannot put back (credentials the batch
	// set are never stored).
	Skipped []string
}

// StaleCheck says an object must still look as the applied batch left it
// for the revert to be safe: the writable fields of Read's GET must equal
// those of After.
type StaleCheck struct {
	OpIndex int
	Node    string
	Read    string
	After   json.RawMessage
	Schema  map[string]any
}

// NodeID is the flow node id of an object an operation is about, or "".
func NodeID(info Info, id string) string {
	if info.Node == "" || id == "" {
		return ""
	}
	return info.Node + ":" + id
}

// ReadPath is the GET path that returns the object (or the list) an
// operation changes.
func ReadPath(spec map[string]apispec.Operation, o Op, id string) (string, bool) {
	info, ok := Ops[o.OperationID]
	if !ok {
		return "", false
	}
	if info.Read != "" {
		return info.Read, true
	}
	if info.Node == "" || id == "" {
		return "", false
	}
	d, ok := spec[Deleters[info.Node]]
	if !ok {
		return "", false
	}
	return strings.ReplaceAll(d.Path, "{"+IDParam+"}", id), true
}

// BuildInverse builds the batch that restores what applied changed:
// updates put the before values back, creates are deleted, deletes are made
// again from their before (with a new id: later references are rewritten to
// the new one), reorders and list replacements are sent again with their
// before. It fails when an operation cannot be undone.
func BuildInverse(spec map[string]apispec.Operation, applied []Applied) (Inverse, error) {
	inv := Inverse{Revive: map[string]Revive{}}
	createdAt := map[string]int{}
	for i, a := range applied {
		if info := Ops[a.Op.OperationID]; info.Create {
			createdAt[NodeID(info, a.Result.ID)] = i
		}
	}
	remap := map[string]string{}
	for i := len(applied) - 1; i >= 0; i-- {
		a := applied[i]
		info, ok := Ops[a.Op.OperationID]
		if !ok {
			return inv, fmt.Errorf("operation %d (%s) is not a batchable operation", i, a.Op.OperationID)
		}
		fail := func(format string, args ...any) error {
			return fmt.Errorf("operation %d (%s) cannot be reverted: %s", i, a.Op.OperationID, fmt.Sprintf(format, args...))
		}
		key := NodeID(info, a.Result.ID)
		id := a.Result.ID
		if r, ok := remap[key]; ok && key != "" {
			id = r
		}
		switch {
		case info.Create:
			del := Deleters[info.Node]
			if del == "" {
				return inv, fail("%s has no delete operation", info.Node)
			}
			inv.Ops = append(inv.Ops, Op{OperationID: del, PathParams: map[string]string{IDParam: id}})
		case info.Delete:
			create := Creators[info.Node]
			cop, ok := spec[create]
			if create == "" || !ok {
				return inv, fail("a deleted %s cannot be made again through a batch", info.Node)
			}
			if isNull(a.Result.Before) {
				return inv, fail("the batch did not record what it deleted")
			}
			body, skipped, err := restoreBody(cop, false, nil, a.Result.Before)
			if err != nil {
				return inv, fail("%v", err)
			}
			ref := "r" + itoa(i)
			inv.Ops = append(inv.Ops, Op{Ref: ref, OperationID: create, Body: body})
			remap[key] = "${" + ref + ".id}"
			inv.Revive[ref] = Revive{Node: info.Node, Layout: a.Result.Layout}
			inv.Skipped = append(inv.Skipped, skipped...)
		case info.Reorder:
			ids, err := listIDs(a.Result.Before)
			if err != nil {
				return inv, fail("%v", err)
			}
			body, _ := json.Marshal(map[string]any{"ids": ids})
			inv.Ops = append(inv.Ops, Op{OperationID: a.Op.OperationID, Body: body})
		case info.Node == "":
			op, ok := spec[a.Op.OperationID]
			if !ok || op.Body == nil {
				return inv, fail("the operation has no body")
			}
			body, skipped, err := restoreBody(op, false, nil, a.Result.Before)
			if err != nil {
				return inv, fail("%v", err)
			}
			inv.Ops = append(inv.Ops, Op{OperationID: a.Op.OperationID, Body: body})
			inv.Skipped = append(inv.Skipped, skipped...)
		default:
			if j, made := createdAt[key]; made && j < i {
				// The object did not exist before the batch: deleting it
				// (the inverse of its create) undoes this change too.
				continue
			}
			op, ok := spec[a.Op.OperationID]
			if !ok || op.Body == nil {
				return inv, fail("the operation has no body")
			}
			body, skipped, err := restoreBody(op, strings.EqualFold(op.Method, "PATCH"), a.Op.Body, a.Result.Before)
			if err != nil {
				return inv, fail("%v", err)
			}
			inv.Skipped = append(inv.Skipped, skipped...)
			if body == nil {
				continue
			}
			inv.Ops = append(inv.Ops, Op{OperationID: a.Op.OperationID, PathParams: map[string]string{IDParam: id}, Body: body})
		}
	}
	return inv, nil
}

// StaleChecks lists, for the last operation that touched each object or
// list, the state the revert expects to find. Deletes expect nothing: the
// object is gone.
func StaleChecks(spec map[string]apispec.Operation, applied []Applied) []StaleCheck {
	var out []StaleCheck
	seen := map[string]bool{}
	for i := len(applied) - 1; i >= 0; i-- {
		a := applied[i]
		info := Ops[a.Op.OperationID]
		key := NodeID(info, a.Result.ID)
		if key == "" {
			key = "list:" + info.Read
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		path, ok := ReadPath(spec, a.Op, a.Result.ID)
		if info.Delete || !ok || isNull(a.Result.After) {
			continue
		}
		var schema map[string]any
		if op, ok := spec[a.Op.OperationID]; ok && op.Body != nil {
			schema = op.Body.JSON
		}
		out = append(out, StaleCheck{OpIndex: i, Node: NodeID(info, a.Result.ID), Read: path, After: a.Result.After, Schema: schema})
	}
	slices.Reverse(out)
	return out
}

// Same reports whether two representations of an object agree on the fields
// the stale check's schema lists as writable (all of them without a
// schema).
func (c StaleCheck) Same(current json.RawMessage) bool {
	a, b := decodeNumbers(c.After), decodeNumbers(current)
	if c.Schema != nil {
		a, b = Prune(a, c.Schema), Prune(b, c.Schema)
	}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func decodeNumbers(raw json.RawMessage) any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	_ = dec.Decode(&v)
	return v
}

func isNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

func listIDs(before json.RawMessage) ([]any, error) {
	var l struct {
		Items []struct {
			ID json.Number `json:"id"`
		} `json:"items"`
	}
	dec := json.NewDecoder(bytes.NewReader(before))
	dec.UseNumber()
	if err := dec.Decode(&l); err != nil {
		return nil, fmt.Errorf("the batch did not record the list it reordered")
	}
	ids := make([]any, 0, len(l.Items))
	for _, it := range l.Items {
		ids = append(ids, it.ID)
	}
	return ids, nil
}

// restoreBody builds the request body that puts before back. A PATCH
// restores only the fields the original body set (its keys); anything else
// restores the whole writable object. Credential fields are never in
// before, so they are listed as skipped.
func restoreBody(op apispec.Operation, patch bool, orig, before json.RawMessage) (body json.RawMessage, skipped []string, err error) {
	prev, ok := decodeNumbers(before).(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("the batch did not record the object before the change")
	}
	m := prev
	if patch {
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(orig, &keys); err != nil {
			return nil, nil, fmt.Errorf("the recorded request body is not an object")
		}
		m = map[string]any{}
		for _, k := range slices.Sorted(maps.Keys(keys)) {
			if IsCredentialKey(k) {
				skipped = append(skipped, k+" (a credential the batch set is not stored)")
				continue
			}
			m[k] = prev[k] // absent in before: null clears it
		}
		if len(m) == 0 {
			return nil, skipped, nil
		}
	}
	var out any = m
	if op.Body != nil && op.Body.JSON != nil {
		out = Prune(m, op.Body.JSON)
	}
	body, err = json.Marshal(out)
	return body, skipped, err
}

// Prune keeps only what schema lists as a property, recursively, and drops
// read-only properties, so a GET representation can be sent back as a
// request body.
func Prune(v any, schema map[string]any) any {
	switch t := v.(type) {
	case map[string]any:
		props, ok := schema["properties"].(map[string]any)
		if !ok {
			return t
		}
		out := make(map[string]any, len(t))
		for k, c := range t {
			ps, ok := props[k].(map[string]any)
			if !ok || ps["readOnly"] == true {
				continue
			}
			out[k] = Prune(c, ps)
		}
		return out
	case []any:
		items, ok := schema["items"].(map[string]any)
		if !ok {
			return t
		}
		out := make([]any, len(t))
		for i, c := range t {
			out[i] = Prune(c, items)
		}
		return out
	}
	return v
}

// IsCredentialKey reports a property named password, pin or secret, or
// ending in Password or Pin: the names whose values a batch withholds from
// everything it stores and returns (spec S-21 and decision 2).
func IsCredentialKey(k string) bool {
	l := strings.ToLower(k)
	return l == "password" || l == "pin" || l == "secret" || strings.HasSuffix(l, "password") || strings.HasSuffix(l, "pin") || strings.HasSuffix(l, "secret")
}

// Redact returns body with every credential property's value replaced by
// replay.Withheld, at any depth.
func Redact(body json.RawMessage) json.RawMessage {
	if len(body) == 0 {
		return body
	}
	v := decodeNumbers(body)
	redactValue(v)
	out, err := json.Marshal(v)
	if err != nil {
		return body
	}
	return out
}

func redactValue(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, c := range t {
			if IsCredentialKey(k) {
				if c != nil {
					t[k] = replay.Withheld
				}
				continue
			}
			redactValue(c)
		}
	case []any:
		for _, c := range t {
			redactValue(c)
		}
	}
}

// RedactOp returns o with its body's credentials withheld.
func RedactOp(o Op) Op {
	o.Body = Redact(o.Body)
	return o
}
