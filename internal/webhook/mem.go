package webhook

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/events"
)

// Mem is an in-memory Store with the semantics of PG, for tests that need no
// database. Store the events deliveries refer to with PutEvent.
type Mem struct {
	mu       sync.Mutex
	nextEp   int64
	nextDel  int64
	eps      map[int64]*memEndpoint
	dels     map[int64]*memDelivery
	events   map[string][]byte
	Audit    []string // "actor action resource id", in order
	attempts map[int64][]Attempt
}

type memEndpoint struct {
	Endpoint
}

type memDelivery struct {
	Delivery
	series  int
	next    time.Time
	claimed time.Time
}

// NewMem returns an empty store.
func NewMem() *Mem {
	return &Mem{eps: map[int64]*memEndpoint{}, dels: map[int64]*memDelivery{}, events: map[string][]byte{}, attempts: map[int64][]Attempt{}}
}

// PutEvent stores the payload deliveries of event id read.
func (m *Mem) PutEvent(id string, payload []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events[id] = payload
}

func (m *Mem) audit(actor, action, resource string, id any) {
	m.Audit = append(m.Audit, actor+" "+action+" "+resource+" "+itoa(id))
}

func itoa(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// NextEndpointID implements Store.
func (m *Mem) NextEndpointID(context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextEp++
	return m.nextEp, nil
}

func (m *Mem) view(e *memEndpoint) Endpoint {
	out := e.Endpoint
	out.Events, out.Extensions = slices.Clone(nonNil(out.Events)), slices.Clone(nonNil(out.Extensions))
	out.Pending, out.Dead = 0, 0
	for _, d := range m.dels {
		if d.EndpointID != e.ID {
			continue
		}
		switch d.Status {
		case StatusPending, StatusRetrying:
			out.Pending++
		case StatusDead:
			out.Dead++
		}
	}
	return out
}

// CreateEndpoint implements Store.
func (m *Mem) CreateEndpoint(_ context.Context, actor string, e Endpoint) (Endpoint, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.eps {
		if x.URL == e.URL && slices.Equal(nonNil(x.Events), nonNil(e.Events)) {
			return m.view(x), false, nil
		}
	}
	now := time.Now().UTC()
	e.CreatedBy, e.CreatedAt, e.UpdatedAt = actor, now, now
	e.Events, e.Extensions = slices.Clone(nonNil(e.Events)), slices.Clone(nonNil(e.Extensions))
	m.eps[e.ID] = &memEndpoint{e}
	m.audit(actor, "create", "webhook_endpoint", e.ID)
	return m.view(m.eps[e.ID]), true, nil
}

// GetEndpoint implements Store.
func (m *Mem) GetEndpoint(_ context.Context, id int64) (Endpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.eps[id]
	if !ok {
		return Endpoint{}, ErrNotFound
	}
	return m.view(e), nil
}

// ListEndpoints implements Store.
func (m *Mem) ListEndpoints(context.Context) ([]Endpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]int64, 0, len(m.eps))
	for id := range m.eps {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := make([]Endpoint, 0, len(ids))
	for _, id := range ids {
		out = append(out, m.view(m.eps[id]))
	}
	return out, nil
}

// UpdateEndpoint implements Store.
func (m *Mem) UpdateEndpoint(_ context.Context, actor string, e Endpoint, resetHealth bool) (Endpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.eps[e.ID]
	if !ok {
		return Endpoint{}, ErrNotFound
	}
	cur.Name, cur.URL, cur.Enabled, cur.AllowPrivate, cur.DisabledReason = e.Name, e.URL, e.Enabled, e.AllowPrivate, e.DisabledReason
	cur.Events, cur.Extensions = slices.Clone(nonNil(e.Events)), slices.Clone(nonNil(e.Extensions))
	if resetHealth {
		cur.ConsecutiveFailures = 0
	}
	cur.UpdatedAt = time.Now().UTC()
	m.audit(actor, "update", "webhook_endpoint", e.ID)
	return m.view(cur), nil
}

// SetSecrets implements Store.
func (m *Mem) SetSecrets(_ context.Context, actor string, id int64, current, previous []byte, previousExpires *time.Time) (Endpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.eps[id]
	if !ok {
		return Endpoint{}, ErrNotFound
	}
	cur.SecretEnc, cur.PreviousSecretEnc, cur.PreviousSecretExpires = current, previous, previousExpires
	m.audit(actor, "rotate-secret", "webhook_endpoint", id)
	return m.view(cur), nil
}

