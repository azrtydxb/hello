package crm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/azrtydxb/hello/internal/secret"
)

// PG is the PostgreSQL Repo. Tokens and client secrets are sealed with Box
// before they are written.
type PG struct {
	DB  *sql.DB
	Box *secret.Box
}

var _ Repo = (*PG)(nil)

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (p *PG) q(tx *sql.Tx) execer {
	if tx != nil {
		return tx
	}
	return p.DB
}

const connectorCols = `id, kind, name, enabled, config, client_secret_enc IS NOT NULL, recordings, link_ttl_hours, log_missed, fallback_owner, created_at`

type scanner interface{ Scan(...any) error }

func scanConnector(r scanner) (ConnectorRow, error) {
	var c ConnectorRow
	var cfg []byte
	if err := r.Scan(&c.ID, &c.Kind, &c.Name, &c.Enabled, &cfg, &c.HasSecret, &c.Recordings, &c.LinkTTLHours, &c.LogMissed, &c.FallbackOwner, &c.CreatedAt); err != nil {
		return c, err
	}
	c.Config = map[string]string{}
	if err := json.Unmarshal(cfg, &c.Config); err != nil {
		return c, err
	}
	return c, nil
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func unique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// ListConnectors implements Repo.
func (p *PG) ListConnectors(ctx context.Context) ([]ConnectorRow, error) {
	rows, err := p.DB.QueryContext(ctx, `SELECT `+connectorCols+` FROM crm_connectors ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ConnectorRow
	for rows.Next() {
		c, err := scanConnector(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetConnector implements Repo.
func (p *PG) GetConnector(ctx context.Context, id int64) (ConnectorRow, error) {
	c, err := scanConnector(p.DB.QueryRowContext(ctx, `SELECT `+connectorCols+` FROM crm_connectors WHERE id = $1`, id))
	return c, notFound(err)
}

// CreateConnector implements Repo.
func (p *PG) CreateConnector(ctx context.Context, in ConnectorInput, clientSecret string) (ConnectorRow, error) {
	cfg, _ := json.Marshal(nonNil(in.Config))
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return ConnectorRow{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var id int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO crm_connectors (kind, name, enabled, config, recordings, link_ttl_hours, log_missed, fallback_owner)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8) RETURNING id`,
		in.Kind, in.Name, in.Enabled, string(cfg), in.Recordings, in.LinkTTLHours, in.LogMissed, in.FallbackOwner).Scan(&id)
	if unique(err) {
		return ConnectorRow{}, ErrConflict
	}
	if err != nil {
		return ConnectorRow{}, err
	}
	if clientSecret != "" {
		sealed, err := p.Box.Seal(clientSecret, secret.CRMSecretAAD(id))
		if err != nil {
			return ConnectorRow{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE crm_connectors SET client_secret_enc = $2 WHERE id = $1`, id, sealed); err != nil {
			return ConnectorRow{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ConnectorRow{}, err
	}
	return p.GetConnector(ctx, id)
}

// UpdateConnector implements Repo.
func (p *PG) UpdateConnector(ctx context.Context, id int64, in ConnectorInput, clientSecret *string) (ConnectorRow, error) {
	cfg, _ := json.Marshal(nonNil(in.Config))
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return ConnectorRow{}, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
		UPDATE crm_connectors SET name = $2, enabled = $3, config = $4::jsonb, recordings = $5, link_ttl_hours = $6,
			log_missed = $7, fallback_owner = $8, updated_at = now() WHERE id = $1`,
		id, in.Name, in.Enabled, string(cfg), in.Recordings, in.LinkTTLHours, in.LogMissed, in.FallbackOwner)
	if unique(err) {
		return ConnectorRow{}, ErrConflict
	}
	if err != nil {
		return ConnectorRow{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ConnectorRow{}, ErrNotFound
	}
	if clientSecret != nil {
		var sealed any // NULL clears the secret
		if *clientSecret != "" {
			b, err := p.Box.Seal(*clientSecret, secret.CRMSecretAAD(id))
			if err != nil {
				return ConnectorRow{}, err
			}
			sealed = b
		}
		if _, err := tx.ExecContext(ctx, `UPDATE crm_connectors SET client_secret_enc = $2 WHERE id = $1`, id, sealed); err != nil {
			return ConnectorRow{}, err
		}
	}
	if err := tx.Commit(); err != nil {
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

// DeleteConnector implements Repo.
func (p *PG) DeleteConnector(ctx context.Context, id int64) error {
	res, err := p.DB.ExecContext(ctx, `DELETE FROM crm_connectors WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ClientSecret implements Repo.
func (p *PG) ClientSecret(ctx context.Context, id int64) (string, error) {
	var sealed []byte
	if err := p.DB.QueryRowContext(ctx, `SELECT client_secret_enc FROM crm_connectors WHERE id = $1`, id).Scan(&sealed); err != nil {
		return "", notFound(err)
	}
	if len(sealed) == 0 {
		return "", nil
	}
	return p.Box.Open(sealed, secret.CRMSecretAAD(id))
}

const connCols = `id, connector_id, scope, COALESCE(user_id, 0), expires_at, granted_scopes, external_account, status, last_error, updated_at,
	calendar_dnd, calendar_active, calendar_busy_until, calendar_checked_at, calendar_error`

// scanConn reads connCols.
func scanConn(r scanner, extra ...any) (ConnectionRow, error) {
	var c ConnectionRow
	var expires, busy, checked sql.NullTime
	dest := append([]any{&c.ID, &c.ConnectorID, &c.Scope, &c.UserID, &expires, &c.GrantedScopes, &c.ExternalAccount, &c.Status, &c.LastError, &c.UpdatedAt,
		&c.CalendarDND, &c.CalendarActive, &busy, &checked, &c.CalendarError}, extra...)
	if err := r.Scan(dest...); err != nil {
		return c, err
	}
	c.ExpiresAt, c.CalendarBusyUntil, c.CalendarCheckedAt = expires.Time, busy.Time, checked.Time
	return c, nil
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
	var access, refresh []byte
	where, args := `connector_id = $1 AND scope = 'org'`, []any{connectorID}
	if userID != 0 {
		where, args = `connector_id = $1 AND scope = 'user' AND user_id = $2`, []any{connectorID, userID}
	}
	c, err := scanConn(p.DB.QueryRowContext(ctx, `SELECT `+connCols+`, access_enc, refresh_enc FROM crm_connections WHERE `+where, args...), &access, &refresh)
	if err != nil {
		return c, Tokens{}, notFound(err)
	}
	t := Tokens{ExpiresAt: c.ExpiresAt, Account: c.ExternalAccount, Scopes: strings.Fields(c.GrantedScopes)}
	if len(access) > 0 {
		if t.Access, err = p.Box.Open(access, tokenAAD(connectorID, userID, "access")); err != nil {
			return c, Tokens{}, err
		}
	}
	if len(refresh) > 0 {
		if t.Refresh, err = p.Box.Open(refresh, tokenAAD(connectorID, userID, "refresh")); err != nil {
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
	if userID == 0 {
		_, err = p.DB.ExecContext(ctx, `
			INSERT INTO crm_connections (connector_id, scope, access_enc, refresh_enc, expires_at, granted_scopes, external_account, status)
			VALUES ($1, 'org', $2, $3, $4, $5, $6, 'connected')
			ON CONFLICT (connector_id) WHERE scope = 'org'`+set,
			connectorID, access, refresh, nullTime(t.ExpiresAt), strings.Join(t.Scopes, " "), t.Account)
	} else {
		_, err = p.DB.ExecContext(ctx, `
			INSERT INTO crm_connections (connector_id, scope, user_id, access_enc, refresh_enc, expires_at, granted_scopes, external_account, status)
			VALUES ($1, 'user', $7, $2, $3, $4, $5, $6, 'connected')
			ON CONFLICT (connector_id, user_id) WHERE scope = 'user'`+set,
			connectorID, access, refresh, nullTime(t.ExpiresAt), strings.Join(t.Scopes, " "), t.Account, userID)
	}
	if err != nil {
		return ConnectionRow{}, err
	}
	c, _, err := p.connection(ctx, connectorID, userID)
	return c, err
}

// DeleteUserConnection implements Repo.
func (p *PG) DeleteUserConnection(ctx context.Context, connectorID, userID int64) error {
	res, err := p.DB.ExecContext(ctx, `DELETE FROM crm_connections WHERE connector_id = $1 AND scope = 'user' AND user_id = $2`, connectorID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *PG) listConns(ctx context.Context, byUser bool, arg int64) ([]ConnectionRow, error) {
	const (
		byConnector = `SELECT ` + connCols + ` FROM crm_connections WHERE scope = 'user' AND connector_id = $1 ORDER BY id`
		ofUser      = `SELECT ` + connCols + ` FROM crm_connections WHERE scope = 'user' AND user_id = $1 ORDER BY id`
	)
	q := byConnector
	if byUser {
		q = ofUser
	}
	rows, err := p.DB.QueryContext(ctx, q, arg)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ConnectionRow
	for rows.Next() {
		c, err := scanConn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
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
	res, err := p.DB.ExecContext(ctx, `
		UPDATE crm_connections SET calendar_dnd = $2,
			calendar_active = CASE WHEN $2 THEN calendar_active ELSE FALSE END,
			calendar_busy_until = CASE WHEN $2 THEN calendar_busy_until ELSE NULL END,
			calendar_error = CASE WHEN $2 THEN calendar_error ELSE '' END, updated_at = now()
		WHERE id = $1 AND scope = 'user'`, id, dnd)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetCalendarState implements Repo.
func (p *PG) SetCalendarState(ctx context.Context, id int64, active bool, busyUntil, checkedAt time.Time, lastError string) error {
	_, err := p.DB.ExecContext(ctx, `
		UPDATE crm_connections SET calendar_active = $2, calendar_busy_until = $3, calendar_checked_at = $4, calendar_error = $5
		WHERE id = $1 AND scope = 'user'`, id, active, nullTime(busyUntil), checkedAt, lastError)
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
	_, err := p.DB.ExecContext(ctx, `UPDATE crm_connections SET status = $2, last_error = $3, updated_at = now() WHERE id = $1`, id, status, lastError)
	return err
}

// DeleteOrgConnection implements Repo.
func (p *PG) DeleteOrgConnection(ctx context.Context, connectorID int64) error {
	res, err := p.DB.ExecContext(ctx, `DELETE FROM crm_connections WHERE connector_id = $1 AND scope = 'org'`, connectorID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
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
	_, err := p.DB.ExecContext(ctx, `INSERT INTO crm_oauth_states (state_hash, connector_id, actor, expires_at) VALUES ($1, $2, $3, now() + $4::float8 * interval '1 second')`,
		stateHash(state), connectorID, actor, ttl.Seconds())
	return state, err
}

// TakeState implements Repo (single use).
func (p *PG) TakeState(ctx context.Context, state string) (int64, string, error) {
	var id int64
	var actor string
	err := p.DB.QueryRowContext(ctx, `DELETE FROM crm_oauth_states WHERE state_hash = $1 AND expires_at > now() RETURNING connector_id, actor`,
		stateHash(state)).Scan(&id, &actor)
	return id, actor, notFound(err)
}

// PurgeStates implements Repo.
func (p *PG) PurgeStates(ctx context.Context, now time.Time) error {
	_, err := p.DB.ExecContext(ctx, `DELETE FROM crm_oauth_states WHERE expires_at < $1`, now)
	return err
}

// ActiveConnectors implements Repo: the enabled connectors with a live
// connection.
func (p *PG) ActiveConnectors(ctx context.Context, tx *sql.Tx) ([]ConnectorRow, error) {
	rows, err := p.q(tx).QueryContext(ctx, `SELECT `+connectorCols+` FROM crm_connectors c
		WHERE enabled AND EXISTS (SELECT 1 FROM crm_connections n WHERE n.connector_id = c.id AND n.status <> 'revoked')
		ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ConnectorRow
	for rows.Next() {
		c, err := scanConnector(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpsertCall implements Repo.
func (p *PG) UpsertCall(ctx context.Context, tx *sql.Tx, connectorID int64, j LogJob) error {
	_, err := p.q(tx).ExecContext(ctx, `
		INSERT INTO crm_call_logs (connector_id, call_id, direction, status, payload, event_type, owner_extension)
		VALUES ($1, $2, $3, 'pending', $4::jsonb, $5, $6)
		ON CONFLICT (connector_id, call_id) DO UPDATE SET direction = EXCLUDED.direction, status = 'pending',
			payload = EXCLUDED.payload, event_type = EXCLUDED.event_type, owner_extension = EXCLUDED.owner_extension,
			next_attempt_at = now(), updated_at = now()
		WHERE crm_call_logs.status = 'waiting'`,
		connectorID, j.CallID, j.Direction, jsonArg(j.Payload), j.EventType, j.OwnerExtension)
	return err
}

// Skip implements Repo.
func (p *PG) Skip(ctx context.Context, tx *sql.Tx, connectorID int64, j LogJob, reason string) error {
	_, err := p.q(tx).ExecContext(ctx, `
		INSERT INTO crm_call_logs (connector_id, call_id, direction, status, last_error, payload, event_type, owner_extension)
		VALUES ($1, $2, $3, 'skipped', $4, $5::jsonb, $6, $7)
		ON CONFLICT (connector_id, call_id) DO UPDATE SET direction = EXCLUDED.direction, status = 'skipped',
			last_error = EXCLUDED.last_error, payload = EXCLUDED.payload, event_type = EXCLUDED.event_type,
			owner_extension = EXCLUDED.owner_extension, updated_at = now()
		WHERE crm_call_logs.status = 'waiting'`, connectorID, j.CallID, j.Direction, reason, jsonArg(j.Payload), j.EventType, j.OwnerExtension)
	return err
}

// AttachRecording implements Repo.
func (p *PG) AttachRecording(ctx context.Context, tx *sql.Tx, connectorID int64, callID string, recordingID int64) error {
	_, err := p.q(tx).ExecContext(ctx, `
		INSERT INTO crm_call_logs (connector_id, call_id, status, recording_id) VALUES ($1, $2, 'waiting', $3)
		ON CONFLICT (connector_id, call_id) DO UPDATE SET recording_id = EXCLUDED.recording_id,
			next_attempt_at = CASE WHEN crm_call_logs.status = 'logged' THEN now() ELSE crm_call_logs.next_attempt_at END,
			status = CASE WHEN crm_call_logs.status = 'logged' THEN 'update' ELSE crm_call_logs.status END,
			updated_at = now()`, connectorID, callID, recordingID)
	return err
}

// AttachEngagement implements Repo: the iframe's engagement becomes the
// call's CRM reference unless the server already logged one.
func (p *PG) AttachEngagement(ctx context.Context, connectorID int64, callID, ref string) (bool, error) {
	var got string
	err := p.DB.QueryRowContext(ctx, `
		INSERT INTO crm_call_logs (connector_id, call_id, status, external_id) VALUES ($1, $2, 'waiting', $3)
		ON CONFLICT (connector_id, call_id) DO UPDATE SET
			external_id = CASE WHEN crm_call_logs.external_id = '' THEN EXCLUDED.external_id ELSE crm_call_logs.external_id END,
			next_attempt_at = CASE WHEN crm_call_logs.status = 'logged' AND crm_call_logs.external_id = '' THEN now() ELSE crm_call_logs.next_attempt_at END,
			updated_at = now()
		RETURNING external_id`, connectorID, callID, ref).Scan(&got)
	return got == ref, err
}

const logCols = `id, connector_id, call_id, direction, status, external_id, attempts, next_attempt_at, last_error,
	COALESCE(payload, 'null'::jsonb), event_type, owner_extension, COALESCE(recording_id, 0), link_expires_at, created_at, updated_at`

func scanLog(r scanner) (LogRow, error) {
	var l LogRow
	var payload []byte
	err := r.Scan(&l.ID, &l.ConnectorID, &l.CallID, &l.Direction, &l.Status, &l.ExternalID, &l.Attempts, &l.NextAttemptAt, &l.LastError,
		&payload, &l.EventType, &l.OwnerExtension, &l.RecordingID, &l.LinkExpiresAt, &l.CreatedAt, &l.UpdatedAt)
	l.Payload = payload
	return l, err
}

// ClaimDue implements Repo: due jobs of enabled connectors whose connection
// is live, leased for lease so another replica does not take them.
func (p *PG) ClaimDue(ctx context.Context, limit int, lease time.Duration) ([]LogRow, error) {
	rows, err := p.DB.QueryContext(ctx, `
		UPDATE crm_call_logs SET next_attempt_at = now() + $2::float8 * interval '1 second', updated_at = now()
		WHERE id IN (
			SELECT l.id FROM crm_call_logs l
			JOIN crm_connectors c ON c.id = l.connector_id AND c.enabled
			JOIN crm_connections n ON n.connector_id = c.id AND n.scope = 'org' AND n.status = 'connected'
			WHERE l.status IN ('pending', 'update') AND l.next_attempt_at <= now()
			ORDER BY l.next_attempt_at LIMIT $1 FOR UPDATE OF l SKIP LOCKED)
		RETURNING `+logCols, limit, lease.Seconds())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []LogRow
	for rows.Next() {
		l, err := scanLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Finish implements Repo.
func (p *PG) Finish(ctx context.Context, id int64, r LogResult) error {
	_, err := p.DB.ExecContext(ctx, `
		UPDATE crm_call_logs SET
			status = CASE WHEN $2::text = 'logged' AND COALESCE(recording_id, 0) <> $9::bigint THEN 'update' ELSE $2::text END,
			external_id = CASE WHEN $3::text = '' THEN external_id ELSE $3::text END,
			attempts = $4::int,
			next_attempt_at = CASE WHEN $2::text = 'logged' AND COALESCE(recording_id, 0) <> $9::bigint THEN now() ELSE $5::timestamptz END,
			last_error = $6::text,
			link_expires_at = CASE WHEN $8::boolean THEN NULL WHEN $7::timestamptz IS NULL THEN link_expires_at ELSE $7::timestamptz END,
			updated_at = now()
		WHERE id = $1`,
		id, r.Status, r.ExternalID, r.Attempts, r.NextAttemptAt, r.LastError, r.LinkExpiresAt, r.ClearLink, r.RecordingSeen)
	return err
}

// MarkRenewals implements Repo.
func (p *PG) MarkRenewals(ctx context.Context, now time.Time, within, maxAge time.Duration) (int, error) {
	res, err := p.DB.ExecContext(ctx, `
		UPDATE crm_call_logs SET status = 'update', attempts = 0, next_attempt_at = $1, updated_at = $1
		WHERE status = 'logged' AND recording_id IS NOT NULL AND link_expires_at IS NOT NULL
			AND link_expires_at < $2 AND created_at > $3`, now, now.Add(within), now.Add(-maxAge))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// Stats implements Repo.
func (p *PG) Stats(ctx context.Context) ([]Stat, error) {
	rows, err := p.DB.QueryContext(ctx, `
		SELECT c.name, 'logs', l.status, count(*) FROM crm_call_logs l JOIN crm_connectors c ON c.id = l.connector_id GROUP BY c.name, l.status
		UNION ALL
		SELECT c.name, 'connections', n.status, count(*) FROM crm_connections n JOIN crm_connectors c ON c.id = n.connector_id GROUP BY c.name, n.status`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Stat
	for rows.Next() {
		var s Stat
		if err := rows.Scan(&s.Connector, &s.Kind, &s.Status, &s.N); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// AdminEmails implements Repo.
func (p *PG) AdminEmails(ctx context.Context) ([]string, error) {
	rows, err := p.DB.QueryContext(ctx, `SELECT email FROM users WHERE role = 'admin' AND email <> '' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UserForExtension implements Repo: the user the extension is linked to.
func (p *PG) UserForExtension(ctx context.Context, ext string) (UserRef, bool, error) {
	var u UserRef
	err := p.DB.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.email, e.number FROM extensions e
		JOIN user_extensions ue ON ue.extension_id = e.id JOIN users u ON u.id = ue.user_id
		WHERE e.number = $1 ORDER BY u.id LIMIT 1`, ext).Scan(&u.ID, &u.Username, &u.Email, &u.Extension)
	if errors.Is(err, sql.ErrNoRows) {
		return UserRef{}, false, nil
	}
	return u, err == nil, err
}

// Users implements Repo.
func (p *PG) Users(ctx context.Context) ([]UserRef, error) {
	rows, err := p.DB.QueryContext(ctx, `
		SELECT u.id, u.username, u.email, COALESCE((SELECT min(e.number) FROM user_extensions ue JOIN extensions e ON e.id = ue.extension_id WHERE ue.user_id = u.id), '')
		FROM users u ORDER BY u.username`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []UserRef
	for rows.Next() {
		var u UserRef
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.Extension); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Mappings implements Repo.
func (p *PG) Mappings(ctx context.Context, connectorID int64) ([]Mapping, error) {
	rows, err := p.DB.QueryContext(ctx, `SELECT user_id, external_user_id, source FROM crm_user_map WHERE connector_id = $1 ORDER BY user_id`, connectorID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Mapping
	for rows.Next() {
		var m Mapping
		if err := rows.Scan(&m.UserID, &m.ExternalUserID, &m.Source); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetMapping implements Repo. A manual mapping is never replaced by an
// automatic one.
func (p *PG) SetMapping(ctx context.Context, connectorID, userID int64, externalUserID, source string) error {
	_, err := p.DB.ExecContext(ctx, `
		INSERT INTO crm_user_map (connector_id, user_id, external_user_id, source) VALUES ($1, $2, $3, $4)
		ON CONFLICT (connector_id, user_id) DO UPDATE SET external_user_id = EXCLUDED.external_user_id, source = EXCLUDED.source
		WHERE crm_user_map.source <> 'manual' OR EXCLUDED.source = 'manual'`, connectorID, userID, externalUserID, source)
	return err
}

// DeleteMapping implements Repo.
func (p *PG) DeleteMapping(ctx context.Context, connectorID, userID int64) error {
	_, err := p.DB.ExecContext(ctx, `DELETE FROM crm_user_map WHERE connector_id = $1 AND user_id = $2`, connectorID, userID)
	return err
}

// ListLogs implements Repo: newest first, keyset-paged on the id.
func (p *PG) ListLogs(ctx context.Context, connectorID int64, status string, limit int, beforeID int64) ([]LogRow, error) {
	rows, err := p.DB.QueryContext(ctx, `SELECT `+logCols+` FROM crm_call_logs
		WHERE connector_id = $1 AND ($2::text = '' OR status = $2::text) AND ($3::bigint = 0 OR id < $3::bigint)
		ORDER BY id DESC LIMIT $4`, connectorID, status, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []LogRow
	for rows.Next() {
		l, err := scanLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// GetLog implements Repo.
func (p *PG) GetLog(ctx context.Context, id int64) (LogRow, error) {
	l, err := scanLog(p.DB.QueryRowContext(ctx, `SELECT `+logCols+` FROM crm_call_logs WHERE id = $1`, id))
	return l, notFound(err)
}

// RetryLog implements Repo: a dead job goes back to pending with a fresh
// series of attempts.
func (p *PG) RetryLog(ctx context.Context, id int64) error {
	res, err := p.DB.ExecContext(ctx, `UPDATE crm_call_logs SET status = CASE WHEN external_id <> '' AND payload IS NULL THEN 'update' ELSE 'pending' END,
		attempts = 0, next_attempt_at = now(), last_error = '', updated_at = now() WHERE id = $1 AND status IN ('dead', 'skipped')`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
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
	args := []any{callID}
	ph := make([]string, len(exts))
	for i, e := range exts {
		args = append(args, e)
		ph[i] = fmt.Sprintf("$%d", i+2)
	}
	var ok bool
	err := p.DB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM integration_events WHERE correlation_id = $1 AND extension_number IN (`+
		strings.Join(ph, ",")+`))`, args...).Scan(&ok)
	return ok, err
}

// Audit implements Repo.
func (p *PG) Audit(ctx context.Context, actor, action, resource, resourceID string) error {
	_, err := p.DB.ExecContext(ctx, `INSERT INTO audit_events (actor, action, resource, resource_id) VALUES ($1, $2, $3, $4)`, actor, action, resource, resourceID)
	return err
}
