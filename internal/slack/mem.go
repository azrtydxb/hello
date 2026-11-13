package slack

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/events"
)

// Mem is an in-memory Store for tests.
type Mem struct {
	mu    sync.Mutex
	next  int64
	dests map[int64]*Destination
	posts []*memPost
	pid   int64
}

type memPost struct {
	Post
	series  int
	claimed time.Time
	payload []byte
}

// NewMem returns an empty store.
func NewMem() *Mem { return &Mem{dests: map[int64]*Destination{}} }

// NextID implements Store.
func (m *Mem) NextID(context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	return m.next, nil
}

func (m *Mem) view(d *Destination) Destination {
	out := *d
	out.HasSecret = len(out.SecretEnc) > 0
	out.Events, out.Extensions = slices.Clone(d.Events), slices.Clone(d.Extensions)
	for _, p := range m.posts {
		if p.DestinationID == d.ID {
			switch p.Status {
			case StatusPending, StatusRetrying:
				out.Pending++
			case StatusDead:
				out.Dead++
			}
		}
	}
	return out
}

// Create implements Store.
func (m *Mem) Create(_ context.Context, actor string, d Destination) (Destination, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.dests {
		if x.Name == d.Name {
			return Destination{}, ErrConflict
		}
	}
	d.CreatedBy, d.CreatedAt, d.UpdatedAt = actor, time.Now(), time.Now()
	m.dests[d.ID] = &d
	return m.view(&d), nil
}

// Get implements Store.
func (m *Mem) Get(_ context.Context, id int64) (Destination, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.dests[id]
	if !ok {
		return Destination{}, ErrNotFound
	}
	return m.view(d), nil
}

// List implements Store.
func (m *Mem) List(context.Context) ([]Destination, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Destination{}
	for _, d := range m.dests {
		out = append(out, m.view(d))
	}
	slices.SortFunc(out, func(a, b Destination) int { return int(a.ID - b.ID) })
	return out, nil
}

// Update implements Store.
func (m *Mem) Update(_ context.Context, _ string, d Destination, reset bool) (Destination, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.dests[d.ID]
	if !ok {
		return Destination{}, ErrNotFound
	}
	for _, x := range m.dests {
		if x.ID != d.ID && x.Name == d.Name {
			return Destination{}, ErrConflict
		}
	}
	sec := cur.SecretEnc
	if len(d.SecretEnc) > 0 {
		sec = d.SecretEnc
	}
	d.SecretEnc, d.CreatedAt, d.CreatedBy = sec, cur.CreatedAt, cur.CreatedBy
	d.ConsecutiveFailures, d.LastError = cur.ConsecutiveFailures, cur.LastError
	d.LastSuccessAt, d.LastFailureAt = cur.LastSuccessAt, cur.LastFailureAt
	if reset {
		d.ConsecutiveFailures, d.LastError = 0, ""
	}
	d.UpdatedAt = time.Now()
	m.dests[d.ID] = &d
	return m.view(&d), nil
}

// Delete implements Store.
func (m *Mem) Delete(_ context.Context, _ string, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.dests[id]; !ok {
		return ErrNotFound
	}
	delete(m.dests, id)
	m.posts = slices.DeleteFunc(m.posts, func(p *memPost) bool { return p.DestinationID == id })
	return nil
}

// Enqueue implements Store.
func (m *Mem) Enqueue(_ context.Context, _ *sql.Tx, e events.Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !slices.Contains(EventTypes, e.Type) {
		return nil
	}
	payload, _ := json.Marshal(e)
	for _, d := range m.dests {
		f := events.Filter{Types: d.Events, Allowed: d.Extensions}
		if len(f.Allowed) == 0 {
			f.Allowed = nil
		}
		if !d.Enabled || !f.Matches(e.Event) {
			continue
		}
		if slices.ContainsFunc(m.posts, func(p *memPost) bool { return p.DestinationID == d.ID && p.EventID == e.ID }) {
			continue
		}
		m.pid++
		due := time.Unix(0, 0) // due at once, whatever clock a test keeps
		m.posts = append(m.posts, &memPost{Post: Post{ID: m.pid, DestinationID: d.ID, EventID: e.ID, EventType: e.Type,
			Status: StatusPending, NextAttemptAt: &due, CreatedAt: time.Now(), UpdatedAt: time.Now()}, payload: payload})
	}
	return nil
}

