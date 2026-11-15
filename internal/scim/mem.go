package scim

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// Mem is an in-memory Store for tests. It follows the PostgreSQL store's
// rules; the SQL itself is tested against PostgreSQL.
type Mem struct {
	mu        sync.Mutex
	locks     map[int64]*sync.Mutex
	tokens    map[int64]*memToken
	resources map[string]*Resource
	members   map[string][]string // group -> members, in order added
	nextToken int64
	// Kinds names the connectors' kinds (CreateToken checks it).
	Kinds  map[int64]string
	Audits []string
	now    func() time.Time
}

type memToken struct {
	Token
	hash []byte
}

// NewMem returns an empty store.
func NewMem() *Mem {
	return &Mem{locks: map[int64]*sync.Mutex{}, tokens: map[int64]*memToken{}, resources: map[string]*Resource{},
		members: map[string][]string{}, Kinds: map[int64]string{}, now: time.Now}
}

// Lock implements Store.
func (m *Mem) Lock(_ context.Context, connectorID int64) (func(), error) {
	m.mu.Lock()
	l := m.locks[connectorID]
	if l == nil {
		l = &sync.Mutex{}
		m.locks[connectorID] = l
	}
	m.mu.Unlock()
	l.Lock()
	return l.Unlock, nil
}

// LookupToken implements Store.
func (m *Mem) LookupToken(_ context.Context, hash []byte) (Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tokens {
		if bytes.Equal(t.hash, hash) && t.RevokedAt == nil && m.Kinds[t.ConnectorID] == "scim" {
			return t.Token, nil
		}
	}
	return Token{}, ErrNotFound
}

// TouchToken implements Store.
func (m *Mem) TouchToken(_ context.Context, id int64, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tokens[id]; ok {
		t.LastUsedAt = &at
	}
	return nil
}

// CreateToken implements Store.
func (m *Mem) CreateToken(_ context.Context, actor string, connectorID int64, hash []byte, hint string) (Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kind, ok := m.Kinds[connectorID]
	if !ok {
		return Token{}, ErrNotFound
	}
	if kind != "scim" {
		return Token{}, ErrWrongKind
	}
	n := 0
	for _, t := range m.tokens {
		if t.ConnectorID == connectorID && t.RevokedAt == nil {
			n++
		}
	}
	if n >= MaxActiveTokens {
		return Token{}, ErrTooMany
	}
	m.nextToken++
	t := &memToken{Token: Token{ID: m.nextToken, ConnectorID: connectorID, Hint: hint, CreatedBy: actor, CreatedAt: m.now()}, hash: hash}
	m.tokens[t.ID] = t
	m.Audits = append(m.Audits, actor+" scim-token-create")
	return t.Token, nil
}

