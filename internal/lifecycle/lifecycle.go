// Package lifecycle is a Hello node's lifecycle state machine (spec
// .procoder/specs/ha.md S-1, S-2, S-3; plan contract 4). It is fed by the
// node's required readiness checks, a drain signal (SIGTERM or an
// operator's drain request) and shutdown, and it:
//
//   - holds the state (JOINING, READY, UNHEALTHY, DRAINING) and why;
//   - logs each change once, with its reason;
//   - publishes the node's cluster.Member on every heartbeat and change;
//   - answers /readyz through internal/ops (200 only in READY);
//   - exports hello_node_state{state}, hello_drain_active_calls and
//     hello_valkey_failovers_total.
//
// hello-sip and hello-control both run one.
package lifecycle

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/prometheus/client_golang/prometheus"
)

// Check is a required dependency check; a nil error means usable.
type Check func(ctx context.Context) error

// Publisher is where membership goes; *cluster.Store satisfies it.
type Publisher interface {
	Publish(ctx context.Context, m cluster.Member) error
	Leave(ctx context.Context, id string) error
	DrainRequested(ctx context.Context, id string) (bool, error)
	CancelDrain(ctx context.Context, id string) error
}

// Load is the node's current load, for its membership record.
type Load struct {
	ActiveCalls int
	// ConnectedCalls is the answered subset of ActiveCalls; nil when the
	// node does not report it.
	ConnectedCalls *int
	Registrations  int
	ConfigRevision int64
}

// Options configure a Machine.
type Options struct {
	// Member is the record's fixed part: ID, Kind, SIPAddr, HTTPAddr,
	// Transports, Version (StartedAt defaults to now).
	Member cluster.Member
	// Checks are required: until all pass the node is JOINING; when one
	// fails later it is UNHEALTHY.
	Checks map[string]Check
	// Publisher receives membership; nil disables it (and drain requests).
	Publisher Publisher
	// Load reports the node's load; nil reports zero.
	Load func() Load
	// Primary returns the current Valkey primary's address; a change
	// between heartbeats counts as a failover. nil disables counting.
	Primary func() string
	// Heartbeat is the publish and drain-request poll interval (5s);
	// CheckEvery the readiness evaluation interval (1s).
	Heartbeat  time.Duration
	CheckEvery time.Duration
	// CheckTimeout bounds one evaluation of all checks (2s).
	CheckTimeout time.Duration
	Metrics      *Metrics
	Log          *slog.Logger
	// OnChange, when set, is called after every state change.
	OnChange func(from, to cluster.State, reason string)
	// Now is the clock (time.Now).
	Now func() time.Time
}

// Metrics are the lifecycle's Prometheus series.
type Metrics struct {
	NodeState       *prometheus.GaugeVec // state, one-hot
	DrainCalls      prometheus.Gauge
	ValkeyFailovers prometheus.Counter
}

// NewMetrics registers the lifecycle series on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		NodeState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "hello_node_state", Help: "The node's lifecycle state: 1 for the current state, 0 for the others.",
		}, []string{"state"}),
		DrainCalls: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hello_drain_active_calls", Help: "Calls still active on this node while it drains; 0 when not draining.",
		}),
		ValkeyFailovers: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "hello_valkey_failovers_total", Help: "Valkey primary changes seen by this node (Sentinel failovers).",
		}),
	}
	reg.MustRegister(m.NodeState, m.DrainCalls, m.ValkeyFailovers)
	return m
}

var states = []cluster.State{cluster.Joining, cluster.Ready, cluster.Unhealthy, cluster.Draining}

// Machine is one node's lifecycle.
type Machine struct {
	o Options

	// evalMu serialises evaluations, effects included (log, metrics,
	// OnChange, publish), so they apply in the order the states were set.
	evalMu sync.Mutex

	mu        sync.Mutex
	state     cluster.State
	reason    string
	everReady bool
	sigDrain  string // SIGTERM (or another local drain): never cancelled
	reqDrain  bool   // an operator's drain request in Valkey
	exiting   bool   // shutdown begun: a drain can no longer be cancelled
	primary   string
	changes   chan struct{}
}

