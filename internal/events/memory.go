package events

import (
	"context"
	"slices"
	"strconv"
	"sync"
)

// Memory is an in-process event store with the Service's reading side: live
// subscribers, replay, history and tickets. It has no Valkey or database, so
// handlers and tools that read events can be tested without services.
type Memory struct {
	mu      sync.Mutex
	hub     *hub
	entries []Entry
	tickets map[string][]byte
	seq     int64
	// Down makes Available report false (the stream is not reachable).
	Down bool
}

// NewMemory returns an empty Memory.
func NewMemory() *Memory {
	return &Memory{hub: newHub(nil), tickets: map[string][]byte{}}
}

// Append stores e with the next cursor, delivers it to the live subscribers
// and returns the entry.
func (m *Memory) Append(e Event) Entry {
	m.mu.Lock()
	m.seq++
	en := Entry{Cursor: strconv.FormatInt(m.seq, 10) + "-0", Event: e}
	m.entries = append(m.entries, en)
	m.mu.Unlock()
	m.hub.publish(en)
	return en
}

// Available implements the reading side of Service.
func (m *Memory) Available() bool { return !m.Down }

// Acquire reserves a stream slot.
func (m *Memory) Acquire(credential string) (func(), error) { return m.hub.acquire(credential) }

// Subscribe registers a live subscriber.
func (m *Memory) Subscribe(f Filter) *Sub { return m.hub.subscribe(f) }

// Replay calls fn for every stored event after the cursor that matches f.
func (m *Memory) Replay(_ context.Context, after Cursor, f Filter, fn func(Entry) error) error {
	m.mu.Lock()
	es := slices.Clone(m.entries)
	m.mu.Unlock()
	for _, e := range es {
		c, _ := ParseCursor(e.Cursor)
		if c.Compare(after) > 0 && f.Matches(e.Event) {
			if err := fn(e); err != nil {
				return err
			}
		}
	}
	return nil
}

// History returns stored events like Service.History.
func (m *Memory) History(_ context.Context, q Query) ([]Entry, error) {
	if q.Limit <= 0 || q.Limit > MaxLimit {
		q.Limit = 100
	}
	m.mu.Lock()
	es := slices.Clone(m.entries)
	m.mu.Unlock()
	if q.Desc {
		slices.Reverse(es)
	}
	var out []Entry
	for _, e := range es {
		c, _ := ParseCursor(e.Cursor)
		if (!q.After.Zero() && c.Compare(q.After) <= 0) || (!q.Before.Zero() && c.Compare(q.Before) >= 0) || !q.Matches(e.Event) {
			continue
		}
		if out = append(out, e); len(out) == q.Limit {
			break
		}
	}
	return out, nil
}

// PutTicket stores a ticket.
func (m *Memory) PutTicket(_ context.Context, hash string, payload []byte) error {
	m.mu.Lock()
	m.tickets[hash] = payload
	m.mu.Unlock()
	return nil
}

// TakeTicket redeems a ticket once.
func (m *Memory) TakeTicket(_ context.Context, hash string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.tickets[hash]
	delete(m.tickets, hash)
	return b, ok, nil
}
