package api

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/lifecycle"
	"github.com/azrtydxb/hello/internal/ops"
	"github.com/azrtydxb/hello/internal/telemetry"
	"github.com/prometheus/client_golang/prometheus"
)

// ClusterMetrics are the cluster-wide gauges hello-control exports (spec
// S-11): members by kind and state, and each live member's configuration
// revision lag. (hello_node_state comes from internal/lifecycle.)
type ClusterMetrics struct {
	Members     *prometheus.GaugeVec
	RevisionLag *prometheus.GaugeVec

	mu       sync.Mutex
	lastMem  map[[2]string]bool
	lastLags map[string]bool
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
	}
	reg.MustRegister(m.Members, m.RevisionLag)
	return m
}

// update sets the gauges from one membership poll, then deletes the series
// that disappeared, so a scrape never sees a transient zero. Without a
// current revision (PostgreSQL down) the lags keep their last values rather
// than report a false zero. OFFLINE members have no lag.
func (m *ClusterMetrics) update(ms []cluster.Member, rev *int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	counts := map[[2]string]int{}
	for _, mem := range ms {
		counts[[2]string{string(mem.Kind), string(mem.State)}]++
	}
	for k, n := range counts {
		m.Members.WithLabelValues(k[0], k[1]).Set(float64(n))
	}
	for k := range m.lastMem {
		if counts[k] == 0 {
			m.Members.DeleteLabelValues(k[0], k[1])
		}
	}
	m.lastMem = map[[2]string]bool{}
	for k := range counts {
		m.lastMem[k] = true
	}
	if rev == nil {
		return
	}
	lags := map[string]bool{}
	for _, mem := range ms {
		if mem.State != cluster.Offline {
			m.RevisionLag.WithLabelValues(mem.ID).Set(float64(*rev - mem.ConfigRevision))
			lags[mem.ID] = true
		}
	}
	for id := range m.lastLags {
		if !lags[id] {
			m.RevisionLag.DeleteLabelValues(id)
		}
	}
	m.lastLags = lags
}

// ClusterPoller reads the configuration revision and the membership on an
// interval, in the background: it keeps the cluster metrics current
// without any API call, and caches the revision this node reports in its
// own membership record.
type ClusterPoller struct {
	Every    time.Duration
	Revision func(context.Context) (int64, error)
	Members  func(context.Context) ([]cluster.Member, error)
	Metrics  *ClusterMetrics

	revision atomic.Int64
}

// LastRevision is the revision read by the last successful poll.
func (p *ClusterPoller) LastRevision() int64 { return p.revision.Load() }

// Poll runs one poll. The two reads run concurrently, so a slow
// configuration-revision read (PostgreSQL) cannot starve the membership
// read and freeze the member gauges.
func (p *ClusterPoller) Poll(ctx context.Context) {
	var (
		wg   sync.WaitGroup
		rev  *int64
		ms   []cluster.Member
		merr error
	)
	wg.Go(func() {
		r, err := p.Revision(ctx)
		if err != nil {
			return // rev stays nil: the lag gauges keep their last values
		}
		p.revision.Store(r)
		rev = &r
	})
	wg.Go(func() {
		ms, merr = p.Members(ctx)
	})
	wg.Wait()
	if merr == nil && p.Metrics != nil {
		p.Metrics.update(ms, rev)
	}
}

