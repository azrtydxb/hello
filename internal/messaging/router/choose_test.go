package router

import (
	"slices"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/queue"
)

func agent(user int64, ext string, mod func(*Agent)) Agent {
	a := Agent{UserID: user, Ext: ext, Position: int(user)}
	if mod != nil {
		mod(&a)
	}
	return a
}

// TestChoose fails if the order differs from queue.Plan, or if an agent who
// is logged out, paused, at capacity, on a call (when the queue cares) or the
// excluded user is offered a conversation.
func TestChoose(t *testing.T) {
	t0 := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	q := Queue{ID: 1, Name: "support", Strategy: queue.LongestIdle, Capacity: 2}
	agents := []Agent{
		agent(1, "101", func(a *Agent) { a.LastEnd = t0.Add(-time.Minute) }),
		agent(2, "102", func(a *Agent) { a.LastEnd = t0.Add(-time.Hour) }),
		agent(3, "103", nil), // never had one
	}
	if got := Choose(q, agents, Availability{}, 0, 0, 0).Order; !slices.Equal(got, []int64{3, 2, 1}) {
		t.Errorf("longest idle order = %v", got)
	}

	q.Strategy = queue.FewestCalls
	agents[0].Answered, agents[1].Answered, agents[2].Answered = 5, 1, 3
	if got := Choose(q, agents, Availability{}, 0, 0, 0).Order; !slices.Equal(got, []int64{2, 3, 1}) {
		t.Errorf("fewest calls order = %v", got)
	}

	q.Strategy = queue.Linear
	if got := Choose(q, agents, Availability{}, 0, 0, 0).Order; !slices.Equal(got, []int64{1, 2, 3}) {
		t.Errorf("linear order = %v", got)
	}

	q.Strategy = queue.RoundRobin
	if got := Choose(q, agents, Availability{}, 0, 1, 0).Order; !slices.Equal(got, []int64{2, 3, 1}) {
		t.Errorf("round robin order = %v", got)
	}

	// The order is queue.Plan's, whatever the strategy.
	for _, strategy := range []string{queue.LongestIdle, queue.FewestCalls, queue.RoundRobin, queue.Random, queue.Linear} {
		q.Strategy = strategy
		var cands []queue.Candidate
		for _, a := range agents {
			cands = append(cands, queue.Candidate{Ext: a.Ext, Position: a.Position, Calls: a.Answered, LastEnd: a.LastEnd})
		}
		var want []int64
		for _, round := range queue.Plan(strategy, cands, 7, 42) {
			for _, ext := range round {
				for _, a := range agents {
					if a.Ext == ext {
						want = append(want, a.UserID)
					}
				}
			}
		}
		if got := Choose(q, agents, Availability{}, 0, 7, 42).Order; !slices.Equal(got, want) {
			t.Errorf("%s: order %v, queue.Plan says %v", strategy, got, want)
		}
	}

	q.Strategy = queue.Linear
	// Penalties: a higher penalty is only reached after the lower ones.
	pen := []Agent{agent(1, "101", func(a *Agent) { a.Penalty = 5 }), agent(2, "102", nil)}
	if got := Choose(q, pen, Availability{}, 0, 0, 0).Order; !slices.Equal(got, []int64{2, 1}) {
		t.Errorf("penalty order = %v", got)
	}

	// Who is left out.
	dyn := []Agent{
		agent(1, "101", nil),
		agent(2, "102", func(a *Agent) { a.Dynamic = true }), // never logged in
		agent(3, "103", func(a *Agent) { a.Dynamic = true }), // logged in
		agent(4, "104", nil),                                              // paused
		agent(5, "105", func(a *Agent) { a.Open = 2 }),                    // at capacity
		agent(6, "106", nil),                                              // on a call
		agent(7, "107", func(a *Agent) { a.Dynamic = true }),              // logged in to another queue only
		agent(8, "108", func(a *Agent) { a.Open = 1; a.Dynamic = false }), // has room
	}
	av := Availability{
		States: map[string]queue.AgentState{
			"103": {Ext: "103", LoggedIn: true},
			"104": {Ext: "104", Paused: true},
			"107": {Ext: "107", LoggedIn: true, Queues: []string{"sales"}},
		},
		OnCall: map[string]bool{"106": true},
	}
	if got := Choose(q, dyn, av, 0, 0, 0).Order; !slices.Equal(got, []int64{1, 3, 6, 8}) {
		t.Errorf("staffed agents = %v, want 1 3 6 8", got)
	}
	q.RequiresIdleVoice = true
	if got := Choose(q, dyn, av, 0, 0, 0).Order; !slices.Equal(got, []int64{1, 3, 8}) {
		t.Errorf("idle voice required = %v, want 1 3 8", got)
	}
	if got := Choose(q, dyn, av, 3, 0, 0).Order; !slices.Equal(got, []int64{1, 8}) {
		t.Errorf("user 3 excluded = %v, want 1 8", got)
	}

	// The state cannot be read: static members only, as the call queues do.
	if got := Choose(q, dyn, Availability{}, 0, 0, 0).Order; !slices.Equal(got, []int64{1, 4, 6, 8}) {
		t.Errorf("no live state = %v, want the static members 1 4 6 8", got)
	}
}

// TestChooseSharedPool fails if ring_all assigns instead of sharing, or
// offers a conversation to nobody who could answer it.
func TestChooseSharedPool(t *testing.T) {
	q := Queue{Name: "support", Strategy: queue.RingAll, Capacity: 3}
	c := Choose(q, []Agent{agent(1, "101", nil), agent(2, "102", nil)}, Availability{}, 0, 0, 0)
	if !c.Shared || len(c.Order) != 2 {
		t.Fatalf("choice = %+v", c)
	}
	if c := Choose(q, []Agent{agent(1, "101", func(a *Agent) { a.Open = 3 })}, Availability{}, 0, 0, 0); len(c.Order) != 0 {
		t.Fatalf("an agent at capacity shares the pool: %+v", c)
	}
}

// TestChooseOneUserManyExtensions fails if a user linked to two member
// extensions is offered twice or ranked by their worse extension.
func TestChooseOneUserManyExtensions(t *testing.T) {
	q := Queue{Name: "support", Strategy: queue.Linear, Capacity: 3}
	agents := []Agent{
		{UserID: 1, Ext: "101", Penalty: 3, Position: 1},
		{UserID: 1, Ext: "111", Penalty: 0, Position: 2},
		{UserID: 2, Ext: "102", Penalty: 1, Position: 3},
	}
	if got := Choose(q, agents, Availability{}, 0, 0, 0).Order; !slices.Equal(got, []int64{1, 2}) {
		t.Fatalf("order = %v, want user 1 once, ahead of user 2", got)
	}
	// If their best extension is logged out the other one carries them.
	agents[1].Dynamic = true
	if got := Choose(q, agents, Availability{States: map[string]queue.AgentState{}}, 0, 0, 0).Order; !slices.Equal(got, []int64{2, 1}) {
		t.Fatalf("order = %v, want 2 then 1 (by their remaining extension)", got)
	}
}
