package crm

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Personal connections (spec integrations P18): a user signs in to an Outlook
// or Google connector with their own account. The administrator creates the
// connector once (the customer's own OAuth app); each user connects, and the
// tokens belong to that user alone.

// userActorPrefix marks the actor of a personal sign-in in the OAuth state.
const userActorPrefix = "user:"

func userActor(userID int64, actor string) string {
	return userActorPrefix + strconv.FormatInt(userID, 10) + ":" + actor
}

// parseActor splits an OAuth state actor: the user id (0 for an
// administrator's sign-in) and the audit actor.
func parseActor(a string) (int64, string) {
	rest, ok := strings.CutPrefix(a, userActorPrefix)
	if !ok {
		return 0, a
	}
	id, actor, _ := strings.Cut(rest, ":")
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return 0, a
	}
	return n, actor
}

// DNDSetter switches the DND of a user's extensions for the calendar. Turning
// it on never overrides a DND the user set; turning it off clears only a DND
// the calendar set. changed is the number of extensions that changed.
type DNDSetter interface {
	SetCalendarDND(ctx context.Context, userID int64, on bool) (changed int, err error)
}

// Features a per-user connector may use; the administrator picks them, and
// they decide which scopes the sign-in asks for.
const (
	FeatureContacts = "contacts"
	FeatureCalendar = "calendar"
)

// Features parses a connector's "features" setting; empty means contacts.
func Features(cfg map[string]string) []string {
	var out []string
	for _, f := range strings.Split(cfg["features"], ",") {
		if f = strings.TrimSpace(f); f != "" && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		out = []string{FeatureContacts}
	}
	slices.Sort(out)
	return out
}

// UserConnection is a user's own connection as the user sees it; it never
// carries a token.
type UserConnection struct {
	Status          string    `json:"status"`
	ExternalAccount string    `json:"externalAccount,omitempty"`
	GrantedScopes   []string  `json:"grantedScopes"`
	LastError       string    `json:"lastError,omitempty"`
	UpdatedAt       time.Time `json:"updatedAt"`
	// CalendarDND is the user's choice; CalendarActive is set while the
	// calendar holds their DND on.
	CalendarDND       bool       `json:"calendarDnd"`
	CalendarActive    bool       `json:"calendarActive"`
	CalendarBusyUntil *time.Time `json:"calendarBusyUntil,omitempty"`
	CalendarCheckedAt *time.Time `json:"calendarCheckedAt,omitempty"`
	CalendarError     string     `json:"calendarError,omitempty"`
}

// UserConnector is a per-user connector with the asking user's connection.
type UserConnector struct {
	ID           int64           `json:"id"`
	Kind         string          `json:"kind"`
	Name         string          `json:"name"`
	Features     []string        `json:"features"`
	Capabilities Capabilities    `json:"capabilities"`
	Connection   *UserConnection `json:"connection"`
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func userConnView(c ConnectionRow) *UserConnection {
	scopes := strings.Fields(c.GrantedScopes)
	if scopes == nil {
		scopes = []string{}
	}
	return &UserConnection{Status: c.Status, ExternalAccount: c.ExternalAccount, GrantedScopes: scopes, LastError: c.LastError,
		UpdatedAt: c.UpdatedAt, CalendarDND: c.CalendarDND, CalendarActive: c.CalendarActive, CalendarBusyUntil: timePtr(c.CalendarBusyUntil),
		CalendarCheckedAt: timePtr(c.CalendarCheckedAt), CalendarError: c.CalendarError}
}

// perUser returns the enabled connector rows that sign in per user.
func (s *Service) perUser(ctx context.Context) ([]ConnectorRow, error) {
	rows, err := s.Repo.ListConnectors(ctx)
	if err != nil {
		return nil, err
	}
	var out []ConnectorRow
	for _, r := range rows {
		if c, ok := s.Tokens.Providers(r.Kind); ok && r.Enabled && c.Capabilities().UserScope {
			out = append(out, r)
		}
	}
	return out, nil
}

// UserConnectors lists the personal connectors the user may connect, with
// the user's own connection to each.
func (s *Service) UserConnectors(ctx context.Context, userID int64) ([]UserConnector, error) {
	rows, err := s.perUser(ctx)
	if err != nil {
		return nil, err
	}
	conns, err := s.Repo.UserConnectionsOf(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]UserConnector, 0, len(rows))
	for _, r := range rows {
		c, _ := s.Tokens.Providers(r.Kind)
		uc := UserConnector{ID: r.ID, Kind: r.Kind, Name: r.Name, Features: Features(r.Config), Capabilities: c.Capabilities()}
		if i := slices.IndexFunc(conns, func(x ConnectionRow) bool { return x.ConnectorID == r.ID }); i >= 0 {
			uc.Connection = userConnView(conns[i])
		}
		out = append(out, uc)
	}
	return out, nil
}

