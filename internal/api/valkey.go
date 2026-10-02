package api

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/valkey-io/valkey-go"
)

// ErrValkeyConnecting is returned by Valkey-backed views before the client
// exists: in Sentinel mode hello-control starts serving management before
// any sentinel has answered.
var ErrValkeyConnecting = errors.New("valkey: still connecting")

// ValkeyHealth is the Valkey row of the cluster view.
type ValkeyHealth struct {
	Up      bool   `json:"up"`
	Mode    string `json:"mode"`              // "single" | "sentinel"
	Primary string `json:"primary,omitempty"` // Sentinel's current primary
	Error   string `json:"error,omitempty"`
}

// LazyValkey holds hello-control's Valkey client once it exists and serves
// the live views, trunk status, membership and Valkey health through it.
// Until Set, every call fails with ErrValkeyConnecting, which the API
// answers with 503: Valkey is optional for management.
type LazyValkey struct {
	sentinel bool
	p        atomic.Pointer[lazyClient]
}

type lazyClient struct {
	c       valkey.Client
	live    *livestate.Store
	members *cluster.Store
}

// NewLazyValkey returns an unconnected holder; sentinel selects the mode
// it reports.
func NewLazyValkey(sentinel bool) *LazyValkey { return &LazyValkey{sentinel: sentinel} }

// Set installs the connected client.
func (l *LazyValkey) Set(c valkey.Client) {
	l.p.Store(&lazyClient{c: c, live: livestate.New(c), members: cluster.New(c)})
}

// Close closes the client, if any.
func (l *LazyValkey) Close() {
	if c := l.p.Load(); c != nil {
		c.c.Close()
	}
}

func (l *LazyValkey) get() (*lazyClient, error) {
	if c := l.p.Load(); c != nil {
		return c, nil
	}
	return nil, ErrValkeyConnecting
}

// Ping is the readiness check for Valkey.
func (l *LazyValkey) Ping(ctx context.Context) error {
	c, err := l.get()
	if err != nil {
		return err
	}
	return c.c.Do(ctx, c.c.B().Ping().Build()).Error()
}

func (l *LazyValkey) mode() string {
	if l.sentinel {
		return "sentinel"
	}
	return "single"
}

// ValkeyHealth reports reachability, the mode and, with Sentinel, the
// primary the client currently uses.
func (l *LazyValkey) ValkeyHealth(ctx context.Context) ValkeyHealth {
	h := ValkeyHealth{Mode: l.mode()}
	c, err := l.get()
	if err == nil {
		err = c.c.Do(ctx, c.c.B().Ping().Build()).Error()
	}
	if err != nil {
		h.Error = err.Error()
		return h
	}
	h.Up = true
	h.Primary = primary(c.c.Mode(), c.c.Nodes())
	return h
}

// primary is the Sentinel primary's address: a Sentinel client that sends
// only to the primary lists exactly that node.
func primary(mode valkey.ClientMode, nodes map[string]valkey.Client) string {
	if mode != valkey.ClientModeSentinel || len(nodes) != 1 {
		return ""
	}
	for addr := range nodes {
		return addr
	}
	return ""
}

// AllBindings implements Live.
func (l *LazyValkey) AllBindings(ctx context.Context) ([]livestate.Binding, error) {
	c, err := l.get()
	if err != nil {
		return nil, err
	}
	return c.live.AllBindings(ctx)
}

// Calls implements Live.
func (l *LazyValkey) Calls(ctx context.Context) ([]livestate.Call, error) {
	c, err := l.get()
	if err != nil {
		return nil, err
	}
	return c.live.Calls(ctx)
}

// TrunkStatus implements TrunkLive.
func (l *LazyValkey) TrunkStatus(ctx context.Context, id int64) (livestate.TrunkStatus, error) {
	c, err := l.get()
	if err != nil {
		return livestate.TrunkStatus{}, err
	}
	return c.live.TrunkStatus(ctx, id)
}

// Members implements ClusterStore.
func (l *LazyValkey) Members(ctx context.Context) ([]cluster.Member, error) {
	c, err := l.get()
	if err != nil {
		return nil, err
	}
	return c.members.Members(ctx)
}

// RequestDrain implements ClusterStore.
func (l *LazyValkey) RequestDrain(ctx context.Context, id string) error {
	c, err := l.get()
	if err != nil {
		return err
	}
	return c.members.RequestDrain(ctx, id)
}

// CancelDrain implements ClusterStore.
func (l *LazyValkey) CancelDrain(ctx context.Context, id string) error {
	c, err := l.get()
	if err != nil {
		return err
	}
	return c.members.CancelDrain(ctx, id)
}

// Publish implements lifecycle.Publisher.
func (l *LazyValkey) Publish(ctx context.Context, m cluster.Member) error {
	c, err := l.get()
	if err != nil {
		return err
	}
	return c.members.Publish(ctx, m)
}

// Leave implements lifecycle.Publisher.
func (l *LazyValkey) Leave(ctx context.Context, id string) error {
	c, err := l.get()
	if err != nil {
		return err
	}
	return c.members.Leave(ctx, id)
}

// DrainRequested implements lifecycle.Publisher.
func (l *LazyValkey) DrainRequested(ctx context.Context, id string) (bool, error) {
	c, err := l.get()
	if err != nil {
		return false, err
	}
	return c.members.DrainRequested(ctx, id)
}

// Primary is the Sentinel primary's address ("" in single mode or before
// connecting), for the lifecycle's failover count.
func (l *LazyValkey) Primary() string {
	c, err := l.get()
	if err != nil {
		return ""
	}
	return primary(c.c.Mode(), c.c.Nodes())
}
