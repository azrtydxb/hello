package queue

import (
	"context"
	"time"
)

// Store is the queues' shared state. Two implementations satisfy it with the
// same semantics: Valkey (every SIP node and hello-control share one) and an
// in-process one for tests and a node without Valkey. Every method honours
// ctx's deadline. All times are on the store's clock, so nodes with skewed
// clocks still agree on the waiting order.
type Store interface {
	// Join puts a caller in the queue and returns its entry, with the store's
	// time as EnteredMs. A call that is already there keeps its entry and its
	// place (a takeover re-joins the same call). An entry with EnteredMs set
	// keeps that time (a taker restoring a dropped entry). maxCallers > 0
	// refuses a new caller with ErrFull once that many wait.
	Join(ctx context.Context, e Entry, maxCallers int) (Entry, error)
	// Touch refreshes the caller's entry for its owner node and records the
	// agent being rung ("" when none). It reports false when the entry is
	// gone.
	Touch(ctx context.Context, queue, callID, node, offering string) (bool, error)
	// Leave removes the caller.
	Leave(ctx context.Context, queue, callID string) error
	// Waiting lists the queue's callers in waiting order, stale ones dropped.
	Waiting(ctx context.Context, queue string) ([]Entry, error)

	// Acquire takes the lease key for token until ttl passes, atomically: it
	// reports false when someone holds it. Release gives it back, only if
	// token still holds it. Holder reports who holds it.
	Acquire(ctx context.Context, key, token string, ttl time.Duration) (bool, error)
	Release(ctx context.Context, key, token string) error
	Holder(ctx context.Context, key string) (string, bool, error)

	// SetAgent replaces an agent's login and pause state.
	SetAgent(ctx context.Context, st AgentState) error
	// Agent reads one agent's state; false when it has none (never logged in).
	Agent(ctx context.Context, ext string) (AgentState, bool, error)
	// Agents reads every agent's state.
	Agents(ctx context.Context) (map[string]AgentState, error)

	// Stats reads the agents' per-queue numbers.
	Stats(ctx context.Context, queue string) (map[string]AgentStats, error)
	// NoteAnswered counts a call the agent answered in the queue.
	NoteAnswered(ctx context.Context, queue, ext string) error
	// NoteEnded records the end of the agent's call: it is idle from now and
	// wrapping up for wrap. talk is the call's length, which feeds the
	// queue's average handling time.
	NoteEnded(ctx context.Context, queue, ext string, talk, wrap time.Duration) error
	// AvgHandle is the queue's average call length; 0 when unmeasured.
	AvgHandle(ctx context.Context, queue string) (time.Duration, error)
	// NextRotation returns the queue's rotation counter and advances it, for
	// the round robin strategy.
	NextRotation(ctx context.Context, queue string) (uint64, error)
}

// CallLease is the lease key of a waiting call: the node holding it is the
// only one offering the call to an agent.
func CallLease(callID string) string { return "call:" + callID }

// AgentLease is the lease key of an agent: whoever holds it is the only one
// ringing the agent.
func AgentLease(ext string) string { return "agent:" + ext }

// MetricsLease is held by the node that publishes the queue gauges.
const MetricsLease = "metrics"

// emaHandle folds one call length into the average handling time (weight
// 1/5), the first call setting it.
func emaHandle(cur, talk time.Duration) time.Duration {
	if talk <= 0 {
		return cur
	}
	if cur <= 0 {
		return talk
	}
	return (cur*4 + talk) / 5
}