func (s *Service) userConnector(ctx context.Context, id int64) (ConnectorRow, Connector, error) {
	cur, err := s.Repo.GetConnector(ctx, id)
	if err != nil {
		return cur, nil, err
	}
	conn, ok := s.Tokens.Providers(cur.Kind)
	if !ok || !cur.Enabled || !conn.Capabilities().UserScope {
		return cur, nil, ErrNotUserScope
	}
	return cur, conn, nil
}

// ConnectUser starts a user's own sign-in and returns where to send the
// browser.
func (s *Service) ConnectUser(ctx context.Context, actor string, userID, id int64) (string, error) {
	cur, conn, err := s.userConnector(ctx, id)
	if err != nil {
		return "", err
	}
	if s.PublicURL == "" {
		return "", ErrNoPublicURL
	}
	oa, ok := conn.(OAuth)
	if !ok {
		return "", ErrNotOAuth
	}
	cfg, err := s.Tokens.Config(ctx, cur)
	if err != nil {
		return "", err
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return "", ErrNoCredentials
	}
	state, err := s.Repo.NewState(ctx, id, userActor(userID, actor), StateTTL)
	if err != nil {
		return "", err
	}
	return oa.AuthURL(cfg, s.redirectURI(), state), nil
}

// DisconnectUser removes a user's connection, revokes the token at the
// provider when it can and clears a DND the calendar set.
func (s *Service) DisconnectUser(ctx context.Context, actor string, userID, id int64) error {
	cur, conn, err := s.userConnector(ctx, id)
	if err != nil {
		return err
	}
	cr, t, err := s.Repo.UserConnection(ctx, id, userID)
	if err != nil {
		return err
	}
	if rv, ok := conn.(Revoker); ok && t.Refresh != "" {
		if cfg, err := s.Tokens.Config(ctx, cur); err == nil {
			rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if err := rv.RevokeToken(rctx, cfg, t.Refresh); err != nil {
				s.log().Warn("crm: revoking a personal token", "connector", id, "error", err)
			}
			cancel()
		}
	}
	if err := s.Repo.DeleteUserConnection(ctx, id, userID); err != nil {
		return err
	}
	if cr.CalendarActive && s.DND != nil {
		if _, err := s.DND.SetCalendarDND(ctx, userID, false); err != nil {
			s.log().Warn("crm: clearing the calendar DND", "user", userID, "error", err)
		}
	}
	_ = s.Repo.Audit(ctx, actor, "disconnect_user", "crm_connector", strconv.FormatInt(id, 10))
	s.Lookup.Invalidate()
	return nil
}

// SetCalendarDND turns calendar-driven DND on or off for a user's
// connection. On needs calendar access in the connection; off clears a DND
// the calendar set.
func (s *Service) SetCalendarDND(ctx context.Context, actor string, userID, id int64, on bool) (UserConnection, error) {
	cur, conn, err := s.userConnector(ctx, id)
	if err != nil {
		return UserConnection{}, err
	}
	cr, _, err := s.Repo.UserConnection(ctx, id, userID)
	if err != nil {
		return UserConnection{}, err
	}
	if on {
		_, isCal := conn.(Calendar)
		if !isCal || !slices.Contains(Features(cur.Config), FeatureCalendar) || !strings.Contains(strings.ToLower(cr.GrantedScopes), "calendar") {
			return UserConnection{}, ErrNoCalendar
		}
		if s.DND == nil {
			return UserConnection{}, ErrNoCalendar
		}
	}
	if err := s.Repo.SetCalendarSettings(ctx, cr.ID, on); err != nil {
		return UserConnection{}, err
	}
	if !on && cr.CalendarActive && s.DND != nil {
		if _, err := s.DND.SetCalendarDND(ctx, userID, false); err != nil {
			return UserConnection{}, err
		}
	}
	_ = s.Repo.Audit(ctx, actor, "calendar_dnd", "crm_connector", strconv.FormatInt(id, 10))
	cr, _, err = s.Repo.UserConnection(ctx, id, userID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return UserConnection{}, err
	}
	return *userConnView(cr), nil
}

// Cancel drops the state of a sign-in the user declined at the provider and
// says whether it was a personal one, so the browser returns to the right
// page.
func (s *Service) Cancel(ctx context.Context, state string) (personal bool) {
	_, actor, err := s.Repo.TakeState(ctx, state)
	if err != nil {
		return false
	}
	id, _ := parseActor(actor)
	return id != 0
}
