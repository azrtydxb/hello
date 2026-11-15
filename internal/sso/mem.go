package sso

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Mem is a Store in memory for tests. Users are set up directly in Users.
type Mem struct {
	mu          sync.Mutex
	Providers   map[string]Provider
	Secrets     map[string]string
	Required    bool
	Users       map[int64]*MemUser
	IdentityRow []Identity
	Audits      []string
	nextIdent   int64
	nextUser    int64
}

// MemUser is a user of Mem.
type MemUser struct {
	User
	Email string
}

// NewMem returns an empty Mem.
func NewMem() *Mem {
	return &Mem{Providers: map[string]Provider{}, Secrets: map[string]string{}, Users: map[int64]*MemUser{}, nextUser: 1000}
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

// ListProviders implements Store.
func (m *Mem) ListProviders(context.Context) ([]Provider, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Provider{}
	for _, n := range []string{ProviderEntra, ProviderGoogle} {
		if p, ok := m.Providers[n]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

// GetProvider implements Store.
func (m *Mem) GetProvider(_ context.Context, name string) (Provider, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.Providers[name]
	if !ok {
		return p, ErrNotFound
	}
	return p, nil
}

// SaveProvider implements Store.
func (m *Mem) SaveProvider(_ context.Context, actor string, p Provider, secret *string) (Provider, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if secret != nil {
		m.Secrets[p.Provider] = *secret
	}
	_, p.HasClientSecret = m.Secrets[p.Provider]
	p.UpdatedAt = time.Now()
	m.Providers[p.Provider] = p
	m.audit(actor, "update", "sso_provider", p.Provider)
	return p, nil
}

// ProviderSecret implements Store.
func (m *Mem) ProviderSecret(_ context.Context, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.Secrets[name]
	if !ok {
		return "", ErrNotFound
	}
	return s, nil
}

// SSORequired implements Store.
func (m *Mem) SSORequired(context.Context) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Required, nil
}

// SetSSORequired implements Store.
func (m *Mem) SetSSORequired(_ context.Context, actor string, on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if on {
		ok := false
		for _, u := range m.Users {
			ok = ok || (u.BreakGlass && u.Enabled && u.Role == "admin")
		}
		if !ok {
			return ErrNoBreakGlass
		}
	}
	m.Required = on
	m.audit(actor, "sso-required:"+strconv.FormatBool(on), "auth_settings", "sso")
	return nil
}

// BreakGlassUsers implements Store.
func (m *Mem) BreakGlassUsers(context.Context) ([]BreakGlassUser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []BreakGlassUser{}
	for _, u := range m.Users {
		if u.BreakGlass {
			out = append(out, BreakGlassUser{ID: u.ID, Username: u.Username, Enabled: u.Enabled})
		}
	}
	return out, nil
}

// GetUser implements Store.
func (m *Mem) GetUser(_ context.Context, id int64) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.Users[id]
	if !ok {
		return User{}, ErrNotFound
	}
	return u.User, nil
}

// IdentityUser implements Store.
func (m *Mem) IdentityUser(_ context.Context, provider, subject string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, i := range m.IdentityRow {
		if i.Provider == provider && i.Subject == subject {
			if u, ok := m.Users[i.UserID]; ok {
				return u.User, nil
			}
		}
	}
	return User{}, ErrNotFound
}

// SyncedUser implements Store.
func (m *Mem) SyncedUser(_ context.Context, kind, externalID string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.Users {
		if u.ConnectorKind == kind && u.ConnectorID != 0 && strings.EqualFold(u.ExternalID, externalID) {
			return u.User, nil
		}
	}
	return User{}, ErrNotFound
}

// SyncedUsersByEmail implements Store.
func (m *Mem) SyncedUsersByEmail(_ context.Context, email string) ([]User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []User
	for _, u := range m.Users {
		if u.ConnectorID != 0 && strings.EqualFold(u.Email, email) {
			out = append(out, u.User)
		}
	}
	return out, nil
}

func (m *Mem) link(actor string, id Identity) error {
	for _, i := range m.IdentityRow {
		if (i.Provider == id.Provider && i.Subject == id.Subject) || (i.UserID == id.UserID && i.Provider == id.Provider) {
			return ErrLinked
		}
	}
	m.nextIdent++
	id.ID, id.CreatedAt = m.nextIdent, time.Now()
	m.IdentityRow = append(m.IdentityRow, id)
	m.audit(actor, "link-identity:"+id.Provider, "user", strconv.FormatInt(id.UserID, 10))
	return nil
}

// Link implements Store.
func (m *Mem) Link(_ context.Context, actor string, id Identity) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.link(actor, id)
}

// TouchIdentity implements Store.
func (m *Mem) TouchIdentity(_ context.Context, provider, subject string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.IdentityRow {
		if m.IdentityRow[i].Provider == provider && m.IdentityRow[i].Subject == subject {
			t := at
			m.IdentityRow[i].LastLoginAt = &t
		}
	}
	return nil
}

// ListIdentities implements Store.
func (m *Mem) ListIdentities(_ context.Context, userID int64) ([]Identity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Identity{}
	for _, i := range m.IdentityRow {
		if i.UserID == userID {
			out = append(out, i)
		}
	}
	return out, nil
}

// Unlink implements Store.
func (m *Mem) Unlink(_ context.Context, actor string, userID int64, provider string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, id := range m.IdentityRow {
		if id.UserID == userID && id.Provider == provider {
			m.IdentityRow = append(m.IdentityRow[:i], m.IdentityRow[i+1:]...)
			m.audit(actor, "unlink-identity:"+provider, "user", strconv.FormatInt(userID, 10))
			return nil
		}
	}
	return ErrNotFound
}

// CreateUser implements Store.
func (m *Mem) CreateUser(_ context.Context, actor, username, email, _ string, id Identity) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.Users {
		if strings.EqualFold(u.Username, username) {
			return User{}, ErrLinked
		}
	}
	m.nextUser++
	u := &MemUser{User: User{ID: m.nextUser, Username: username, Role: "user", Enabled: true}, Email: email}
	m.Users[u.ID] = u
	id.UserID = u.ID
	if err := m.link(actor, id); err != nil {
		delete(m.Users, u.ID)
		return User{}, err
	}
	m.audit(actor, "create-jit", "user", strconv.FormatInt(u.ID, 10))
	return u.User, nil
}
