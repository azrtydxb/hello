package crm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/crm/sidebar"
)

// Errors the API maps to statuses.
var (
	ErrNoPublicURL   = errors.New("crm: HELLO_PUBLIC_URL is not set")
	ErrNotOAuth      = errors.New("crm: the connector does not use OAuth")
	ErrNoCredentials = errors.New("crm: the client id and secret are not set")
	ErrNotConnected  = errors.New("crm: the connector is not connected")
	ErrBadState      = errors.New("crm: unknown or expired state")
	ErrRateLimited   = errors.New("crm: too many tests")
	ErrForbidden     = errors.New("crm: not allowed")
	ErrNoApp         = sidebar.ErrNoApp
)

// ValidationError lists the fields of a request that failed validation.
type ValidationError struct{ Fields map[string]string }

func (e *ValidationError) Error() string { return "crm: invalid settings" }

// Optional connector interfaces.
type (
	// ConfigValidator checks and completes a connector's own settings.
	ConfigValidator interface {
		ValidateConfig(cfg map[string]string) (map[string]string, error)
	}
	// Pinger checks a connection with a harmless call.
	Pinger interface {
		Ping(ctx context.Context, c Conn) error
	}
	// Revoker revokes a refresh token at the CRM when a connection is
	// removed.
	Revoker interface {
		RevokeToken(ctx context.Context, c Config, refresh string) error
	}
)

// CallbackPath is the OAuth redirect URI path shared by every connector.
const CallbackPath = "/api/v1/integrations/crm/oauth/callback"

// StateTTL is how long an authorization may take.
const StateTTL = 10 * time.Minute

