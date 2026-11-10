package backup

import (
	"context"
	"sync/atomic"
)

// SharedFlag carries the maintenance flag to the other replicas (Valkey).
type SharedFlag interface {
	Set(ctx context.Context, on bool) error
	Get(ctx context.Context) (bool, error)
}

// Maintenance is the restore's write gate: while it is on, the API answers
// writes with 503 maintenance. SIP keeps running on its snapshot.
type Maintenance struct {
	local  atomic.Bool
	Shared SharedFlag
}

// Active reports whether this node, or (when a shared flag is configured and
// reachable) the cluster, is in maintenance.
func (m *Maintenance) Active(ctx context.Context) bool {
	if m == nil {
		return false
	}
	if m.local.Load() {
		return true
	}
	if m.Shared != nil {
		on, err := m.Shared.Get(ctx)
		return err == nil && on
	}
	return false
}

// Enter turns maintenance on; the local flag always takes effect, the shared
// one is best effort.
func (m *Maintenance) Enter(ctx context.Context) {
	m.local.Store(true)
	if m.Shared != nil {
		_ = m.Shared.Set(ctx, true)
	}
}

// Leave turns maintenance off.
func (m *Maintenance) Leave(ctx context.Context) {
	m.local.Store(false)
	if m.Shared != nil {
		_ = m.Shared.Set(ctx, false)
	}
}
