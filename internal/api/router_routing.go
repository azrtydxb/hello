//go:build routing_engine

package api

import "github.com/azrtydxb/hello/internal/routing"

// engineRouter adapts routing.Compile to Router. The build tag goes away
// when the routing engine (Phase 2 Task 2) is merged.
type engineRouter struct{}

func (engineRouter) Compile(c routing.Config) (RouteTable, []routing.FieldError) {
	t, errs := routing.Compile(c)
	if t == nil {
		return nil, errs // a nil *Table must not become a non-nil RouteTable
	}
	return t, errs
}

func init() { defaultRouter = engineRouter{} }
