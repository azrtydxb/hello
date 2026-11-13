package crm

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"slices"
	"sync"
	"time"
)

// Mem is an in-memory Repo for tests.
type Mem struct {
	mu         sync.Mutex
	Now        func() time.Time
	nextID     int64
	connectors map[int64]*memConnector
	conns      map[int64]*memConn // by connector id
	uconns     map[[2]int64]*memConn
	connSeq    int64
	states     map[string]memState
	logs       map[int64]*LogRow
	maps       map[[2]int64]Mapping
	// UsersList is what Users and UserForExtension read.
	UsersList []UserRef
	// Admins are the addresses AdminEmails returns.
	Admins []string
	// Events are the (callID, extension) pairs CallInvolves reads.
	Events [][2]string
	// AuditLog collects Audit calls as "actor action resource id".
	AuditLog []string
}

type memConnector struct {
	row    ConnectorRow
	secret string
}

type memConn struct {
	row    ConnectionRow
	tokens Tokens
}

type memState struct {
	connector int64
	actor     string
	expires   time.Time
}

var _ Repo = (*Mem)(nil)

// NewMem returns an empty Mem.
func NewMem() *Mem {
	return &Mem{Now: time.Now, connectors: map[int64]*memConnector{}, conns: map[int64]*memConn{}, uconns: map[[2]int64]*memConn{},
		states: map[string]memState{}, logs: map[int64]*LogRow{}, maps: map[[2]int64]Mapping{}}
}

func (m *Mem) next() int64 { m.nextID++; return m.nextID }

func cloneCfg(c map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range c {
		out[k] = v
	}
	return out
}

func (m *Mem) nameTaken(name string, except int64) bool {
	for id, c := range m.connectors {
		if id != except && c.row.Name == name {
			return true
		}
	}
	return false
}

func (m *Mem) ListConnectors(context.Context) ([]ConnectorRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ConnectorRow
	for _, c := range m.connectors {
		out = append(out, c.row)
	}
	slices.SortFunc(out, func(a, b ConnectorRow) int { return int(a.ID - b.ID) })
	return out, nil
}

func (m *Mem) GetConnector(_ context.Context, id int64) (ConnectorRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.connectors[id]
	if !ok {
		return ConnectorRow{}, ErrNotFound
	}
	return c.row, nil
}

func (m *Mem) CreateConnector(_ context.Context, in ConnectorInput, clientSecret string) (ConnectorRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nameTaken(in.Name, 0) {
		return ConnectorRow{}, ErrConflict
	}
	id := m.next()
	c := &memConnector{secret: clientSecret, row: ConnectorRow{ID: id, Kind: in.Kind, Name: in.Name, Enabled: in.Enabled,
		Config: cloneCfg(in.Config), HasSecret: clientSecret != "", Recordings: in.Recordings, LinkTTLHours: in.LinkTTLHours,
		LogMissed: in.LogMissed, FallbackOwner: in.FallbackOwner, CreatedAt: m.Now()}}
	m.connectors[id] = c
	return c.row, nil
}

func (m *Mem) UpdateConnector(_ context.Context, id int64, in ConnectorInput, clientSecret *string) (ConnectorRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.connectors[id]
	if !ok {
		return ConnectorRow{}, ErrNotFound
	}
	if m.nameTaken(in.Name, id) {
		return ConnectorRow{}, ErrConflict
	}
	c.row.Name, c.row.Enabled, c.row.Config = in.Name, in.Enabled, cloneCfg(in.Config)
	c.row.Recordings, c.row.LinkTTLHours, c.row.LogMissed, c.row.FallbackOwner = in.Recordings, in.LinkTTLHours, in.LogMissed, in.FallbackOwner
	if clientSecret != nil {
		c.secret, c.row.HasSecret = *clientSecret, *clientSecret != ""
	}
	return c.row, nil
}

func (m *Mem) DeleteConnector(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.connectors[id]; !ok {
		return ErrNotFound
	}
	delete(m.connectors, id)
	delete(m.conns, id)
	for k := range m.uconns {
		if k[0] == id {
			delete(m.uconns, k)
		}
	}
	for k, l := range m.logs {
		if l.ConnectorID == id {
			delete(m.logs, k)
		}
	}
	return nil
}

func (m *Mem) ClientSecret(_ context.Context, id int64) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.connectors[id]
	if !ok {
		return "", ErrNotFound
	}
	return c.secret, nil
}

func (m *Mem) OrgConnection(_ context.Context, connectorID int64) (ConnectionRow, Tokens, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.conns[connectorID]
	if !ok {
		return ConnectionRow{}, Tokens{}, ErrNotFound
	}
	return c.row, c.tokens, nil
}

