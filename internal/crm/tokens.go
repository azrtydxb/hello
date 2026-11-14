package crm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Provider returns the connector of a kind.
type Provider func(kind string) (Connector, bool)

// DefaultProvider builds connectors of the registered kinds on hc (the
// SSRF-safe client), once each.
func DefaultProvider(hc *http.Client) Provider {
	var mu sync.Mutex
	built := map[string]Connector{}
	return func(kind string) (Connector, bool) {
		mu.Lock()
		defer mu.Unlock()
		if c, ok := built[kind]; ok {
			return c, true
		}
		c, ok := New(kind, hc)
		if ok {
			built[kind] = c
		}
		return c, ok
	}
}

// ReauthNotifier tells the administrators a connection stopped working. It
// must not block for long.
type ReauthNotifier func(ctx context.Context, c ConnectorRow, reason string)

// TokenManager hands out valid access tokens: it opens the sealed tokens
// (the Repo does), refreshes one that is about to expire and marks the
// connection needs_reauth when the CRM refuses the refresh token for good.
type TokenManager struct {
	Repo      Repo
	Providers Provider
	Notify    ReauthNotifier
	Log       *slog.Logger
	Now       func() time.Time
	// Skew is how long before expiry a token is refreshed (60 s by default).
	Skew time.Duration

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (m *TokenManager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *TokenManager) log() *slog.Logger {
	if m.Log != nil {
		return m.Log
	}
	return slog.New(slog.DiscardHandler)
}

func (m *TokenManager) lock(id, userID int64) *sync.Mutex {
	key := strconv.FormatInt(id, 10) + ":" + strconv.FormatInt(userID, 10)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locks == nil {
		m.locks = map[string]*sync.Mutex{}
	}
	l, ok := m.locks[key]
	if !ok {
		l = &sync.Mutex{}
		m.locks[key] = l
	}
	return l
}

// connection reads the org connection (userID 0) or a user's.
func (m *TokenManager) connection(ctx context.Context, id, userID int64) (ConnectionRow, Tokens, error) {
	if userID == 0 {
		return m.Repo.OrgConnection(ctx, id)
	}
	return m.Repo.UserConnection(ctx, id, userID)
}

func (m *TokenManager) save(ctx context.Context, id, userID int64, t Tokens) error {
	var err error
	if userID == 0 {
		_, err = m.Repo.SaveOrgConnection(ctx, id, t)
	} else {
		_, err = m.Repo.SaveUserConnection(ctx, id, userID, t)
	}
	return err
}

func (m *TokenManager) skew() time.Duration {
	if m.Skew > 0 {
		return m.Skew
	}
	return time.Minute
}

// Config assembles a connector's Config with its opened client secret.
func (m *TokenManager) Config(ctx context.Context, row ConnectorRow) (Config, error) {
	sec, err := m.Repo.ClientSecret(ctx, row.ID)
	if err != nil {
		return Config{}, err
	}
	return Config{Kind: row.Kind, ClientID: row.Config["clientId"], ClientSecret: sec, Extra: row.Config}, nil
}

// Conn returns the connector with a valid access token for the connector's
// org connection. ErrNeedsReauth means the connection is not usable until an
// administrator connects again.
func (m *TokenManager) Conn(ctx context.Context, row ConnectorRow) (Connector, Conn, error) {
	return m.ConnFor(ctx, row, 0)
}

// ConnFor is Conn for the connection of one user (userID 0 is the
// organisation's): the Outlook and Google connectors sign in per user.
func (m *TokenManager) ConnFor(ctx context.Context, row ConnectorRow, userID int64) (Connector, Conn, error) {
	conn, ok := m.Providers(row.Kind)
	if !ok {
		return nil, Conn{}, fmt.Errorf("crm: unknown connector kind %q", row.Kind)
	}
	cfg, err := m.Config(ctx, row)
	if err != nil {
		return nil, Conn{}, err
	}
	cr, t, err := m.connection(ctx, row.ID, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, Conn{}, ErrNeedsReauth
		}
		return nil, Conn{}, err
	}
	if cr.Status != StatusConnected {
		return nil, Conn{}, ErrNeedsReauth
	}
	if !t.ExpiresAt.IsZero() && !t.ExpiresAt.After(m.now().Add(m.skew())) {
		if t, err = m.refresh(ctx, row, conn, cfg, userID, false); err != nil {
			return nil, Conn{}, err
		}
	}
	return conn, Conn{Config: cfg, AccessToken: t.Access, ExternalAcct: t.Account}, nil
}

