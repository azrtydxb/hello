package router

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/queue"
)

// RouteResult counts what RouteNew did.
type RouteResult struct {
	// Placed is how many new conversations were given their route.
	Placed int
	// Replies is how many closed-hours texts were queued.
	Replies int
}

// Waiting is a conversation in a text queue that nobody holds.
type Waiting struct {
	ID int64
	// Exclude is the user who last let it lapse, who is not offered it
	// again; 0 for none.
	Exclude int64
}

// Store is what the router needs of PostgreSQL; *store.Store implements it.
// Every method is safe to run on any number of control pods at once: the
// store claims rows with SKIP LOCKED and assigns by compare-and-set.
type Store interface {
	// RouteNew places the conversations no router has placed yet by their
	// number's route and schedule.
	RouteNew(ctx context.Context, now time.Time, limit int) (RouteResult, error)
	// ReleaseOrphans returns to the pool the unassigned conversations of a
	// queue that stopped taking texts, and the conversations the router gave
	// to a user who is no longer one of the queue's agents. It reports how
	// many moved.
	ReleaseOrphans(ctx context.Context) (int, error)
	// ReturnStale returns the conversations an agent left unanswered for
	// longer than their queue's reassign time to the pool.
	ReturnStale(ctx context.Context, now time.Time) (int, error)
	// TextQueues lists the enabled queues that take texts.
	TextQueues(ctx context.Context) ([]Queue, error)
	// Agents lists the queue's members linked to enabled users.
	Agents(ctx context.Context, queueID int64) ([]Agent, error)
	// Waiting lists the queue's unassigned conversations, oldest first. A
	// ring_all queue shares its pool and has none.
	Waiting(ctx context.Context, queueID int64, limit int) ([]Waiting, error)
	// Assign gives the conversation to the user if it is still waiting and
	// the user is under the queue's capacity, counting the capacity under a
	// per-agent lock.
	Assign(ctx context.Context, convID, userID int64, q Queue) (messaging.AssignOutcome, error)
	// NextRotation returns the queue's text rotation counter and advances it.
	NextRotation(ctx context.Context, queueID int64) (uint64, error)
	// CountFirstResponses marks the first responses not yet counted and
	// returns how long each took.
	CountFirstResponses(ctx context.Context) ([]time.Duration, error)
}

// Live is the shared state of the call queues the router reads: who is
// logged in or paused, and who is on a call. *api.LazyValkey and
// queue.Store back it.
type Live interface {
	Agents(ctx context.Context) (map[string]queue.AgentState, error)
	BusyExtensions(ctx context.Context) (map[string]bool, error)
}

// batch bounds the work of one sweep.
const (
	routeBatch   = 200
	waitingBatch = 200
)

// DefaultSlowEvery is how often the stages that look at every assigned
// conversation (returning the unanswered, releasing the orphaned) run; the
// placing and assigning stages run on every tick.
const DefaultSlowEvery = 10 * time.Second

// Router runs the sweeps. Nothing it keeps matters for correctness: a
// restart, or any number of replicas, change nothing. One goroutine calls
// Tick.
type Router struct {
	Store   Store
	Live    Live
	Metrics *Metrics
	Log     *slog.Logger
	// SlowEvery is how often the slow stages run; 0 means DefaultSlowEvery.
	SlowEvery time.Duration

	lastSlow time.Time
}

func (r *Router) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

func (r *Router) count(result string, n int) {
	if n > 0 && r.Metrics != nil {
		r.Metrics.Assign.WithLabelValues(result).Add(float64(n))
	}
}

// Tick runs one sweep at now: place new conversations, release what no
// longer fits, return the unanswered, and hand the waiting to the agents the
// strategy picks. A failure of one stage does not stop the others; the first
// error is returned.
func (r *Router) Tick(ctx context.Context, now time.Time) error {
	var first error
	keep := func(err error) {
		if err != nil && first == nil && ctx.Err() == nil {
			first = err
		}
	}

	res, err := r.Store.RouteNew(ctx, now, routeBatch)
	keep(err)
	r.count("placed", res.Placed)
	r.count("after_hours_reply", res.Replies)

	every := r.SlowEvery
	if every <= 0 {
		every = DefaultSlowEvery
	}
	if r.lastSlow.IsZero() || now.Before(r.lastSlow) || now.Sub(r.lastSlow) >= every {
		r.lastSlow = now
		n, err := r.Store.ReleaseOrphans(ctx)
		keep(err)
		r.count("returned", n)
		n, err = r.Store.ReturnStale(ctx, now)
		keep(err)
		r.count("returned", n)
	}

	keep(r.assignWaiting(ctx, now))

	durs, err := r.Store.CountFirstResponses(ctx)
	keep(err)
	if r.Metrics != nil {
		for _, d := range durs {
			r.Metrics.FirstResponse.Observe(d.Seconds())
		}
	}
	return first
}

func (r *Router) availability(ctx context.Context) Availability {
	var av Availability
	states, err := r.Live.Agents(ctx)
	if err != nil {
		if r.Metrics != nil {
			r.Metrics.LiveDown.Inc()
		}
		return av // static members only
	}
	av.States = states
	if states == nil {
		av.States = map[string]queue.AgentState{}
	}
	if busy, err := r.Live.BusyExtensions(ctx); err == nil {
		av.OnCall = busy
	}
	return av
}

func (r *Router) assignWaiting(ctx context.Context, now time.Time) error {
	qs, err := r.Store.TextQueues(ctx)
	if err != nil || len(qs) == 0 {
		return err
	}
	var av *Availability
	var first error
	for _, q := range qs {
		waiting, err := r.Store.Waiting(ctx, q.ID, waitingBatch)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if r.Metrics != nil {
			r.Metrics.QueueWaiting.WithLabelValues(q.Name).Set(float64(len(waiting)))
		}
		if len(waiting) == 0 {
			continue
		}
		agents, err := r.Store.Agents(ctx, q.ID)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if av == nil {
			a := r.availability(ctx)
			av = &a
		}
		left := 0
		stamp := now
		for _, w := range waiting {
			var rr uint64
			if q.Strategy == queue.RoundRobin {
				if rr, err = r.Store.NextRotation(ctx, q.ID); err != nil {
					return err
				}
			}
			c := Choose(q, agents, *av, w.Exclude, rr, queue.Seed(q.Name, strconv.FormatInt(w.ID, 10)))
			if c.Shared {
				left++
				continue
			}
			assigned := false
		offer:
			for _, user := range c.Order {
				out, err := r.Store.Assign(ctx, w.ID, user, q)
				if err != nil {
					r.log().Warn("assign conversation", "conversation", w.ID, "queue", q.Name, "error", err)
					break
				}
				switch out {
				case messaging.AssignDone:
					assigned = true
					r.count("assigned", 1)
					// The store stamps each assignment with its own clock; do
					// the same here so the next conversation sees who went last.
					stamp = stamp.Add(time.Microsecond)
					for i := range agents {
						if agents[i].UserID == user {
							agents[i].Open++
							agents[i].Answered++
							agents[i].LastEnd = stamp
						}
					}
					break offer
				case messaging.AssignFull:
					r.count("full", 1)
					for i := range agents {
						if agents[i].UserID == user {
							agents[i].Open = q.Capacity
						}
					}
				case messaging.AssignGone:
					r.count("lost", 1)
					assigned = true // nothing left to place
					break offer
				}
			}
			if !assigned {
				left++
			}
		}
		if r.Metrics != nil {
			r.Metrics.QueueWaiting.WithLabelValues(q.Name).Set(float64(left))
		}
	}
	return first
}
