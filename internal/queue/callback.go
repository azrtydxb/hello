package queue

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"
)

// Queue callback (pbx-gaps S-9, S-10, migration 00049). A caller who would
// wait long can ask to be called back: the request is a durable row, so it
// survives the call, a restart and the loss of a node. A sweep on every SIP
// node claims the head request when an agent could take it, rings the caller
// first and the agent after the caller has answered.

// Callback statuses.
const (
	CallbackWaiting   = "waiting"   // due at NextAttemptAt
	CallbackDialling  = "dialling"  // claimed: the caller is being rung, or is on the line waiting for an agent
	CallbackConnected = "connected" // the caller and an agent were connected
	CallbackFailed    = "failed"    // every attempt failed
	CallbackCancelled = "cancelled" // an operator cancelled it
	CallbackExpired   = "expired"   // nobody got to it in time
)

// Callback timing and limits (decision D8).
const (
	CallbackMaxAttempts = 3
	CallbackRetryAfter  = 2 * time.Minute
	CallbackTTL         = 2 * time.Hour
	// CallbackLease is how long a claim lasts without a renewal: a node that
	// dies mid-callback strands the row for at most this long.
	CallbackLease = 60 * time.Second
	// CallbackMaxWaiting bounds the open requests of one queue.
	CallbackMaxWaiting = 100
	// CallbackKeep is how long finished rows stay readable.
	CallbackKeep = 7 * 24 * time.Hour
	// DefaultCallbackOffer is the estimated wait, in seconds, above which a
	// callback is offered.
	DefaultCallbackOffer = 120
	// DefaultCallbackDigit accepts the offer.
	DefaultCallbackDigit = "2"
)

// Callback errors.
var (
	// ErrCallbackFull means the queue already holds its maximum of open
	// callback requests.
	ErrCallbackFull = errors.New("queue: callback requests full")
	// ErrCallbackNotFound means no such request, or it is no longer open.
	ErrCallbackNotFound = errors.New("queue: callback request not found")
)

// Callback is one callback request.
type Callback struct {
	ID         int64  `json:"id"`
	QueueID    int64  `json:"queueId"`
	Number     string `json:"number"`
	CallerName string `json:"callerName"`
	Status     string `json:"status"`
	Attempts   int    `json:"attempts"`
	// PositionKey is the caller's original enqueue time (Unix ms): the
	// request sorts by it among the callers waiting, so it keeps its place.
	PositionKey   int64     `json:"positionKey"`
	NextAttemptAt time.Time `json:"nextAttemptAt"`
	ClaimedBy     string    `json:"claimedBy"`
	LeaseUntil    time.Time `json:"leaseUntil,omitzero"`
	Agent         string    `json:"agent"`
	Result        string    `json:"result"`
	CreatedAt     time.Time `json:"createdAt"`
	ExpiresAt     time.Time `json:"expiresAt"`
	FinishedAt    time.Time `json:"finishedAt,omitzero"`
}

// Open reports whether the request can still be served.
func (c Callback) Open() bool { return c.Status == CallbackWaiting || c.Status == CallbackDialling }

