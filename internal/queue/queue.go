// Package queue is the state and the pure logic of ACD call queues
// (migration 00036). The configuration lives in PostgreSQL and reaches
// hello-sip as a snapshot; what changes while calls wait (the waiting
// callers, which agent is logged in or paused, who has just finished a call)
// lives in the Store, in Valkey, so every SIP node and hello-control see the
// same queue. Nothing here touches SIP or media: the strategies are pure
// functions over plain values, so they unit-test as tables.
package queue

import (
	"errors"
	"hash/fnv"
	"slices"
	"time"
)

// Ring strategies.
const (
	RingAll     = "ring_all"
	LongestIdle = "longest_idle"
	RoundRobin  = "round_robin"
	FewestCalls = "fewest_calls"
	Random      = "random"
	Linear      = "linear"
)

// Strategies lists the ring strategies in the order the console shows them.
var Strategies = []string{RingAll, LongestIdle, RoundRobin, FewestCalls, Random, Linear}

// ValidStrategy reports whether s is a ring strategy.
func ValidStrategy(s string) bool { return slices.Contains(Strategies, s) }

// ErrFull means the queue already holds its maximum of waiting callers.
var ErrFull = errors.New("queue: full")

// Entry is one waiting caller. The node that holds the caller's dialog owns
// the entry and refreshes it; an entry nobody refreshes goes stale and is
// dropped, unless a surviving node adopts the call first (the caller then
// keeps its place, because EnteredMs is kept).
type Entry struct {
	CallID     string `json:"callId"` // the call's correlation id
	Queue      string `json:"queue"`
	Caller     string `json:"caller"`
	CallerName string `json:"callerName,omitempty"`
	// EnteredMs is when the caller joined (Unix ms, on the store's clock): the
	// waiting order is (EnteredMs, CallID).
	EnteredMs int64  `json:"enteredMs"`
	Node      string `json:"node"`
	// Offering is the extension currently ringing for this caller, "" when
	// none; it also tells the callers behind that this one is being served.
	Offering string `json:"offering,omitempty"`
	// HeartbeatMs is the owner's last refresh (Unix ms, store clock).
	HeartbeatMs int64 `json:"heartbeatMs"`
}

// Entered is when the caller joined.
func (e Entry) Entered() time.Time { return time.UnixMilli(e.EnteredMs) }

// EntryTTL is how long an entry survives without its owner's refresh. It
// outlasts a takeover (a few seconds), so a survivor adopts the call before
// the entry is dropped.
const EntryTTL = 20 * time.Second

// AgentState is an agent's login and pause state; it is global to the
// extension, shared by every queue the agent is a member of.
type AgentState struct {
	Ext string `json:"ext"`
	// LoggedIn is the dynamic members' availability switch. Static members
	// are always logged in.
	LoggedIn bool `json:"loggedIn"`
	// Queues limits the login to those queue names; empty means every queue
	// the agent is a dynamic member of.
	Queues []string `json:"queues,omitempty"`
	Paused bool     `json:"paused"`
	Reason string   `json:"reason,omitempty"`
	// SinceMs is when the state last changed (Unix ms).
	SinceMs int64 `json:"sinceMs"`
}

// LoggedInTo reports whether the agent is logged in to the queue.
func (a AgentState) LoggedInTo(queue string) bool {
	return a.LoggedIn && (len(a.Queues) == 0 || slices.Contains(a.Queues, queue))
}

// AgentStats is what the strategies need to know about an agent in one
// queue.
type AgentStats struct {
	Calls int `json:"calls"` // calls answered
	// LastEndMs is when the last call ended (Unix ms); 0 never.
	LastEndMs int64 `json:"lastEndMs"`
	// WrapUntilMs is the end of the wrap-up time after the last call.
	WrapUntilMs int64 `json:"wrapUntilMs"`
}

// LastEnd is when the agent's last call ended; zero when never.
func (s AgentStats) LastEnd() time.Time {
	if s.LastEndMs == 0 {
		return time.Time{}
	}
	return time.UnixMilli(s.LastEndMs)
}

// Wrapping reports whether the agent is still in wrap-up time at now.
func (s AgentStats) Wrapping(now time.Time) bool { return now.UnixMilli() < s.WrapUntilMs }

// Member is a queue's configured agent.
type Member struct {
	Ext      string
	Dynamic  bool
	Penalty  int
	Position int
}

// Staffed reports whether the member is logged in and not paused as far as
// the shared state goes: a static member always is (unless paused), a
// dynamic one when logged in to the queue. Whether the phone is busy, in DND
// or registered is the SIP node's to judge.
func (m Member) Staffed(queue string, st AgentState, known bool) bool {
	if known && st.Paused {
		return false
	}
	if !m.Dynamic {
		return true
	}
	return known && st.LoggedInTo(queue)
}

