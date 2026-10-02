package api

import (
	"context"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/routing"
)

// Router compiles a routing configuration; it is the routing engine as the
// API sees it (contract 2's routing.Compile).
type Router interface {
	Compile(routing.Config) (RouteTable, []routing.FieldError)
}

// RouteTable decides calls; *routing.Table implements it.
type RouteTable interface {
	Decide(routing.Call, routing.TrunkUsability) routing.Decision
}

// TrunkLive reads trunk live state; *livestate.Store implements it.
type TrunkLive interface {
	TrunkStatus(ctx context.Context, id int64) (livestate.TrunkStatus, error)
}
