package router

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/queue"
	"github.com/prometheus/client_golang/prometheus"
)

// fakeStore is the router's store in memory. A conversation is waiting while
// owner is 0; assign is a compare-and-set like the real one.
type fakeStore struct {
	mu      sync.Mutex
	queues  []Queue
	agents  map[int64][]Agent // by queue
	waiting map[int64][]*conv // by queue
	rr      map[int64]uint64
	stolen  map[int64]bool // conversations another pod assigns first
	routed  int
	stale   int
	orphans int
	frt     []time.Duration
	asked   []int64
	reg     *prometheus.Registry
}

type conv struct {
	id      int64
	owner   int64
	exclude int64
}

func (f *fakeStore) RouteNew(context.Context, time.Time, int) (RouteResult, error) {
	f.routed++
	return RouteResult{Placed: 2, Replies: 1}, nil
}
func (f *fakeStore) ReleaseOrphans(context.Context) (int, error) { return f.orphans, nil }
func (f *fakeStore) ReturnStale(context.Context, time.Time) (int, error) {
	return f.stale, nil
}
func (f *fakeStore) TextQueues(context.Context) ([]Queue, error) { return f.queues, nil }
func (f *fakeStore) Agents(_ context.Context, q int64) ([]Agent, error) {
	return slices.Clone(f.agents[q]), nil
}
func (f *fakeStore) Waiting(_ context.Context, q int64, _ int) ([]Waiting, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Waiting
	for _, c := range f.waiting[q] {
		if c.owner == 0 {
			out = append(out, Waiting{ID: c.id, Exclude: c.exclude})
		}
	}
	return out, nil
}
func (f *fakeStore) Assign(_ context.Context, id, user int64, q Queue) (messaging.AssignOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, user)
	var target *conv
	for _, c := range f.waiting[q.ID] {
		if c.id == id {
			target = c
		}
	}
	if target == nil || target.owner != 0 || f.stolen[id] {
		return messaging.AssignGone, nil
	}
	open := 0
	for _, c := range f.waiting[q.ID] {
		if c.owner == user {
			open++
		}
	}
	if open >= q.Capacity {
		return messaging.AssignFull, nil
	}
	target.owner = user
	return messaging.AssignDone, nil
}
func (f *fakeStore) NextRotation(_ context.Context, q int64) (uint64, error) {
	f.rr[q]++
	return f.rr[q] - 1, nil
}
func (f *fakeStore) CountFirstResponses(context.Context) ([]time.Duration, error) {
	return f.frt, nil
}

type fakeLive struct {
	states map[string]queue.AgentState
	busy   map[string]bool
	err    error
}

func (l fakeLive) Agents(context.Context) (map[string]queue.AgentState, error) {
	return l.states, l.err
}
func (l fakeLive) BusyExtensions(context.Context) (map[string]bool, error) { return l.busy, l.err }

func newRig(strategy string, capacity int, agents ...Agent) (*Router, *fakeStore) {
	f := &fakeStore{
		queues:  []Queue{{ID: 1, Name: "support", Strategy: strategy, Capacity: capacity}},
		agents:  map[int64][]Agent{1: agents},
		waiting: map[int64][]*conv{},
		rr:      map[int64]uint64{},
		stolen:  map[int64]bool{},
	}
	f.reg = prometheus.NewRegistry()
	r := &Router{Store: f, Live: fakeLive{}, Metrics: NewMetrics(f.reg)}
	return r, f
}

func (f *fakeStore) wait(ids ...int64) {
	for _, id := range ids {
		f.waiting[1] = append(f.waiting[1], &conv{id: id})
	}
}

func (f *fakeStore) owners() []int64 {
	var out []int64
	for _, c := range f.waiting[1] {
		out = append(out, c.owner)
	}
	return out
}