// Refresh forces a refresh after the CRM refused the access token.
func (m *TokenManager) Refresh(ctx context.Context, row ConnectorRow) (Conn, error) {
	return m.RefreshFor(ctx, row, 0)
}

// RefreshFor is Refresh for one user's connection.
func (m *TokenManager) RefreshFor(ctx context.Context, row ConnectorRow, userID int64) (Conn, error) {
	conn, ok := m.Providers(row.Kind)
	if !ok {
		return Conn{}, fmt.Errorf("crm: unknown connector kind %q", row.Kind)
	}
	cfg, err := m.Config(ctx, row)
	if err != nil {
		return Conn{}, err
	}
	t, err := m.refresh(ctx, row, conn, cfg, userID, true)
	if err != nil {
		return Conn{}, err
	}
	return Conn{Config: cfg, AccessToken: t.Access, ExternalAcct: t.Account}, nil
}

func (m *TokenManager) refresh(ctx context.Context, row ConnectorRow, conn Connector, cfg Config, userID int64, force bool) (Tokens, error) {
	l := m.lock(row.ID, userID)
	l.Lock()
	defer l.Unlock()
	cr, t, err := m.connection(ctx, row.ID, userID)
	if err != nil {
		return Tokens{}, err
	}
	// Another goroutine may have refreshed while this one waited.
	if !force && (t.ExpiresAt.IsZero() || t.ExpiresAt.After(m.now().Add(m.skew()))) {
		return t, nil
	}
	var nt Tokens
	// Connect treats a connector that is both as direct; so does this.
	switch c := conn.(type) {
	case Direct:
		// A client-credentials token is requested again; a static
		// credential comes back unchanged, so a second refusal marks the
		// connection needs_reauth in the caller.
		nt, err = c.Authenticate(ctx, cfg)
	case OAuth:
		if t.Refresh == "" {
			m.needsReauth(ctx, row, cr.ID, userID, "no refresh token")
			return Tokens{}, ErrNeedsReauth
		}
		nt, err = c.Refresh(ctx, cfg, t.Refresh)
	default:
		m.needsReauth(ctx, row, cr.ID, userID, "no refresh token")
		return Tokens{}, ErrNeedsReauth
	}
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && !ae.Retry {
			m.needsReauth(ctx, row, cr.ID, userID, "the CRM refused the credentials")
			return Tokens{}, ErrNeedsReauth
		}
		return Tokens{}, err
	}
	if nt.Refresh == "" {
		nt.Refresh = t.Refresh
	}
	if nt.Account == "" {
		nt.Account = t.Account
	}
	if len(nt.Scopes) == 0 {
		nt.Scopes = t.Scopes
	}
	if err := m.save(ctx, row.ID, userID, nt); err != nil {
		return Tokens{}, err
	}
	return nt, nil
}

// MarkNeedsReauth is used when the CRM refuses a freshly refreshed token.
func (m *TokenManager) MarkNeedsReauth(ctx context.Context, row ConnectorRow, reason string) {
	cr, _, err := m.Repo.OrgConnection(ctx, row.ID)
	if err != nil {
		return
	}
	m.needsReauth(ctx, row, cr.ID, 0, reason)
}

func (m *TokenManager) needsReauth(ctx context.Context, row ConnectorRow, connID, userID int64, reason string) {
	m.log().Warn("crm: connection needs re-authorisation", "connector", row.ID, "kind", row.Kind, "reason", reason)
	if err := m.Repo.SetConnectionStatus(ctx, connID, StatusNeedsReauth, reason); err != nil {
		m.log().Error("crm: marking the connection", "connector", row.ID, "error", err)
		return
	}
	_ = m.Repo.Audit(ctx, "system", "needs_reauth", "crm_connector", strconv.FormatInt(row.ID, 10))
	// A user's own connection is shown to that user in the console; the
	// administrators are told only about the organisation's.
	if m.Notify != nil && userID == 0 {
		m.Notify(ctx, row, reason)
	}
}
