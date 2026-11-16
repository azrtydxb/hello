package scim

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/advisory"
	"github.com/jackc/pgx/v5/pgconn"
)

// PG is the PostgreSQL Store, on momo (internal/db).
//
// The statements are the hand-written SQL they were on database/sql, with
// @name binds instead of $n, and rows are scanned positionally as before
// (queryRow and queryEach over momo.RawRows). Errors reach callers as
// before: ErrNotFound for a missing row, a moved version or a missing
// referenced row, ErrConflict for a taken name or externalId, and the
// driver's error (a *pgconn.PgError is still reachable with errors.As) for
// anything else.
type PG struct{ DB *momo.DB }

// row is one row of a query, run when it is scanned: QueryRow(...).Scan's
// contract on momo, sql.ErrNoRows when there is no row (G7: momo has no
// single-row raw helper).
type row struct {
	ctx  context.Context
	q    momo.Querier
	sql  string
	args []any
}

func queryRow(ctx context.Context, q momo.Querier, sql string, args ...any) row {
	return row{ctx: ctx, q: q, sql: sql, args: args}
}

// Scan scans the first row into dest.
func (r row) Scan(dest ...any) error {
	rows, err := momo.RawRows(r.ctx, r.q, r.sql, r.args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	if err := rows.Scan(dest...); err != nil {
		return err
	}
	return rows.Close()
}

// scanner is a row being read: a momo.Rows or a row.
type scanner interface{ Scan(...any) error }

// queryEach runs the query and calls fn for every row, in order.
func queryEach(ctx context.Context, q momo.Querier, fn func(scanner) error, sql string, args ...any) error {
	rows, err := momo.RawRows(ctx, q, sql, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return rows.Close()
}

// lockKey is the directory engine's per-connector advisory lock key.
func lockKey(id int64) string { return "hello:dirsync:" + strconv.FormatInt(id, 10) }

// lockWait bounds how long a write waits for the connector's lock.
const lockWait = 15 * time.Second

// ErrBusy is a write that could not take the connector's lock in time.
var ErrBusy = errors.New("scim: the connector is busy")

// Lock implements Store: a session advisory lock on a dedicated connection,
// waited for at most lockWait. The key is hashtext(lockKey), the directory
// engine's (directory.PG.TryLock). A wait that fails, and an unlock that
// fails, discard the connection instead of returning it to the pool in an
// unknown state.
func (p *PG) Lock(ctx context.Context, connectorID int64) (func(), error) {
	pinner, ok := p.DB.Driver().(momo.Pinner)
	if !ok {
		return nil, errors.New("scim: the database handle cannot pin a connection")
	}
	pc, err := pinner.Pin(ctx)
	if err != nil {
		return nil, err
	}
	conn := p.DB.OnConn(pc)
	discard := func() {
		if d, ok := pc.(momo.Discarder); ok {
			_ = d.Discard()
			return
		}
		_ = pc.Release()
	}
	wctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	if err := advisory.LockString(wctx, conn, lockKey(connectorID)); err != nil {
		// A cancelled wait may leave the connection in an unknown state.
		discard()
		if wctx.Err() != nil && ctx.Err() == nil {
			return nil, ErrBusy
		}
		return nil, err
	}
	return func() {
		uctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := advisory.UnlockString(uctx, conn, lockKey(connectorID)); err != nil {
			discard()
			return
		}
		_ = pc.Release()
	}, nil
}

func audit(ctx context.Context, q momo.Querier, actor, action string, connectorID int64) error {
	_, err := momo.Exec(ctx, q, `INSERT INTO audit_events (actor, action, resource, resource_id, via) VALUES (@actor, @action, 'directory_connector', @resource_id, NULL)`,
		momo.Named("actor", actor), momo.Named("action", action), momo.Named("resource_id", strconv.FormatInt(connectorID, 10)))
	return err
}

//nolint:gosec // G101: column names, not a credential.
const tokenCols = `id, connector_id, hint, created_by, created_at, last_used_at, revoked_at`

func scanToken(r scanner) (Token, error) {
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
	return scanToken(queryRow(ctx, p.DB, `
		SELECT t.id, t.connector_id, t.hint, t.created_by, t.created_at, t.last_used_at, t.revoked_at
		FROM scim_tokens t JOIN directory_connectors c ON c.id = t.connector_id
		WHERE t.token_hash = @hash AND t.revoked_at IS NULL AND c.kind = 'scim'`, momo.Named("hash", hash)))
}

// TouchToken implements Store.
func (p *PG) TouchToken(ctx context.Context, id int64, at time.Time) error {
	_, err := momo.Exec(ctx, p.DB, `UPDATE scim_tokens SET last_used_at = @at WHERE id = @id`, momo.Named("id", id), momo.Named("at", at))
	return err
}

// CreateToken implements Store.
func (p *PG) CreateToken(ctx context.Context, actor string, connectorID int64, hash []byte, hint string) (Token, error) {
	var t Token
	err := p.tx(ctx, func(tx *momo.Tx) error {
		conn := momo.Named("connector_id", connectorID)
		var kind string
		if err := queryRow(ctx, tx, `SELECT kind FROM directory_connectors WHERE id = @connector_id FOR UPDATE`, conn).Scan(&kind); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if kind != "scim" {
			return ErrWrongKind
		}
		var n int
		if err := queryRow(ctx, tx, `SELECT count(*) FROM scim_tokens WHERE connector_id = @connector_id AND revoked_at IS NULL`, conn).Scan(&n); err != nil {
			return err
		}
		if n >= MaxActiveTokens {
			return ErrTooMany
		}
		var err error
		t, err = scanToken(queryRow(ctx, tx, `INSERT INTO scim_tokens (connector_id, token_hash, hint, created_by) VALUES (@connector_id, @hash, @hint, @created_by)
			RETURNING `+tokenCols, conn, momo.Named("hash", hash), momo.Named("hint", hint), momo.Named("created_by", actor)))
		if err != nil {
			return err
		}
		return audit(ctx, tx, actor, "scim-token-create", connectorID)
	})
	return t, err
}

// ListTokens implements Store: newest first.
func (p *PG) ListTokens(ctx context.Context, connectorID int64) ([]Token, error) {
	out := []Token{}
	err := queryEach(ctx, p.DB, func(r scanner) error {
		t, err := scanToken(r)
		if err != nil {
			return err
		}
		out = append(out, t)
		return nil
	}, `SELECT `+tokenCols+` FROM scim_tokens WHERE connector_id = @connector_id ORDER BY id DESC LIMIT 50`, momo.Named("connector_id", connectorID))
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RevokeToken implements Store.
func (p *PG) RevokeToken(ctx context.Context, actor string, id int64) (Token, error) {
	var t Token
	err := p.tx(ctx, func(tx *momo.Tx) error {
		var err error
		t, err = scanToken(queryRow(ctx, tx, `UPDATE scim_tokens SET revoked_at = coalesce(revoked_at, now()) WHERE id = @id
			RETURNING `+tokenCols, momo.Named("id", id)))
		if err != nil {
			return err
		}
		return audit(ctx, tx, actor, "scim-token-revoke", t.ConnectorID)
	})
	return t, err
}

// tx runs fn in a transaction: committed when fn returns nil, rolled back
// when it returns an error, which is returned as is.
func (p *PG) tx(ctx context.Context, fn func(*momo.Tx) error) error {
	return p.DB.Tx(ctx, momo.TxOptions{}, fn)
}

const resourceCols = `id, connector_id, resource_type, name, coalesce(external_id, ''), data::text, version, deleted_at IS NOT NULL, created_at, updated_at`

func scanResource(r scanner) (Resource, error) {
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
	return scanResource(queryRow(ctx, p.DB, `SELECT `+resourceCols+` FROM scim_resources
		WHERE connector_id = @connector_id AND resource_type = @type AND id = @id AND (@with_deleted OR deleted_at IS NULL)`,
		momo.Named("connector_id", connectorID), momo.Named("type", typ), momo.Named("id", id), momo.Named("with_deleted", withDeleted)))
}

// FindResources implements Store.
func (p *PG) FindResources(ctx context.Context, connectorID int64, typ string, hint *Hint) ([]Resource, error) {
	q := `SELECT ` + resourceCols + ` FROM scim_resources WHERE connector_id = @connector_id AND resource_type = @type AND deleted_at IS NULL`
	args := []any{momo.Named("connector_id", connectorID), momo.Named("type", typ)}
	if hint != nil {
		switch hint.Field {
		case "id":
			q += ` AND id = @value`
		case "name":
			q += ` AND lower(name) = lower(@value)`
		case "external_id":
			q += ` AND external_id = @value`
		}
		args = append(args, momo.Named("value", hint.Value))
	}
	var out []Resource
	err := queryEach(ctx, p.DB, func(r scanner) error {
		x, err := scanResource(r)
		if err != nil {
			return err
		}
		out = append(out, x)
		return nil
	}, q+` ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// FindDeleted implements Store.
func (p *PG) FindDeleted(ctx context.Context, connectorID int64, typ, name, externalID string) (*Resource, error) {
	r, err := scanResource(queryRow(ctx, p.DB, `SELECT `+resourceCols+` FROM scim_resources
		WHERE connector_id = @connector_id AND resource_type = @type AND deleted_at IS NOT NULL
			AND (lower(name) = lower(@name) OR (@external_id::text <> '' AND external_id = @external_id::text))
		ORDER BY (external_id = @external_id::text) DESC, updated_at DESC LIMIT 1`,
		momo.Named("connector_id", connectorID), momo.Named("type", typ), momo.Named("name", name), momo.Named("external_id", externalID)))
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
	err = p.tx(ctx, func(tx *momo.Tx) error {
		cols := []any{momo.Named("id", r.ID), momo.Named("connector_id", r.ConnectorID), momo.Named("name", r.Name),
			momo.Named("external_id", nullString(r.ExternalID)), momo.Named("data", string(data))}
		var err error
		if r.Version == 0 {
			out, err = scanResource(queryRow(ctx, tx, `
				INSERT INTO scim_resources (id, connector_id, resource_type, name, external_id, data)
				VALUES (@id, @connector_id, @type, @name, @external_id, @data::jsonb) RETURNING `+resourceCols,
				append(cols, momo.Named("type", r.Type))...))
		} else {
			out, err = scanResource(queryRow(ctx, tx, `
				UPDATE scim_resources SET name = @name, external_id = @external_id, data = @data::jsonb, version = version + 1, deleted_at = NULL, updated_at = now()
				WHERE connector_id = @connector_id AND id = @id AND version = @version RETURNING `+resourceCols,
				append(cols, momo.Named("version", r.Version))...))
		}
		if err != nil {
			return err
		}
		if members == nil {
			return nil
		}
		group, list := momo.Named("group_id", r.ID), momo.Named("members", members)
		if _, err := momo.Exec(ctx, tx, `DELETE FROM scim_group_members WHERE group_id = @group_id AND NOT (member_id = ANY(@members::text[]))`, group, list); err != nil {
			return err
		}
		_, err = momo.Exec(ctx, tx, `INSERT INTO scim_group_members (group_id, member_id) SELECT @group_id, m FROM unnest(@members::text[]) AS m
			ON CONFLICT DO NOTHING`, group, list)
		return err
	})
	var pg *pgconn.PgError
	if errors.Is(err, momo.ErrUniqueViolation) || errors.As(err, &pg) && pg.Code == "23505" {
		return Resource{}, ErrConflict
	}
	if errors.Is(err, momo.ErrForeignKeyViolation) || errors.As(err, &pg) && pg.Code == "23503" {
		return Resource{}, ErrNotFound
	}
	return out, err
}

// MarkDeleted implements Store.
func (p *PG) MarkDeleted(ctx context.Context, connectorID int64, typ, id string) error {
	q := `UPDATE scim_resources SET deleted_at = now(), version = version + 1, updated_at = now()
		WHERE connector_id = @connector_id AND resource_type = @type AND id = @id AND deleted_at IS NULL`
	if typ == TypeGroup {
		q = `DELETE FROM scim_resources WHERE connector_id = @connector_id AND resource_type = @type AND id = @id`
	}
	n, err := momo.Exec(ctx, p.DB, q, momo.Named("connector_id", connectorID), momo.Named("type", typ), momo.Named("id", id))
	if err != nil {
		return err
	}
	if n == 0 {
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
	err := queryEach(ctx, p.DB, func(r scanner) error {
		var g string
		var m memberRef
		if err := r.Scan(&g, &m.ID, &m.Name); err != nil {
			return err
		}
		out[g] = append(out[g], m)
		return nil
	}, `
		SELECT m.group_id, r.id, r.name FROM scim_group_members m JOIN scim_resources r ON r.id = m.member_id
		WHERE m.group_id = ANY(@groups::text[]) AND r.deleted_at IS NULL ORDER BY m.group_id, r.created_at, r.id`, momo.Named("groups", groupIDs))
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GroupsOf implements Store.
func (p *PG) GroupsOf(ctx context.Context, userIDs []string) (map[string][]groupRef, error) {
	out := map[string][]groupRef{}
	if len(userIDs) == 0 {
		return out, nil
	}
	err := queryEach(ctx, p.DB, func(r scanner) error {
		var u string
		var g groupRef
		if err := r.Scan(&u, &g.ID, &g.ExternalID, &g.Name); err != nil {
			return err
		}
		out[u] = append(out[u], g)
		return nil
	}, `
		SELECT m.member_id, g.id, coalesce(g.external_id, ''), g.name FROM scim_group_members m JOIN scim_resources g ON g.id = m.group_id
		WHERE m.member_id = ANY(@users::text[]) ORDER BY m.member_id, g.name`, momo.Named("users", userIDs))
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Existing implements Store.
func (p *PG) Existing(ctx context.Context, connectorID int64, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	err := queryEach(ctx, p.DB, func(r scanner) error {
		var id string
		if err := r.Scan(&id); err != nil {
			return err
		}
		out[id] = true
		return nil
	}, `SELECT id FROM scim_resources WHERE connector_id = @connector_id AND resource_type = 'User' AND id = ANY(@ids::text[])`,
		momo.Named("connector_id", connectorID), momo.Named("ids", ids))
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Counts implements Store.
func (p *PG) Counts(ctx context.Context, connectorID int64) (Counts, error) {
	var c Counts
	err := queryRow(ctx, p.DB, `
		SELECT
			count(*) FILTER (WHERE resource_type = 'User' AND deleted_at IS NULL),
			count(*) FILTER (WHERE resource_type = 'User' AND deleted_at IS NULL AND coalesce(data->>'active', 'true') <> 'false'),
			count(*) FILTER (WHERE resource_type = 'User' AND deleted_at IS NOT NULL),
			count(*) FILTER (WHERE resource_type = 'Group')
		FROM scim_resources WHERE connector_id = @connector_id`, momo.Named("connector_id", connectorID)).Scan(&c.Users, &c.ActiveUsers, &c.DeletedUsers, &c.Groups)
	return c, err
}

// LastUsed implements Store.
func (p *PG) LastUsed(ctx context.Context, connectorID int64) (*time.Time, error) {
	var t sql.NullTime
	if err := queryRow(ctx, p.DB, `SELECT max(last_used_at) FROM scim_tokens WHERE connector_id = @connector_id`,
		momo.Named("connector_id", connectorID)).Scan(&t); err != nil {
		return nil, err
	}
	if !t.Valid {
		return nil, nil
	}
	return &t.Time, nil
}
