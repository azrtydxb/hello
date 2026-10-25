package lifecycle

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cluster"
)

// fakeRollouts is an in-memory restart token and member list with the
// semantics of cluster.Store.
type fakeRollouts struct {
	mu      sync.Mutex
	holder  string
	members []cluster.Member
	err     error
}

func (f *fakeRollouts) AcquireRollout(_ context.Context, _ cluster.Kind, id string, _ time.Duration) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	if f.holder == "" {
		f.holder = id
	}
	return f.holder, nil
}

func (f *fakeRollouts) ReleaseRollout(_ context.Context, _ cluster.Kind, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.holder == id {
		f.holder = ""
	}
	return nil
}

func (f *fakeRollouts) Members(context.Context) ([]cluster.Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]cluster.Member(nil), f.members...), nil
}

func (f *fakeRollouts) setMember(m cluster.Member) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.members {
		if f.members[i].ID == m.ID {
			f.members[i] = m
			return
		}
	}
	f.members = append(f.members, m)
}

func (f *fakeRollouts) holderNow() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.holder
}

func gate(f *fakeRollouts, id string, wait time.Duration) *RolloutGate {
	return &RolloutGate{
		Store: f, Kind: cluster.KindSIP, ID: id, Wait: wait, TTL: time.Minute, Poll: 10 * time.Millisecond,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func sipMember(id string, st cluster.State) cluster.Member {
	return cluster.Member{ID: id, Kind: cluster.KindSIP, State: st}
}

// enterAsync runs Enter and reports its result.
func enterAsync(ctx context.Context, g *RolloutGate) chan bool {
	out := make(chan bool, 1)
	go func() { out <- g.Enter(ctx) }()
	return out
}

func expectBlocked(t *testing.T, res chan bool, what string) {
	t.Helper()
	select {
	case <-res:
		t.Fatalf("Enter returned: %s", what)
	case <-time.After(150 * time.Millisecond):
	}
}

func expectEntered(t *testing.T, res chan bool, what string) bool {
	t.Helper()
	select {
	case ok := <-res:
		return ok
	case <-time.After(3 * time.Second):
		t.Fatalf("Enter did not return: %s", what)
		return false
	}
}

// TestRolloutGateWaitsForReadyPeer fails if a node drains while no other node
// is READY (the first failure of the 2026-10-25 deploy: the draining node
// had no survivor), or does not go the moment one is.
func TestRolloutGateWaitsForReadyPeer(t *testing.T) {
	f := &fakeRollouts{}
	f.setMember(sipMember("hello-sip-1", cluster.Ready))
	f.setMember(sipMember("hello-sip-2", cluster.Joining))
	res := enterAsync(t.Context(), gate(f, "hello-sip-1", time.Minute))
	expectBlocked(t, res, "the only peer is still JOINING")
	f.setMember(sipMember("hello-sip-2", cluster.Ready))
	if !expectEntered(t, res, "the peer is READY") {
		t.Fatal("Enter reported no token held")
	}
	if h := f.holderNow(); h != "hello-sip-1" {
		t.Fatalf("token holder = %q, want hello-sip-1 (kept until its restart is READY)", h)
	}
}

// TestRolloutGateOneAtATime fails if two nodes told to stop together both
// pass: each sees the other READY, which is exactly the simultaneous roll.
// The second must wait for the first's replacement, which releases the
// token when it is READY.
func TestRolloutGateOneAtATime(t *testing.T) {
	f := &fakeRollouts{}
	f.setMember(sipMember("hello-sip-1", cluster.Ready))
	f.setMember(sipMember("hello-sip-2", cluster.Ready))
	g1, g2 := gate(f, "hello-sip-1", time.Minute), gate(f, "hello-sip-2", time.Minute)
	r1, r2 := enterAsync(t.Context(), g1), enterAsync(t.Context(), g2)

	var first, second *RolloutGate
	var firstRes, secondRes chan bool
	select {
	case <-r1:
		first, second, firstRes, secondRes = g1, g2, r1, r2
	case <-r2:
		first, second, firstRes, secondRes = g2, g1, r2, r1
	case <-time.After(3 * time.Second):
		t.Fatal("neither node passed the gate")
	}
	_ = firstRes
	expectBlocked(t, secondRes, "the first node is still down")

	// The first node exits; its replacement is JOINING, then READY.
	f.setMember(sipMember(first.ID, cluster.Joining))
	expectBlocked(t, secondRes, "the first node's replacement is not READY yet")
	f.setMember(sipMember(first.ID, cluster.Ready))
	first.Release() // what the replacement does on becoming READY
	if !expectEntered(t, secondRes, "the replacement is READY and released the token") {
		t.Fatalf("%s reported no token held", second.ID)
	}
	if h := f.holderNow(); h != second.ID {
		t.Fatalf("token holder = %q, want %s", h, second.ID)
	}
}

// TestRolloutGateSingleNodePassesAtOnce fails if a node with no peer waits:
// a single-node install must stop at once.
func TestRolloutGateSingleNodePassesAtOnce(t *testing.T) {
	f := &fakeRollouts{}
	f.setMember(sipMember("hello-sip-1", cluster.Ready))
	f.setMember(cluster.Member{ID: "hello-control-1", Kind: cluster.KindControl, State: cluster.Ready})
	if !expectEntered(t, enterAsync(t.Context(), gate(f, "hello-sip-1", time.Minute)), "no SIP peer exists") {
		t.Fatal("no token held")
	}
}

// TestRolloutGateGivesUp fails if a node whose peer never returns hangs its
// shutdown: it proceeds after Wait, without the token when another node
// holds it.
func TestRolloutGateGivesUp(t *testing.T) {
	f := &fakeRollouts{holder: "hello-sip-2"}
	f.setMember(sipMember("hello-sip-1", cluster.Ready))
	f.setMember(sipMember("hello-sip-2", cluster.Offline))
	start := time.Now()
	if expectEntered(t, enterAsync(t.Context(), gate(f, "hello-sip-1", 200*time.Millisecond)), "Wait elapsed") {
		t.Fatal("Enter claimed a token another node holds")
	}
	if time.Since(start) < 150*time.Millisecond {
		t.Fatal("Enter gave up before Wait")
	}
	if h := f.holderNow(); h != "hello-sip-2" {
		t.Fatalf("token holder = %q, the other node's token was taken", h)
	}
}

// TestRolloutGateStoreDown fails if an unreachable store delays shutdown.
func TestRolloutGateStoreDown(t *testing.T) {
	f := &fakeRollouts{err: errors.New("valkey down")}
	start := time.Now()
	expectEntered(t, enterAsync(t.Context(), gate(f, "hello-sip-1", time.Minute)), "store unavailable")
	if time.Since(start) > time.Second {
		t.Fatal("Enter waited on an unreachable store")
	}
}

// TestRolloutGateDisabled fails if Wait 0 does not switch the gate off.
func TestRolloutGateDisabled(t *testing.T) {
	f := &fakeRollouts{}
	f.setMember(sipMember("hello-sip-2", cluster.Joining))
	if expectEntered(t, enterAsync(t.Context(), gate(f, "hello-sip-1", 0)), "gate disabled") || f.holderNow() != "" {
		t.Fatal("a disabled gate took the token")
	}
}

// TestRolloutReleaseOnlyByHolder fails if a restarted node that does not
// hold the token releases the peer's.
func TestRolloutReleaseOnlyByHolder(t *testing.T) {
	f := &fakeRollouts{holder: "hello-sip-2"}
	gate(f, "hello-sip-1", time.Minute).Release()
	if f.holderNow() != "hello-sip-2" {
		t.Fatal("release by a non-holder dropped the token")
	}
}

// TestAwaitDrainHandsOffToLateSurvivor fails if a node draining with no
// READY survivor ends its calls or exits before one appears, or does not
// exit once the survivor has taken them: the calls only wait (the drain
// timeout is the bound), and a survivor that turns READY later claims them
// and lets the node go.
func TestAwaitDrainHandsOffToLateSurvivor(t *testing.T) {
	m := runMachine(t, Options{Checks: map[string]Check{}, Publisher: &fakePub{}})
	d := &fakeDrainer{}
	d.calls.Store(3)
	res := awaitAsync(m, d, time.Hour)
	m.Drain("SIGTERM")
	select {
	case <-res:
		t.Fatal("exited with calls and no survivor")
	case <-time.After(250 * time.Millisecond):
	}
	if d.hungUp.Load() != nil {
		t.Fatalf("calls hung up before the drain timeout: %v", d.hungUp.Load())
	}
	d.calls.Store(0) // the late survivor claimed them
	select {
	case ok := <-res:
		if !ok || d.hungUp.Load() != nil {
			t.Fatalf("AwaitDrain = %v, hung up %v", ok, d.hungUp.Load())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not exit after the survivor took the calls")
	}
}