// CallbackStore is the callback requests' durable state (PostgreSQL in
// production, MemCallbacks in tests). Every time it reads is the store's
// own clock, so nodes with skewed clocks agree on leases.
type CallbackStore interface {
	// Request records a callback for number in the queue. A number that
	// already has an open request in the queue is coalesced into it
	// (created false). ErrCallbackFull when the queue holds the maximum.
	Request(ctx context.Context, queueID int64, number, name string, positionKey int64) (cb Callback, created bool, err error)
	// Head is the request the next claim would take: the open one with the
	// lowest PositionKey that is due (waiting past its NextAttemptAt, or
	// dialling with a lapsed lease). Expiry is applied first.
	Head(ctx context.Context, queueID int64) (Callback, bool, error)
	// Claim takes the Head for node under a fresh lease and counts the
	// attempt, atomically: two nodes never claim one row. A row whose lease
	// lapsed on its last attempt becomes failed instead.
	Claim(ctx context.Context, queueID int64, node string) (Callback, bool, error)
	// Renew extends the lease of a dialling request and makes node its
	// holder (a survivor that took the call over adopts it this way).
	Renew(ctx context.Context, id int64, node string) error
	// Fail ends the attempt: the request waits for its next attempt, or is
	// failed once the attempts are used up (final).
	Fail(ctx context.Context, id int64, node, result string) (final bool, err error)
	// Connect marks the request connected to agent.
	Connect(ctx context.Context, id int64, node, agent string) error
	// Cancel withdraws an open request.
	Cancel(ctx context.Context, queueID, id int64) error
	// Due reads, for every queue with one, the lowest PositionKey among its
	// requests that are due or being served (waiting past NextAttemptAt, or
	// dialling). A waiting call that entered later does not take an agent
	// ahead of such a request. One cheap read serves every queue.
	Due(ctx context.Context) (map[int64]int64, error)
	// Dialling counts the requests whose lease is live: callers being rung
	// or waiting on the line for an agent.
	Dialling(ctx context.Context, queueID int64) (int, error)
	// List returns the queue's requests, newest first, open ones included.
	List(ctx context.Context, queueID int64) ([]Callback, error)
	// Purge deletes finished requests older than CallbackKeep.
	Purge(ctx context.Context) error
}

// MemCallbacks is the in-process CallbackStore of tests: the same rules as
// the PostgreSQL one, with a clock a test can move.
type MemCallbacks struct {
	// Now is the store's clock; time.Now when nil.
	Now func() time.Time

	mu   sync.Mutex
	next int64
	rows []*Callback
}

// NewMemCallbacks returns an empty store.
func NewMemCallbacks() *MemCallbacks { return &MemCallbacks{} }

func (m *MemCallbacks) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// settle applies expiry and exhausted leases (m.mu held).
func (m *MemCallbacks) settle(now time.Time) {
	for _, r := range m.rows {
		switch {
		case !r.Open():
		case !r.ExpiresAt.After(now):
			r.Status, r.Result, r.FinishedAt = CallbackExpired, "expired", now
		case r.Status == CallbackDialling && r.LeaseUntil.Before(now) && r.Attempts >= CallbackMaxAttempts:
			r.Status, r.Result, r.FinishedAt = CallbackFailed, "node_lost", now
		}
	}
}

func (m *MemCallbacks) openCount(queueID int64) int {
	n := 0
	for _, r := range m.rows {
		if r.QueueID == queueID && r.Open() {
			n++
		}
	}
	return n
}

// Request implements CallbackStore.
func (m *MemCallbacks) Request(_ context.Context, queueID int64, number, name string, positionKey int64) (Callback, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.settle(now)
	for _, r := range m.rows {
		if r.QueueID == queueID && r.Number == number && r.Open() {
			return *r, false, nil
		}
	}
	if m.openCount(queueID) >= CallbackMaxWaiting {
		return Callback{}, false, ErrCallbackFull
	}
	m.next++
	r := &Callback{ID: m.next, QueueID: queueID, Number: number, CallerName: name, Status: CallbackWaiting,
		PositionKey: positionKey, NextAttemptAt: now, CreatedAt: now, ExpiresAt: now.Add(CallbackTTL)}
	m.rows = append(m.rows, r)
	return *r, true, nil
}

func (m *MemCallbacks) head(queueID int64, now time.Time) *Callback {
	var best *Callback
	for _, r := range m.rows {
		if r.QueueID != queueID {
			continue
		}
		due := (r.Status == CallbackWaiting && !r.NextAttemptAt.After(now)) ||
			(r.Status == CallbackDialling && r.LeaseUntil.Before(now))
		if !due {
			continue
		}
		if best == nil || r.PositionKey < best.PositionKey || (r.PositionKey == best.PositionKey && r.ID < best.ID) {
			best = r
		}
	}
	return best
}

// Head implements CallbackStore.
func (m *MemCallbacks) Head(_ context.Context, queueID int64) (Callback, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.settle(now)
	if r := m.head(queueID, now); r != nil {
		return *r, true, nil
	}
	return Callback{}, false, nil
}

