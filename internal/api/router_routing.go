package api

import "github.com/azrtydxb/hello/internal/routing"

// engineRouter adapts routing.Compile to Router; it is the Router Handler
// uses unless Config names another (tests use fakes).
type engineRouter struct{}

func (engineRouter) Compile(c routing.Config) (RouteTable, []routing.FieldError) {
	t, errs := routing.Compile(c)
	if t == nil {
		return nil, errs // a nil *Table must not become a non-nil RouteTable
	}
	return t, errs
}
