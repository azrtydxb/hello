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

// defaultRouter is the routing engine adapter, set by router_routing.go
// when the engine is built in; nil otherwise.
var defaultRouter Router

// DefaultRouter returns the built-in routing engine, or nil when this build
// does not include it. Without a Router, trunk and route changes and the
// route tester answer 503 rather than save unvalidated configuration.
func DefaultRouter() Router { return defaultRouter }

// TrunkLive reads trunk live state; *livestate.Store implements it.
type TrunkLive interface {
	TrunkStatus(ctx context.Context, id int64) (livestate.TrunkStatus, error)
}
