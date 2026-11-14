// Package router is the assignment side of the unified inbox (spec
// messaging-channels M-13): it places new conversations by their number's
// route and hands the ones that wait in a text queue to the agents the queue's
// strategy picks. It has no SIP in it. The ordering is queue.Plan, the same
// function the call queues use, fed with the agents' text numbers; the agents'
// login and pause state is the one the call queues keep in Valkey. What is
// stored and compare-and-set lives in internal/store.
package router

import (
	"time"

	"github.com/azrtydxb/hello/internal/queue"
)

// Queue is the part of a queue's configuration the router needs.
type Queue struct {
	ID       int64
	Name     string
	Strategy string
	// Capacity is how many open conversations an agent holds at once.
	Capacity int
	// RequiresIdleVoice keeps an agent who is on a call from being offered a
	// conversation.
	RequiresIdleVoice bool
	// ReassignSeconds is how long an assigned conversation may wait for a
	// reply before it returns to the pool; 0 never.
	ReassignSeconds int
}

// Agent is a queue member whose extension is linked to a user, with what the
// router needs to rank them.
type Agent struct {
	UserID   int64
	Ext      string
	Dynamic  bool
	Penalty  int
	Position int
	// Open is how many open conversations the user holds now.
	Open int
	// Answered counts the text conversations the queue gave the user.
	Answered int
	// LastEnd is when the queue last gave the user a conversation; zero
	// when never.
	LastEnd time.Time
}

// Availability is what the shared agent state says right now.
type Availability struct {
	// States is every agent's login and pause state by extension number. A
	// nil map means the state could not be read: only static members count,
	// as the call queues degrade.
	States map[string]queue.AgentState
	// OnCall is the extensions with a call in progress.
	OnCall map[string]bool
}

// Choice is the outcome of Choose.
type Choice struct {
	// Order is the users to offer the conversation to, best first. For a
	// shared pool it is every eligible user, unordered by preference.
	Order []int64
	// Shared is true for ring_all: the conversation stays unassigned and
	// every eligible agent sees it; the first to reply takes it.
	Shared bool
}

// Choose ranks the agents that may take a conversation now: staffed (logged
// in when dynamic, not paused), under the queue's capacity, not on a call
// when the queue says so, and not the excluded user (the one who just let a
// conversation lapse). A user linked to several member extensions counts
// once, by their best one. The order is queue.Plan's, with the agents' text
// counts as Calls and LastEnd; rr is the queue's text rotation counter and
// seed makes the random strategy deterministic per conversation.
func Choose(q Queue, agents []Agent, av Availability, exclude int64, rr, seed uint64) Choice {
	best := map[int64]Agent{}
	var users []int64
	for _, a := range agents {
		if a.UserID == exclude || a.Open >= q.Capacity {
			continue
		}
		var st queue.AgentState
		known := false
		if av.States != nil {
			st, known = av.States[a.Ext]
		}
		if !(queue.Member{Ext: a.Ext, Dynamic: a.Dynamic}).Staffed(q.Name, st, known) {
			continue
		}
		if q.RequiresIdleVoice && av.OnCall[a.Ext] {
			continue
		}
		cur, ok := best[a.UserID]
		if !ok {
			users = append(users, a.UserID)
		}
		if !ok || a.Penalty < cur.Penalty || (a.Penalty == cur.Penalty && a.Position < cur.Position) {
			best[a.UserID] = a
		}
	}
	userOf := map[string]int64{}
	cands := make([]queue.Candidate, 0, len(users))
	for _, u := range users {
		a := best[u]
		userOf[a.Ext] = u
		cands = append(cands, queue.Candidate{Ext: a.Ext, Penalty: a.Penalty, Position: a.Position, Calls: a.Answered, LastEnd: a.LastEnd})
	}
	var out Choice
	out.Shared = q.Strategy == queue.RingAll
	for _, round := range queue.Plan(q.Strategy, cands, rr, seed) {
		for _, ext := range round {
			out.Order = append(out.Order, userOf[ext])
		}
	}
	return out
}
