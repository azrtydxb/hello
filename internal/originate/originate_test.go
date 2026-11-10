package originate

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/store"
)

// TestLimiterWindowsAndSlot fails if the fixed-window counters do not refuse
// the request over each ceiling with the time left in the window, if a refused
// request keeps the extension's setup slot, or if a new window does not start
// from zero.
func TestLimiterWindowsAndSlot(t *testing.T) {
	now := time.Date(2026, 10, 27, 10, 0, 20, 0, time.UTC)
	l := &Limiter{B: NewMemory(), L: Limits{CredentialPerMinute: 2, DestinationCount: 100, DestinationWindow: time.Minute, SetupHold: time.Minute}, Now: func() time.Time { return now }}
	ctx := context.Background()
	for i, id := range []string{"a", "b"} {
		d, err := l.Check(ctx, "tok:1", "101", "0500", id)
		if err != nil || d != nil {
			t.Fatalf("request %d = %v, %v", i+1, d, err)
		}
		_ = l.Release(ctx, "101", id)
	}
	d, err := l.Check(ctx, "tok:1", "101", "0500", "c")
	if err != nil || d == nil || d.Scope != ScopeCredentialMinute || d.RetryAfter != 40*time.Second {
		t.Fatalf("third request = %+v, %v, want credential_minute with 40s left", d, err)
	}
	// The refused request must not keep the slot.
	if ok, _ := l.B.AcquireOriginateSetup(ctx, "101", "x", time.Minute); !ok {
		t.Fatal("a refused request kept the extension's setup slot")
	}
	_ = l.Release(ctx, "101", "x")
	// Another credential is unaffected; the next minute starts clean.
	if d, _ := l.Check(ctx, "tok:2", "101", "0500", "d"); d != nil {
		t.Fatalf("another credential = %+v", d)
	}
	_ = l.Release(ctx, "101", "d")
	now = now.Add(time.Minute)
	if d, _ := l.Check(ctx, "tok:1", "101", "0500", "e"); d != nil {
		t.Fatalf("next window = %+v", d)
	}
	// The slot is held until released.
	if d, _ := l.Check(ctx, "tok:1", "101", "0500", "f"); d == nil || d.Scope != ScopeExtensionSetup {
		t.Fatalf("second call in setup = %+v", d)
	}
}

type sweepStore struct {
	mu   sync.Mutex
	recs map[string]store.OriginateRecord
}

func (s *sweepStore) OpenOriginates(context.Context, time.Duration, int) ([]store.OriginateRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.OriginateRecord
	for _, r := range s.recs {
		if !livestate.OriginateTerminal(r.State) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *sweepStore) SetOriginateState(_ context.Context, id, state, reason, corr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.recs[id]
	r.State, r.Reason, r.CorrelationID = state, reason, corr
	s.recs[id] = r
	return nil
}

func (s *sweepStore) PurgeOriginates(context.Context, time.Duration) (int64, error) { return 0, nil }

type sweepLive struct {
	mu       sync.Mutex
	states   map[string]livestate.OriginateState
	released []string
}

func (l *sweepLive) GetOriginateState(_ context.Context, id string) (livestate.OriginateState, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.states[id]
	return st, ok, nil
}

func (l *sweepLive) PutOriginateState(_ context.Context, st livestate.OriginateState) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	st.UpdatedAt = time.Now()
	l.states[st.ID] = st
	return nil
}

func (l *sweepLive) ReleaseOriginateSetup(_ context.Context, ext, id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.released = append(l.released, ext+"/"+id)
	return nil
}

// TestSweeperClosesOrphans fails if a request whose node never reported is
// not closed failed node_unavailable after a minute, if a request a node is
// still working on is, if a state a node posted is not copied into the row,
// or if the extension's slot is not freed for a finished request.
func TestSweeperClosesOrphans(t *testing.T) {
	t0 := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	st := &sweepStore{recs: map[string]store.OriginateRecord{
		"lost":    {ID: "lost", Extension: "101", State: "queued", UpdatedAt: t0},
		"working": {ID: "working", Extension: "102", State: "queued", UpdatedAt: t0},
		"done":    {ID: "done", Extension: "103", State: "queued", UpdatedAt: t0},
		"stuck":   {ID: "stuck", Extension: "104", State: "ringing_agent", UpdatedAt: t0},
	}}
	live := &sweepLive{states: map[string]livestate.OriginateState{
		"working": {ID: "working", State: "ringing_agent", UpdatedAt: t0.Add(50 * time.Second)},
		"done":    {ID: "done", State: "ended", CorrelationID: "c1", UpdatedAt: t0.Add(30 * time.Second)},
		"stuck":   {ID: "stuck", State: "ringing_agent", UpdatedAt: t0},
	}}
	sw := &Sweeper{Store: st, Live: live, Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return t0.Add(70 * time.Second) }}
	sw.Once(context.Background(), false)
	if r := st.recs["lost"]; r.State != "failed" || r.Reason != "node_unavailable" {
		t.Fatalf("lost = %+v", r)
	}
	if r := st.recs["stuck"]; r.State != "failed" || r.Reason != "node_unavailable" {
		t.Fatalf("stuck = %+v", r)
	}
	if r := st.recs["working"]; r.State != "ringing_agent" {
		t.Fatalf("working = %+v, want it left alone", r)
	}
	if r := st.recs["done"]; r.State != "ended" || r.CorrelationID != "c1" {
		t.Fatalf("done = %+v, want the node's state copied", r)
	}
	if got := live.states["lost"]; got.State != "failed" {
		t.Fatalf("live state of lost = %+v, want failed so GET agrees", got)
	}
	for _, want := range []string{"101/lost", "103/done", "104/stuck"} {
		found := false
		for _, r := range live.released {
			found = found || r == want
		}
		if !found {
			t.Errorf("slot %s not released (%v)", want, live.released)
		}
	}
}