// TestAssignmentACD fails if two concurrent assignments both succeed for one
// conversation, an agent is given more than the capacity, an unstaffed,
// paused or unlinked agent is chosen, the strategy order differs from
// queue.Plan, a waiting conversation is not assigned when an agent frees up,
// or an agent is chosen for a ring_all queue.
func TestAssignmentACD(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)

	t.Run("strategy order", func(t *testing.T) {
		r, f := newRig(queue.LongestIdle, 5,
			agent(1, "101", func(a *Agent) { a.LastEnd = t0.Add(-time.Minute) }),
			agent(2, "102", func(a *Agent) { a.LastEnd = t0.Add(-time.Hour) }))
		f.wait(10, 11, 12)
		if err := r.Tick(ctx, t0); err != nil {
			t.Fatal(err)
		}
		// 2 has been idle longest; once it has one, 1 is idler than it.
		if got := f.owners(); !slices.Equal(got, []int64{2, 1, 2}) {
			t.Fatalf("owners = %v, want 2 1 2", got)
		}
	})

	t.Run("capacity", func(t *testing.T) {
		r, f := newRig(queue.Linear, 2, agent(1, "101", nil), agent(2, "102", nil))
		f.wait(10, 11, 12, 13, 14)
		if err := r.Tick(ctx, t0); err != nil {
			t.Fatal(err)
		}
		if got := f.owners(); !slices.Equal(got, []int64{1, 1, 2, 2, 0}) {
			t.Fatalf("owners = %v, want 1 1 2 2 and the fifth waiting", got)
		}
	})

	t.Run("unstaffed agents are not chosen", func(t *testing.T) {
		r, f := newRig(queue.Linear, 5,
			agent(1, "101", func(a *Agent) { a.Dynamic = true }), // logged out
			agent(2, "102", nil), // paused
			agent(3, "103", func(a *Agent) { a.Dynamic = true }), // logged in
		)
		r.Live = fakeLive{states: map[string]queue.AgentState{"102": {Ext: "102", Paused: true}, "103": {Ext: "103", LoggedIn: true}}}
		f.wait(10)
		if err := r.Tick(ctx, t0); err != nil {
			t.Fatal(err)
		}
		if got := f.owners(); !slices.Equal(got, []int64{3}) {
			t.Fatalf("owners = %v, want 3", got)
		}
	})

	t.Run("nobody available, then somebody", func(t *testing.T) {
		r, f := newRig(queue.Linear, 5, agent(1, "101", func(a *Agent) { a.Dynamic = true }))
		f.wait(10, 11)
		r.Live = fakeLive{states: map[string]queue.AgentState{}}
		if err := r.Tick(ctx, t0); err != nil {
			t.Fatal(err)
		}
		if got := f.owners(); !slices.Equal(got, []int64{0, 0}) {
			t.Fatalf("owners = %v, want both waiting", got)
		}
		// The agent logs in: the next sweep assigns, oldest first.
		r.Live = fakeLive{states: map[string]queue.AgentState{"101": {Ext: "101", LoggedIn: true}}}
		if err := r.Tick(ctx, t0.Add(10*time.Second)); err != nil {
			t.Fatal(err)
		}
		if got := f.owners(); !slices.Equal(got, []int64{1, 1}) {
			t.Fatalf("owners = %v, want both assigned", got)
		}
	})

	t.Run("an agent at capacity frees up", func(t *testing.T) {
		r, f := newRig(queue.Linear, 1, agent(1, "101", nil))
		f.wait(10, 11)
		if err := r.Tick(ctx, t0); err != nil {
			t.Fatal(err)
		}
		if got := f.owners(); !slices.Equal(got, []int64{1, 0}) {
			t.Fatalf("owners = %v", got)
		}
		f.waiting[1][0].owner = -1 // resolved: no longer open for agent 1
		if err := r.Tick(ctx, t0.Add(10*time.Second)); err != nil {
			t.Fatal(err)
		}
		if got := f.owners(); !slices.Equal(got, []int64{-1, 1}) {
			t.Fatalf("owners = %v, want the waiting one assigned", got)
		}
	})

	t.Run("ring_all keeps the conversation in the pool", func(t *testing.T) {
		r, f := newRig(queue.RingAll, 5, agent(1, "101", nil), agent(2, "102", nil))
		f.wait(10)
		if err := r.Tick(ctx, t0); err != nil {
			t.Fatal(err)
		}
		if got := f.owners(); !slices.Equal(got, []int64{0}) || len(f.asked) != 0 {
			t.Fatalf("owners = %v, asked %v: a shared pool must not assign", got, f.asked)
		}
	})

	t.Run("an agent filled by another pod is skipped, a taken conversation is left", func(t *testing.T) {
		r, f := newRig(queue.Linear, 1, agent(1, "101", nil), agent(2, "102", nil))
		f.wait(10)
		// The store already counts a conversation for agent 1 that the router's
		// snapshot does not know about.
		f.waiting[1] = append(f.waiting[1], &conv{id: 99, owner: 1})
		if err := r.Tick(ctx, t0); err != nil {
			t.Fatal(err)
		}
		if got := f.owners(); !slices.Equal(got, []int64{2, 1}) || !slices.Equal(f.asked, []int64{1, 2}) {
			t.Fatalf("owners = %v, asked %v; want agent 1 refused (full) and agent 2 given it", got, f.asked)
		}
		f.waiting[1][0].owner = 0
		f.stolen[10] = true
		f.asked = nil
		if err := r.Tick(ctx, t0); err != nil {
			t.Fatal(err)
		}
		if len(f.asked) != 1 {
			t.Fatalf("asked %v: a conversation another pod took is offered to one agent only", f.asked)
		}
	})

	t.Run("the agent who let it lapse is not offered it again", func(t *testing.T) {
		r, f := newRig(queue.Linear, 5, agent(1, "101", nil), agent(2, "102", nil))
		f.wait(10)
		f.waiting[1][0].exclude = 1
		if err := r.Tick(ctx, t0); err != nil {
			t.Fatal(err)
		}
		if got := f.owners(); !slices.Equal(got, []int64{2}) {
			t.Fatalf("owners = %v, want 2", got)
		}
		r, f = newRig(queue.Linear, 5, agent(1, "101", nil))
		f.wait(10)
		f.waiting[1][0].exclude = 1
		if err := r.Tick(ctx, t0); err != nil {
			t.Fatal(err)
		}
		if got := f.owners(); !slices.Equal(got, []int64{0}) {
			t.Fatalf("owners = %v: with nobody else it waits in the pool", got)
		}
	})

	t.Run("round robin advances the queue's own counter", func(t *testing.T) {
		r, f := newRig(queue.RoundRobin, 5, agent(1, "101", nil), agent(2, "102", nil), agent(3, "103", nil))
		f.wait(10, 11, 12, 13)
		if err := r.Tick(ctx, t0); err != nil {
			t.Fatal(err)
		}
		if got := f.owners(); !slices.Equal(got, []int64{1, 2, 3, 1}) {
			t.Fatalf("owners = %v, want 1 2 3 1", got)
		}
	})

	t.Run("a broken Valkey leaves static members", func(t *testing.T) {
		r, f := newRig(queue.Linear, 5, agent(1, "101", func(a *Agent) { a.Dynamic = true }), agent(2, "102", nil))
		r.Live = fakeLive{err: errors.New("valkey down")}
		f.wait(10)
		if err := r.Tick(ctx, t0); err != nil {
			t.Fatal(err)
		}
		if got := f.owners(); !slices.Equal(got, []int64{2}) {
			t.Fatalf("owners = %v, want the static member 2", got)
		}
	})
}

