package directory

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Mem is an in-memory Store for tests. Its Apply follows the PostgreSQL one's
// rules closely enough that the engine tests exercise the same behaviour; the
// SQL itself is tested against PostgreSQL.
type Mem struct {
	mu sync.Mutex

	Connectors map[int64]*Connector
	Secrets    map[int64]string
	FPs        map[int64]string
	Runs       map[int64]*Run
	Changes    map[int64][]Change
	Users      map[int64]*MemUser
	Exts       map[string]*LocalExt
	// Rings maps ring group id to its members: extension id to the connector
	// that put it there (0 manual).
	Rings   map[int64]map[int64]int64
	Audits  []string
	Applied []Op

	nextConn, nextRun, nextChange, nextUser int64
	locked                                  map[string]bool
	// ApplyErr makes Apply fail for the user of that external id.
	ApplyErr map[string]error
}

// MemUser is a user row of the in-memory store.
type MemUser struct {
	LocalUser
	Devices bool
}

// NewMem returns an empty store.
func NewMem() *Mem {
	return &Mem{Connectors: map[int64]*Connector{}, Secrets: map[int64]string{}, FPs: map[int64]string{}, Runs: map[int64]*Run{},
		Changes: map[int64][]Change{}, Users: map[int64]*MemUser{}, Exts: map[string]*LocalExt{}, Rings: map[int64]map[int64]int64{},
		locked: map[string]bool{}, ApplyErr: map[string]error{}}
}

// AddExt adds an extension.
func (m *Mem) AddExt(number string, door bool) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := int64(len(m.Exts) + 100)
	m.Exts[number] = &LocalExt{ID: id, Number: number, Door: door}
	return id
}

// AddUser adds a user and returns its id.
func (m *Mem) AddUser(u LocalUser) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextUser++
	u.ID = m.nextUser
	if u.Role == "" {
		u.Role = "user"
	}
	m.Users[u.ID] = &MemUser{LocalUser: u, Devices: true}
	for _, e := range u.ExtensionIDs {
		for _, x := range m.Exts {
			if x.ID == e {
				x.UserID = u.ID
			}
		}
	}
	return u.ID
}

// AddRing makes a ring group.
func (m *Mem) AddRing(id int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Rings[id] = map[int64]int64{}
}

func (m *Mem) audit(actor, action, resource, id string) {
	m.Audits = append(m.Audits, actor+" "+action+" "+resource+" "+id)
}

// Audit implements Store.
func (m *Mem) Audit(_ context.Context, actor, action, resource, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audit(actor, action, resource, id)
	return nil
}

func (m *Mem) linked(id int64) int {
	n := 0
	for _, u := range m.Users {
		if u.ConnectorID == id {
			n++
		}
	}
	return n
}