func (m *Mem) SaveOrgConnection(_ context.Context, connectorID int64, t Tokens) (ConnectionRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.conns[connectorID]
	if !ok {
		m.connSeq++
		c = &memConn{row: ConnectionRow{ID: m.connSeq, ConnectorID: connectorID, Scope: "org"}}
		m.conns[connectorID] = c
	}
	if t.Refresh == "" {
		t.Refresh = c.tokens.Refresh
	}
	if t.Account == "" {
		t.Account = c.tokens.Account
	}
	c.tokens = t
	c.row.ExpiresAt, c.row.ExternalAccount, c.row.GrantedScopes = t.ExpiresAt, t.Account, joinScopes(t.Scopes)
	c.row.Status, c.row.LastError, c.row.UpdatedAt = StatusConnected, "", m.Now()
	return c.row, nil
}

func joinScopes(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += " "
		}
		out += v
	}
	return out
}

func (m *Mem) SetConnectionStatus(_ context.Context, id int64, status, lastError string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.conns {
		if c.row.ID == id {
			c.row.Status, c.row.LastError = status, lastError
		}
	}
	for _, c := range m.uconns {
		if c.row.ID == id {
			c.row.Status, c.row.LastError = status, lastError
		}
	}
	return nil
}

func (m *Mem) DeleteOrgConnection(_ context.Context, connectorID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.conns[connectorID]; !ok {
		return ErrNotFound
	}
	delete(m.conns, connectorID)
	return nil
}

func (m *Mem) NewState(_ context.Context, connectorID int64, actor string, ttl time.Duration) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b [16]byte
	_, _ = rand.Read(b[:])
	s := base64.RawURLEncoding.EncodeToString(b[:])
	m.states[s] = memState{connectorID, actor, m.Now().Add(ttl)}
	return s, nil
}

func (m *Mem) TakeState(_ context.Context, state string) (int64, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.states[state]
	delete(m.states, state)
	if !ok || !st.expires.After(m.Now()) {
		return 0, "", ErrNotFound
	}
	return st.connector, st.actor, nil
}

func (m *Mem) PurgeStates(_ context.Context, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, s := range m.states {
		if s.expires.Before(now) {
			delete(m.states, k)
		}
	}
	return nil
}

func (m *Mem) ActiveConnectors(context.Context, *sql.Tx) ([]ConnectorRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ConnectorRow
	for id, c := range m.connectors {
		if !c.row.Enabled {
			continue
		}
		active := false
		if cn, ok := m.conns[id]; ok && cn.row.Status != StatusRevoked {
			active = true
		}
		for k, cn := range m.uconns {
			active = active || (k[0] == id && cn.row.Status != StatusRevoked)
		}
		if active {
			out = append(out, c.row)
		}
	}
	slices.SortFunc(out, func(a, b ConnectorRow) int { return int(a.ID - b.ID) })
	return out, nil
}

func (m *Mem) find(connectorID int64, callID string) *LogRow {
	for _, l := range m.logs {
		if l.ConnectorID == connectorID && l.CallID == callID {
			return l
		}
	}
	return nil
}

func (m *Mem) insertLog(l LogRow) *LogRow {
	l.ID = m.next()
	l.CreatedAt, l.UpdatedAt = m.Now(), m.Now()
	m.logs[l.ID] = &l
	return &l
}

func (m *Mem) UpsertCall(_ context.Context, _ *sql.Tx, connectorID int64, j LogJob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.find(connectorID, j.CallID)
	if l == nil {
		m.insertLog(LogRow{ConnectorID: connectorID, CallID: j.CallID, Direction: j.Direction, Status: LogPending,
			NextAttemptAt: m.Now(), Payload: j.Payload, EventType: j.EventType, OwnerExtension: j.OwnerExtension})
		return nil
	}
	if l.Status == LogWaiting {
		l.Direction, l.Status, l.Payload, l.EventType, l.OwnerExtension = j.Direction, LogPending, j.Payload, j.EventType, j.OwnerExtension
		l.NextAttemptAt, l.UpdatedAt = m.Now(), m.Now()
	}
	return nil
}

func (m *Mem) Skip(_ context.Context, _ *sql.Tx, connectorID int64, j LogJob, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.find(connectorID, j.CallID)
	if l == nil {
		m.insertLog(LogRow{ConnectorID: connectorID, CallID: j.CallID, Direction: j.Direction, Status: LogSkipped, LastError: reason,
			Payload: j.Payload, EventType: j.EventType, OwnerExtension: j.OwnerExtension})
		return nil
	}
	if l.Status == LogWaiting {
		l.Direction, l.Status, l.LastError = j.Direction, LogSkipped, reason
		l.Payload, l.EventType, l.OwnerExtension = j.Payload, j.EventType, j.OwnerExtension
	}
	return nil
}

