//go:build !routing_engine

package snapshot

import "github.com/azrtydxb/hello/internal/routing"

// STUB — routing engine not merged yet.
//
// The routing engine (routing.Compile, Task 2) is built on another branch.
// Until it is merged, a build without the routing_engine tag compiles every
// configuration to stubRouter, which routes only to internal extensions and
// rejects everything else with 404. At merge the lead drops the build tag
// from compile_engine.go and deletes this file.
func compileRouting(cfg routing.Config) (Router, []routing.FieldError) {
	return stubRouter{cfg: cfg}, nil
}
