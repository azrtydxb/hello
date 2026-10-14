package snapshot

import "github.com/azrtydxb/hello/internal/routing"

// compileRouting compiles the configuration with the routing engine; any
// field error means no Table, and the watcher keeps the last good one.
func compileRouting(cfg routing.Config) (Router, []routing.FieldError) {
	t, errs := routing.Compile(cfg)
	if len(errs) > 0 || t == nil {
		return nil, errs
	}
	return t, nil
}

// checkDialing judges a trunk's stored internal dialing policy (trunk plan
// Task 2). A policy that does not compile — reachable only by writing the
// column directly, since the API validates at save time — makes the trunk
// misconfigured, like a password that does not open: routing skips it and
// its routes never take a call. The trunk's policy is dropped to "off" so
// the rest of the configuration still compiles, and the reason is returned.
func checkDialing(t *routing.Trunk) string {
	if t.InternalDialing.Mode != "patterns" {
		return ""
	}
	if errs := routing.CompileDialing(t.InternalDialing); len(errs) > 0 {
		t.InternalDialing = routing.Dialing{Mode: "off"}
		return "internal dialing policy does not compile"
	}
	return ""
}
