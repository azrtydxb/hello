package proposal

import (
	"bytes"
	"encoding/json"
	"maps"

	"github.com/azrtydxb/hello/internal/jsondiff"
)

// Change is one field that differs between two JSON documents
// (jsondiff.Change).
type Change = jsondiff.Change

// Diff lists the fields that differ between before and after
// (jsondiff.Diff).
func Diff(before, after json.RawMessage) ([]Change, error) { return jsondiff.Diff(before, after) }

// Equal reports whether two JSON documents are the same value, ignoring
// formatting and key order.
func Equal(a, b json.RawMessage) bool { return jsondiff.Equal(a, b) }

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

// mergePatch applies a JSON merge patch (RFC 7386) to doc: an object
// member set to null is removed, other members replace or merge in.
func mergePatch(doc, patch json.RawMessage) (json.RawMessage, error) {
	d, err := decode(doc)
	if err != nil {
		return nil, err
	}
	p, err := decode(patch)
	if err != nil {
		return nil, err
	}
	return json.Marshal(mergeValue(d, p))
}

func mergeValue(doc, patch any) any {
	pm, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	dm, _ := doc.(map[string]any)
	out := make(map[string]any, len(dm)+len(pm))
	maps.Copy(out, dm)
	for k, v := range pm {
		if v == nil {
			delete(out, k)
			continue
		}
		out[k] = mergeValue(out[k], v)
	}
	return out
}
