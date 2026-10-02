//go:build routing_engine

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