// DeleteEndpoint implements Store.
func (m *Mem) DeleteEndpoint(_ context.Context, actor string, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.eps[id]; !ok {
		return ErrNotFound
	}
	delete(m.eps, id)
	for did, d := range m.dels {
		if d.EndpointID == id {
			delete(m.dels, did)
			delete(m.attempts, did)
		}
	}
	m.audit(actor, "delete", "webhook_endpoint", id)
	return nil
}

// Enqueue implements Store.
func (m *Mem) Enqueue(_ context.Context, _ *sql.Tx, e events.Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.events[e.ID]; !ok {
		b, _ := json.Marshal(e.Event)
		m.events[e.ID] = b
	}
	for _, ep := range m.eps {
		if !ep.Enabled {
			continue
		}
		f := events.Filter{Types: ep.Events}
		if len(ep.Extensions) > 0 {
			f.Allowed = ep.Extensions
		}
		if !f.Matches(e.Event) {
			continue
		}
		if slices.ContainsFunc(mapValues(m.dels), func(d *memDelivery) bool { return d.EndpointID == ep.ID && d.EventID == e.ID }) {
			continue
		}
		m.nextDel++
		now := time.Now().UTC()
		m.dels[m.nextDel] = &memDelivery{
			Delivery: Delivery{ID: m.nextDel, EndpointID: ep.ID, EventID: e.ID, EventType: e.Type, Status: StatusPending, CreatedAt: now, UpdatedAt: now},
			next:     now,
		}
	}
	return nil
}