// ListTokens implements Store.
func (m *Mem) ListTokens(_ context.Context, connectorID int64) ([]Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Token{}
	for _, t := range m.tokens {
		if t.ConnectorID == connectorID {
			out = append(out, t.Token)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// RevokeToken implements Store.
func (m *Mem) RevokeToken(_ context.Context, actor string, id int64) (Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[id]
	if !ok {
		return Token{}, ErrNotFound
	}
	if t.RevokedAt == nil {
		now := m.now()
		t.RevokedAt = &now
	}
	m.Audits = append(m.Audits, actor+" scim-token-revoke")
	return t.Token, nil
}

func copyResource(r *Resource) Resource {
	c := *r
	c.Data, _ = cloneValue(r.Data).(map[string]any)
	if c.Data == nil {
		c.Data = map[string]any{}
	}
	return c
}

// GetResource implements Store.
func (m *Mem) GetResource(_ context.Context, connectorID int64, typ, id string, withDeleted bool) (Resource, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.resources[id]
	if !ok || r.ConnectorID != connectorID || r.Type != typ || r.Deleted && !withDeleted {
		return Resource{}, ErrNotFound
	}
	return copyResource(r), nil
}

func (m *Mem) sorted() []*Resource {
	out := make([]*Resource, 0, len(m.resources))
	for _, r := range m.resources {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.Before(out[j].Created)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// FindResources implements Store.
func (m *Mem) FindResources(_ context.Context, connectorID int64, typ string, hint *Hint) ([]Resource, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Resource
	for _, r := range m.sorted() {
		if r.ConnectorID != connectorID || r.Type != typ || r.Deleted {
			continue
		}
		if hint != nil {
			switch hint.Field {
			case "id":
				if r.ID != hint.Value {
					continue
				}
			case "name":
				if !strings.EqualFold(r.Name, hint.Value) {
					continue
				}
			case "external_id":
				if r.ExternalID != hint.Value {
					continue
				}
			}
		}
		out = append(out, copyResource(r))
	}
	return out, nil
}

// FindDeleted implements Store.
func (m *Mem) FindDeleted(_ context.Context, connectorID int64, typ, name, externalID string) (*Resource, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best *Resource
	for _, r := range m.sorted() {
		if r.ConnectorID != connectorID || r.Type != typ || !r.Deleted {
			continue
		}
		if externalID != "" && r.ExternalID == externalID {
			c := copyResource(r)
			return &c, nil
		}
		if strings.EqualFold(r.Name, name) {
			best = r
		}
	}
	if best == nil {
		return nil, nil
	}
	c := copyResource(best)
	return &c, nil
}

// SaveResource implements Store.
func (m *Mem) SaveResource(_ context.Context, r Resource, members []string) (Resource, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.resources {
		if o.ID == r.ID || o.ConnectorID != r.ConnectorID || o.Type != r.Type || o.Deleted {
			continue
		}
		if strings.EqualFold(o.Name, r.Name) || r.ExternalID != "" && o.ExternalID == r.ExternalID {
			return Resource{}, ErrConflict
		}
	}
	now := m.now()
	if r.Version == 0 {
		if _, ok := m.resources[r.ID]; ok {
			return Resource{}, ErrConflict
		}
		r.Version, r.Created, r.Updated, r.Deleted = 1, now, now, false
	} else {
		o, ok := m.resources[r.ID]
		if !ok || o.Version != r.Version {
			return Resource{}, ErrNotFound
		}
		r.Version, r.Created, r.Updated, r.Deleted = o.Version+1, o.Created, now, false
	}
	for _, id := range members {
		if _, ok := m.resources[id]; !ok {
			return Resource{}, ErrNotFound
		}
	}
	c := copyResource(&r)
	m.resources[r.ID] = &c
	if members != nil {
		m.members[r.ID] = append([]string(nil), members...)
	}
	return copyResource(&c), nil
}

// MarkDeleted implements Store.
func (m *Mem) MarkDeleted(_ context.Context, connectorID int64, typ, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.resources[id]
	if !ok || r.ConnectorID != connectorID || r.Type != typ || r.Deleted {
		return ErrNotFound
	}
	if typ == TypeGroup {
		delete(m.resources, id)
		delete(m.members, id)
		return nil
	}
	r.Deleted, r.Version, r.Updated = true, r.Version+1, m.now()
	return nil
}

// Members implements Store.
func (m *Mem) Members(_ context.Context, groupIDs []string) (map[string][]memberRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string][]memberRef{}
	for _, g := range groupIDs {
		for _, id := range m.members[g] {
			if r, ok := m.resources[id]; ok && !r.Deleted {
				out[g] = append(out[g], memberRef{ID: id, Name: r.Name})
			}
		}
	}
	return out, nil
}

// GroupsOf implements Store.
func (m *Mem) GroupsOf(_ context.Context, userIDs []string) (map[string][]groupRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := map[string]bool{}
	for _, u := range userIDs {
		want[u] = true
	}
	out := map[string][]groupRef{}
	for g, ms := range m.members {
		gr, ok := m.resources[g]
		if !ok {
			continue
		}
		for _, u := range ms {
			if want[u] {
				out[u] = append(out[u], groupRef{ID: g, ExternalID: gr.ExternalID, Name: gr.Name})
			}
		}
	}
	for u := range out {
		sort.Slice(out[u], func(i, j int) bool { return out[u][i].Name < out[u][j].Name })
	}
	return out, nil
}

// Existing implements Store.
func (m *Mem) Existing(_ context.Context, connectorID int64, ids []string) (map[string]bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]bool{}
	for _, id := range ids {
		if r, ok := m.resources[id]; ok && r.ConnectorID == connectorID && r.Type == TypeUser {
			out[id] = true
		}
	}
	return out, nil
}

// Counts implements Store.
func (m *Mem) Counts(_ context.Context, connectorID int64) (Counts, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var c Counts
	for _, r := range m.resources {
		if r.ConnectorID != connectorID {
			continue
		}
		switch {
		case r.Type == TypeGroup:
			c.Groups++
		case r.Deleted:
			c.DeletedUsers++
		default:
			c.Users++
			if a, ok := r.Data["active"].(bool); !ok || a {
				c.ActiveUsers++
			}
		}
	}
	return c, nil
}

// LastUsed implements Store.
func (m *Mem) LastUsed(_ context.Context, connectorID int64) (*time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var last *time.Time
	for _, t := range m.tokens {
		if t.ConnectorID == connectorID && t.LastUsedAt != nil && (last == nil || t.LastUsedAt.After(*last)) {
			v := *t.LastUsedAt
			last = &v
		}
	}
	return last, nil
}
