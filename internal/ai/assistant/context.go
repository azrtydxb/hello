package assistant

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

// MaxContextNodes is the most selected nodes a question's context carries.
const MaxContextNodes = 50

var (
	// contextNode is a flow node id: "<kind>:<key>".
	contextNode = regexp.MustCompile(`^[a-zA-Z]+:[^\s<>"]{1,96}$`)
	// contextParty is a traced caller or number: an extension, a number or
	// trunk:<id>.
	contextParty = regexp.MustCompile(`^[A-Za-z0-9_.:+*#@-]{1,64}$`)
	contextDigit = regexp.MustCompile(`^[0-9*#]{0,32}$`)
)

// checkContext refuses a question context that is not what the console
// sends (spec flow-canvas S-19): a positive canvas id, at most
// MaxContextNodes node ids, and a trace with traceFlow's parameters. It is
// data for the model, so the bounds keep it small and plain.
func checkContext(mc *Context) error {
	if mc == nil {
		return nil
	}
	if mc.CanvasID != nil && *mc.CanvasID <= 0 {
		return errors.New("canvasId must be positive")
	}
	if len(mc.Nodes) > MaxContextNodes {
		return fmt.Errorf("at most %d nodes", MaxContextNodes)
	}
	for _, n := range mc.Nodes {
		if !contextNode.MatchString(n) {
			return fmt.Errorf("node %q is not a flow node id", n)
		}
	}
	if t := mc.Trace; t != nil {
		switch {
		case !contextParty.MatchString(t.From):
			return errors.New("trace.from must be an extension or trunk:<id>")
		case !contextParty.MatchString(t.Number):
			return errors.New("trace.number must be a dialled number")
		case t.CallerID != "" && !contextParty.MatchString(t.CallerID):
			return errors.New("trace.callerId must be a number")
		case !contextDigit.MatchString(t.Digits):
			return errors.New("trace.digits must be at most 32 of 0-9 * #")
		}
		if t.At != "" {
			if _, err := time.Parse(time.RFC3339, t.At); err != nil {
				return errors.New("trace.at must be RFC 3339")
			}
		}
	}
	return nil
}
