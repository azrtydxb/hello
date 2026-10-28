package configbatch

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/azrtydxb/hello/internal/apispec"
)

// Problem is one thing wrong with a batch, at an operation (OpIndex -1 for
// the request itself).
type Problem struct {
	OpIndex int    `json:"opIndex"`
	Node    string `json:"node,omitempty"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

var paramValue = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}$`)

// ValidParam reports whether v may be put into a request path.
func ValidParam(v string) bool { return paramValue.MatchString(v) }

// Check validates a request without running anything. refused is the first
// operation that may not be in a batch (400 op_not_batchable); problems are
// the rest (422 validation_failed): wrong path parameters, bad or reused
// refs, and placeholders that name no earlier result or a field the earlier
// operation's response does not have.
func Check(spec map[string]apispec.Operation, req Request) (refused *Problem, problems []Problem) {
	problems = []Problem{}
	if len(req.Ops) == 0 {
		return nil, append(problems, Problem{OpIndex: -1, Path: "ops", Message: "a batch needs at least one operation"})
	}
	outputs := map[string]map[string]any{} // ref -> response properties
	for i, o := range req.Ops {
		_, op, err := Lookup(spec, o.OperationID)
		if err != nil {
			return &Problem{OpIndex: i, Path: "ops[" + itoa(i) + "].operationId", Message: err.Error()}, nil
		}
		p := func(path, format string, a ...any) {
			problems = append(problems, Problem{OpIndex: i, Path: "ops[" + itoa(i) + "]." + path, Message: fmt.Sprintf(format, a...)})
		}
		want := map[string]bool{}
		for _, pa := range op.Params {
			if pa.In == "path" {
				want[pa.Name] = true
				if _, ok := o.PathParams[pa.Name]; !ok {
					p("pathParams."+pa.Name, "is required")
				}
			}
		}
		for _, k := range sortedKeys(o.PathParams) {
			switch {
			case !want[k]:
				p("pathParams."+k, "is not a path parameter of %s", o.OperationID)
			case !strings.Contains(o.PathParams[k], "${") && !ValidParam(o.PathParams[k]):
				p("pathParams."+k, "must be 1-64 of A-Z a-z 0-9 . _ -")
			}
		}
		if op.Body != nil && len(o.Body) == 0 {
			p("body", "is required by %s", o.OperationID)
		}
		for _, ph := range OpPlaceholders(o) {
			props, ok := outputs[ph[0]]
			switch {
			case !ok:
				p("body", "placeholder ${%s.%s}: no earlier operation has ref %q", ph[0], ph[1], ph[0])
			case props != nil:
				if _, has := props[ph[1]]; !has {
					p("body", "placeholder ${%s.%s}: the response of the operation with ref %q has no %q", ph[0], ph[1], ph[0], ph[1])
				}
			}
		}
		if o.Ref != "" {
			if !ValidRef(o.Ref) {
				p("ref", "must be a letter or _ followed by up to 31 of A-Z a-z 0-9 _")
			} else if _, dup := outputs[o.Ref]; dup {
				p("ref", "%q is already used by an earlier operation", o.Ref)
			}
			if op.Output == nil {
				p("ref", "%s returns no object to refer to", o.OperationID)
			}
			// An object without declared properties accepts any field.
			props, _ := op.Output["properties"].(map[string]any)
			outputs[o.Ref] = props
		}
	}
	for i, l := range req.Layout {
		for _, ph := range Placeholders(l.Node) {
			if _, ok := outputs[ph[0]]; !ok {
				problems = append(problems, Problem{OpIndex: -1, Path: "layout[" + itoa(i) + "].node",
					Message: fmt.Sprintf("placeholder ${%s.%s}: no operation has ref %q", ph[0], ph[1], ph[0])})
			}
		}
	}
	return nil, problems
}

func itoa(i int) string { return fmt.Sprint(i) }