// Run polls until ctx ends.
func (p *ClusterPoller) Run(ctx context.Context) {
	t := time.NewTicker(p.Every)
	defer t.Stop()
	for {
		pctx, cancel := context.WithTimeout(ctx, p.Every)
		p.Poll(pctx)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// noCalls is hello-control's lifecycle.Drainer: it has no calls, so a
// drain completes at once and the HTTP server's DrainDelay is the drain.
type noCalls struct{}

func (noCalls) ActiveCalls() int { return 0 }
func (noCalls) HangupAll(string) {}

// NodeOptions configure hello-control's node lifecycle.
type NodeOptions struct {
	ID, HTTPAddr, Version string
	// Postgres is the one required check; Valkey is optional for
	// management and never a lifecycle check.
	Postgres lifecycle.Check
	// Valkey publishes membership and polls drain requests.
	Valkey *LazyValkey
	// Revision reads the configuration revision.
	Revision func(context.Context) (int64, error)
	// App is the management API.
	App     http.Handler
	Metrics *telemetry.Metrics
	Log     *slog.Logger
	// DrainDelay is how long /readyz fails before the HTTP server stops;
	// ShutdownTimeout bounds in-flight requests after that.
	DrainDelay, ShutdownTimeout time.Duration
	// Heartbeat (5s) and CheckEvery (1s) are the lifecycle intervals;
	// Drainer defaults to none (hello-control has no calls). Tests shorten
	// and replace them.
	Heartbeat, CheckEvery time.Duration
	Drainer               lifecycle.Drainer
}

// Node is hello-control's lifecycle: JOINING until PostgreSQL first
// answers, READY, UNHEALTHY while it fails, DRAINING on SIGTERM or a drain
// request (then /readyz is 503 and it exits), OFFLINE after it leaves.
type Node struct {
	Machine *lifecycle.Machine
	Poller  *ClusterPoller
	ops     *ops.Server
	o       NodeOptions
}

// NewNode wires the lifecycle, the cluster poller and the ops server.
func NewNode(o NodeOptions) *Node {
	if o.Heartbeat <= 0 {
		o.Heartbeat = cluster.TTL / 3
	}
	if o.Drainer == nil {
		o.Drainer = noCalls{}
	}
	n := &Node{o: o}
	n.Poller = &ClusterPoller{
		Every: o.Heartbeat, Revision: o.Revision, Members: o.Valkey.Members,
		Metrics: NewClusterMetrics(o.Metrics.Registry),
	}
	n.Machine = lifecycle.New(lifecycle.Options{
		Member:    cluster.Member{ID: o.ID, Kind: cluster.KindControl, HTTPAddr: o.HTTPAddr, Version: o.Version},
		Checks:    map[string]lifecycle.Check{"postgres": o.Postgres},
		Publisher: o.Valkey,
		Load:      func() lifecycle.Load { return lifecycle.Load{ConfigRevision: n.Poller.LastRevision()} },
		Primary:   o.Valkey.Primary,
		Heartbeat: o.Heartbeat, CheckEvery: o.CheckEvery,
		Metrics: lifecycle.NewMetrics(o.Metrics.Registry),
		Log:     o.Log.With("component", "lifecycle"),
	})
	n.ops = &ops.Server{
		Lifecycle: n.Machine, // /readyz is 200 only in READY
		// Valkey backs views, not management: its loss degrades, not fails.
		Optional:        map[string]ops.Check{"valkey": o.Valkey.Ping},
		App:             o.App,
		Metrics:         o.Metrics,
		Log:             o.Log,
		DrainDelay:      o.DrainDelay,
		ShutdownTimeout: o.ShutdownTimeout,
	}
	return n
}

// Run serves on ln until the node has drained, after sigterm closes or an
// operator's drain request; then /readyz fails for DrainDelay, in-flight
// requests finish, and the node leaves the cluster.
func (n *Node) Run(sigterm <-chan struct{}, ln net.Listener) error {
	runCtx, shutdown := context.WithCancel(context.Background())
	defer shutdown()
	bg, stopBG := context.WithCancel(context.Background())
	pollDone, lcDone := make(chan struct{}), make(chan struct{})
	go func() { defer close(pollDone); n.Poller.Run(bg) }()
	go func() { defer close(lcDone); n.Machine.Run(bg) }()
	go func() {
		select {
		case <-sigterm:
			n.Machine.Drain("SIGTERM")
		case <-runCtx.Done():
		}
	}()
	go func() {
		if n.Machine.AwaitDrain(runCtx, n.o.Drainer, time.Minute) {
			shutdown()
		}
	}()
	err := n.ops.Serve(runCtx, ln)
	stopBG() // the lifecycle leaves: listed OFFLINE from its tombstone
	<-lcDone
	<-pollDone
	return err
}