// New returns a Machine in JOINING.
func New(o Options) *Machine {
	if o.Heartbeat <= 0 {
		o.Heartbeat = 5 * time.Second
	}
	if o.CheckEvery <= 0 {
		o.CheckEvery = time.Second
	}
	if o.CheckTimeout <= 0 {
		o.CheckTimeout = 2 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Member.StartedAt.IsZero() {
		o.Member.StartedAt = o.Now().UTC()
	}
	m := &Machine{o: o, state: cluster.Joining, reason: "starting", changes: make(chan struct{}, 1)}
	m.setMetrics(cluster.Joining)
	return m
}

// State returns the current state and its reason.
func (m *Machine) State() (cluster.State, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state, m.reason
}

// Readiness is the internal/ops readiness hook: ready only in READY.
func (m *Machine) Readiness() (ready bool, state, reason string) {
	s, r := m.State()
	return s == cluster.Ready, string(s), r
}

// Drain makes the node DRAINING for a local reason (SIGTERM). Unlike an
// operator's request, it is never cancelled.
func (m *Machine) Drain(reason string) {
	m.mu.Lock()
	m.sigDrain = reason
	m.mu.Unlock()
	m.evaluate(context.Background())
}

// Changed is signalled (coalesced) after each state change.
func (m *Machine) Changed() <-chan struct{} { return m.changes }

// Run evaluates readiness every CheckEvery, and every Heartbeat polls the
// drain request, counts Valkey failovers and publishes membership, until
// ctx ends; then it leaves the cluster (the tombstone lists it OFFLINE).
func (m *Machine) Run(ctx context.Context) {
	checks := time.NewTicker(m.o.CheckEvery)
	defer checks.Stop()
	beat := time.NewTicker(m.o.Heartbeat)
	defer beat.Stop()
	m.Heartbeat(ctx)
	for {
		select {
		case <-ctx.Done():
			m.leave()
			return
		case <-checks.C:
			m.evaluate(ctx)
		case <-beat.C:
			m.Heartbeat(ctx)
		}
	}
}

// Heartbeat runs one heartbeat: drain-request poll, failover count,
// readiness evaluation and publish. Run calls it; tests call it directly.
func (m *Machine) Heartbeat(ctx context.Context) {
	m.pollDrain(ctx)
	m.countFailover()
	m.evalMu.Lock()
	defer m.evalMu.Unlock()
	if !m.evaluateLocked(ctx) {
		m.publish(ctx) // a change already published
	}
}

func (m *Machine) pollDrain(ctx context.Context) {
	if m.o.Publisher == nil {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, m.o.CheckTimeout)
	req, err := m.o.Publisher.DrainRequested(pctx, m.o.Member.ID)
	cancel()
	if err != nil {
		return // keep the last known request; the Valkey check reports the outage
	}
	m.mu.Lock()
	if req || !m.exiting {
		m.reqDrain = req
	}
	m.mu.Unlock()
}

func (m *Machine) countFailover() {
	if m.o.Primary == nil {
		return
	}
	p := m.o.Primary()
	m.mu.Lock()
	changed := p != "" && m.primary != "" && p != m.primary
	if p != "" {
		m.primary = p
	}
	m.mu.Unlock()
	if changed {
		m.o.Log.Warn("valkey primary changed", "primary", p)
		if m.o.Metrics != nil {
			m.o.Metrics.ValkeyFailovers.Inc()
		}
	}
}

// evaluate derives the state from the drain signals and the checks. It
// reports whether the state changed (and was published).
func (m *Machine) evaluate(ctx context.Context) bool {
	m.evalMu.Lock()
	defer m.evalMu.Unlock()
	return m.evaluateLocked(ctx)
}

// evaluateLocked is evaluate with evalMu held.
func (m *Machine) evaluateLocked(ctx context.Context) bool {
	failing := m.firstFailing(ctx)
	m.mu.Lock()
	var next cluster.State
	var reason string
	switch {
	case m.sigDrain != "":
		next, reason = cluster.Draining, m.sigDrain
	case m.reqDrain:
		next, reason = cluster.Draining, "drain requested"
	case failing != "" && !m.everReady:
		next, reason = cluster.Joining, failing
	case failing != "":
		next, reason = cluster.Unhealthy, failing
	default:
		next, reason = cluster.Ready, ""
		m.everReady = true
	}
	from, fromReason := m.state, m.reason
	m.state, m.reason = next, reason
	m.mu.Unlock()
	m.setDrainCalls()
	if next == from && reason == fromReason {
		return false
	}
	if next != from {
		m.o.Log.Info("node state changed", "from", string(from), "to", string(next), "reason", reason)
		m.setMetrics(next)
		if m.o.OnChange != nil {
			m.o.OnChange(from, next, reason)
		}
		select {
		case m.changes <- struct{}{}:
		default:
		}
	}
	m.publish(ctx)
	return true
}

// firstFailing runs every check and names the first failure (by name).
func (m *Machine) firstFailing(ctx context.Context) string {
	cctx, cancel := context.WithTimeout(ctx, m.o.CheckTimeout)
	defer cancel()
	for _, name := range slices.Sorted(maps.Keys(m.o.Checks)) {
		if err := m.o.Checks[name](cctx); err != nil {
			return name + ": " + err.Error()
		}
	}
	return ""
}

func (m *Machine) load() Load {
	if m.o.Load == nil {
		return Load{}
	}
	return m.o.Load()
}

func (m *Machine) member() cluster.Member {
	mem := m.o.Member
	mem.State, mem.Reason = m.State()
	l := m.load()
	mem.ActiveCalls, mem.Registrations, mem.ConfigRevision = l.ActiveCalls, l.Registrations, l.ConfigRevision
	mem.ConnectedCalls = l.ConnectedCalls
	mem.Heartbeat = m.o.Now().UTC()
	return mem
}

func (m *Machine) publish(ctx context.Context) {
	if m.o.Publisher == nil {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, m.o.CheckTimeout)
	defer cancel()
	if err := m.o.Publisher.Publish(pctx, m.member()); err != nil {
		m.o.Log.Debug("membership publish failed", "error", err)
	}
}

func (m *Machine) leave() {
	if m.o.Publisher == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.o.CheckTimeout)
	defer cancel()
	if err := m.o.Publisher.Leave(ctx, m.o.Member.ID); err != nil {
		m.o.Log.Warn("could not leave the cluster", "error", err)
	}
}