// TestTickObserves fails if the router skips a stage, or fails to report the
// first responses and the queue gauges.
func TestTickObserves(t *testing.T) {
	r, f := newRig(queue.Linear, 5, agent(1, "101", nil))
	f.stale, f.orphans = 1, 2
	f.frt = []time.Duration{30 * time.Second, 2 * time.Minute}
	f.wait(10, 11)
	f.waiting[1][1].owner = 9 // not waiting
	if err := r.Tick(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if f.routed != 1 {
		t.Fatalf("RouteNew ran %d times", f.routed)
	}
	mfs, err := f.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			key := mf.GetName()
			for _, l := range m.GetLabel() {
				key += "/" + l.GetName() + "=" + l.GetValue()
			}
			switch {
			case m.GetCounter() != nil:
				got[key] = m.GetCounter().GetValue()
			case m.GetGauge() != nil:
				got[key] = m.GetGauge().GetValue()
			case m.GetHistogram() != nil:
				got[key] = float64(m.GetHistogram().GetSampleCount())
			}
		}
	}
	for k, want := range map[string]float64{
		"hello_msg_assign_total/result=assigned":     1,
		"hello_msg_assign_total/result=returned":     3,
		"hello_msg_assign_total/result=placed":       2,
		"hello_msg_first_response_seconds":           2,
		"hello_msg_queue_text_waiting/queue=support": 0,
	} {
		if got[k] != want {
			t.Errorf("%s = %v, want %v (all: %v)", k, got[k], want, got)
		}
	}
}
