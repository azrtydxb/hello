package scim

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// PG is the PostgreSQL Store.
type PG struct{ DB *sql.DB }

// lockKey is the directory engine's per-connector advisory lock key.
func lockKey(id int64) string { return "hello:dirsync:" + strconv.FormatInt(id, 10) }

// lockWait bounds how long a write waits for the connector's lock.
const lockWait = 15 * time.Second

// ErrBusy is a write that could not take the connector's lock in time.
var ErrBusy = errors.New("scim: the connector is busy")

// Lock implements Store: a session advisory lock on a dedicated connection,
// waited for at most lockWait.
func (p *PG) Lock(ctx context.Context, connectorID int64) (func(), error) {
	conn, err := p.DB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	wctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	if _, err := conn.ExecContext(wctx, `SELECT pg_advisory_lock(hashtext($1::text))`, lockKey(connectorID)); err != nil {
		// A cancelled wait may leave the connection in an unknown state.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		_ = conn.Close()
		if wctx.Err() != nil && ctx.Err() == nil {
			return nil, ErrBusy
		}
		return nil, err
	}
	return func() {
		uctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(uctx, `SELECT pg_advisory_unlock(hashtext($1::text))`, lockKey(connectorID)); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = conn.Close()
	}, nil
}

func audit(ctx context.Context, q interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, actor, action string, connectorID int64) error {
	_, err := q.ExecContext(ctx, `INSERT INTO audit_events (actor, action, resource, resource_id, via) VALUES ($1, $2, 'directory_connector', $3, NULL)`,
		actor, action, strconv.FormatInt(connectorID, 10))
	return err
}

//nolint:gosec // G101: column names, not a credential.
const tokenCols = `id, connector_id, hint, created_by, created_at, last_used_at, revoked_at`

func scanToken(r interface{ Scan(...any) error }) (Token, error) {
	var (
		t          Token
		used, revd sql.NullTime
	)
	if err := r.Scan(&t.ID, &t.ConnectorID, &t.Hint, &t.CreatedBy, &t.CreatedAt, &used, &revd); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return t, ErrNotFound
		}
		return t, err
	}
	if used.Valid {
		t.LastUsedAt = &used.Time
	}
	if revd.Valid {
		t.RevokedAt = &revd.Time
	}
	return t, nil
}

// LookupToken implements Store: an active token of a scim connector.
func (p *PG) LookupToken(ctx context.Context, hash []byte) (Token, error) {
	return scanToken(p.DB.QueryRowContext(ctx, `
		SELECT t.id, t.connector_id, t.hint, t.created_by, t.created_at, t.last_used_at, t.revoked_at
		FROM scim_tokens t JOIN directory_connectors c ON c.id = t.connector_id
		WHERE t.token_hash = $1 AND t.revoked_at IS NULL AND c.kind = 'scim'`, hash))
}

// TouchToken implements Store.
func (p *PG) TouchToken(ctx context.Context, id int64, at time.Time) error {
	_, err := p.DB.ExecContext(ctx, `UPDATE scim_tokens SET last_used_at = $2 WHERE id = $1`, id, at)
	return err
}