// Candidate is a member that may be rung, with the numbers the strategies
// order by.
type Candidate struct {
	Ext      string
	Penalty  int
	Position int
	Calls    int
	LastEnd  time.Time // zero: never had a call
}

// Plan orders the candidates into rounds: the agents of one round ring
// together, rounds are tried in order. Lower penalties come first and a
// higher penalty is only reached once every agent below it has been tried.
// ring_all rings each penalty tier at once; every other strategy rings one
// agent at a time. rr is the queue's rotation counter (round robin) and
// seed makes the random order deterministic per call.
func Plan(strategy string, cands []Candidate, rr, seed uint64) [][]string {
	tiers := map[int][]Candidate{}
	var penalties []int
	for _, c := range cands {
		if _, ok := tiers[c.Penalty]; !ok {
			penalties = append(penalties, c.Penalty)
		}
		tiers[c.Penalty] = append(tiers[c.Penalty], c)
	}
	slices.Sort(penalties)
	var rounds [][]string
	for _, p := range penalties {
		tier := tiers[p]
		slices.SortStableFunc(tier, func(a, b Candidate) int { return a.Position - b.Position })
		switch strategy {
		case RingAll:
			round := make([]string, len(tier))
			for i, c := range tier {
				round[i] = c.Ext
			}
			rounds = append(rounds, round)
			continue
		case LongestIdle:
			slices.SortStableFunc(tier, func(a, b Candidate) int { return a.LastEnd.Compare(b.LastEnd) })
		case FewestCalls:
			slices.SortStableFunc(tier, func(a, b Candidate) int {
				if a.Calls != b.Calls {
					return a.Calls - b.Calls
				}
				return a.LastEnd.Compare(b.LastEnd)
			})
		case RoundRobin:
			// The rotation starts one past the agent the previous call
			// started with; the tier keeps its position order.
			start := int(rr % uint64(len(tier))) //nolint:gosec // bounded by the tier's length
			tier = append(slices.Clone(tier[start:]), tier[:start]...)
		case Random:
			s := seed + uint64(p) //nolint:gosec // penalties are 0-99
			for i := len(tier) - 1; i > 0; i-- {
				j := int(mix(&s) % uint64(i+1)) //nolint:gosec // bounded by the tier's length
				tier[i], tier[j] = tier[j], tier[i]
			}
		}
		for _, c := range tier {
			rounds = append(rounds, []string{c.Ext})
		}
	}
	return rounds
}

func mix(state *uint64) uint64 {
	*state += 0x9e3779b97f4a7c15
	z := *state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// Seed makes a call's random order deterministic.
func Seed(queue, callID string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(queue))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(callID))
	return h.Sum64()
}

// DefaultHandle is the average call length assumed until the queue has
// measured its own.
const DefaultHandle = 3 * time.Minute

// EstimatedWait is the expected wait of the caller in position pos (1 is
// next): the agents work through the callers ahead, each taking avgHandle
// per call. With no staffed agent nobody can tell, and the estimate is zero.
func EstimatedWait(pos int, avgHandle time.Duration, staffed int) time.Duration {
	if pos < 1 || staffed < 1 {
		return 0
	}
	if avgHandle <= 0 {
		avgHandle = DefaultHandle
	}
	return time.Duration(pos-1) * avgHandle / time.Duration(staffed)
}

// Minutes rounds a wait up to whole minutes, at least one.
func Minutes(d time.Duration) int {
	return max(int((d+time.Minute-1)/time.Minute), 1)
}

// Position is the 1-based place of callID among the entries (sorted by
// Sort), 0 when it is not there.
func Position(entries []Entry, callID string) int {
	for i, e := range entries {
		if e.CallID == callID {
			return i + 1
		}
	}
	return 0
}

// Sort puts entries in waiting order: the longest waiting first.
func Sort(entries []Entry) {
	slices.SortFunc(entries, func(a, b Entry) int {
		switch {
		case a.EnteredMs < b.EnteredMs:
			return -1
		case a.EnteredMs > b.EnteredMs:
			return 1
		case a.CallID < b.CallID:
			return -1
		case a.CallID > b.CallID:
			return 1
		}
		return 0
	})
}

// NextInLine reports whether callID may be offered to an agent now: every
// caller ahead of it is already being offered (has an agent ringing), so it
// is the longest waiting caller nobody is serving. Callers behind wait their
// turn, which keeps the order fair across the nodes that own them.
func NextInLine(entries []Entry, callID string) bool {
	for _, e := range entries {
		if e.CallID == callID {
			return true
		}
		if e.Offering == "" {
			return false
		}
	}
	return false
}