// ListConnectors implements Store.
func (m *Mem) ListConnectors(context.Context) ([]Connector, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Connector
	for _, c := range m.Connectors {
		cc := *c
		cc.LinkedUsers = m.linked(c.ID)
		out = append(out, cc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// GetConnector implements Store.
func (m *Mem) GetConnector(_ context.Context, id int64) (Connector, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.Connectors[id]
	if !ok {
		return Connector{}, ErrNotFound
	}
	cc := *c
	cc.LinkedUsers = m.linked(id)
	cc.CredentialFP = m.FPs[id]
	cc.HasCredential = m.Secrets[id] != ""
	return cc, nil
}

// SaveConnector implements Store.
func (m *Mem) SaveConnector(_ context.Context, actor string, c Connector, secret *string, fp string) (Connector, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.Connectors {
		if o.ID != c.ID && strings.EqualFold(o.Name, c.Name) {
			return Connector{}, ErrInvalid
		}
	}
	if c.ID == 0 {
		m.nextConn++
		c.ID = m.nextConn
		c.Status = "new"
		c.CreatedAt = time.Now()
		m.audit(actor, "create", "directory_connector", "")
	} else {
		m.audit(actor, "update", "directory_connector", "")
	}
	if secret != nil {
		m.Secrets[c.ID] = *secret
		m.FPs[c.ID] = fp
	}
	c.HasCredential = m.Secrets[c.ID] != ""
	c.UpdatedAt = time.Now()
	cc := c
	m.Connectors[c.ID] = &cc
	return c, nil
}

// SaveSyncState implements Store.
func (m *Mem) SaveSyncState(_ context.Context, id int64, st SyncState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.Connectors[id]
	if !ok {
		return ErrNotFound
	}
	c.Sync, c.LastFullRunAt = st, st.LastFullAt
	return nil
}

// DeleteConnector implements Store.
func (m *Mem) DeleteConnector(_ context.Context, actor string, id int64, release bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.Connectors[id]; !ok {
		return ErrNotFound
	}
	if m.linked(id) > 0 {
		if !release {
			return ErrHasUsers
		}
		for _, u := range m.Users {
			if u.ConnectorID == id {
				u.ConnectorID, u.ExternalID, u.Managed = 0, "", nil
			}
		}
	}
	delete(m.Connectors, id)
	m.audit(actor, "delete", "directory_connector", "")
	return nil
}

// Secret implements Store.
func (m *Mem) Secret(_ context.Context, id int64) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Secrets[id] == "" {
		return "", ErrNotFound
	}
	return m.Secrets[id], nil
}

// RecordOutcome implements Store.
func (m *Mem) RecordOutcome(_ context.Context, id int64, ok bool, code, detail string, at time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.Connectors[id]
	if c == nil {
		return 0, ErrNotFound
	}
	c.LastRunAt = &at
	if ok {
		c.Status, c.LastSuccessAt, c.ConsecutiveFailure, c.LastErrorCode, c.LastErrorDetail = "ok", &at, 0, "", ""
	} else {
		c.Status, c.ConsecutiveFailure, c.LastErrorCode, c.LastErrorDetail = "failing", c.ConsecutiveFailure+1, code, detail
	}
	return c.ConsecutiveFailure, nil
}

// CreateRun implements Store.
func (m *Mem) CreateRun(_ context.Context, r Run) (Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextRun++
	r.ID = m.nextRun
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	rr := r
	m.Runs[r.ID] = &rr
	return r, nil
}

// GetRun implements Store.
func (m *Mem) GetRun(_ context.Context, id int64) (Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.Runs[id]
	if !ok {
		return Run{}, ErrNotFound
	}
	return *r, nil
}

// ListRuns implements Store.
func (m *Mem) ListRuns(_ context.Context, connectorID int64, limit int) ([]Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Run
	for _, r := range m.Runs {
		if r.ConnectorID == connectorID {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// UpdateRun implements Store.
func (m *Mem) UpdateRun(_ context.Context, r Run) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.Runs[r.ID]; !ok {
		return ErrNotFound
	}
	rr := r
	m.Runs[r.ID] = &rr
	return nil
}

// ClaimQueued implements Store.
func (m *Mem) ClaimQueued(_ context.Context, connectorID int64, replica string) (*Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best *Run
	for _, r := range m.Runs {
		if r.ConnectorID == connectorID && r.Status == StatusQueued && (best == nil || r.ID < best.ID) {
			best = r
		}
	}
	if best == nil {
		return nil, nil
	}
	now := time.Now()
	best.Status, best.Replica, best.StartedAt = StatusRunning, replica, &now
	out := *best
	return &out, nil
}

// CloseOrphans implements Store.
func (m *Mem) CloseOrphans(_ context.Context, connectorID int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.Runs {
		if r.ConnectorID == connectorID && r.Status == StatusRunning {
			now := time.Now()
			r.Status, r.ErrorCode, r.ErrorDetail, r.FinishedAt = StatusFailed, CodeReplicaLost, "the replica running it went away", &now
			n++
		}
	}
	return n, nil
}

// LastScheduled implements Store.
func (m *Mem) LastScheduled(_ context.Context, connectorID int64) (time.Time, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var last time.Time
	found := false
	for _, r := range m.Runs {
		if r.ConnectorID == connectorID && r.Kind == RunScheduled && (!found || r.CreatedAt.After(last)) {
			last, found = r.CreatedAt, true
		}
	}
	return last, found, nil
}

// HasApplied implements Store.
func (m *Mem) HasApplied(_ context.Context, connectorID int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.Runs {
		if r.ConnectorID == connectorID && r.Status == StatusSucceeded && (r.Kind == RunApply || r.Kind == RunScheduled) {
			return true, nil
		}
	}
	return false, nil
}

// ReplaceChanges implements Store.
func (m *Mem) ReplaceChanges(_ context.Context, runID int64, cs []Change) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Change, len(cs))
	for i, c := range cs {
		m.nextChange++
		c.ID, c.RunID, c.op = m.nextChange, runID, nil
		out[i] = c
	}
	m.Changes[runID] = out
	return nil
}

// ListChanges implements Store.
func (m *Mem) ListChanges(_ context.Context, runID int64, f ChangeFilter) ([]Change, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Change
	for _, c := range m.Changes[runID] {
		if (f.Action == "" || c.Action == f.Action) && c.ID > f.After {
			out = append(out, c)
		}
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// LoadState implements Store.
func (m *Mem) LoadState(_ context.Context, connectorID int64) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := State{Exts: map[string]LocalExt{}, RingGroups: map[int64]bool{}}
	for n, e := range m.Exts {
		st.Exts[n] = *e
	}
	for id := range m.Rings {
		st.RingGroups[id] = true
	}
	ids := make([]int64, 0, len(m.Users))
	for id := range m.Users {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		u := m.Users[id].LocalUser
		u.MemberOf, u.SyncedMemberOf = nil, nil
		for g, members := range m.Rings {
			for ext, by := range members {
				if slices.Contains(u.ExtensionIDs, ext) {
					u.MemberOf = append(u.MemberOf, g)
					if by == connectorID && by != 0 {
						u.SyncedMemberOf = append(u.SyncedMemberOf, g)
					}
				}
			}
		}
		slices.Sort(u.MemberOf)
		slices.Sort(u.SyncedMemberOf)
		if u.Role == "admin" && u.Enabled {
			st.Admins++
		}
		st.Users = append(st.Users, u)
	}
	return st, nil
}

// Apply implements Store.
func (m *Mem) Apply(_ context.Context, actor string, c Connector, op Op) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ApplyErr[op.ExternalID]; err != nil {
		return err
	}
	m.Applied = append(m.Applied, op)
	set := func(u *MemUser) {
		if op.Username != nil {
			u.Username = *op.Username
		}
		if op.Email != nil {
			u.Email = *op.Email
		}
		if op.Display != nil {
			u.DisplayName = *op.Display
		}
		if op.Role != nil {
			u.Role = *op.Role
		}
		if op.SetManaged {
			u.Managed = op.Managed
		}
		if op.SetExtension {
			if op.Override {
				for _, o := range m.Users {
					o.ExtensionIDs = slices.DeleteFunc(slices.Clone(o.ExtensionIDs), func(e int64) bool { return e == op.Extension })
					if o.OwnedExtension == op.Extension {
						o.OwnedExtension = 0
					}
				}
			}
			if u.OwnedExtension != 0 {
				u.ExtensionIDs = slices.DeleteFunc(slices.Clone(u.ExtensionIDs), func(e int64) bool { return e == u.OwnedExtension })
			}
			u.OwnedExtension = op.Extension
			if !slices.Contains(u.ExtensionIDs, op.Extension) {
				u.ExtensionIDs = append(u.ExtensionIDs, op.Extension)
			}
			for _, x := range m.Exts {
				if x.ID == op.Extension {
					x.UserID = u.ID
				}
			}
		}
		for _, g := range op.RingAdd {
			if _, have := m.Rings[g][u.OwnedExtension]; m.Rings[g] != nil && u.OwnedExtension != 0 && !have {
				m.Rings[g][u.OwnedExtension] = c.ID
			}
		}
		for _, g := range op.RingRemove {
			if m.Rings[g] != nil && m.Rings[g][u.OwnedExtension] == c.ID {
				delete(m.Rings[g], u.OwnedExtension)
			}
		}
	}
	switch op.Kind {
	case ActionCreate:
		for _, o := range m.Users {
			if strings.EqualFold(o.Username, *op.Username) {
				return ErrInvalid
			}
		}
		m.nextUser++
		u := &MemUser{LocalUser: LocalUser{ID: m.nextUser, Enabled: true, ConnectorID: c.ID, ExternalID: op.ExternalID}, Devices: true}
		m.Users[u.ID] = u
		set(u)
		m.audit(actor, "create", "user", itoa(u.ID))
	case ActionUpdate, ActionAdopt, "managed":
		u := m.Users[op.UserID]
		if u == nil {
			return ErrNotFound
		}
		if op.Role != nil && u.Role == "admin" && u.Enabled && *op.Role != "admin" {
			n := 0
			for _, o := range m.Users {
				if o.Role == "admin" && o.Enabled {
					n++
				}
			}
			if n <= 1 {
				return ErrLastAdmin
			}
		}
		if op.Kind == ActionAdopt {
			u.ConnectorID, u.ExternalID = c.ID, op.ExternalID
		}
		set(u)
		if op.Kind != "managed" {
			m.audit(actor, op.Kind, "user", itoa(u.ID))
		}
	case ActionDisable:
		u := m.Users[op.UserID]
		if u == nil {
			return ErrNotFound
		}
		if u.Role == "admin" && u.Enabled {
			n := 0
			for _, o := range m.Users {
				if o.Role == "admin" && o.Enabled {
					n++
				}
			}
			if n <= 1 {
				return ErrLastAdmin
			}
		}
		u.Enabled, u.DisabledByDirectory = false, true
		if op.AbsentRuns > 0 {
			u.AbsentRuns = op.AbsentRuns
		}
		if op.Devices {
			u.Devices = false
		}
		m.audit(actor, "disable", "user", itoa(u.ID))
	case ActionEnable:
		u := m.Users[op.UserID]
		if u == nil {
			return ErrNotFound
		}
		u.Enabled, u.DisabledByDirectory, u.AbsentRuns = true, false, 0
		if op.Devices {
			u.Devices = true
		}
		m.audit(actor, "enable", "user", itoa(u.ID))
	case "absent":
		if u := m.Users[op.UserID]; u != nil {
			u.AbsentRuns = op.AbsentRuns
		}
	case "touch":
		if u := m.Users[op.UserID]; u != nil {
			u.AbsentRuns = 0
		}
	}
	return nil
}

func itoa(id int64) string {
	b, _ := json.Marshal(id)
	return string(b)
}

// PruneChanges implements Store.
func (m *Mem) PruneChanges(context.Context, time.Time) (int64, error) { return 0, nil }

// TryLock implements Store.
func (m *Mem) TryLock(_ context.Context, key string) (func(), bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locked[key] {
		return nil, false, nil
	}
	m.locked[key] = true
	return func() {
		m.mu.Lock()
		delete(m.locked, key)
		m.mu.Unlock()
	}, true, nil
}

// Hold takes a lock as if another replica did, and returns its release.
func (m *Mem) Hold(key string) func() {
	m.mu.Lock()
	m.locked[key] = true
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		delete(m.locked, key)
		m.mu.Unlock()
	}
}

// Static is a Driver that returns a fixed result, for tests of the layers
// above the engine (the API, the scheduler wiring).
type Static struct {
	Res *Result
	Err error
}

// Validate implements Driver.
func (Static) Validate(cfg json.RawMessage, _ ValidateOptions) (json.RawMessage, error) {
	if len(cfg) == 0 {
		return json.RawMessage(`{}`), nil
	}
	return cfg, nil
}

// Identity implements Driver.
func (Static) Identity(cfg json.RawMessage) string { return string(cfg) }

// DefaultMappings implements Driver.
func (Static) DefaultMappings() Mappings { return LDAP{}.DefaultMappings() }

// Fetch implements Driver.
func (s *Static) Fetch(context.Context, json.RawMessage, string, FetchOptions) (*Result, error) {
	if s.Err != nil {
		return nil, s.Err
	}
	return s.Res, nil
}

// Test implements Driver.
func (s *Static) Test(context.Context, json.RawMessage, string, FetchOptions) (*TestResult, error) {
	if s.Err != nil {
		return nil, s.Err
	}
	return &TestResult{OK: true, Sample: []SampleRow{}, Groups: []SourceGroup{}}, nil
}
