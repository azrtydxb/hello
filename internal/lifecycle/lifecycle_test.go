package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/valkey-io/valkey-go"
)

type fakePub struct {
	mu        sync.Mutex
	published []cluster.Member
	drain     bool
	drainErr  error
	left      bool
	withdrawn int
}

func (f *fakePub) Publish(_ context.Context, m cluster.Member) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = append(f.published, m)
	return nil
}

func (f *fakePub) Leave(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.left = true
	return nil
}

func (f *fakePub) DrainRequested(context.Context, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.drain, f.drainErr
}

func (f *fakePub) last() cluster.Member {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.published[len(f.published)-1]
}

func (f *fakePub) setDrain(v bool) {
	f.mu.Lock()
	f.drain = v
	f.mu.Unlock()
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func gauge(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetGauge().GetValue()
}

func counter(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetCounter().GetValue()
}

func want(t *testing.T, m *Machine, s cluster.State, reasonPart string) {
	t.Helper()
	got, reason := m.State()
	if got != s || !strings.Contains(reason, reasonPart) {
		t.Fatalf("state = %s (%q), want %s (containing %q)", got, reason, s, reasonPart)
	}
	if ready, _, _ := m.Readiness(); ready != (s == cluster.Ready) {
		t.Fatalf("readiness = %v in %s", ready, s)
	}
}

// TestStateMachine fails if the lifecycle does not go JOINING until its
// checks pass, READY, UNHEALTHY while a check fails (never back to
// JOINING), DRAINING on a drain request and back when it is cancelled,
// DRAINING for good on a local drain or once exiting, or if a state change
// is logged more than once.
func TestStateMachine(t *testing.T) {
	var valkeyDown atomic.Bool
	valkeyDown.Store(true)
	pub := &fakePub{}
	var logs syncBuf
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	calls := 3
	m := New(Options{
		Member: cluster.Member{ID: "sip-1", Kind: cluster.KindSIP, SIPAddr: "10.0.0.1:5060", Transports: []string{"udp"}, Version: "v1"},
		Checks: map[string]Check{
			"valkey": func(context.Context) error {
				return map[bool]error{true: errors.New("connection refused")}[valkeyDown.Load()]
			},
			"snapshot": func(context.Context) error { return nil },
		},
		Publisher: pub, Log: slog.New(slog.NewTextHandler(&logs, nil)), Now: func() time.Time { return now },
		Load: func() Load { return Load{ActiveCalls: calls, Registrations: 7, ConfigRevision: 42} },
	})
	ctx := context.Background()
	want(t, m, cluster.Joining, "starting")
	m.Heartbeat(ctx)
	want(t, m, cluster.Joining, "valkey: connection refused")
	valkeyDown.Store(false)
	m.Heartbeat(ctx)
	want(t, m, cluster.Ready, "")
	p := pub.last()
	if p.State != cluster.Ready || p.ActiveCalls != 3 || p.Registrations != 7 || p.ConfigRevision != 42 || p.Version != "v1" ||
		p.SIPAddr != "10.0.0.1:5060" || len(p.Transports) != 1 || !p.Heartbeat.Equal(now) || p.StartedAt.IsZero() {
		t.Fatalf("published = %+v", p)
	}
	valkeyDown.Store(true)
	m.Heartbeat(ctx)
	want(t, m, cluster.Unhealthy, "valkey")
	valkeyDown.Store(false)
	m.Heartbeat(ctx)
	want(t, m, cluster.Ready, "")

	pub.setDrain(true)
	m.Heartbeat(ctx)
	want(t, m, cluster.Draining, "drain requested")
	if pub.last().State != cluster.Draining || pub.last().ActiveCalls != 3 {
		t.Fatalf("draining published = %+v", pub.last())
	}
	// A failing check does not end a drain; cancelling while unhealthy
	// returns to UNHEALTHY, and to READY only once healthy.
	valkeyDown.Store(true)
	m.Heartbeat(ctx)
	want(t, m, cluster.Draining, "drain requested")
	pub.setDrain(false)
	m.Heartbeat(ctx)
	want(t, m, cluster.Unhealthy, "valkey")
	valkeyDown.Store(false)
	m.Heartbeat(ctx)
	want(t, m, cluster.Ready, "")

	// A drain request after shutdown began cannot be cancelled.
	pub.setDrain(true)
	m.Heartbeat(ctx)
	m.Exiting()
	pub.setDrain(false)
	m.Heartbeat(ctx)
	want(t, m, cluster.Draining, "drain requested")

	// SIGTERM drains for good.
	m2 := New(Options{Checks: map[string]Check{}, Publisher: &fakePub{}})
	m2.Heartbeat(ctx)
	m2.Drain("SIGTERM")
	want(t, m2, cluster.Draining, "SIGTERM")
	m2.Heartbeat(ctx)
	want(t, m2, cluster.Draining, "SIGTERM")

	// Each change logged once: JOINING->READY, READY->UNHEALTHY, ->READY,
	// ->DRAINING, ->UNHEALTHY, ->READY, ->DRAINING.
	if n := strings.Count(logs.String(), "node state changed"); n != 7 {
		t.Fatalf("state changes logged %d times, want 7:\n%s", n, logs.String())
	}
}

// TestHAMetrics fails if hello_node_state is not one-hot through a JOINING
// -> READY -> DRAINING cycle, if hello_drain_active_calls does not follow
// the calls only while draining, or if a Valkey primary change is not
// counted in hello_valkey_failovers_total.
func TestHAMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	mt := NewMetrics(reg)
	var up atomic.Bool
	pub := &fakePub{}
	primary := "10.0.0.11:6379"
	var pmu sync.Mutex
	calls := 2
	m := New(Options{
		Checks:    map[string]Check{"valkey": func(context.Context) error { return map[bool]error{false: errors.New("down")}[up.Load()] }},
		Publisher: pub, Metrics: mt,
		Load:    func() Load { return Load{ActiveCalls: calls} },
		Primary: func() string { pmu.Lock(); defer pmu.Unlock(); return primary },
	})
	ctx := context.Background()
	oneHot := func(s cluster.State) {
		t.Helper()
		for _, st := range states {
			want := 0.0
			if st == s {
				want = 1
			}
			if v := gauge(t, mt.NodeState.WithLabelValues(string(st))); v != want {
				t.Fatalf("hello_node_state{%s} = %v in %s", st, v, s)
			}
		}
	}
	m.Heartbeat(ctx)
	oneHot(cluster.Joining)
	up.Store(true)
	m.Heartbeat(ctx)
	oneHot(cluster.Ready)
	if gauge(t, mt.DrainCalls) != 0 {
		t.Fatal("drain calls while READY")
	}
	pub.setDrain(true)
	m.Heartbeat(ctx)
	oneHot(cluster.Draining)
	if v := gauge(t, mt.DrainCalls); v != 2 {
		t.Fatalf("hello_drain_active_calls = %v, want 2", v)
	}
	if counter(t, mt.ValkeyFailovers) != 0 {
		t.Fatal("failover counted without a primary change")
	}
	pmu.Lock()
	primary = "10.0.0.12:6379"
	pmu.Unlock()
	m.Heartbeat(ctx)
	if v := counter(t, mt.ValkeyFailovers); v != 1 {
		t.Fatalf("hello_valkey_failovers_total = %v, want 1", v)
	}
}

