package crm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/jackc/pgx/v5/pgconn"

	hellodb "github.com/azrtydxb/hello/internal/db"
	"github.com/azrtydxb/hello/internal/secret"
)

// PG is the PostgreSQL Repo, on momo (internal/db). Tokens and client
// secrets are sealed with Box before they are written.
//
// The statements are the hand-written SQL they were on database/sql, with
// @name binds instead of $n so that adding a column renumbers nothing.
// Errors reach callers as before: ErrNotFound for a missing row, ErrConflict
// for a taken name, and the driver's error (a *pgconn.PgError is still
// reachable with errors.As) for anything else.
type PG struct {
	DB  *momo.DB
	Box *secret.Box
}

var _ Repo = (*PG)(nil)

// q is the handle a hook-side statement runs on: the event ingest
// transaction when there is one, else the pool.
func (p *PG) q(tx *sql.Tx) momo.Querier { return hellodb.AdoptTx(p.DB, tx) }

// one returns the single row of rows, or sql.ErrNoRows when there is none,
// the error QueryRow(...).Scan gave, so notFound maps it as before.
func one[T any](rows []T, err error) (T, error) {
	var zero T
	if err != nil {
		return zero, err
	}
	if len(rows) == 0 {
		return zero, sql.ErrNoRows
	}
	return rows[0], nil
}

// connectorSQL is the column list of a ConnectorRow.
const connectorSQL = `id, kind, name, enabled, config, client_secret_enc IS NOT NULL AS has_secret, recordings, link_ttl_hours, log_missed, fallback_owner, created_at`

// connectorScan is a connectorSQL row.
type connectorScan struct {
	ID            int64 `momo:"column:id"`
	Kind          string
	Name          string
	Enabled       bool
	Config        []byte
	HasSecret     bool `momo:"column:has_secret"`
	Recordings    bool
	LinkTTLHours  int `momo:"column:link_ttl_hours"`
	LogMissed     bool
	FallbackOwner string
	CreatedAt     time.Time
}

func (s connectorScan) row() (ConnectorRow, error) {
	c := ConnectorRow{ID: s.ID, Kind: s.Kind, Name: s.Name, Enabled: s.Enabled, HasSecret: s.HasSecret, Recordings: s.Recordings,
		LinkTTLHours: s.LinkTTLHours, LogMissed: s.LogMissed, FallbackOwner: s.FallbackOwner, CreatedAt: s.CreatedAt, Config: map[string]string{}}
	if err := json.Unmarshal(s.Config, &c.Config); err != nil {
		return c, err
	}
	return c, nil
}