func mapValues[K comparable, V any](m map[K]V) []V {
	out := make([]V, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func (m *Mem) sorted() []*memDelivery {
	out := mapValues(m.dels)
	slices.SortFunc(out, func(a, b *memDelivery) int {
		switch {
		case a.next.Before(b.next):
			return -1
		case a.next.After(b.next):
			return 1
		}
		return int(a.ID - b.ID)
	})
	return out
}

// Claim implements Store.
func (m *Mem) Claim(_ context.Context, now time.Time, limit, perEndpoint int, lease time.Duration) ([]Claim, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	busy := map[int64]int{}
	for _, d := range m.dels {
		if d.claimed.After(now) {
			busy[d.EndpointID]++
		}
	}
	var out []Claim
	for _, d := range m.sorted() {
		if len(out) >= limit {
			break
		}
		ep, ok := m.eps[d.EndpointID]
		if !ok || !ep.Enabled || (d.Status != StatusPending && d.Status != StatusRetrying) ||
			d.next.After(now) || d.claimed.After(now) || busy[d.EndpointID] >= perEndpoint {
			continue
		}
		d.claimed = now.Add(lease)
		busy[d.EndpointID]++
		c := Claim{Delivery: d.Delivery, SeriesAttempt: d.series, URL: ep.URL, AllowPrivate: ep.AllowPrivate,
			SecretEnc: ep.SecretEnc, PreviousSecretEnc: ep.PreviousSecretEnc, PreviousSecretExpires: ep.PreviousSecretExpires,
			Payload: m.events[d.EventID]}
		out = append(out, c)
	}
	return out, nil
}

// Finish implements Store.
func (m *Mem) Finish(_ context.Context, f Finish) (FinishResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.dels[f.DeliveryID]
	if !ok {
		return FinishResult{}, ErrNotFound
	}
	d.Status, d.next, d.claimed = StatusSucceeded, f.Now, time.Time{}
	switch f.Outcome {
	case OutcomeRetry:
		d.Status, d.next = StatusRetrying, f.NextAttemptAt
	case OutcomeDead, OutcomeGone:
		d.Status = StatusDead
	}
	d.Attempts++
	d.series++
	var code *int
	if f.Status != 0 {
		v := f.Status
		code = &v
	}
	lat := f.LatencyMs
	d.LastStatus, d.LastError, d.LastLatencyMs, d.UpdatedAt = code, f.Error, &lat, f.Now
	if d.Status == StatusSucceeded {
		t := f.Now
		d.DeliveredAt = &t
	}
	m.attempts[d.ID] = append(m.attempts[d.ID], Attempt{Attempt: d.Attempts, Ref: f.Ref, At: f.Now, Status: code, Error: f.Error, LatencyMs: f.LatencyMs})
	ep, ok := m.eps[d.EndpointID]
	if !ok {
		return FinishResult{}, nil
	}
	if f.Internal {
		return FinishResult{}, nil
	}
	ep.LastStatus = code
	if f.Outcome == OutcomeSuccess {
		ep.ConsecutiveFailures = 0
		t := f.Now
		ep.LastSuccessAt = &t
		return FinishResult{}, nil
	}
	ep.ConsecutiveFailures++
	t := f.Now
	ep.LastFailureAt = &t
	reason := ""
	switch {
	case f.Outcome == OutcomeGone:
		reason = ReasonGone
	case f.MaxFailures > 0 && ep.ConsecutiveFailures >= f.MaxFailures:
		reason = ReasonFailures
	}
	if reason == "" || !ep.Enabled {
		return FinishResult{}, nil
	}
	ep.Enabled, ep.DisabledReason = false, reason
	m.audit("system:webhooks", "disable", "webhook_endpoint", ep.ID)
	return FinishResult{Disabled: true, Reason: reason, Endpoint: m.view(ep)}, nil
}

func (m *Mem) publicDelivery(d *memDelivery) Delivery {
	out := d.Delivery
	if out.Status == StatusPending || out.Status == StatusRetrying {
		t := d.next
		out.NextAttemptAt = &t
	} else {
		out.NextAttemptAt = nil
	}
	return out
}

// ListDeliveries implements Store.
func (m *Mem) ListDeliveries(_ context.Context, q DeliveryQuery) ([]Delivery, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Delivery
	ds := mapValues(m.dels)
	slices.SortFunc(ds, func(a, b *memDelivery) int { return int(b.ID - a.ID) })
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	for _, d := range ds {
		if (q.EndpointID != 0 && d.EndpointID != q.EndpointID) || (q.Status != "" && d.Status != q.Status) || (q.Before != 0 && d.ID >= q.Before) {
			continue
		}
		out = append(out, m.publicDelivery(d))
		if len(out) == limit {
			break
		}
	}
	if out == nil {
		out = []Delivery{}
	}
	return out, nil
}

// GetDelivery implements Store.
func (m *Mem) GetDelivery(_ context.Context, id int64) (DeliveryDetail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.dels[id]
	if !ok {
		return DeliveryDetail{}, ErrNotFound
	}
	log := slices.Clone(m.attempts[id])
	if log == nil {
		log = []Attempt{}
	}
	return DeliveryDetail{Delivery: m.publicDelivery(d), Log: log, Event: m.events[d.EventID]}, nil
}

// Redeliver implements Store.
func (m *Mem) Redeliver(_ context.Context, actor string, id int64, now time.Time) (Delivery, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.dels[id]
	if !ok {
		return Delivery{}, ErrNotFound
	}
	if d.claimed.After(now) {
		return Delivery{}, ErrInFlight
	}
	d.Status, d.series, d.next, d.claimed, d.UpdatedAt = StatusPending, 0, now, time.Time{}, now
	m.audit(actor, "redeliver", "webhook_delivery", id)
	return m.publicDelivery(d), nil
}

// RedeliverDead implements Store.
func (m *Mem) RedeliverDead(_ context.Context, actor string, endpointID int64, now time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, d := range m.dels {
		if d.EndpointID == endpointID && d.Status == StatusDead {
			d.Status, d.series, d.next, d.claimed, d.UpdatedAt = StatusPending, 0, now, time.Time{}, now
			n++
		}
	}
	m.audit(actor, "redeliver-dead", "webhook_endpoint", endpointID)
	return n, nil
}

// Purge implements Store.
func (m *Mem) Purge(_ context.Context, succeededBefore, deadBefore time.Time, deadCap int) (PurgeResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var r PurgeResult
	for id, d := range m.dels {
		switch {
		case d.Status == StatusSucceeded && d.UpdatedAt.Before(succeededBefore):
			delete(m.dels, id)
			r.Succeeded++
		case d.Status == StatusDead && d.UpdatedAt.Before(deadBefore):
			delete(m.dels, id)
			r.Dead++
		}
	}
	if deadCap > 0 {
		perEndpoint := map[int64][]*memDelivery{}
		for _, d := range m.dels {
			if d.Status == StatusDead {
				perEndpoint[d.EndpointID] = append(perEndpoint[d.EndpointID], d)
			}
		}
		for _, ds := range perEndpoint {
			slices.SortFunc(ds, func(a, b *memDelivery) int {
				if !a.UpdatedAt.Equal(b.UpdatedAt) {
					if a.UpdatedAt.After(b.UpdatedAt) {
						return -1
					}
					return 1
				}
				return int(b.ID - a.ID)
			})
			for _, d := range ds[min(deadCap, len(ds)):] {
				delete(m.dels, d.ID)
				r.Dropped++
			}
		}
	}
	return r, nil
}

// Counts implements Store.
func (m *Mem) Counts(context.Context) (Counts, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var c Counts
	for _, d := range m.dels {
		switch d.Status {
		case StatusPending, StatusRetrying:
			c.Pending++
		case StatusDead:
			c.Dead++
		}
	}
	for _, e := range m.eps {
		if !e.Enabled {
			c.DisabledEndpoints++
		}
	}
	return c, nil
}

var _ Store = (*Mem)(nil)