// TestMembershipLifecycle runs the machine against Valkey through
// cluster.Store. It fails if the node is not reported JOINING, then READY,
// then DRAINING, then OFFLINE after leaving; if /readyz-equivalent
// readiness is true outside READY; or if a node that stops heartbeating is
// not tombstoned OFFLINE once its record expires.
func TestMembershipLifecycle(t *testing.T) {
	addr := os.Getenv("HELLO_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("HELLO_TEST_VALKEY_ADDR not set")
	}
	c, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, ForceSingleClient: true, SelectDB: 6}) // its own DB: other packages flush theirs in parallel
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	if err := c.Do(context.Background(), c.B().Flushdb().Build()).Error(); err != nil {
		t.Fatal(err)
	}
	store := cluster.New(c)
	var up atomic.Bool
	m := New(Options{
		Member:    cluster.Member{ID: "sip-a", Kind: cluster.KindSIP},
		Checks:    map[string]Check{"snapshot": func(context.Context) error { return map[bool]error{false: errors.New("not loaded")}[up.Load()] }},
		Publisher: store, Heartbeat: 100 * time.Millisecond, CheckEvery: 50 * time.Millisecond,
	})
	stateOf := func(id string) cluster.State {
		ms, err := store.Members(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, mem := range ms {
			if mem.ID == id {
				return mem.State
			}
		}
		return ""
	}
	waitFor := func(d time.Duration, s cluster.State) {
		t.Helper()
		deadline := time.Now().Add(d)
		for stateOf("sip-a") != s {
			if time.Now().After(deadline) {
				t.Fatalf("not %s within %s (is %s)", s, d, stateOf("sip-a"))
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	waitFor(time.Second, cluster.Joining)
	if ready, _, _ := m.Readiness(); ready {
		t.Fatal("ready while JOINING")
	}
	up.Store(true)
	waitFor(time.Second, cluster.Ready)
	if err := store.RequestDrain(context.Background(), "sip-a"); err != nil {
		t.Fatal(err)
	}
	waitFor(time.Second, cluster.Draining)
	if ready, _, _ := m.Readiness(); ready {
		t.Fatal("ready while DRAINING")
	}
	cancel()
	<-done
	waitFor(time.Second, cluster.Offline) // left: listed from the tombstone

	// A node that just stops (killed) expires after the record TTL.
	dead := New(Options{Member: cluster.Member{ID: "sip-dead", Kind: cluster.KindSIP}, Checks: map[string]Check{}, Publisher: store})
	dead.Heartbeat(context.Background())
	if stateOf("sip-dead") != cluster.Ready {
		t.Fatalf("killed node not READY first: %s", stateOf("sip-dead"))
	}
	time.Sleep(cluster.TTL + time.Second)
	if s := stateOf("sip-dead"); s != cluster.Offline {
		t.Fatalf("node %s after its record expired, want OFFLINE", s)
	}
}

func (f *fakePub) CancelDrain(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.drain = false
	f.withdrawn++
	return nil
}

type fakeDrainer struct {
	calls    atomic.Int64
	hungUp   atomic.Value // reason
	onHangup func()
}

func (d *fakeDrainer) ActiveCalls() int { return int(d.calls.Load()) }
func (d *fakeDrainer) HangupAll(reason string) {
	d.hungUp.Store(reason)
	if d.onHangup != nil {
		d.onHangup()
	}
}

func runMachine(t *testing.T, o Options) *Machine {
	t.Helper()
	o.Heartbeat, o.CheckEvery = 20*time.Millisecond, 10*time.Millisecond
	m := New(o)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return m
}

func awaitAsync(m *Machine, d Drainer, timeout time.Duration) chan bool {
	out := make(chan bool, 1)
	go func() { out <- m.AwaitDrain(context.Background(), d, timeout) }()
	return out
}

// TestAwaitDrainExitsAtZeroCalls fails if a draining node exits while it
// still has calls, does not exit once they reach 0, or leaves the drain
// request behind (the restarted node would drain again).
func TestAwaitDrainExitsAtZeroCalls(t *testing.T) {
	pub := &fakePub{}
	m := runMachine(t, Options{Checks: map[string]Check{}, Publisher: pub})
	d := &fakeDrainer{}
	d.calls.Store(1)
	res := awaitAsync(m, d, time.Hour)
	pub.setDrain(true)
	select {
	case <-res:
		t.Fatal("exited with a call active")
	case <-time.After(200 * time.Millisecond):
	}
	d.calls.Store(0)
	select {
	case ok := <-res:
		if !ok {
			t.Fatal("AwaitDrain = false")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not exit at 0 calls")
	}
	pub.mu.Lock()
	withdrawn := pub.withdrawn
	pub.mu.Unlock()
	if withdrawn != 1 || d.hungUp.Load() != nil {
		t.Fatalf("withdrawn %d, hung up %v", withdrawn, d.hungUp.Load())
	}
}

// TestAwaitDrainTimeoutEndsCalls fails if the drain timeout does not end
// the remaining calls with reason "drain timeout" and then exit.
func TestAwaitDrainTimeoutEndsCalls(t *testing.T) {
	m := runMachine(t, Options{Checks: map[string]Check{}, Publisher: &fakePub{}})
	d := &fakeDrainer{}
	d.calls.Store(2)
	d.onHangup = func() { d.calls.Store(0) }
	res := awaitAsync(m, d, 150*time.Millisecond)
	m.Drain("SIGTERM")
	select {
	case ok := <-res:
		if !ok || d.hungUp.Load() != DrainTimeoutReason {
			t.Fatalf("AwaitDrain = %v, hung up with %v", ok, d.hungUp.Load())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not exit after the drain timeout")
	}
}

// TestAwaitDrainCancelled fails if a drain cancelled while calls are
// active makes the node exit, or stops it from draining later.
func TestAwaitDrainCancelled(t *testing.T) {
	pub := &fakePub{}
	m := runMachine(t, Options{Checks: map[string]Check{}, Publisher: pub})
	d := &fakeDrainer{}
	d.calls.Store(1)
	res := awaitAsync(m, d, time.Hour)
	pub.setDrain(true)
	time.Sleep(100 * time.Millisecond)
	pub.setDrain(false) // cancelled
	time.Sleep(100 * time.Millisecond)
	if s, _ := m.State(); s != cluster.Ready {
		t.Fatalf("after cancel: %s", s)
	}
	d.calls.Store(0)
	select {
	case <-res:
		t.Fatal("exited after the drain was cancelled")
	case <-time.After(200 * time.Millisecond):
	}
	m.Drain("SIGTERM")
	select {
	case ok := <-res:
		if !ok {
			t.Fatal("AwaitDrain = false")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a later drain did not exit")
	}
}