func connectors(scans []connectorScan, err error) ([]ConnectorRow, error) {
	if err != nil {
		return nil, err
	}
	var out []ConnectorRow
	for _, s := range scans {
		c, err := s.row()
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// unique reports a unique violation. momo classifies it as
// momo.ErrUniqueViolation; the *pgconn.PgError check is the one this
// repository always made, and still holds through momo's error.
func unique(err error) bool {
	var pe *pgconn.PgError
	return errors.Is(err, momo.ErrUniqueViolation) || errors.As(err, &pe) && pe.Code == "23505"
}

// ListConnectors implements Repo.
func (p *PG) ListConnectors(ctx context.Context) ([]ConnectorRow, error) {
	return connectors(momo.Query[connectorScan](ctx, p.DB, `SELECT `+connectorSQL+` FROM crm_connectors ORDER BY id`))
}

// GetConnector implements Repo.
func (p *PG) GetConnector(ctx context.Context, id int64) (ConnectorRow, error) {
	s, err := one(momo.Query[connectorScan](ctx, p.DB, `SELECT `+connectorSQL+` FROM crm_connectors WHERE id = @id`, momo.Named("id", id)))
	if err != nil {
		return ConnectorRow{}, notFound(err)
	}
	return s.row()
}

// CreateConnector implements Repo.
func (p *PG) CreateConnector(ctx context.Context, in ConnectorInput, clientSecret string) (ConnectorRow, error) {
	cfg, _ := json.Marshal(nonNil(in.Config))
	var id int64
	err := p.DB.Tx(ctx, momo.TxOptions{}, func(tx *momo.Tx) error {
		ids, err := momo.Scalars[int64](ctx, tx, `
			INSERT INTO crm_connectors (kind, name, enabled, config, recordings, link_ttl_hours, log_missed, fallback_owner)
			VALUES (@kind, @name, @enabled, @config::jsonb, @recordings, @link_ttl_hours, @log_missed, @fallback_owner) RETURNING id`,
			momo.Named("kind", in.Kind), momo.Named("name", in.Name), momo.Named("enabled", in.Enabled), momo.Named("config", string(cfg)),
			momo.Named("recordings", in.Recordings), momo.Named("link_ttl_hours", in.LinkTTLHours), momo.Named("log_missed", in.LogMissed),
			momo.Named("fallback_owner", in.FallbackOwner))
		if id, err = one(ids, err); err != nil {
			return err
		}
		if clientSecret == "" {
			return nil
		}
		sealed, err := p.Box.Seal(clientSecret, secret.CRMSecretAAD(id))
		if err != nil {
			return err
		}
		_, err = momo.Exec(ctx, tx, `UPDATE crm_connectors SET client_secret_enc = @sealed WHERE id = @id`, momo.Named("id", id), momo.Named("sealed", sealed))
		return err
	})
	if unique(err) {
		return ConnectorRow{}, ErrConflict
	}
	if err != nil {
		return ConnectorRow{}, err
	}
	return p.GetConnector(ctx, id)
}

// UpdateConnector implements Repo.
func (p *PG) UpdateConnector(ctx context.Context, id int64, in ConnectorInput, clientSecret *string) (ConnectorRow, error) {
	cfg, _ := json.Marshal(nonNil(in.Config))
	err := p.DB.Tx(ctx, momo.TxOptions{}, func(tx *momo.Tx) error {
		n, err := momo.Exec(ctx, tx, `
			UPDATE crm_connectors SET name = @name, enabled = @enabled, config = @config::jsonb, recordings = @recordings,
				link_ttl_hours = @link_ttl_hours, log_missed = @log_missed, fallback_owner = @fallback_owner, updated_at = now()
			WHERE id = @id`,
			momo.Named("id", id), momo.Named("name", in.Name), momo.Named("enabled", in.Enabled), momo.Named("config", string(cfg)),
			momo.Named("recordings", in.Recordings), momo.Named("link_ttl_hours", in.LinkTTLHours), momo.Named("log_missed", in.LogMissed),
			momo.Named("fallback_owner", in.FallbackOwner))
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		if clientSecret == nil {
			return nil
		}
		var sealed any // NULL clears the secret
		if *clientSecret != "" {
			b, err := p.Box.Seal(*clientSecret, secret.CRMSecretAAD(id))
			if err != nil {
				return err
			}
			sealed = b
		}
		_, err = momo.Exec(ctx, tx, `UPDATE crm_connectors SET client_secret_enc = @sealed WHERE id = @id`, momo.Named("id", id), momo.Named("sealed", sealed))
		return err
	})
	if unique(err) {
		return ConnectorRow{}, ErrConflict
	}
	if err != nil {
		return ConnectorRow{}, err
	}
	return p.GetConnector(ctx, id)
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// affected maps "no row changed" to ErrNotFound.
func affected(n int64, err error) error {
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteConnector implements Repo.
func (p *PG) DeleteConnector(ctx context.Context, id int64) error {
	return affected(momo.Exec(ctx, p.DB, `DELETE FROM crm_connectors WHERE id = @id`, momo.Named("id", id)))
}

// ClientSecret implements Repo.
func (p *PG) ClientSecret(ctx context.Context, id int64) (string, error) {
	sealed, err := one(momo.Query[momo.Row1[[]byte]](ctx, p.DB, `SELECT client_secret_enc FROM crm_connectors WHERE id = @id`, momo.Named("id", id)))
	if err != nil {
		return "", notFound(err)
	}
	if len(sealed.C1) == 0 {
		return "", nil
	}
	return p.Box.Open(sealed.C1, secret.CRMSecretAAD(id))
}

// connSQL is the column list of a ConnectionRow.
const connSQL = `id, connector_id, scope, COALESCE(user_id, 0) AS user_id, expires_at, granted_scopes, external_account, status, last_error, updated_at,
	calendar_dnd, calendar_active, calendar_busy_until, calendar_checked_at, calendar_error`

// connScan is a connSQL row.
type connScan struct {
	ID                int64 `momo:"column:id"`
	ConnectorID       int64 `momo:"column:connector_id"`
	Scope             string
	UserID            int64 `momo:"column:user_id"`
	ExpiresAt         *time.Time
	GrantedScopes     string
	ExternalAccount   string
	Status            string
	LastError         string
	UpdatedAt         time.Time
	CalendarDND       bool `momo:"column:calendar_dnd"`
	CalendarActive    bool
	CalendarBusyUntil *time.Time
	CalendarCheckedAt *time.Time
	CalendarError     string
}

func orZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func (s connScan) row() ConnectionRow {
	return ConnectionRow{ID: s.ID, ConnectorID: s.ConnectorID, Scope: s.Scope, UserID: s.UserID, ExpiresAt: orZero(s.ExpiresAt),
		GrantedScopes: s.GrantedScopes, ExternalAccount: s.ExternalAccount, Status: s.Status, LastError: s.LastError, UpdatedAt: s.UpdatedAt,
		CalendarDND: s.CalendarDND, CalendarActive: s.CalendarActive, CalendarBusyUntil: orZero(s.CalendarBusyUntil),
		CalendarCheckedAt: orZero(s.CalendarCheckedAt), CalendarError: s.CalendarError}
}

// connTokensScan is a connSQL row with the sealed tokens.
type connTokensScan struct {
	connScan
	AccessEnc  []byte
	RefreshEnc []byte
}

// OrgConnection implements Repo.
func (p *PG) OrgConnection(ctx context.Context, connectorID int64) (ConnectionRow, Tokens, error) {
	return p.connection(ctx, connectorID, 0)
}

// UserConnection implements Repo.
func (p *PG) UserConnection(ctx context.Context, connectorID, userID int64) (ConnectionRow, Tokens, error) {
	if userID == 0 {
		return ConnectionRow{}, Tokens{}, ErrNotFound
	}
	return p.connection(ctx, connectorID, userID)
}

func tokenAAD(connectorID, userID int64, which string) string {
	if userID == 0 {
		return secret.CRMTokenAAD(connectorID, which)
	}
	return secret.CRMUserTokenAAD(connectorID, userID, which)
}

// connection reads the org connection (userID 0) or a user's.
func (p *PG) connection(ctx context.Context, connectorID, userID int64) (ConnectionRow, Tokens, error) {
	where, args := `connector_id = @connector_id AND scope = 'org'`, []any{momo.Named("connector_id", connectorID)}
	if userID != 0 {
		where, args = `connector_id = @connector_id AND scope = 'user' AND user_id = @user_id`, append(args, momo.Named("user_id", userID))
	}
	s, err := one(momo.Query[connTokensScan](ctx, p.DB, `SELECT `+connSQL+`, access_enc, refresh_enc FROM crm_connections WHERE `+where, args...))
	if err != nil {
		return ConnectionRow{}, Tokens{}, notFound(err)
	}
	c := s.row()
	t := Tokens{ExpiresAt: c.ExpiresAt, Account: c.ExternalAccount, Scopes: strings.Fields(c.GrantedScopes)}
	if len(s.AccessEnc) > 0 {
		if t.Access, err = p.Box.Open(s.AccessEnc, tokenAAD(connectorID, userID, "access")); err != nil {
			return c, Tokens{}, err
		}
	}
	if len(s.RefreshEnc) > 0 {
		if t.Refresh, err = p.Box.Open(s.RefreshEnc, tokenAAD(connectorID, userID, "refresh")); err != nil {
			return c, Tokens{}, err
		}
	}
	return c, t, nil
}

// SaveOrgConnection implements Repo.
func (p *PG) SaveOrgConnection(ctx context.Context, connectorID int64, t Tokens) (ConnectionRow, error) {
	return p.saveConnection(ctx, connectorID, 0, t)
}

// SaveUserConnection implements Repo.
func (p *PG) SaveUserConnection(ctx context.Context, connectorID, userID int64, t Tokens) (ConnectionRow, error) {
	if userID == 0 {
		return ConnectionRow{}, ErrNotFound
	}
	return p.saveConnection(ctx, connectorID, userID, t)
}

func (p *PG) saveConnection(ctx context.Context, connectorID, userID int64, t Tokens) (ConnectionRow, error) {
	access, err := p.Box.Seal(t.Access, tokenAAD(connectorID, userID, "access"))
	if err != nil {
		return ConnectionRow{}, err
	}
	var refresh any // NULL keeps the stored refresh token
	if t.Refresh != "" {
		b, err := p.Box.Seal(t.Refresh, tokenAAD(connectorID, userID, "refresh"))
		if err != nil {
			return ConnectionRow{}, err
		}
		refresh = b
	}
	const set = ` DO UPDATE SET access_enc = EXCLUDED.access_enc,
			refresh_enc = CASE WHEN EXCLUDED.refresh_enc IS NULL THEN crm_connections.refresh_enc ELSE EXCLUDED.refresh_enc END,
			expires_at = EXCLUDED.expires_at,
			granted_scopes = CASE WHEN EXCLUDED.granted_scopes = '' THEN crm_connections.granted_scopes ELSE EXCLUDED.granted_scopes END,
			external_account = CASE WHEN EXCLUDED.external_account = '' THEN crm_connections.external_account ELSE EXCLUDED.external_account END,
			status = 'connected', last_error = '', updated_at = now()`
	args := []any{momo.Named("connector_id", connectorID), momo.Named("access", access), momo.Named("refresh", refresh),
		momo.Named("expires_at", nullTime(t.ExpiresAt)), momo.Named("scopes", strings.Join(t.Scopes, " ")), momo.Named("account", t.Account)}
	if userID == 0 {
		_, err = momo.Exec(ctx, p.DB, `
			INSERT INTO crm_connections (connector_id, scope, access_enc, refresh_enc, expires_at, granted_scopes, external_account, status)
			VALUES (@connector_id, 'org', @access, @refresh, @expires_at, @scopes, @account, 'connected')
			ON CONFLICT (connector_id) WHERE scope = 'org'`+set, args...)
	} else {
		_, err = momo.Exec(ctx, p.DB, `
			INSERT INTO crm_connections (connector_id, scope, user_id, access_enc, refresh_enc, expires_at, granted_scopes, external_account, status)
			VALUES (@connector_id, 'user', @user_id, @access, @refresh, @expires_at, @scopes, @account, 'connected')
			ON CONFLICT (connector_id, user_id) WHERE scope = 'user'`+set, append(args, momo.Named("user_id", userID))...)
	}
	if err != nil {
		return ConnectionRow{}, err
	}
	c, _, err := p.connection(ctx, connectorID, userID)
	return c, err
}

// DeleteUserConnection implements Repo.
func (p *PG) DeleteUserConnection(ctx context.Context, connectorID, userID int64) error {
	return affected(momo.Exec(ctx, p.DB, `DELETE FROM crm_connections WHERE connector_id = @connector_id AND scope = 'user' AND user_id = @user_id`,
		momo.Named("connector_id", connectorID), momo.Named("user_id", userID)))
}

func (p *PG) listConns(ctx context.Context, byUser bool, arg int64) ([]ConnectionRow, error) {
	const (
		byConnector = `SELECT ` + connSQL + ` FROM crm_connections WHERE scope = 'user' AND connector_id = @id ORDER BY id`
		ofUser      = `SELECT ` + connSQL + ` FROM crm_connections WHERE scope = 'user' AND user_id = @id ORDER BY id`
	)
	q := byConnector
	if byUser {
		q = ofUser
	}
	scans, err := momo.Query[connScan](ctx, p.DB, q, momo.Named("id", arg))
	if err != nil {
		return nil, err
	}
	var out []ConnectionRow
	for _, s := range scans {
		out = append(out, s.row())
	}
	return out, nil
}

// UserConnections implements Repo.
func (p *PG) UserConnections(ctx context.Context, connectorID int64) ([]ConnectionRow, error) {
	return p.listConns(ctx, false, connectorID)
}

// UserConnectionsOf implements Repo.
func (p *PG) UserConnectionsOf(ctx context.Context, userID int64) ([]ConnectionRow, error) {
	return p.listConns(ctx, true, userID)
}

// SetCalendarSettings implements Repo.
func (p *PG) SetCalendarSettings(ctx context.Context, id int64, dnd bool) error {
	return affected(momo.Exec(ctx, p.DB, `
		UPDATE crm_connections SET calendar_dnd = @dnd::boolean,
			calendar_active = CASE WHEN @dnd::boolean THEN calendar_active ELSE FALSE END,
			calendar_busy_until = CASE WHEN @dnd::boolean THEN calendar_busy_until ELSE NULL END,
			calendar_error = CASE WHEN @dnd::boolean THEN calendar_error ELSE '' END, updated_at = now()
		WHERE id = @id AND scope = 'user'`, momo.Named("id", id), momo.Named("dnd", dnd)))
}

// SetCalendarState implements Repo.
func (p *PG) SetCalendarState(ctx context.Context, id int64, active bool, busyUntil, checkedAt time.Time, lastError string) error {
	_, err := momo.Exec(ctx, p.DB, `
		UPDATE crm_connections SET calendar_active = @active, calendar_busy_until = @busy_until, calendar_checked_at = @checked_at,
			calendar_error = @last_error
		WHERE id = @id AND scope = 'user'`,
		momo.Named("id", id), momo.Named("active", active), momo.Named("busy_until", nullTime(busyUntil)), momo.Named("checked_at", checkedAt),
		momo.Named("last_error", lastError))
	return err
}

// jsonArg is a JSON document as a statement argument, NULL for none.
func jsonArg(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// SetConnectionStatus implements Repo.
func (p *PG) SetConnectionStatus(ctx context.Context, id int64, status, lastError string) error {
	_, err := momo.Exec(ctx, p.DB, `UPDATE crm_connections SET status = @status, last_error = @last_error, updated_at = now() WHERE id = @id`,
		momo.Named("id", id), momo.Named("status", status), momo.Named("last_error", lastError))
	return err
}

// DeleteOrgConnection implements Repo.
func (p *PG) DeleteOrgConnection(ctx context.Context, connectorID int64) error {
	return affected(momo.Exec(ctx, p.DB, `DELETE FROM crm_connections WHERE connector_id = @connector_id AND scope = 'org'`,
		momo.Named("connector_id", connectorID)))
}

func stateHash(state string) []byte {
	h := sha256.Sum256([]byte(state))
	return h[:]
}

// NewState implements Repo.
func (p *PG) NewState(ctx context.Context, connectorID int64, actor string, ttl time.Duration) (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	state := base64.RawURLEncoding.EncodeToString(b[:])
	_, err := momo.Exec(ctx, p.DB, `
		INSERT INTO crm_oauth_states (state_hash, connector_id, actor, expires_at)
		VALUES (@state_hash, @connector_id, @actor, now() + @ttl::float8 * interval '1 second')`,
		momo.Named("state_hash", stateHash(state)), momo.Named("connector_id", connectorID), momo.Named("actor", actor), momo.Named("ttl", ttl.Seconds()))
	return state, err
}

// TakeState implements Repo (single use).
func (p *PG) TakeState(ctx context.Context, state string) (int64, string, error) {
	r, err := one(momo.Query[momo.Row2[int64, string]](ctx, p.DB,
		`DELETE FROM crm_oauth_states WHERE state_hash = @state_hash AND expires_at > now() RETURNING connector_id, actor`,
		momo.Named("state_hash", stateHash(state))))
	return r.C1, r.C2, notFound(err)
}

// PurgeStates implements Repo.
func (p *PG) PurgeStates(ctx context.Context, now time.Time) error {
	_, err := momo.Exec(ctx, p.DB, `DELETE FROM crm_oauth_states WHERE expires_at < @now`, momo.Named("now", now))
	return err
}

// ActiveConnectors implements Repo: the enabled connectors with a live
// connection.
func (p *PG) ActiveConnectors(ctx context.Context, tx *sql.Tx) ([]ConnectorRow, error) {
	return connectors(momo.Query[connectorScan](ctx, p.q(tx), `SELECT `+connectorSQL+` FROM crm_connectors c
		WHERE enabled AND EXISTS (SELECT 1 FROM crm_connections n WHERE n.connector_id = c.id AND n.status <> 'revoked')
		ORDER BY id`))
}

// UpsertCall implements Repo.
func (p *PG) UpsertCall(ctx context.Context, tx *sql.Tx, connectorID int64, j LogJob) error {
	_, err := momo.Exec(ctx, p.q(tx), `
		INSERT INTO crm_call_logs (connector_id, call_id, direction, status, payload, event_type, owner_extension)
		VALUES (@connector_id, @call_id, @direction, 'pending', @payload::jsonb, @event_type, @owner_extension)
		ON CONFLICT (connector_id, call_id) DO UPDATE SET direction = EXCLUDED.direction, status = 'pending',
			payload = EXCLUDED.payload, event_type = EXCLUDED.event_type, owner_extension = EXCLUDED.owner_extension,
			next_attempt_at = now(), updated_at = now()
		WHERE crm_call_logs.status = 'waiting'`,
		momo.Named("connector_id", connectorID), momo.Named("call_id", j.CallID), momo.Named("direction", j.Direction),
		momo.Named("payload", jsonArg(j.Payload)), momo.Named("event_type", j.EventType), momo.Named("owner_extension", j.OwnerExtension))
	return err
}

// Skip implements Repo.
func (p *PG) Skip(ctx context.Context, tx *sql.Tx, connectorID int64, j LogJob, reason string) error {
	_, err := momo.Exec(ctx, p.q(tx), `
		INSERT INTO crm_call_logs (connector_id, call_id, direction, status, last_error, payload, event_type, owner_extension)
		VALUES (@connector_id, @call_id, @direction, 'skipped', @reason, @payload::jsonb, @event_type, @owner_extension)
		ON CONFLICT (connector_id, call_id) DO UPDATE SET direction = EXCLUDED.direction, status = 'skipped',
			last_error = EXCLUDED.last_error, payload = EXCLUDED.payload, event_type = EXCLUDED.event_type,
			owner_extension = EXCLUDED.owner_extension, updated_at = now()
		WHERE crm_call_logs.status = 'waiting'`,
		momo.Named("connector_id", connectorID), momo.Named("call_id", j.CallID), momo.Named("direction", j.Direction), momo.Named("reason", reason),
		momo.Named("payload", jsonArg(j.Payload)), momo.Named("event_type", j.EventType), momo.Named("owner_extension", j.OwnerExtension))
	return err
}

// AttachRecording implements Repo.
func (p *PG) AttachRecording(ctx context.Context, tx *sql.Tx, connectorID int64, callID string, recordingID int64) error {
	_, err := momo.Exec(ctx, p.q(tx), `
		INSERT INTO crm_call_logs (connector_id, call_id, status, recording_id) VALUES (@connector_id, @call_id, 'waiting', @recording_id)
		ON CONFLICT (connector_id, call_id) DO UPDATE SET recording_id = EXCLUDED.recording_id,
			next_attempt_at = CASE WHEN crm_call_logs.status = 'logged' THEN now() ELSE crm_call_logs.next_attempt_at END,
			status = CASE WHEN crm_call_logs.status = 'logged' THEN 'update' ELSE crm_call_logs.status END,
			updated_at = now()`,
		momo.Named("connector_id", connectorID), momo.Named("call_id", callID), momo.Named("recording_id", recordingID))
	return err
}

// AttachEngagement implements Repo: the iframe's engagement becomes the
// call's CRM reference unless the server already logged one.
func (p *PG) AttachEngagement(ctx context.Context, connectorID int64, callID, ref string) (bool, error) {
	got, err := one(momo.Scalars[string](ctx, p.DB, `
		INSERT INTO crm_call_logs (connector_id, call_id, status, external_id) VALUES (@connector_id, @call_id, 'waiting', @ref)
		ON CONFLICT (connector_id, call_id) DO UPDATE SET
			external_id = CASE WHEN crm_call_logs.external_id = '' THEN EXCLUDED.external_id ELSE crm_call_logs.external_id END,
			next_attempt_at = CASE WHEN crm_call_logs.status = 'logged' AND crm_call_logs.external_id = '' THEN now() ELSE crm_call_logs.next_attempt_at END,
			updated_at = now()
		RETURNING external_id`,
		momo.Named("connector_id", connectorID), momo.Named("call_id", callID), momo.Named("ref", ref)))
	return got == ref, err
}

// logSQL is the column list of a LogRow.
const logSQL = `id, connector_id, call_id, direction, status, external_id, attempts, next_attempt_at, last_error,
	COALESCE(payload, 'null'::jsonb) AS payload, event_type, owner_extension, COALESCE(recording_id, 0) AS recording_id, link_expires_at,
	created_at, updated_at, state`

// logScan is a logSQL row.
type logScan struct {
	ID             int64  `momo:"column:id"`
	ConnectorID    int64  `momo:"column:connector_id"`
	CallID         string `momo:"column:call_id"`
	Direction      string
	Status         string
	ExternalID     string `momo:"column:external_id"`
	Attempts       int
	NextAttemptAt  time.Time
	LastError      string
	Payload        []byte
	EventType      string
	OwnerExtension string
	RecordingID    int64 `momo:"column:recording_id"`
	LinkExpiresAt  *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	State          string
}

func (s logScan) row() LogRow {
	return LogRow{ID: s.ID, ConnectorID: s.ConnectorID, CallID: s.CallID, Direction: s.Direction, Status: s.Status, ExternalID: s.ExternalID,
		Attempts: s.Attempts, NextAttemptAt: s.NextAttemptAt, LastError: s.LastError, Payload: s.Payload, EventType: s.EventType,
		OwnerExtension: s.OwnerExtension, RecordingID: s.RecordingID, LinkExpiresAt: s.LinkExpiresAt, CreatedAt: s.CreatedAt,
		UpdatedAt: s.UpdatedAt, State: s.State}
}

func logs(scans []logScan, err error) ([]LogRow, error) {
	if err != nil {
		return nil, err
	}
	var out []LogRow
	for _, s := range scans {
		out = append(out, s.row())
	}
	return out, nil
}

// ClaimDue implements Repo: due jobs of enabled connectors whose connection
// is live, leased for lease so another replica does not take them.
func (p *PG) ClaimDue(ctx context.Context, limit int, lease time.Duration) ([]LogRow, error) {
	return logs(momo.Query[logScan](ctx, p.DB, `
		UPDATE crm_call_logs SET next_attempt_at = now() + @lease::float8 * interval '1 second', updated_at = now()
		WHERE id IN (
			SELECT l.id FROM crm_call_logs l
			JOIN crm_connectors c ON c.id = l.connector_id AND c.enabled
			JOIN crm_connections n ON n.connector_id = c.id AND n.scope = 'org' AND n.status = 'connected'
			WHERE l.status IN ('pending', 'update') AND l.next_attempt_at <= now()
			ORDER BY l.next_attempt_at LIMIT @limit FOR UPDATE OF l SKIP LOCKED)
		RETURNING `+logSQL, momo.Named("limit", limit), momo.Named("lease", lease.Seconds())))
}

// Finish implements Repo.
func (p *PG) Finish(ctx context.Context, id int64, r LogResult) error {
	_, err := momo.Exec(ctx, p.DB, `
		UPDATE crm_call_logs SET
			status = CASE WHEN @status::text = 'logged' AND COALESCE(recording_id, 0) <> @recording_seen::bigint THEN 'update' ELSE @status::text END,
			external_id = CASE WHEN @external_id::text = '' THEN external_id ELSE @external_id::text END,
			attempts = @attempts::int,
			next_attempt_at = CASE WHEN @status::text = 'logged' AND COALESCE(recording_id, 0) <> @recording_seen::bigint THEN now() ELSE @next_attempt_at::timestamptz END,
			last_error = @last_error::text,
			link_expires_at = CASE WHEN @clear_link::boolean THEN NULL WHEN @link_expires_at::timestamptz IS NULL THEN link_expires_at ELSE @link_expires_at::timestamptz END,
			state = COALESCE(@state::text, state),
			updated_at = now()
		WHERE id = @id`,
		momo.Named("id", id), momo.Named("status", r.Status), momo.Named("external_id", r.ExternalID), momo.Named("attempts", r.Attempts),
		momo.Named("next_attempt_at", r.NextAttemptAt), momo.Named("last_error", r.LastError), momo.Named("link_expires_at", r.LinkExpiresAt),
		momo.Named("clear_link", r.ClearLink), momo.Named("recording_seen", r.RecordingSeen), momo.Named("state", r.State))
	return err
}

// MarkRenewals implements Repo.
func (p *PG) MarkRenewals(ctx context.Context, now time.Time, within, maxAge time.Duration) (int, error) {
	n, err := momo.Exec(ctx, p.DB, `
		UPDATE crm_call_logs SET status = 'update', attempts = 0, next_attempt_at = @now::timestamptz, updated_at = @now::timestamptz
		WHERE status = 'logged' AND recording_id IS NOT NULL AND link_expires_at IS NOT NULL
			AND link_expires_at < @renew_before AND created_at > @created_after`,
		momo.Named("now", now), momo.Named("renew_before", now.Add(within)), momo.Named("created_after", now.Add(-maxAge)))
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// Stats implements Repo.
func (p *PG) Stats(ctx context.Context) ([]Stat, error) {
	rows, err := momo.Query[momo.Row4[string, string, string, int]](ctx, p.DB, `
		SELECT c.name, 'logs', l.status, count(*) FROM crm_call_logs l JOIN crm_connectors c ON c.id = l.connector_id GROUP BY c.name, l.status
		UNION ALL
		SELECT c.name, 'connections', n.status, count(*) FROM crm_connections n JOIN crm_connectors c ON c.id = n.connector_id GROUP BY c.name, n.status`)
	if err != nil {
		return nil, err
	}
	var out []Stat
	for _, r := range rows {
		out = append(out, Stat{Connector: r.C1, Kind: r.C2, Status: r.C3, N: r.C4})
	}
	return out, nil
}

// AdminEmails implements Repo.
func (p *PG) AdminEmails(ctx context.Context) ([]string, error) {
	out, err := momo.Scalars[string](ctx, p.DB, `SELECT email FROM users WHERE role = 'admin' AND email <> '' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// userRefScan is a (id, username, email, extension) row.
type userRefScan = momo.Row4[int64, string, string, string]

func userRef(r userRefScan) UserRef {
	return UserRef{ID: r.C1, Username: r.C2, Email: r.C3, Extension: r.C4}
}

// UserForExtension implements Repo: the user the extension is linked to.
func (p *PG) UserForExtension(ctx context.Context, ext string) (UserRef, bool, error) {
	r, err := one(momo.Query[userRefScan](ctx, p.DB, `
		SELECT u.id, u.username, u.email, e.number FROM extensions e
		JOIN user_extensions ue ON ue.extension_id = e.id JOIN users u ON u.id = ue.user_id
		WHERE e.number = @ext ORDER BY u.id LIMIT 1`, momo.Named("ext", ext)))
	if errors.Is(err, sql.ErrNoRows) {
		return UserRef{}, false, nil
	}
	if err != nil {
		return UserRef{}, false, err
	}
	return userRef(r), true, nil
}

// Users implements Repo.
func (p *PG) Users(ctx context.Context) ([]UserRef, error) {
	rows, err := momo.Query[userRefScan](ctx, p.DB, `
		SELECT u.id, u.username, u.email, COALESCE((SELECT min(e.number) FROM user_extensions ue JOIN extensions e ON e.id = ue.extension_id WHERE ue.user_id = u.id), '')
		FROM users u ORDER BY u.username`)
	if err != nil {
		return nil, err
	}
	var out []UserRef
	for _, r := range rows {
		out = append(out, userRef(r))
	}
	return out, nil
}

// Mappings implements Repo.
func (p *PG) Mappings(ctx context.Context, connectorID int64) ([]Mapping, error) {
	rows, err := momo.Query[momo.Row3[int64, string, string]](ctx, p.DB,
		`SELECT user_id, external_user_id, source FROM crm_user_map WHERE connector_id = @connector_id ORDER BY user_id`,
		momo.Named("connector_id", connectorID))
	if err != nil {
		return nil, err
	}
	var out []Mapping
	for _, r := range rows {
		out = append(out, Mapping{UserID: r.C1, ExternalUserID: r.C2, Source: r.C3})
	}
	return out, nil
}

// SetMapping implements Repo. A manual mapping is never replaced by an
// automatic one.
func (p *PG) SetMapping(ctx context.Context, connectorID, userID int64, externalUserID, source string) error {
	_, err := momo.Exec(ctx, p.DB, `
		INSERT INTO crm_user_map (connector_id, user_id, external_user_id, source) VALUES (@connector_id, @user_id, @external_user_id, @source)
		ON CONFLICT (connector_id, user_id) DO UPDATE SET external_user_id = EXCLUDED.external_user_id, source = EXCLUDED.source
		WHERE crm_user_map.source <> 'manual' OR EXCLUDED.source = 'manual'`,
		momo.Named("connector_id", connectorID), momo.Named("user_id", userID), momo.Named("external_user_id", externalUserID), momo.Named("source", source))
	return err
}

// DeleteMapping implements Repo.
func (p *PG) DeleteMapping(ctx context.Context, connectorID, userID int64) error {
	_, err := momo.Exec(ctx, p.DB, `DELETE FROM crm_user_map WHERE connector_id = @connector_id AND user_id = @user_id`,
		momo.Named("connector_id", connectorID), momo.Named("user_id", userID))
	return err
}

// ListLogs implements Repo: newest first, keyset-paged on the id.
func (p *PG) ListLogs(ctx context.Context, connectorID int64, status string, limit int, beforeID int64) ([]LogRow, error) {
	return logs(momo.Query[logScan](ctx, p.DB, `SELECT `+logSQL+` FROM crm_call_logs
		WHERE connector_id = @connector_id AND (@status::text = '' OR status = @status::text) AND (@before_id::bigint = 0 OR id < @before_id::bigint)
		ORDER BY id DESC LIMIT @limit`,
		momo.Named("connector_id", connectorID), momo.Named("status", status), momo.Named("before_id", beforeID), momo.Named("limit", limit)))
}

// GetLog implements Repo.
func (p *PG) GetLog(ctx context.Context, id int64) (LogRow, error) {
	s, err := one(momo.Query[logScan](ctx, p.DB, `SELECT `+logSQL+` FROM crm_call_logs WHERE id = @id`, momo.Named("id", id)))
	if err != nil {
		return LogRow{}, notFound(err)
	}
	return s.row(), nil
}

// RetryLog implements Repo: a dead job goes back to pending with a fresh
// series of attempts.
func (p *PG) RetryLog(ctx context.Context, id int64) error {
	n, err := momo.Exec(ctx, p.DB, `
		UPDATE crm_call_logs SET status = CASE WHEN external_id <> '' AND payload IS NULL THEN 'update' ELSE 'pending' END,
			attempts = 0, next_attempt_at = now(), last_error = '', updated_at = now()
		WHERE id = @id AND status IN ('dead', 'skipped')`, momo.Named("id", id))
	if err != nil {
		return err
	}
	if n == 0 {
		if _, err := p.GetLog(ctx, id); err != nil {
			return err // ErrNotFound for an unknown job
		}
		return ErrConflict
	}
	return nil
}

// CallInvolves implements Repo.
func (p *PG) CallInvolves(ctx context.Context, callID string, exts []string) (bool, error) {
	if len(exts) == 0 {
		return false, nil
	}
	return one(momo.Scalars[bool](ctx, p.DB,
		`SELECT EXISTS (SELECT 1 FROM integration_events WHERE correlation_id = @call_id AND extension_number IN (@exts))`,
		momo.Named("call_id", callID), momo.Named("exts", momo.In(exts))))
}

// Audit implements Repo.
func (p *PG) Audit(ctx context.Context, actor, action, resource, resourceID string) error {
	_, err := momo.Exec(ctx, p.DB, `INSERT INTO audit_events (actor, action, resource, resource_id) VALUES (@actor, @action, @resource, @resource_id)`,
		momo.Named("actor", actor), momo.Named("action", action), momo.Named("resource", resource), momo.Named("resource_id", resourceID))
	return err
}
