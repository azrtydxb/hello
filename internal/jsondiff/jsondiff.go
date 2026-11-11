// Package jsondiff is the field-level JSON diff that AI proposals and
// configuration batches show (spec ai-agent S-13, flow-canvas S-13). It
// imports nothing of Hello's, so both can use it.
package jsondiff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// Change is one field that differs between two JSON documents; Before or
// After is nil where the field is absent on that side.
type Change struct {
	Path   string `json:"path"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

// Diff lists the fields that differ between before and after, as a
// field-level JSON diff in path order (objects by key, arrays by index). An
// empty side (a create's before, a delete's after) is a null document, so a
// delete lists every field that disappears. Invalid JSON is an error.
func Diff(before, after json.RawMessage) ([]Change, error) {
	b, err := decode(before)
	if err != nil {
		return nil, fmt.Errorf("before: %w", err)
	}
	a, err := decode(after)
	if err != nil {
		return nil, fmt.Errorf("after: %w", err)
	}
	var out []Change
	diffAt("", b, a, &out)
	return out, nil
}

// Equal reports whether two JSON documents are the same value, ignoring
// formatting and key order.
func Equal(a, b json.RawMessage) bool {
	x, err1 := decode(a)
	y, err2 := decode(b)
	return err1 == nil && err2 == nil && reflect.DeepEqual(x, y)
}

// decode is raw as a generic value; an empty document is nil.
func decode(raw json.RawMessage) (any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func diffAt(path string, b, a any, out *[]Change) {
	bm, bok := b.(map[string]any)
	am, aok := a.(map[string]any)
	if bok && aok {
		for _, k := range slices.Sorted(maps.Keys(mergeKeys(bm, am))) {
			bv, bin := bm[k]
			av, ain := am[k]
			p := path + "/" + k
			switch {
			case !bin:
				*out = append(*out, Change{Path: p, After: av})
			case !ain:
				*out = append(*out, Change{Path: p, Before: bv})
			default:
				diffAt(p, bv, av, out)
			}
		}
		return
	}
	bs, bok := b.([]any)
	as, aok := a.([]any)
	if bok && aok {
		for i := range max(len(bs), len(as)) {
			p := fmt.Sprintf("%s/%d", path, i)
			switch {
			case i >= len(bs):
				*out = append(*out, Change{Path: p, After: as[i]})
			case i >= len(as):
				*out = append(*out, Change{Path: p, Before: bs[i]})
			default:
				diffAt(p, bs[i], as[i], out)
			}
		}
		return
	}
	if reflect.DeepEqual(b, a) {
		return
	}
	// A whole-document change (create or delete) lists its fields.
	if b == nil && isMap(a) || a == nil && isMap(b) {
		flatten(path, b, a, out)
		return
	}
	if path == "" {
		path = "/"
	}
	*out = append(*out, Change{Path: path, Before: b, After: a})
}

// flatten lists every leaf of the side that exists, for a create or delete.
func flatten(path string, b, a any, out *[]Change) {
	side, isAfter := b, false
	if b == nil {
		side, isAfter = a, true
	}
	var walk func(p string, v any)
	walk = func(p string, v any) {
		switch t := v.(type) {
		case map[string]any:
			for _, k := range slices.Sorted(maps.Keys(t)) {
				walk(p+"/"+k, t[k])
			}
		default:
			c := Change{Path: strings.TrimSuffix(p, "/")}
			if c.Path == "" {
				c.Path = "/"
			}
			if isAfter {
				c.After = v
			} else {
				c.Before = v
			}
			*out = append(*out, c)
		}
	}
	walk(path, side)
}

func mergeKeys(a, b map[string]any) map[string]struct{} {
	out := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		out[k] = struct{}{}
	}
	for k := range b {
		out[k] = struct{}{}
	}
	return out
}

func isMap(v any) bool { _, ok := v.(map[string]any); return ok }
