package configbatch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// Op is one operation of a batch: the same shape as an AI proposal action.
type Op struct {
	// Ref names the result so later operations can use it as ${ref.field}.
	Ref         string            `json:"ref,omitempty"`
	OperationID string            `json:"operationId"`
	PathParams  map[string]string `json:"pathParams,omitempty"`
	Body        json.RawMessage   `json:"body,omitempty"`
	// Expect is the version of the target node the caller based the
	// operation on; it is checked when the configuration moved.
	Expect string `json:"expect,omitempty"`
}

// LayoutNode is where a new node is placed on the request's canvas. Node may
// hold a placeholder: "ringGroup:${rg.id}".
type LayoutNode struct {
	Node string  `json:"node"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
}

// Request is the body of POST /api/v1/config/batch.
type Request struct {
	BaseRevision *int64       `json:"baseRevision"`
	DryRun       bool         `json:"dryRun"`
	CanvasID     *int64       `json:"canvasId,omitempty"`
	Ops          []Op         `json:"ops"`
	Layout       []LayoutNode `json:"layout,omitempty"`
}

var (
	refName     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,31}$`)
	placeholder = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z0-9_]+)\}`)
	wholeHolder = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z0-9_]+)\}$`)
)

// ValidRef reports whether s may name a result.
func ValidRef(s string) bool { return refName.MatchString(s) }

// Placeholders lists the "ref.field" pairs a string uses, in order.
func Placeholders(s string) [][2]string {
	var out [][2]string
	for _, m := range placeholder.FindAllStringSubmatch(s, -1) {
		out = append(out, [2]string{m[1], m[2]})
	}
	return out
}

// OpPlaceholders lists every placeholder in an operation's path parameters
// and body.
func OpPlaceholders(o Op) [][2]string {
	var out [][2]string
	for _, k := range sortedKeys(o.PathParams) {
		out = append(out, Placeholders(o.PathParams[k])...)
	}
	if len(o.Body) > 0 {
		var v any
		if err := json.Unmarshal(o.Body, &v); err == nil {
			walkStrings(v, func(s string) { out = append(out, Placeholders(s)...) })
		}
	}
	return out
}

func walkStrings(v any, f func(string)) {
	switch t := v.(type) {
	case string:
		f(t)
	case []any:
		for _, c := range t {
			walkStrings(c, f)
		}
	case map[string]any:
		for _, k := range sortedKeys(t) {
			walkStrings(t[k], f)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string { return slices.Sorted(maps.Keys(m)) }

// Refs holds the objects earlier operations returned, by ref.
type Refs map[string]map[string]any

// Set stores the decoded result of the operation named ref.
func (r Refs) Set(ref string, body json.RawMessage) error {
	if ref == "" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	m := map[string]any{}
	if err := dec.Decode(&m); err != nil {
		return fmt.Errorf("the result of %q is not an object", ref)
	}
	r[ref] = m
	return nil
}

func (r Refs) lookup(ref, field string) (any, error) {
	obj, ok := r[ref]
	if !ok {
		return nil, fmt.Errorf("placeholder ${%s.%s}: no earlier operation has ref %q", ref, field, ref)
	}
	v, ok := obj[field]
	if !ok || v == nil {
		return nil, fmt.Errorf("placeholder ${%s.%s}: the result has no %q", ref, field, field)
	}
	return v, nil
}

// String fills the placeholders in s as text.
func (r Refs) String(s string) (string, error) {
	var firstErr error
	out := placeholder.ReplaceAllStringFunc(s, func(m string) string {
		sub := placeholder.FindStringSubmatch(m)
		v, err := r.lookup(sub[1], sub[2])
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return m
		}
		return fmt.Sprint(v)
	})
	return out, firstErr
}

// Body fills the placeholders in a JSON body. A string that is exactly one
// placeholder becomes the referenced value with its type (a number stays a
// number); a placeholder inside longer text is replaced as text.
func (r Refs) Body(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || !bytes.Contains(raw, []byte("${")) {
		return raw, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	out, err := r.fill(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

func (r Refs) fill(v any) (any, error) {
	switch t := v.(type) {
	case string:
		if m := wholeHolder.FindStringSubmatch(t); m != nil {
			return r.lookup(m[1], m[2])
		}
		if strings.Contains(t, "${") {
			return r.String(t)
		}
		return t, nil
	case []any:
		for i, c := range t {
			f, err := r.fill(c)
			if err != nil {
				return nil, err
			}
			t[i] = f
		}
		return t, nil
	case map[string]any:
		for k, c := range t {
			f, err := r.fill(c)
			if err != nil {
				return nil, err
			}
			t[k] = f
		}
		return t, nil
	}
	return v, nil
}