// CreateToken implements Store.
func (p *PG) CreateToken(ctx context.Context, actor string, connectorID int64, hash []byte, hint string) (Token, error) {
	var t Token
	err := p.tx(ctx, func(tx *sql.Tx) error {
		var kind string
		if err := tx.QueryRowContext(ctx, `SELECT kind FROM directory_connectors WHERE id = $1 FOR UPDATE`, connectorID).Scan(&kind); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if kind != "scim" {
			return ErrWrongKind
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM scim_tokens WHERE connector_id = $1 AND revoked_at IS NULL`, connectorID).Scan(&n); err != nil {
			return err
		}
		if n >= MaxActiveTokens {
			return ErrTooMany
		}
		var err error
		t, err = scanToken(tx.QueryRowContext(ctx, `INSERT INTO scim_tokens (connector_id, token_hash, hint, created_by) VALUES ($1, $2, $3, $4)
			RETURNING `+tokenCols, connectorID, hash, hint, actor))
		if err != nil {
			return err
		}
		return audit(ctx, tx, actor, "scim-token-create", connectorID)
	})
	return t, err
}

// ListTokens implements Store: newest first.
func (p *PG) ListTokens(ctx context.Context, connectorID int64) ([]Token, error) {
	rows, err := p.DB.QueryContext(ctx, `SELECT `+tokenCols+` FROM scim_tokens WHERE connector_id = $1 ORDER BY id DESC LIMIT 50`, connectorID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Token{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeToken implements Store.
func (p *PG) RevokeToken(ctx context.Context, actor string, id int64) (Token, error) {
	var t Token
	err := p.tx(ctx, func(tx *sql.Tx) error {
		var err error
		t, err = scanToken(tx.QueryRowContext(ctx, `UPDATE scim_tokens SET revoked_at = coalesce(revoked_at, now()) WHERE id = $1
			RETURNING `+tokenCols, id))
		if err != nil {
			return err
		}
		return audit(ctx, tx, actor, "scim-token-revoke", t.ConnectorID)
	})
	return t, err
}

func (p *PG) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

const resourceCols = `id, connector_id, resource_type, name, coalesce(external_id, ''), data::text, version, deleted_at IS NOT NULL, created_at, updated_at`

func scanResource(r interface{ Scan(...any) error }) (Resource, error) {
	var (
		x    Resource
		data string
	)
	if err := r.Scan(&x.ID, &x.ConnectorID, &x.Type, &x.Name, &x.ExternalID, &data, &x.Version, &x.Deleted, &x.Created, &x.Updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return x, ErrNotFound
		}
		return x, err
	}
	d := json.NewDecoder(strings.NewReader(data))
	d.UseNumber()
	if err := d.Decode(&x.Data); err != nil {
		return x, err
	}
	if x.Data == nil {
		x.Data = map[string]any{}
	}
	return x, nil
}

// GetResource implements Store.
func (p *PG) GetResource(ctx context.Context, connectorID int64, typ, id string, withDeleted bool) (Resource, error) {
	return scanResource(p.DB.QueryRowContext(ctx, `SELECT `+resourceCols+` FROM scim_resources
		WHERE connector_id = $1 AND resource_type = $2 AND id = $3 AND ($4 OR deleted_at IS NULL)`, connectorID, typ, id, withDeleted))
}

// FindResources implements Store.
func (p *PG) FindResources(ctx context.Context, connectorID int64, typ string, hint *Hint) ([]Resource, error) {
	q := `SELECT ` + resourceCols + ` FROM scim_resources WHERE connector_id = $1 AND resource_type = $2 AND deleted_at IS NULL`
	args := []any{connectorID, typ}
	if hint != nil {
		switch hint.Field {
		case "id":
			q += ` AND id = $3`
		case "name":
			q += ` AND lower(name) = lower($3)`
		case "external_id":
			q += ` AND external_id = $3`
		}
		args = append(args, hint.Value)
	}
	rows, err := p.DB.QueryContext(ctx, q+` ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Resource
	for rows.Next() {
		r, err := scanResource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FindDeleted implements Store.
func (p *PG) FindDeleted(ctx context.Context, connectorID int64, typ, name, externalID string) (*Resource, error) {
	r, err := scanResource(p.DB.QueryRowContext(ctx, `SELECT `+resourceCols+` FROM scim_resources
		WHERE connector_id = $1 AND resource_type = $2 AND deleted_at IS NOT NULL
			AND (lower(name) = lower($3) OR ($4 <> '' AND external_id = $4))
		ORDER BY (external_id = $4) DESC, updated_at DESC LIMIT 1`, connectorID, typ, name, externalID))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// SaveResource implements Store.
func (p *PG) SaveResource(ctx context.Context, r Resource, members []string) (Resource, error) {
	var out Resource
	data, err := json.Marshal(r.Data)
	if err != nil {
		return out, err
	}
	err = p.tx(ctx, func(tx *sql.Tx) error {
		var err error
		if r.Version == 0 {
			out, err = scanResource(tx.QueryRowContext(ctx, `
				INSERT INTO scim_resources (id, connector_id, resource_type, name, external_id, data)
				VALUES ($1, $2, $3, $4, $5, $6::jsonb) RETURNING `+resourceCols,
				r.ID, r.ConnectorID, r.Type, r.Name, nullString(r.ExternalID), string(data)))
		} else {
			out, err = scanResource(tx.QueryRowContext(ctx, `
				UPDATE scim_resources SET name = $4, external_id = $5, data = $6::jsonb, version = version + 1, deleted_at = NULL, updated_at = now()
				WHERE connector_id = $1 AND id = $2 AND version = $3 RETURNING `+resourceCols,
				r.ConnectorID, r.ID, r.Version, r.Name, nullString(r.ExternalID), string(data)))
		}
		if err != nil {
			return err
		}
		if members == nil {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM scim_group_members WHERE group_id = $1 AND NOT (member_id = ANY($2::text[]))`, r.ID, members); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO scim_group_members (group_id, member_id) SELECT $1, m FROM unnest($2::text[]) AS m
			ON CONFLICT DO NOTHING`, r.ID, members)
		return err
	})
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return Resource{}, ErrConflict
	}
	if errors.As(err, &pg) && pg.Code == "23503" {
		return Resource{}, ErrNotFound
	}
	return out, err
}

// MarkDeleted implements Store.
func (p *PG) MarkDeleted(ctx context.Context, connectorID int64, typ, id string) error {
	q := `UPDATE scim_resources SET deleted_at = now(), version = version + 1, updated_at = now()
		WHERE connector_id = $1 AND resource_type = $2 AND id = $3 AND deleted_at IS NULL`
	if typ == TypeGroup {
		q = `DELETE FROM scim_resources WHERE connector_id = $1 AND resource_type = $2 AND id = $3`
	}
	res, err := p.DB.ExecContext(ctx, q, connectorID, typ, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Members implements Store: live members only.
func (p *PG) Members(ctx context.Context, groupIDs []string) (map[string][]memberRef, error) {
	out := map[string][]memberRef{}
	if len(groupIDs) == 0 {
		return out, nil
	}
	rows, err := p.DB.QueryContext(ctx, `
		SELECT m.group_id, r.id, r.name FROM scim_group_members m JOIN scim_resources r ON r.id = m.member_id
		WHERE m.group_id = ANY($1::text[]) AND r.deleted_at IS NULL ORDER BY m.group_id, r.created_at, r.id`, groupIDs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var g string
		var m memberRef
		if err := rows.Scan(&g, &m.ID, &m.Name); err != nil {
			return nil, err
		}
		out[g] = append(out[g], m)
	}
	return out, rows.Err()
}

// GroupsOf implements Store.
func (p *PG) GroupsOf(ctx context.Context, userIDs []string) (map[string][]groupRef, error) {
	out := map[string][]groupRef{}
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := p.DB.QueryContext(ctx, `
		SELECT m.member_id, g.id, coalesce(g.external_id, ''), g.name FROM scim_group_members m JOIN scim_resources g ON g.id = m.group_id
		WHERE m.member_id = ANY($1::text[]) ORDER BY m.member_id, g.name`, userIDs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var u string
		var g groupRef
		if err := rows.Scan(&u, &g.ID, &g.ExternalID, &g.Name); err != nil {
			return nil, err
		}
		out[u] = append(out[u], g)
	}
	return out, rows.Err()
}

// Existing implements Store.
func (p *PG) Existing(ctx context.Context, connectorID int64, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := p.DB.QueryContext(ctx, `SELECT id FROM scim_resources WHERE connector_id = $1 AND resource_type = 'User' AND id = ANY($2::text[])`,
		connectorID, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// Counts implements Store.
func (p *PG) Counts(ctx context.Context, connectorID int64) (Counts, error) {
	var c Counts
	err := p.DB.QueryRowContext(ctx, `
		SELECT
			count(*) FILTER (WHERE resource_type = 'User' AND deleted_at IS NULL),
			count(*) FILTER (WHERE resource_type = 'User' AND deleted_at IS NULL AND coalesce(data->>'active', 'true') <> 'false'),
			count(*) FILTER (WHERE resource_type = 'User' AND deleted_at IS NOT NULL),
			count(*) FILTER (WHERE resource_type = 'Group')
		FROM scim_resources WHERE connector_id = $1`, connectorID).Scan(&c.Users, &c.ActiveUsers, &c.DeletedUsers, &c.Groups)
	return c, err
}

// LastUsed implements Store.
func (p *PG) LastUsed(ctx context.Context, connectorID int64) (*time.Time, error) {
	var t sql.NullTime
	if err := p.DB.QueryRowContext(ctx, `SELECT max(last_used_at) FROM scim_tokens WHERE connector_id = $1`, connectorID).Scan(&t); err != nil {
		return nil, err
	}
	if !t.Valid {
		return nil, nil
	}
	return &t.Time, nil
}