func (m *Machine) setMetrics(s cluster.State) {
	if m.o.Metrics == nil {
		return
	}
	for _, st := range states {
		v := 0.0
		if st == s {
			v = 1
		}
		m.o.Metrics.NodeState.WithLabelValues(string(st)).Set(v)
	}
}

func (m *Machine) setDrainCalls() {
	if m.o.Metrics == nil {
		return
	}
	s, _ := m.State()
	n := 0
	if s == cluster.Draining {
		n = m.load().ActiveCalls
	}
	m.o.Metrics.DrainCalls.Set(float64(n))
}

// Drainer is the work a node drains: hello-sip's calls (hello-control has
// none and drains at once).
type Drainer interface {
	ActiveCalls() int
	// HangupAll ends what is left at the drain timeout.
	HangupAll(reason string)
}

// DrainTimeoutReason is the CDR reason for calls ended at the timeout.
const DrainTimeoutReason = "drain timeout"

// AwaitDrain blocks until the node has drained and may exit, then reports
// true; false means ctx ended first. Once DRAINING, the node is drained
// when d's active calls reach 0, or at timeout, when the rest are ended
// (HangupAll with DrainTimeoutReason) and given up to graceWait to go. A
// drain cancelled meanwhile returns to waiting for the next one. Before
// returning true the machine is marked exiting, and a drain request that
// stands is withdrawn so the restarted node is not drained again.
func (m *Machine) AwaitDrain(ctx context.Context, d Drainer, timeout time.Duration) bool {
	poll := m.o.CheckEvery
	for {
		for st, _ := m.State(); st != cluster.Draining; st, _ = m.State() {
			select {
			case <-ctx.Done():
				return false
			case <-m.Changed():
			case <-time.After(poll):
			}
		}
		_, reason := m.State()
		m.o.Log.Info("draining", "reason", reason, "active_calls", d.ActiveCalls(), "timeout", timeout.String())
		if !m.waitCalls(ctx, d, timeout, poll) {
			if ctx.Err() != nil {
				return false
			}
			continue // drain cancelled
		}
		m.mu.Lock()
		if m.state != cluster.Draining { // cancelled at the last moment
			m.mu.Unlock()
			continue
		}
		m.exiting = true
		// Withdraw an operator's request even when SIGTERM came too, or the
		// restarted node (same ID) would drain and exit again.
		requested := m.reqDrain
		m.mu.Unlock()
		if requested {
			m.withdrawDrain()
		}
		m.o.Log.Info("drained: exiting")
		return true
	}
}

// graceWait bounds the wait for calls ended at the drain timeout.
const graceWait = 30 * time.Second

// waitCalls waits while draining for d's calls to reach 0, ending them at
// timeout; false means the drain was cancelled (or ctx ended).
func (m *Machine) waitCalls(ctx context.Context, d Drainer, timeout, poll time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for d.ActiveCalls() > 0 {
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			// Exiting before the calls are ended: a drain cancelled from
			// here on must not return to READY a node whose calls are gone.
			m.mu.Lock()
			if m.state != cluster.Draining {
				m.mu.Unlock()
				return false
			}
			m.exiting = true
			m.mu.Unlock()
			m.o.Log.Warn("drain timeout: ending the remaining calls", "active_calls", d.ActiveCalls())
			d.HangupAll(DrainTimeoutReason)
			grace := time.Now().Add(graceWait)
			for d.ActiveCalls() > 0 && time.Now().Before(grace) {
				select {
				case <-ctx.Done():
					return false
				case <-time.After(poll):
				}
			}
			return true
		case <-time.After(poll):
			if st, _ := m.State(); st != cluster.Draining {
				return false
			}
		}
	}
	return true
}

func (m *Machine) withdrawDrain() {
	if m.o.Publisher == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.o.CheckTimeout)
	defer cancel()
	if err := m.o.Publisher.CancelDrain(ctx, m.o.Member.ID); err != nil {
		m.o.Log.Warn("could not withdraw the drain request", "error", err)
	}
}