// Service is the administrative side: connector settings, the OAuth flow,
// the job list and the user mapping.
type Service struct {
	Repo   Repo
	Tokens *TokenManager
	Lookup *Lookup
	Links  LinkMinter
	// PublicURL is HELLO_PUBLIC_URL; without it the OAuth flow is off.
	PublicURL string
	Log       *slog.Logger
	Now       func() time.Time

	mu    sync.Mutex
	tests map[int64][]time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Connection is the public view of a connector's connection; it never
// carries a token.
type Connection struct {
	Status          string    `json:"status"`
	ExternalAccount string    `json:"externalAccount,omitempty"`
	GrantedScopes   []string  `json:"grantedScopes"`
	LastError       string    `json:"lastError,omitempty"`
	ExpiresAt       time.Time `json:"expiresAt,omitzero"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// ConnectorView is a connector with its capabilities and connection.
type ConnectorView struct {
	ConnectorRow
	Capabilities Capabilities `json:"capabilities"`
	Connection   *Connection  `json:"connection"`
	RedirectURI  string       `json:"redirectUri,omitempty"`
	// Auth is how the connector signs in: "oauth" sends the administrator to
	// the CRM to approve the app, "direct" signs in with the credentials the
	// administrator typed.
	Auth string `json:"auth"`
}

func (s *Service) redirectURI() string {
	if s.PublicURL == "" {
		return ""
	}
	return strings.TrimRight(s.PublicURL, "/") + CallbackPath
}

func (s *Service) view(ctx context.Context, r ConnectorRow) (ConnectorView, error) {
	v := ConnectorView{ConnectorRow: r, RedirectURI: s.redirectURI()}
	if c, ok := s.Tokens.Providers(r.Kind); ok {
		v.Capabilities = c.Capabilities()
		v.Auth = authKind(c)
	}
	cr, _, err := s.Repo.OrgConnection(ctx, r.ID)
	switch {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return v, err
	default:
		v.Connection = &Connection{Status: cr.Status, ExternalAccount: cr.ExternalAccount, GrantedScopes: strings.Fields(cr.GrantedScopes),
			LastError: cr.LastError, ExpiresAt: cr.ExpiresAt, UpdatedAt: cr.UpdatedAt}
		if v.Connection.GrantedScopes == nil {
			v.Connection.GrantedScopes = []string{}
		}
	}
	return v, nil
}

func authKind(c Connector) string {
	if _, ok := c.(Direct); ok {
		return "direct"
	}
	return "oauth"
}

// secretBound lists the settings whose change asks for the secret again.
func secretBound(c Connector) []string {
	if sb, ok := c.(SecretBound); ok {
		return sb.SecretBoundKeys()
	}
	return []string{"clientId"}
}

// List returns every connector.
func (s *Service) List(ctx context.Context) ([]ConnectorView, error) {
	rows, err := s.Repo.ListConnectors(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ConnectorView, 0, len(rows))
	for _, r := range rows {
		v, err := s.view(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Get returns one connector.
func (s *Service) Get(ctx context.Context, id int64) (ConnectorView, error) {
	r, err := s.Repo.GetConnector(ctx, id)
	if err != nil {
		return ConnectorView{}, err
	}
	return s.view(ctx, r)
}

// Settings is a create or update request; nil fields keep the stored value
// on update.
type Settings struct {
	Kind          *string
	Name          *string
	Enabled       *bool
	Config        map[string]string
	ClientSecret  *string
	Recordings    *bool
	LinkTTLHours  *int
	LogMissed     *bool
	FallbackOwner *string
}

func (s *Service) input(base ConnectorInput, in Settings) (ConnectorInput, *ValidationError) {
	f := map[string]string{}
	if in.Kind != nil {
		base.Kind = *in.Kind
	}
	if in.Name != nil {
		base.Name = strings.TrimSpace(*in.Name)
	}
	if in.Enabled != nil {
		base.Enabled = *in.Enabled
	}
	if in.Recordings != nil {
		base.Recordings = *in.Recordings
	}
	if in.LinkTTLHours != nil {
		base.LinkTTLHours = *in.LinkTTLHours
	}
	if in.LogMissed != nil {
		base.LogMissed = *in.LogMissed
	}
	if in.FallbackOwner != nil {
		base.FallbackOwner = strings.TrimSpace(*in.FallbackOwner)
	}
	if in.Config != nil {
		cfg := cloneCfg(base.Config)
		for k, v := range in.Config {
			cfg[k] = strings.TrimSpace(v)
		}
		base.Config = cfg
	}
	conn, known := s.Tokens.Providers(base.Kind)
	if !known {
		f["kind"] = "unknown connector kind; one of " + strings.Join(Kinds(), ", ")
	}
	if n := len([]rune(base.Name)); n < 1 || n > 64 {
		f["name"] = "must be 1-64 characters"
	}
	if base.LinkTTLHours < 1 || base.LinkTTLHours > 8760 {
		f["linkTtlHours"] = "must be 1 to 8760"
	}
	if len(base.FallbackOwner) > 64 {
		f["fallbackOwner"] = "must be at most 64 characters"
	}
	if len(base.Config["clientId"]) > 200 {
		f["config.clientId"] = "must be at most 200 characters"
	}
	if v, ok := conn.(ConfigValidator); ok && known {
		cfg, err := v.ValidateConfig(nonNil(base.Config))
		if err != nil {
			f["config"] = err.Error()
		} else {
			base.Config = cfg
		}
	}
	if known && base.Recordings && !conn.Capabilities().Recordings {
		f["recordings"] = "this connector does not take recording links"
	}
	if len(f) > 0 {
		return base, &ValidationError{f}
	}
	return base, nil
}

// Create adds a connector.
func (s *Service) Create(ctx context.Context, actor string, in Settings) (ConnectorView, error) {
	base := ConnectorInput{Enabled: true, LinkTTLHours: 720, LogMissed: true, Config: map[string]string{}}
	ci, verr := s.input(base, in)
	if verr != nil {
		return ConnectorView{}, verr
	}
	secret := ""
	if in.ClientSecret != nil {
		secret = *in.ClientSecret
	}
	r, err := s.Repo.CreateConnector(ctx, ci, secret)
	if err != nil {
		return ConnectorView{}, err
	}
	_ = s.Repo.Audit(ctx, actor, "create", "crm_connector", strconv.FormatInt(r.ID, 10))
	s.Lookup.Invalidate()
	return s.view(ctx, r)
}

// Update changes a connector's settings. The kind cannot change, and a new
// client id re-requires the secret (a changed id with the old secret would
// send the secret to another app).
func (s *Service) Update(ctx context.Context, actor string, id int64, in Settings) (ConnectorView, error) {
	cur, err := s.Repo.GetConnector(ctx, id)
	if err != nil {
		return ConnectorView{}, err
	}
	if in.Kind != nil && *in.Kind != cur.Kind {
		return ConnectorView{}, &ValidationError{map[string]string{"kind": "cannot be changed"}}
	}
	base := ConnectorInput{Kind: cur.Kind, Name: cur.Name, Enabled: cur.Enabled, Config: cur.Config, Recordings: cur.Recordings,
		LinkTTLHours: cur.LinkTTLHours, LogMissed: cur.LogMissed, FallbackOwner: cur.FallbackOwner}
	ci, verr := s.input(base, in)
	if verr != nil {
		return ConnectorView{}, verr
	}
	conn, _ := s.Tokens.Providers(cur.Kind)
	if in.ClientSecret == nil {
		for _, k := range secretBound(conn) {
			if ci.Config[k] != cur.Config[k] {
				return ConnectorView{}, &ValidationError{map[string]string{"clientSecret": "required when the " + k + " changes"}}
			}
		}
	}
	// A direct connector signs in again with the new credentials before they
	// are stored, so a typo never replaces a working connection.
	var fresh *Tokens
	if d, ok := conn.(Direct); ok && in.ClientSecret != nil {
		if _, _, cerr := s.Repo.OrgConnection(ctx, id); cerr == nil {
			t, aerr := d.Authenticate(ctx, Config{Kind: ci.Kind, ClientID: ci.Config["clientId"], ClientSecret: *in.ClientSecret, Extra: ci.Config})
			if aerr != nil {
				return ConnectorView{}, &ValidationError{map[string]string{"clientSecret": scrub(aerr.Error())}}
			}
			fresh = &t
		}
	}
	r, err := s.Repo.UpdateConnector(ctx, id, ci, in.ClientSecret)
	if err != nil {
		return ConnectorView{}, err
	}
	if fresh != nil {
		if _, err := s.Repo.SaveOrgConnection(ctx, id, *fresh); err != nil {
			return ConnectorView{}, err
		}
	}
	if (cur.Enabled && !r.Enabled) || (cur.Recordings && !r.Recordings) {
		s.revokeLinks(ctx, id)
	}
	_ = s.Repo.Audit(ctx, actor, "update", "crm_connector", strconv.FormatInt(id, 10))
	s.Lookup.Invalidate()
	return s.view(ctx, r)
}

func (s *Service) revokeLinks(ctx context.Context, id int64) {
	if s.Links == nil {
		return
	}
	if err := s.Links.RevokeAll(ctx, LinkLabel(id)); err != nil {
		s.log().Warn("crm: revoking recording links", "connector", id, "error", err)
	}
}

// Delete removes a connector, its connection and its jobs, and revokes its
// recording links.
func (s *Service) Delete(ctx context.Context, actor string, id int64) error {
	cur, err := s.Repo.GetConnector(ctx, id)
	if err != nil {
		return err
	}
	s.revokeToken(ctx, cur)
	s.revokeLinks(ctx, id)
	if err := s.Repo.DeleteConnector(ctx, id); err != nil {
		return err
	}
	_ = s.Repo.Audit(ctx, actor, "delete", "crm_connector", strconv.FormatInt(id, 10))
	s.Lookup.Invalidate()
	return nil
}

func (s *Service) revokeToken(ctx context.Context, cur ConnectorRow) {
	conn, ok := s.Tokens.Providers(cur.Kind)
	rv, isRv := conn.(Revoker)
	if !ok || !isRv {
		return
	}
	_, t, err := s.Repo.OrgConnection(ctx, cur.ID)
	if err != nil || t.Refresh == "" {
		return
	}
	cfg, err := s.Tokens.Config(ctx, cur)
	if err != nil {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := rv.RevokeToken(rctx, cfg, t.Refresh); err != nil {
		s.log().Warn("crm: revoking the token at the CRM", "connector", cur.ID, "error", err)
	}
}

// Connect starts the OAuth flow and returns where to send the browser. A
// direct connector has no browser step: Connect signs in with the stored
// credentials, stores the connection and returns an empty address.
func (s *Service) Connect(ctx context.Context, actor string, id int64) (string, error) {
	cur, err := s.Repo.GetConnector(ctx, id)
	if err != nil {
		return "", err
	}
	conn, ok := s.Tokens.Providers(cur.Kind)
	if !ok {
		return "", ErrNotOAuth
	}
	if d, isDirect := conn.(Direct); isDirect {
		return "", s.connectDirect(ctx, actor, cur, d)
	}
	if s.PublicURL == "" {
		return "", ErrNoPublicURL
	}
	oa, isOA := conn.(OAuth)
	if !isOA {
		return "", ErrNotOAuth
	}
	cfg, err := s.Tokens.Config(ctx, cur)
	if err != nil {
		return "", err
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return "", ErrNoCredentials
	}
	state, err := s.Repo.NewState(ctx, id, actor, StateTTL)
	if err != nil {
		return "", err
	}
	return oa.AuthURL(cfg, s.redirectURI(), state), nil
}

// connectDirect signs a direct connector in and stores the connection. A
// refused credential is a validation error naming the secret, never the
// CRM's raw answer.
func (s *Service) connectDirect(ctx context.Context, actor string, cur ConnectorRow, d Direct) error {
	cfg, err := s.Tokens.Config(ctx, cur)
	if err != nil {
		return err
	}
	if !cur.HasSecret {
		return ErrNoCredentials
	}
	actx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	t, err := d.Authenticate(actx, cfg)
	if err != nil {
		_ = s.Repo.Audit(ctx, actor, "connect_failed", "crm_connector", strconv.FormatInt(cur.ID, 10))
		return &ValidationError{map[string]string{"clientSecret": scrub(err.Error())}}
	}
	if _, err := s.Repo.SaveOrgConnection(ctx, cur.ID, t); err != nil {
		return err
	}
	_ = s.Repo.Audit(ctx, actor, "connect", "crm_connector", strconv.FormatInt(cur.ID, 10))
	s.Lookup.Invalidate()
	return nil
}

// Callback completes the OAuth flow: the state names the connector and the
// administrator who started it. It returns the connector id.
func (s *Service) Callback(ctx context.Context, state, code string) (int64, error) {
	id, actor, err := s.Repo.TakeState(ctx, state)
	if err != nil {
		return 0, ErrBadState
	}
	cur, err := s.Repo.GetConnector(ctx, id)
	if err != nil {
		return 0, ErrBadState
	}
	conn, ok := s.Tokens.Providers(cur.Kind)
	oa, isOA := conn.(OAuth)
	if !ok || !isOA {
		return id, ErrNotOAuth
	}
	cfg, err := s.Tokens.Config(ctx, cur)
	if err != nil {
		return id, err
	}
	t, err := oa.Exchange(ctx, cfg, s.redirectURI(), code)
	if err != nil {
		_ = s.Repo.Audit(ctx, actor, "connect_failed", "crm_connector", strconv.FormatInt(id, 10))
		return id, err
	}
	if _, err := s.Repo.SaveOrgConnection(ctx, id, t); err != nil {
		return id, err
	}
	_ = s.Repo.Audit(ctx, actor, "connect", "crm_connector", strconv.FormatInt(id, 10))
	s.Lookup.Invalidate()
	return id, nil
}

// Disconnect removes the connection, revokes the token at the CRM when it
// can, and revokes the connector's recording links. Jobs stay and wait.
func (s *Service) Disconnect(ctx context.Context, actor string, id int64) error {
	cur, err := s.Repo.GetConnector(ctx, id)
	if err != nil {
		return err
	}
	s.revokeToken(ctx, cur)
	if err := s.Repo.DeleteOrgConnection(ctx, id); err != nil {
		return err
	}
	s.revokeLinks(ctx, id)
	_ = s.Repo.Audit(ctx, actor, "disconnect", "crm_connector", strconv.FormatInt(id, 10))
	s.Lookup.Invalidate()
	return nil
}

// TestResult is the answer of a connection test.
type TestResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Test makes a harmless call to the CRM (6 per minute per connector).
func (s *Service) Test(ctx context.Context, actor string, id int64) (TestResult, error) {
	cur, err := s.Repo.GetConnector(ctx, id)
	if err != nil {
		return TestResult{}, err
	}
	s.mu.Lock()
	if s.tests == nil {
		s.tests = map[int64][]time.Time{}
	}
	now := s.now()
	recent := s.tests[id][:0]
	for _, t := range s.tests[id] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= 6 {
		s.tests[id] = recent
		s.mu.Unlock()
		return TestResult{}, ErrRateLimited
	}
	s.tests[id] = append(recent, now)
	s.mu.Unlock()

	conn, cc, err := s.Tokens.Conn(ctx, cur)
	if err != nil {
		if errors.Is(err, ErrNeedsReauth) {
			return TestResult{}, ErrNotConnected
		}
		return TestResult{OK: false, Error: scrub(err.Error())}, nil
	}
	p, ok := conn.(Pinger)
	if !ok {
		return TestResult{OK: true}, nil
	}
	_ = s.Repo.Audit(ctx, actor, "test", "crm_connector", strconv.FormatInt(id, 10))
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err = p.Ping(tctx, cc)
	if errors.Is(err, ErrAuth) {
		nc, rerr := s.Tokens.Refresh(ctx, cur)
		if rerr != nil {
			return TestResult{OK: false, Error: "the CRM refused the access token and it could not be refreshed"}, nil
		}
		err = p.Ping(tctx, nc)
	}
	if err != nil {
		return TestResult{OK: false, Error: scrub(err.Error())}, nil
	}
	return TestResult{OK: true}, nil
}

// Logs lists a connector's jobs, newest first.
func (s *Service) Logs(ctx context.Context, id int64, status string, limit int, before int64) ([]LogRow, error) {
	if _, err := s.Repo.GetConnector(ctx, id); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.Repo.ListLogs(ctx, id, status, limit, before)
	if rows == nil {
		rows = []LogRow{}
	}
	return rows, err
}

// RetryLog puts a dead or skipped job back in the queue.
func (s *Service) RetryLog(ctx context.Context, actor string, id int64) (LogRow, error) {
	if err := s.Repo.RetryLog(ctx, id); err != nil {
		return LogRow{}, err
	}
	_ = s.Repo.Audit(ctx, actor, "retry", "crm_call_log", strconv.FormatInt(id, 10))
	return s.Repo.GetLog(ctx, id)
}

// UserMapping is one Hello user and the CRM user their calls are logged for.
type UserMapping struct {
	UserID         int64  `json:"userId"`
	Username       string `json:"username"`
	Email          string `json:"email"`
	Extension      string `json:"extension,omitempty"`
	ExternalUserID string `json:"externalUserId,omitempty"`
	Source         string `json:"source,omitempty"`
}

// Mappings lists the users with their CRM user, if any.
func (s *Service) Mappings(ctx context.Context, id int64) ([]UserMapping, error) {
	if _, err := s.Repo.GetConnector(ctx, id); err != nil {
		return nil, err
	}
	users, err := s.Repo.Users(ctx)
	if err != nil {
		return nil, err
	}
	maps, err := s.Repo.Mappings(ctx, id)
	if err != nil {
		return nil, err
	}
	by := map[int64]Mapping{}
	for _, m := range maps {
		by[m.UserID] = m
	}
	out := make([]UserMapping, 0, len(users))
	for _, u := range users {
		um := UserMapping{UserID: u.ID, Username: u.Username, Email: u.Email, Extension: u.Extension}
		if m, ok := by[u.ID]; ok {
			um.ExternalUserID, um.Source = m.ExternalUserID, m.Source
		}
		out = append(out, um)
	}
	return out, nil
}

// SetMapping sets (or, with an empty externalID, clears) a manual mapping.
func (s *Service) SetMapping(ctx context.Context, actor string, id, userID int64, externalID string) error {
	if _, err := s.Repo.GetConnector(ctx, id); err != nil {
		return err
	}
	users, err := s.Repo.Users(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, u := range users {
		found = found || u.ID == userID
	}
	if !found {
		return ErrNotFound
	}
	externalID = strings.TrimSpace(externalID)
	if len(externalID) > 64 {
		return &ValidationError{map[string]string{"externalUserId": "must be at most 64 characters"}}
	}
	if externalID == "" {
		err = s.Repo.DeleteMapping(ctx, id, userID)
	} else {
		err = s.Repo.SetMapping(ctx, id, userID, externalID, "manual")
	}
	if err != nil {
		return err
	}
	_ = s.Repo.Audit(ctx, actor, "map_user", "crm_connector", fmt.Sprintf("%d:%d", id, userID))
	return nil
}

// Engagement records the CRM reference the iframe created for a call, so the
// server patches it instead of creating a duplicate. exts is the set of
// extensions a restricted caller (the user role) may act for; nil means no
// restriction.
func (s *Service) Engagement(ctx context.Context, actor, kind, callID, ref string, exts []string, restricted bool) (adopted bool, err error) {
	rows, err := s.Repo.ListConnectors(ctx)
	if err != nil {
		return false, err
	}
	var conn *ConnectorRow
	for i := range rows {
		if rows[i].Kind == kind && rows[i].Enabled {
			conn = &rows[i]
			break
		}
	}
	if conn == nil {
		return false, ErrNotFound
	}
	if restricted {
		ok, err := s.Repo.CallInvolves(ctx, callID, exts)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, ErrForbidden
		}
	}
	adopted, err = s.Repo.AttachEngagement(ctx, conn.ID, callID, ref)
	if err != nil {
		return false, err
	}
	_ = s.Repo.Audit(ctx, actor, "engagement", "crm_connector", strconv.FormatInt(conn.ID, 10))
	return adopted, nil
}

// SidebarApp builds the ticket-sidebar app of a helpdesk connector (Zendesk,
// Freshdesk) for the customer to upload to their own account. It needs
// HELLO_PUBLIC_URL: the app's Call button opens Hello's dialer there.
func (s *Service) SidebarApp(ctx context.Context, actor string, id int64) (name string, data []byte, err error) {
	cur, err := s.Repo.GetConnector(ctx, id)
	if err != nil {
		return "", nil, err
	}
	if !sidebar.Supported(cur.Kind) {
		return "", nil, ErrNoApp
	}
	if s.PublicURL == "" {
		return "", nil, ErrNoPublicURL
	}
	name, data, err = sidebar.Build(cur.Kind, s.PublicURL)
	if err != nil {
		return "", nil, err
	}
	_ = s.Repo.Audit(ctx, actor, "download_app", "crm_connector", strconv.FormatInt(id, 10))
	return name, data, nil
}
