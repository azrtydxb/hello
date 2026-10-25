package queue

import (
	"context"
	"sync"
	"time"
)

// MemStore is the in-process Store: the queue state of one process. hello-sip
// uses it when it has no Valkey (then it is a single node), and tests share
// one MemStore between several "nodes" to exercise the multi-node rules.
type MemStore struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]map[string]Entry // queue -> call id -> entry
	leases  map[string]memLease
	agents  map[string]AgentState
	stats   map[string]map[string]AgentStats
	handle  map[string]time.Duration
	rot     map[string]uint64
}

type memLease struct {
	token   string
	expires time.Time
}

// NewMemStore returns an empty in-process store.
func NewMemStore() *MemStore { return NewMemStoreClock(time.Now) }

// NewMemStoreClock is NewMemStore with a clock tests can move.
func NewMemStoreClock(now func() time.Time) *MemStore {
	return &MemStore{
		now: now, entries: map[string]map[string]Entry{}, leases: map[string]memLease{},
		agents: map[string]AgentState{}, stats: map[string]map[string]AgentStats{},
		handle: map[string]time.Duration{}, rot: map[string]uint64{},
	}
}

var _ Store = (*MemStore)(nil)

// prune drops the stale entries of a queue; the caller holds the lock.
func (m *MemStore) prune(queue string) {
	cut := m.now().Add(-EntryTTL).UnixMilli()
	for id, e := range m.entries[queue] {
		if e.HeartbeatMs < cut {
			delete(m.entries[queue], id)
		}
	}
}

func (m *MemStore) Join(_ context.Context, e Entry, maxCallers int) (Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prune(e.Queue)
	if cur, ok := m.entries[e.Queue][e.CallID]; ok {
		return cur, nil
	}
	if maxCallers > 0 && len(m.entries[e.Queue]) >= maxCallers {
		return Entry{}, ErrFull
	}
	now := m.now().UnixMilli()
	if e.EnteredMs == 0 {
		e.EnteredMs = now
	}
	e.HeartbeatMs = now
	if m.entries[e.Queue] == nil {
		m.entries[e.Queue] = map[string]Entry{}
	}
	m.entries[e.Queue][e.CallID] = e
	return e, nil
}

func (m *MemStore) Touch(_ context.Context, queue, callID, node, offering string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[queue][callID]
	if !ok {
		return false, nil
	}
	e.Node, e.Offering, e.HeartbeatMs = node, offering, m.now().UnixMilli()
	m.entries[queue][callID] = e
	return true, nil
}

func (m *MemStore) Leave(_ context.Context, queue, callID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries[queue], callID)
	return nil
}

func (m *MemStore) Waiting(_ context.Context, queue string) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prune(queue)
	out := make([]Entry, 0, len(m.entries[queue]))
	for _, e := range m.entries[queue] {
		out = append(out, e)
	}
	Sort(out)
	return out, nil
}

func (m *MemStore) Acquire(_ context.Context, key, token string, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if l, ok := m.leases[key]; ok && l.expires.After(now) {
		return false, nil
	}
	m.leases[key] = memLease{token: token, expires: now.Add(ttl)}
	return true, nil
}

func (m *MemStore) Release(_ context.Context, key, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.leases[key]; ok && l.token == token {
		delete(m.leases, key)
	}
	return nil
}

func (m *MemStore) Holder(_ context.Context, key string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.leases[key]
	if !ok || !l.expires.After(m.now()) {
		return "", false, nil
	}
	return l.token, true, nil
}

func (m *MemStore) SetAgent(_ context.Context, st AgentState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.agents[st.Ext] = st
	return nil
}

func (m *MemStore) Agent(_ context.Context, ext string) (AgentState, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.agents[ext]
	return st, ok, nil
}

func (m *MemStore) Agents(_ context.Context) (map[string]AgentState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]AgentState, len(m.agents))
	for k, v := range m.agents {
		out[k] = v
	}
	return out, nil
}

func (m *MemStore) Stats(_ context.Context, queue string) (map[string]AgentStats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]AgentStats, len(m.stats[queue]))
	for k, v := range m.stats[queue] {
		out[k] = v
	}
	return out, nil
}

func (m *MemStore) NoteAnswered(_ context.Context, queue, ext string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stats[queue] == nil {
		m.stats[queue] = map[string]AgentStats{}
	}
	s := m.stats[queue][ext]
	s.Calls++
	m.stats[queue][ext] = s
	return nil
}

func (m *MemStore) NoteEnded(_ context.Context, queue, ext string, talk, wrap time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stats[queue] == nil {
		m.stats[queue] = map[string]AgentStats{}
	}
	now := m.now()
	s := m.stats[queue][ext]
	s.LastEndMs, s.WrapUntilMs = now.UnixMilli(), now.Add(wrap).UnixMilli()
	m.stats[queue][ext] = s
	m.handle[queue] = emaHandle(m.handle[queue], talk)
	return nil
}

func (m *MemStore) AvgHandle(_ context.Context, queue string) (time.Duration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.handle[queue], nil
}

func (m *MemStore) NextRotation(_ context.Context, queue string) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := m.rot[queue]
	m.rot[queue] = n + 1
	return n, nil
}
