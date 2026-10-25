package lifecycle

import (
	"context"
	"log/slog"
	"time"

	"github.com/azrtydxb/hello/internal/cluster"
)

// Rollouts is the cluster's restart token and member list (cluster.Store).
type Rollouts interface {
	AcquireRollout(ctx context.Context, kind cluster.Kind, id string, ttl time.Duration) (holder string, err error)
	ReleaseRollout(ctx context.Context, kind cluster.Kind, id string) error
	Members(ctx context.Context) ([]cluster.Member, error)
}

// RolloutGate makes planned restarts of one node kind go one at a time, and
// only while a READY peer can take the node's calls. Kuvryn Sync applies
// hello-sip-1 and hello-sip-2 in the same second and both are Recreate (the
// new pod needs the old one's host ports), so without it both nodes stop
// together: the draining node has no READY survivor and its calls wait for
// the drain timeout while new calls find no node at all.
//
// Enter, run when SIGTERM arrives and before the drain starts, takes the
// kind's restart token and waits until another node of the kind is READY.
// The token is renewed for as long as the process lives and stays held after
// it exits, until the restarted node is READY and calls Release, so the
// peer's Enter cannot pass while this node's replacement is still starting.
// A node with no peer (a single-node install) passes at once; one that waits
// longer than Wait (a peer that never comes back, the store unreachable)
// proceeds anyway, because a shutdown must not hang.
type RolloutGate struct {
	Store Rollouts
	Kind  cluster.Kind
	ID    string
	// Wait bounds Enter. 0 disables the gate.
	Wait time.Duration
	// TTL is the token's lifetime without renewal: it must cover the
	// restart of the node (image pull, startup, readiness).
	TTL time.Duration
	// Poll is how often Enter looks again (1s).
	Poll time.Duration
	Log  *slog.Logger
}

func (g *RolloutGate) poll() time.Duration {
	if g.Poll > 0 {
		return g.Poll
	}
	return time.Second
}

// Enter blocks until this node may start draining and reports whether it
// holds the token (false: it gave up waiting, or the gate is off, and the
// drain goes ahead without one).
func (g *RolloutGate) Enter(ctx context.Context) bool {
	if g.Wait <= 0 || g.Store == nil {
		return false
	}
	deadline := time.NewTimer(g.Wait)
	defer deadline.Stop()
	waited := false
	for {
		held, ready, err := g.try(ctx)
		switch {
		case err != nil:
			// Without the store there is no membership either, and no
			// survivor this node could learn of.
			g.Log.Warn("rollout gate: store unavailable, draining now", "error", err)
			return false
		case held && ready:
			if waited {
				g.Log.Info("rollout gate: a peer is READY, draining")
			}
			g.keepAlive(ctx)
			return true
		}
		if !waited {
			waited = true
			g.Log.Info("rollout gate: waiting for the restart token and a READY peer", "held", held)
		}
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			g.Log.Warn("rollout gate: gave up waiting, draining without a READY peer", "wait", g.Wait.String(), "held", held)
			if held {
				g.keepAlive(ctx)
			}
			return held
		case <-time.After(g.poll()):
		}
	}
}

// try takes or renews the token and reports whether this node holds it and,
// when it does, whether it may go: another node of the kind is READY, or
// there is none at all.
func (g *RolloutGate) try(ctx context.Context) (held, ready bool, err error) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	holder, err := g.Store.AcquireRollout(cctx, g.Kind, g.ID, g.TTL)
	if err != nil || holder != g.ID {
		return false, false, err
	}
	members, err := g.Store.Members(cctx)
	if err != nil {
		return true, false, err
	}
	peers := 0
	for _, m := range members {
		if m.Kind != g.Kind || m.ID == g.ID {
			continue
		}
		peers++
		if m.State == cluster.Ready {
			return true, true, nil
		}
	}
	return true, peers == 0, nil
}

// keepAlive renews the token until ctx ends (the process exits), so a long
// drain does not lose it. After that the TTL runs from the last renewal.
func (g *RolloutGate) keepAlive(ctx context.Context) {
	go func() {
		t := time.NewTicker(g.TTL / 3)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
				if _, err := g.Store.AcquireRollout(cctx, g.Kind, g.ID, g.TTL); err != nil {
					g.Log.Debug("rollout gate: renewal failed", "error", err)
				}
				cancel()
			}
		}
	}()
}

// Release gives the token back; the node calls it once it is READY, so the
// next node of the kind may restart. A no-op when another node holds it.
func (g *RolloutGate) Release() {
	if g.Store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := g.Store.ReleaseRollout(ctx, g.Kind, g.ID); err != nil {
		g.Log.Debug("rollout gate: release failed", "error", err)
	}
}