func (m *Mem) AttachRecording(_ context.Context, _ *sql.Tx, connectorID int64, callID string, recordingID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.find(connectorID, callID)
	if l == nil {
		m.insertLog(LogRow{ConnectorID: connectorID, CallID: callID, Status: LogWaiting, RecordingID: recordingID})
		return nil
	}
	l.RecordingID = recordingID
	if l.Status == LogLogged {
		l.Status, l.NextAttemptAt = LogUpdate, m.Now()
	}
	return nil
}

func (m *Mem) AttachEngagement(_ context.Context, connectorID int64, callID, ref string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.find(connectorID, callID)
	if l == nil {
		m.insertLog(LogRow{ConnectorID: connectorID, CallID: callID, Status: LogWaiting, ExternalID: ref})
		return true, nil
	}
	if l.ExternalID == "" {
		l.ExternalID = ref
		if l.Status == LogLogged {
			l.NextAttemptAt = m.Now()
		}
	}
	return l.ExternalID == ref, nil
}

func (m *Mem) ClaimDue(_ context.Context, limit int, lease time.Duration) ([]LogRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.Now()
	var due []*LogRow
	for _, l := range m.logs {
		c, ok := m.connectors[l.ConnectorID]
		cn := m.conns[l.ConnectorID]
		if !ok || cn == nil || !c.row.Enabled || cn.row.Status != StatusConnected {
			continue
		}
		if (l.Status == LogPending || l.Status == LogUpdate) && !l.NextAttemptAt.After(now) {
			due = append(due, l)
		}
	}
	slices.SortFunc(due, func(a, b *LogRow) int { return int(a.ID - b.ID) })
	if len(due) > limit {
		due = due[:limit]
	}
	out := make([]LogRow, len(due))
	for i, l := range due {
		out[i] = *l
		l.NextAttemptAt = now.Add(lease)
	}
	return out, nil
}

func (m *Mem) Finish(_ context.Context, id int64, r LogResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.logs[id]
	if !ok {
		return ErrNotFound
	}
	l.Status, l.Attempts, l.NextAttemptAt, l.LastError = r.Status, r.Attempts, r.NextAttemptAt, r.LastError
	if r.Status == LogLogged && l.RecordingID != r.RecordingSeen {
		l.Status, l.NextAttemptAt = LogUpdate, m.Now()
	}
	if r.ExternalID != "" {
		l.ExternalID = r.ExternalID
	}
	if r.State != nil {
		l.State = *r.State
	}
	if r.ClearLink {
		l.LinkExpiresAt = nil
	} else if r.LinkExpiresAt != nil {
		l.LinkExpiresAt = r.LinkExpiresAt
	}
	return nil
}

func (m *Mem) MarkRenewals(_ context.Context, now time.Time, within, maxAge time.Duration) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, l := range m.logs {
		if l.Status == LogLogged && l.RecordingID != 0 && l.LinkExpiresAt != nil && l.LinkExpiresAt.Before(now.Add(within)) && l.CreatedAt.After(now.Add(-maxAge)) {
			l.Status, l.Attempts, l.NextAttemptAt = LogUpdate, 0, now
			n++
		}
	}
	return n, nil
}

func (m *Mem) Stats(context.Context) ([]Stat, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := map[[3]string]int{}
	for _, l := range m.logs {
		n[[3]string{m.connectors[l.ConnectorID].row.Name, "logs", l.Status}]++
	}
	for id, c := range m.conns {
		n[[3]string{m.connectors[id].row.Name, "connections", c.row.Status}]++
	}
	var out []Stat
	for k, v := range n {
		out = append(out, Stat{k[0], k[1], k[2], v})
	}
	return out, nil
}

func (m *Mem) AdminEmails(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.Admins), nil
}

func (m *Mem) UserForExtension(_ context.Context, ext string) (UserRef, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.UsersList {
		if u.Extension == ext {
			return u, true, nil
		}
	}
	return UserRef{}, false, nil
}

func (m *Mem) Users(context.Context) ([]UserRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.UsersList), nil
}

func (m *Mem) Mappings(_ context.Context, connectorID int64) ([]Mapping, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Mapping
	for k, v := range m.maps {
		if k[0] == connectorID {
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(a, b Mapping) int { return int(a.UserID - b.UserID) })
	return out, nil
}

func (m *Mem) SetMapping(_ context.Context, connectorID, userID int64, externalUserID, source string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [2]int64{connectorID, userID}
	if old, ok := m.maps[k]; ok && old.Source == "manual" && source != "manual" {
		return nil
	}
	m.maps[k] = Mapping{UserID: userID, ExternalUserID: externalUserID, Source: source}
	return nil
}

func (m *Mem) DeleteMapping(_ context.Context, connectorID, userID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.maps, [2]int64{connectorID, userID})
	return nil
}