// Claim implements Store.
func (m *Mem) Claim(_ context.Context, now time.Time, limit int, lease time.Duration) ([]Claim, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Claim
	for _, p := range m.posts {
		if len(out) >= limit {
			break
		}
		d := m.dests[p.DestinationID]
		if d == nil || !d.Enabled || (p.Status != StatusPending && p.Status != StatusRetrying) ||
			p.NextAttemptAt == nil || p.NextAttemptAt.After(now) || p.claimed.After(now) {
			continue
		}
		p.claimed = now.Add(lease)
		out = append(out, Claim{Post: p.Post, Mode: d.Mode, Channel: d.Channel, SecretEnc: d.SecretEnc, SeriesAttempt: p.series, Payload: p.payload})
	}
	return out, nil
}

// Finish implements Store.
func (m *Mem) Finish(_ context.Context, f Finish) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.posts {
		if p.ID != f.PostID {
			continue
		}
		p.Attempts++
		p.series++
		p.claimed, p.LastError, p.UpdatedAt = time.Time{}, f.Error, f.Now
		d := m.dests[p.DestinationID]
		switch f.Outcome {
		case OutcomeSuccess:
			p.Status, p.NextAttemptAt = StatusSucceeded, nil
			t := f.Now
			p.DeliveredAt = &t
			d.ConsecutiveFailures, d.LastSuccessAt, d.LastError = 0, &t, ""
			return nil
		case OutcomeRetry:
			p.Status = StatusRetrying
			t := f.NextAttemptAt
			p.NextAttemptAt = &t
		default:
			p.Status, p.NextAttemptAt = StatusDead, nil
		}
		t := f.Now
		d.ConsecutiveFailures++
		d.LastFailureAt, d.LastError = &t, f.Error
		reason := ""
		if f.Outcome == OutcomeDisable {
			reason = f.Reason
		} else if f.MaxFailures > 0 && d.ConsecutiveFailures >= f.MaxFailures {
			reason = ReasonFailures
		}
		if reason != "" && d.Enabled {
			d.Enabled, d.DisabledReason = false, reason
		}
		return nil
	}
	return ErrNotFound
}

// ListPosts implements Store.
func (m *Mem) ListPosts(_ context.Context, q PostQuery) ([]Post, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Post{}
	for i := len(m.posts) - 1; i >= 0; i-- {
		p := m.posts[i]
		if (q.DestinationID != 0 && p.DestinationID != q.DestinationID) || (q.Status != "" && p.Status != q.Status) ||
			(q.Before != 0 && p.ID >= q.Before) {
			continue
		}
		out = append(out, p.Post)
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
	}
	return out, nil
}

// Retry implements Store.
func (m *Mem) Retry(_ context.Context, _ string, id int64, now time.Time) (Post, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.posts {
		if p.ID == id {
			if p.Status != StatusDead {
				return Post{}, ErrConflict
			}
			p.Status, p.series, p.NextAttemptAt, p.claimed = StatusPending, 0, &now, time.Time{}
			return p.Post, nil
		}
	}
	return Post{}, ErrNotFound
}

// Purge implements Store.
func (m *Mem) Purge(_ context.Context, succeededBefore, deadBefore time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := len(m.posts)
	m.posts = slices.DeleteFunc(m.posts, func(p *memPost) bool {
		return (p.Status == StatusSucceeded && p.UpdatedAt.Before(succeededBefore)) || (p.Status == StatusDead && p.UpdatedAt.Before(deadBefore))
	})
	return int64(n - len(m.posts)), nil
}

var _ Store = (*Mem)(nil)
