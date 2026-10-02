package api

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/prometheus/client_golang/prometheus"
)

// This file is hello-control's minimal membership publisher (plan Task 3).
// It is isolated so it can be replaced by internal/lifecycle once that
// lands; only Run's caller in cmd/hello-control needs to change.

// MemberStore publishes and withdraws membership; *cluster.Store and
// *LazyValkey implement it.
type MemberStore interface {
	Publish(ctx context.Context, m cluster.Member) error
	Leave(ctx context.Context, id string) error
	Members(ctx context.Context) ([]cluster.Member, error)
}

// ClusterMetrics are the cluster-wide gauges hello-control exports (spec
// S-11): members by kind and state, and each live member's configuration
// revision lag, plus this node's own lifecycle state.
type ClusterMetrics struct {
	Members     *prometheus.GaugeVec
	RevisionLag *prometheus.GaugeVec
	NodeState   *prometheus.GaugeVec
}

// NewClusterMetrics registers the gauges on reg.
func NewClusterMetrics(reg prometheus.Registerer) *ClusterMetrics {
	m := &ClusterMetrics{
		Members: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "hello_cluster_members", Help: "Cluster members by kind and lifecycle state.",
		}, []string{"kind", "state"}),
		RevisionLag: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "hello_config_revision_lag", Help: "Current configuration revision minus the revision a live member reports.",
		}, []string{"node"}),
		NodeState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "hello_node_state", Help: "This node's lifecycle state; 1 for the current state, 0 otherwise.",
		}, []string{"state"}),
	}
	reg.MustRegister(m.Members, m.RevisionLag, m.NodeState)
	return m
}

var publishedStates = []cluster.State{cluster.Joining, cluster.Ready, cluster.Draining, cluster.Unhealthy}

func (m *ClusterMetrics) setState(st cluster.State) {
	for _, s := range publishedStates {
		v := 0.0
		if s == st {
			v = 1
		}
		m.NodeState.WithLabelValues(string(s)).Set(v)
	}
}

// update replaces the cluster gauges from one membership poll. Without a
// current revision (PostgreSQL down) the lags keep their last values rather
// than report a false zero. OFFLINE members have no lag.
func (m *ClusterMetrics) update(ms []cluster.Member, rev *int64) {
	m.Members.Reset()
	for _, mem := range ms {
		m.Members.WithLabelValues(string(mem.Kind), string(mem.State)).Inc()
	}
	if rev == nil {
		return
	}
	m.RevisionLag.Reset()
	for _, mem := range ms {
		if mem.State != cluster.Offline {
			m.RevisionLag.WithLabelValues(mem.ID).Set(float64(*rev - mem.ConfigRevision))
		}
	}
}

// Publisher publishes hello-control's membership every Heartbeat and keeps
// the cluster metrics current. Its state follows the required readiness
// checks: JOINING until every one has passed once, then READY, or
// UNHEALTHY while one fails; DRAINING once ctx ends (shutdown).
type Publisher struct {
	ID        string
	HTTPAddr  string
	Version   string
	StartedAt time.Time
	Heartbeat time.Duration
	// Required are the /readyz required checks, by name.
	Required map[string]func(context.Context) error
	// Revision reads the configuration revision.
	Revision func(context.Context) (int64, error)
	Store    MemberStore
	Metrics  *ClusterMetrics
	Log      *slog.Logger
	Now      func() time.Time

	joined    bool
	state     cluster.State
	reason    string
	revision  int64
	storeDown bool
}

// evaluate returns the state the required checks imply. Check errors are
// logged, not published: the reason names the dependency only.
func (p *Publisher) evaluate(ctx context.Context) (cluster.State, string) {
	names := make([]string, 0, len(p.Required))
	for n := range p.Required {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		if err := p.Required[n](ctx); err != nil {
			if !p.joined {
				return cluster.Joining, "waiting for " + n
			}
			p.Log.Debug("membership: required check failed", "check", n, "error", err)
			return cluster.Unhealthy, n + " unavailable"
		}
	}
	p.joined = true
	return cluster.Ready, ""
}

// tick evaluates, publishes and refreshes the metrics once.
func (p *Publisher) tick(ctx context.Context, st cluster.State, reason string) {
	if st != p.state || reason != p.reason {
		p.Log.Info("node state", "state", st, "reason", reason, "previous", p.state)
		p.state, p.reason = st, reason
	}
	if p.Metrics != nil {
		p.Metrics.setState(st)
	}
	var rev *int64
	if r, err := p.Revision(ctx); err == nil {
		p.revision, rev = r, &r
	}
	now := p.Now().UTC()
	m := cluster.Member{
		ID: p.ID, Kind: cluster.KindControl, State: st, Reason: reason, HTTPAddr: p.HTTPAddr,
		Version: p.Version, ConfigRevision: p.revision, StartedAt: p.StartedAt, Heartbeat: now,
	}
	err := p.Store.Publish(ctx, m)
	if (err != nil) != p.storeDown {
		p.storeDown = err != nil
		if err != nil {
			p.Log.Warn("membership: cannot publish; retrying on each heartbeat", "error", err)
		} else {
			p.Log.Info("membership: publishing")
		}
	}
	if err == nil && p.Metrics != nil {
		if ms, err := p.Store.Members(ctx); err == nil {
			p.Metrics.update(ms, rev)
		}
	}
}

// Run publishes until ctx ends, then publishes DRAINING once. Call Leave
// after the HTTP server has stopped.
func (p *Publisher) Run(ctx context.Context) {
	if p.Now == nil {
		p.Now = time.Now
	}
	t := time.NewTicker(p.Heartbeat)
	defer t.Stop()
	for {
		cctx, cancel := context.WithTimeout(ctx, p.Heartbeat)
		st, reason := p.evaluate(cctx)
		p.tick(cctx, st, reason)
		cancel()
		select {
		case <-ctx.Done():
			dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			p.tick(dctx, cluster.Draining, "shutting down")
			cancel()
			return
		case <-t.C:
		}
	}
}

// Leave withdraws the live record so the node is listed OFFLINE at once.
func (p *Publisher) Leave(ctx context.Context) {
	if err := p.Store.Leave(ctx, p.ID); err != nil {
		p.Log.Warn("membership: leave", "error", err)
	}
}