// Claim implements CallbackStore.
func (m *MemCallbacks) Claim(_ context.Context, queueID int64, node string) (Callback, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.settle(now)
	r := m.head(queueID, now)
	if r == nil {
		return Callback{}, false, nil
	}
	r.Status, r.ClaimedBy, r.LeaseUntil = CallbackDialling, node, now.Add(CallbackLease)
	r.Attempts++
	return *r, true, nil
}

func (m *MemCallbacks) find(id int64) *Callback {
	for _, r := range m.rows {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// Renew implements CallbackStore.
func (m *MemCallbacks) Renew(_ context.Context, id int64, node string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.find(id)
	if r == nil || r.Status != CallbackDialling {
		return ErrCallbackNotFound
	}
	r.ClaimedBy, r.LeaseUntil = node, m.now().Add(CallbackLease)
	return nil
}

// Fail implements CallbackStore.
func (m *MemCallbacks) Fail(_ context.Context, id int64, node, result string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.find(id)
	if r == nil || r.Status != CallbackDialling || r.ClaimedBy != node {
		return false, ErrCallbackNotFound
	}
	now := m.now()
	r.Result = result
	if r.Attempts >= CallbackMaxAttempts {
		r.Status, r.FinishedAt = CallbackFailed, now
		return true, nil
	}
	r.Status, r.ClaimedBy, r.LeaseUntil, r.NextAttemptAt = CallbackWaiting, "", time.Time{}, now.Add(CallbackRetryAfter)
	return false, nil
}

// Connect implements CallbackStore.
func (m *MemCallbacks) Connect(_ context.Context, id int64, node, agent string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.find(id)
	if r == nil || r.Status != CallbackDialling || r.ClaimedBy != node {
		return ErrCallbackNotFound
	}
	r.Status, r.Agent, r.Result, r.FinishedAt = CallbackConnected, agent, "connected", m.now()
	return nil
}

// Cancel implements CallbackStore.
func (m *MemCallbacks) Cancel(_ context.Context, queueID, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.find(id)
	if r == nil || r.QueueID != queueID || !r.Open() {
		return ErrCallbackNotFound
	}
	r.Status, r.Result, r.FinishedAt = CallbackCancelled, "cancelled", m.now()
	return nil
}

// Due implements CallbackStore.
func (m *MemCallbacks) Due(_ context.Context) (map[int64]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.settle(now)
	out := map[int64]int64{}
	for _, r := range m.rows {
		due := (r.Status == CallbackWaiting && !r.NextAttemptAt.After(now)) || r.Status == CallbackDialling
		if !due {
			continue
		}
		if k, ok := out[r.QueueID]; !ok || r.PositionKey < k {
			out[r.QueueID] = r.PositionKey
		}
	}
	return out, nil
}

// Dialling implements CallbackStore.
func (m *MemCallbacks) Dialling(_ context.Context, queueID int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	n := 0
	for _, r := range m.rows {
		if r.QueueID == queueID && r.Status == CallbackDialling && !r.LeaseUntil.Before(now) {
			n++
		}
	}
	return n, nil
}

// List implements CallbackStore.
func (m *MemCallbacks) List(_ context.Context, queueID int64) ([]Callback, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settle(m.now())
	var out []Callback
	for _, r := range m.rows {
		if r.QueueID == queueID {
			out = append(out, *r)
		}
	}
	slices.SortFunc(out, func(a, b Callback) int { return int(b.ID - a.ID) })
	return out, nil
}

// Purge implements CallbackStore.
func (m *MemCallbacks) Purge(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cut := m.now().Add(-CallbackKeep)
	m.rows = slices.DeleteFunc(m.rows, func(r *Callback) bool { return !r.FinishedAt.IsZero() && r.FinishedAt.Before(cut) })
	return nil
}

// AheadOfCallback reports whether a waiting caller holds a place before the
// callback's (it entered earlier) and has no agent ringing yet: such a call
// is served first. A caller that is already being offered does not hold the
// callback back, since its agent is taken.
func AheadOfCallback(entries []Entry, positionKey int64) bool {
	for _, e := range entries {
		if e.EnteredMs < positionKey && e.Offering == "" {
			return true
		}
	}
	return false
}