func (m *Mem) ListLogs(_ context.Context, connectorID int64, status string, limit int, beforeID int64) ([]LogRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []LogRow
	for _, l := range m.logs {
		if l.ConnectorID == connectorID && (status == "" || l.Status == status) && (beforeID == 0 || l.ID < beforeID) {
			out = append(out, *l)
		}
	}
	slices.SortFunc(out, func(a, b LogRow) int { return int(b.ID - a.ID) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *Mem) GetLog(_ context.Context, id int64) (LogRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.logs[id]
	if !ok {
		return LogRow{}, ErrNotFound
	}
	return *l, nil
}

func (m *Mem) RetryLog(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.logs[id]
	if !ok {
		return ErrNotFound
	}
	if l.Status != LogDead && l.Status != LogSkipped {
		return ErrConflict
	}
	l.Status = LogPending
	if l.ExternalID != "" && l.Payload == nil {
		l.Status = LogUpdate
	}
	l.Attempts, l.NextAttemptAt, l.LastError = 0, m.Now(), ""
	return nil
}

func (m *Mem) CallInvolves(_ context.Context, callID string, exts []string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.Events {
		if e[0] == callID && slices.Contains(exts, e[1]) {
			return true, nil
		}
	}
	return false, nil
}

func (m *Mem) Audit(_ context.Context, actor, action, resource, resourceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.AuditLog = append(m.AuditLog, actor+" "+action+" "+resource+" "+resourceID)
	return nil
}

// Log returns a stored log row by call id for tests.
func (m *Mem) Log(connectorID int64, callID string) (LogRow, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l := m.find(connectorID, callID); l != nil {
		return *l, true
	}
	return LogRow{}, false
}

func (m *Mem) UserConnection(_ context.Context, connectorID, userID int64) (ConnectionRow, Tokens, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.uconns[[2]int64{connectorID, userID}]
	if !ok {
		return ConnectionRow{}, Tokens{}, ErrNotFound
	}
	return c.row, c.tokens, nil
}

func (m *Mem) SaveUserConnection(_ context.Context, connectorID, userID int64, t Tokens) (ConnectionRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [2]int64{connectorID, userID}
	c, ok := m.uconns[k]
	if !ok {
		m.connSeq++
		c = &memConn{row: ConnectionRow{ID: m.connSeq, ConnectorID: connectorID, Scope: "user", UserID: userID}}
		m.uconns[k] = c
	}
	if t.Refresh == "" {
		t.Refresh = c.tokens.Refresh
	}
	if t.Account == "" {
		t.Account = c.tokens.Account
	}
	c.tokens = t
	c.row.ExpiresAt, c.row.ExternalAccount, c.row.GrantedScopes = t.ExpiresAt, t.Account, joinScopes(t.Scopes)
	c.row.Status, c.row.LastError, c.row.UpdatedAt = StatusConnected, "", m.Now()
	return c.row, nil
}

func (m *Mem) DeleteUserConnection(_ context.Context, connectorID, userID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [2]int64{connectorID, userID}
	if _, ok := m.uconns[k]; !ok {
		return ErrNotFound
	}
	delete(m.uconns, k)
	return nil
}

func (m *Mem) userRows(match func(k [2]int64) bool) []ConnectionRow {
	var out []ConnectionRow
	for k, c := range m.uconns {
		if match(k) {
			out = append(out, c.row)
		}
	}
	slices.SortFunc(out, func(a, b ConnectionRow) int { return int(a.ID - b.ID) })
	return out
}

func (m *Mem) UserConnections(_ context.Context, connectorID int64) ([]ConnectionRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.userRows(func(k [2]int64) bool { return k[0] == connectorID }), nil
}

func (m *Mem) UserConnectionsOf(_ context.Context, userID int64) ([]ConnectionRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.userRows(func(k [2]int64) bool { return k[1] == userID }), nil
}

func (m *Mem) SetCalendarSettings(_ context.Context, id int64, dnd bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.uconns {
		if c.row.ID == id {
			c.row.CalendarDND = dnd
			if !dnd {
				c.row.CalendarActive, c.row.CalendarBusyUntil, c.row.CalendarError = false, time.Time{}, ""
			}
			return nil
		}
	}
	return ErrNotFound
}

func (m *Mem) SetCalendarState(_ context.Context, id int64, active bool, busyUntil, checkedAt time.Time, lastError string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.uconns {
		if c.row.ID == id {
			c.row.CalendarActive, c.row.CalendarBusyUntil, c.row.CalendarCheckedAt, c.row.CalendarError = active, busyUntil, checkedAt, lastError
			return nil
		}
	}
	return ErrNotFound
}
