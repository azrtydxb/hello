package directory

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/azrtydxb/hello/internal/secret"
)

// configLockKey is the transaction-scoped advisory lock every configuration
// change takes first (the store's "hellocfg" key), so ring-group and device
// changes made here serialise with the administrators' own.
const configLockKey = 0x68656c6c6f636667

// notifyChannel is the channel hello-sip listens on for a new configuration
// revision (store.NotifyChannel).
const notifyChannel = "hello_config"

// PG is the PostgreSQL Store.
type PG struct {
	DB  *sql.DB
	Box *secret.Box
}

func credentialAAD(id int64) string { return "directory:" + strconv.FormatInt(id, 10) + ":credential" }

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

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func audit(ctx context.Context, q execer, actor, action, resource, id string) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO audit_events (actor, action, resource, resource_id, via) VALUES ($1, $2, $3, $4, NULL)`,
		actor, action, resource, id)
	return err
}

// Audit implements Store.
func (p *PG) Audit(ctx context.Context, actor, action, resource, id string) error {
	return audit(ctx, p.DB, actor, action, resource, id)
}

const connectorCols = `c.id, c.kind, c.name, c.enabled, c.mode, c.schedule_minutes, c.config::text, c.mappings::text, c.group_rules::text,
	c.options::text, c.auto_provision::text, c.credential_enc IS NOT NULL, c.credential_fp, c.status, c.last_run_at, c.last_success_at,
	c.last_error_code, c.last_error_detail, c.consecutive_failures, c.created_at, c.updated_at,
	(SELECT count(*) FROM users u WHERE u.source_connector_id = c.id)`

func scanConnector(r interface{ Scan(...any) error }) (Connector, error) {
	var (
		c                             Connector
		cfg, maps, rules, opts, autoP string
		lastRun, lastOK               sql.NullTime
	)
	if err := r.Scan(&c.ID, &c.Kind, &c.Name, &c.Enabled, &c.Mode, &c.ScheduleMinutes, &cfg, &maps, &rules, &opts, &autoP,
		&c.HasCredential, &c.CredentialFP, &c.Status, &lastRun, &lastOK, &c.LastErrorCode, &c.LastErrorDetail,
		&c.ConsecutiveFailure, &c.CreatedAt, &c.UpdatedAt, &c.LinkedUsers); err != nil {
		return c, err
	}
	c.Config, c.AutoProvision = json.RawMessage(cfg), json.RawMessage(autoP)
	for _, d := range []struct {
		raw string
		to  any
	}{{maps, &c.Mappings}, {rules, &c.GroupRules}, {opts, &c.Options}} {
		if err := json.Unmarshal([]byte(d.raw), d.to); err != nil {
			return c, err
		}
	}
	c.Mappings = c.Mappings.Normalize()
	if c.GroupRules.Roles == nil {
		c.GroupRules.Roles = []RoleRule{}
	}
	if c.GroupRules.RingGroups == nil {
		c.GroupRules.RingGroups = []RingGroupRule{}
	}
	if lastRun.Valid {
		c.LastRunAt = &lastRun.Time
	}
	if lastOK.Valid {
		c.LastSuccessAt = &lastOK.Time
	}
	return c, nil
}

func mapErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// ListConnectors implements Store.
func (p *PG) ListConnectors(ctx context.Context) ([]Connector, error) {
	rows, err := p.DB.QueryContext(ctx, `SELECT `+connectorCols+` FROM directory_connectors c ORDER BY c.id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Connector{}
	for rows.Next() {
		c, err := scanConnector(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetConnector implements Store.
func (p *PG) GetConnector(ctx context.Context, id int64) (Connector, error) {
	c, err := scanConnector(p.DB.QueryRowContext(ctx, `SELECT `+connectorCols+` FROM directory_connectors c WHERE c.id = $1`, id))
	return c, mapErr(err)
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// SaveConnector implements Store.
func (p *PG) SaveConnector(ctx context.Context, actor string, c Connector, secretValue *string, fp string) (Connector, error) {
	var id int64
	err := p.tx(ctx, func(tx *sql.Tx) error {
		wasEnabled := false
		if c.ID == 0 {
			if err := tx.QueryRowContext(ctx, `
				INSERT INTO directory_connectors (kind, name, enabled, mode, schedule_minutes, config, mappings, group_rules, options, auto_provision)
				VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8::jsonb, $9::jsonb, $10::jsonb) RETURNING id`,
				c.Kind, c.Name, c.Enabled, c.Mode, c.ScheduleMinutes, string(c.Config), jsonOf(c.Mappings), jsonOf(c.GroupRules),
				jsonOf(c.Options), string(c.AutoProvision)).Scan(&id); err != nil {
				return err
			}
			if err := audit(ctx, tx, actor, "create", "directory_connector", strconv.FormatInt(id, 10)); err != nil {
				return err
			}
		} else {
			id = c.ID
			if err := tx.QueryRowContext(ctx, `SELECT enabled FROM directory_connectors WHERE id = $1 FOR UPDATE`, id).Scan(&wasEnabled); err != nil {
				return mapErr(err)
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE directory_connectors SET name = $2, enabled = $3, mode = $4, schedule_minutes = $5, config = $6::jsonb,
					mappings = $7::jsonb, group_rules = $8::jsonb, options = $9::jsonb, auto_provision = $10::jsonb, updated_at = now()
				WHERE id = $1`,
				id, c.Name, c.Enabled, c.Mode, c.ScheduleMinutes, string(c.Config), jsonOf(c.Mappings), jsonOf(c.GroupRules),
				jsonOf(c.Options), string(c.AutoProvision)); err != nil {
				return err
			}
			if err := audit(ctx, tx, actor, "update", "directory_connector", strconv.FormatInt(id, 10)); err != nil {
				return err
			}
			if wasEnabled != c.Enabled {
				act := "disable"
				if c.Enabled {
					act = "enable"
				}
				if err := audit(ctx, tx, actor, act, "directory_connector", strconv.FormatInt(id, 10)); err != nil {
					return err
				}
			}
		}
		if secretValue != nil {
			if p.Box == nil {
				return errors.New("directory: no secret box configured")
			}
			sealed, err := p.Box.Seal(*secretValue, credentialAAD(id))
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE directory_connectors SET credential_enc = $2, credential_fp = $3 WHERE id = $1`, id, sealed, fp); err != nil {
				return err
			}
			return audit(ctx, tx, actor, "set-credential", "directory_connector", strconv.FormatInt(id, 10))
		}
		return nil
	})
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			return Connector{}, fieldErr("name", "a connector with this name already exists")
		}
		return Connector{}, err
	}
	return p.GetConnector(ctx, id)
}

// DeleteConnector implements Store.
func (p *PG) DeleteConnector(ctx context.Context, actor string, id int64, release bool) error {
	return p.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(configLockKey)); err != nil {
			return err
		}
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM directory_connectors WHERE id = $1 FOR UPDATE`, id).Scan(&one); err != nil {
			return mapErr(err)
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE source_connector_id = $1`, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 && !release {
			return ErrHasUsers
		}
		// Released users become local users: they keep their rows, lose the
		// lock on their fields, and have no password until an administrator
		// sets one.
		if _, err := tx.ExecContext(ctx, `UPDATE users SET source_connector_id = NULL, external_id = NULL, managed_fields = '{}',
			absent_runs = 0 WHERE source_connector_id = $1`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE user_extensions SET source_connector_id = NULL WHERE source_connector_id = $1`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ring_group_members SET source_connector_id = NULL WHERE source_connector_id = $1`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM directory_connectors WHERE id = $1`, id); err != nil {
			return err
		}
		if n > 0 {
			if err := audit(ctx, tx, actor, "release-users", "directory_connector", strconv.FormatInt(id, 10)); err != nil {
				return err
			}
		}
		return audit(ctx, tx, actor, "delete", "directory_connector", strconv.FormatInt(id, 10))
	})
}

// Secret implements Store.
func (p *PG) Secret(ctx context.Context, id int64) (string, error) {
	var enc []byte
	if err := p.DB.QueryRowContext(ctx, `SELECT credential_enc FROM directory_connectors WHERE id = $1`, id).Scan(&enc); err != nil {
		return "", mapErr(err)
	}
	if enc == nil {
		return "", ErrNotFound
	}
	if p.Box == nil {
		return "", errors.New("directory: no secret box configured")
	}
	return p.Box.Open(enc, credentialAAD(id))
}

// RecordOutcome implements Store.
func (p *PG) RecordOutcome(ctx context.Context, id int64, ok bool, code, detail string, at time.Time) (int, error) {
	if len(detail) > 300 {
		detail = detail[:300]
	}
	var n int
	err := p.DB.QueryRowContext(ctx, `
		UPDATE directory_connectors SET
			last_run_at = $5,
			status = CASE WHEN $2 THEN 'ok' ELSE 'failing' END,
			last_success_at = CASE WHEN $2 THEN $5 ELSE last_success_at END,
			consecutive_failures = CASE WHEN $2 THEN 0 ELSE consecutive_failures + 1 END,
			last_error_code = CASE WHEN $2 THEN '' ELSE $3 END,
			last_error_detail = CASE WHEN $2 THEN '' ELSE $4 END
		WHERE id = $1 RETURNING consecutive_failures`, id, ok, code, detail, at).Scan(&n)
	return n, mapErr(err)
}

const runCols = `id, connector_id, kind, status, requested_by, created_at, started_at, finished_at, replica, counts::text, diff_hash, error_code, error_detail`

func scanRun(r interface{ Scan(...any) error }) (Run, error) {
	var (
		x       Run
		started sql.NullTime
		done    sql.NullTime
		counts  string
	)
	if err := r.Scan(&x.ID, &x.ConnectorID, &x.Kind, &x.Status, &x.RequestedBy, &x.CreatedAt, &started, &done, &x.Replica, &counts,
		&x.DiffHash, &x.ErrorCode, &x.ErrorDetail); err != nil {
		return x, err
	}
	if started.Valid {
		x.StartedAt = &started.Time
	}
	if done.Valid {
		x.FinishedAt = &done.Time
	}
	x.Counts = map[string]int{}
	_ = json.Unmarshal([]byte(counts), &x.Counts)
	return x, nil
}

// CreateRun implements Store.
func (p *PG) CreateRun(ctx context.Context, r Run) (Run, error) {
	x, err := scanRun(p.DB.QueryRowContext(ctx, `
		INSERT INTO directory_sync_runs (connector_id, kind, status, requested_by, started_at, replica, counts)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb) RETURNING `+runCols,
		r.ConnectorID, r.Kind, r.Status, r.RequestedBy, r.StartedAt, r.Replica, jsonOf(orEmpty(r.Counts))))
	return x, mapErr(err)
}

func orEmpty(m map[string]int) map[string]int {
	if m == nil {
		return map[string]int{}
	}
	return m
}

// GetRun implements Store.
func (p *PG) GetRun(ctx context.Context, id int64) (Run, error) {
	x, err := scanRun(p.DB.QueryRowContext(ctx, `SELECT `+runCols+` FROM directory_sync_runs WHERE id = $1`, id))
	return x, mapErr(err)
}

// ListRuns implements Store.
func (p *PG) ListRuns(ctx context.Context, connectorID int64, limit int) ([]Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := p.DB.QueryContext(ctx, `SELECT `+runCols+` FROM directory_sync_runs WHERE connector_id = $1 ORDER BY id DESC LIMIT $2`, connectorID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Run{}
	for rows.Next() {
		x, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// UpdateRun implements Store.
func (p *PG) UpdateRun(ctx context.Context, r Run) error {
	_, err := p.DB.ExecContext(ctx, `
		UPDATE directory_sync_runs SET kind = $2, status = $3, requested_by = $4, started_at = $5, finished_at = $6, replica = $7,
			counts = $8::jsonb, diff_hash = $9, error_code = $10, error_detail = $11 WHERE id = $1`,
		r.ID, r.Kind, r.Status, r.RequestedBy, r.StartedAt, r.FinishedAt, r.Replica, jsonOf(orEmpty(r.Counts)), r.DiffHash, r.ErrorCode, r.ErrorDetail)
	return err
}

// ClaimQueued implements Store.
func (p *PG) ClaimQueued(ctx context.Context, connectorID int64, replica string) (*Run, error) {
	x, err := scanRun(p.DB.QueryRowContext(ctx, `
		UPDATE directory_sync_runs SET status = 'running', started_at = now(), replica = $2
		WHERE id = (SELECT id FROM directory_sync_runs WHERE connector_id = $1 AND status = 'queued' ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING `+runCols, connectorID, replica))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &x, nil
}

// CloseOrphans implements Store.
func (p *PG) CloseOrphans(ctx context.Context, connectorID int64) (int, error) {
	res, err := p.DB.ExecContext(ctx, `
		UPDATE directory_sync_runs SET status = 'failed', error_code = $2, error_detail = 'the replica running it went away', finished_at = now()
		WHERE connector_id = $1 AND status = 'running'`, connectorID, CodeReplicaLost)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// LastScheduled implements Store.
func (p *PG) LastScheduled(ctx context.Context, connectorID int64) (time.Time, bool, error) {
	var t sql.NullTime
	if err := p.DB.QueryRowContext(ctx, `SELECT max(created_at) FROM directory_sync_runs WHERE connector_id = $1 AND kind = 'scheduled'`, connectorID).Scan(&t); err != nil {
		return time.Time{}, false, err
	}
	return t.Time, t.Valid, nil
}

// HasApplied implements Store.
func (p *PG) HasApplied(ctx context.Context, connectorID int64) (bool, error) {
	var ok bool
	err := p.DB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM directory_sync_runs
		WHERE connector_id = $1 AND status = 'succeeded' AND kind IN ('apply', 'scheduled'))`, connectorID).Scan(&ok)
	return ok, err
}

// ReplaceChanges implements Store.
func (p *PG) ReplaceChanges(ctx context.Context, runID int64, cs []Change) error {
	return p.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM directory_sync_changes WHERE run_id = $1`, runID); err != nil {
			return err
		}
		st, err := tx.PrepareContext(ctx, `INSERT INTO directory_sync_changes (run_id, action, user_id, external_id, username, field_diffs, reason)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7)`)
		if err != nil {
			return err
		}
		defer func() { _ = st.Close() }()
		for _, c := range cs {
			if _, err := st.ExecContext(ctx, runID, c.Action, c.UserID, c.ExternalID, c.Username, jsonOf(nonNilDiffs(c.FieldDiffs)), c.Reason); err != nil {
				return err
			}
		}
		return nil
	})
}

func nonNilDiffs(m map[string]Diff) map[string]Diff {
	if m == nil {
		return map[string]Diff{}
	}
	return m
}

// ListChanges implements Store.
func (p *PG) ListChanges(ctx context.Context, runID int64, f ChangeFilter) ([]Change, error) {
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := p.DB.QueryContext(ctx, `
		SELECT id, run_id, action, user_id, external_id, username, field_diffs::text, reason, created_at
		FROM directory_sync_changes WHERE run_id = $1 AND id > $2 AND ($3 = '' OR action = $3) ORDER BY id LIMIT $4`,
		runID, f.After, f.Action, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Change{}
	for rows.Next() {
		var (
			c     Change
			uid   sql.NullInt64
			diffs string
		)
		if err := rows.Scan(&c.ID, &c.RunID, &c.Action, &uid, &c.ExternalID, &c.Username, &diffs, &c.Reason, &c.CreatedAt); err != nil {
			return nil, err
		}
		if uid.Valid {
			c.UserID = &uid.Int64
		}
		c.FieldDiffs = map[string]Diff{}
		_ = json.Unmarshal([]byte(diffs), &c.FieldDiffs)
		c.Overridable = c.Action == ActionConflict && c.Reason == ReasonExtensionLinked
		out = append(out, c)
	}
	return out, rows.Err()
}

// LoadState implements Store.
func (p *PG) LoadState(ctx context.Context, connectorID int64) (State, error) {
	st := State{Exts: map[string]LocalExt{}, RingGroups: map[int64]bool{}}
	rows, err := p.DB.QueryContext(ctx, `
		SELECT id, username, email, display_name, role, enabled, coalesce(source_connector_id, 0), coalesce(external_id, ''),
			disabled_at IS NOT NULL, absent_runs, to_json(managed_fields)::text
		FROM users ORDER BY id`)
	if err != nil {
		return st, err
	}
	idx := map[int64]int{}
	for rows.Next() {
		var (
			u       LocalUser
			managed string
		)
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.DisplayName, &u.Role, &u.Enabled, &u.ConnectorID, &u.ExternalID,
			&u.DisabledByDirectory, &u.AbsentRuns, &managed); err != nil {
			_ = rows.Close()
			return st, err
		}
		_ = json.Unmarshal([]byte(managed), &u.Managed)
		if len(u.Managed) == 0 {
			u.Managed = nil
		}
		if u.Role == "admin" && u.Enabled {
			st.Admins++
		}
		idx[u.ID] = len(st.Users)
		st.Users = append(st.Users, u)
	}
	if err := rows.Close(); err != nil {
		return st, err
	}
	owned := map[int64]int64{} // user -> owned extension
	links, err := p.DB.QueryContext(ctx, `SELECT user_id, extension_id, coalesce(source_connector_id, 0) FROM user_extensions ORDER BY user_id, position`)
	if err != nil {
		return st, err
	}
	extUser := map[int64]int64{}
	for links.Next() {
		var u, e, by int64
		if err := links.Scan(&u, &e, &by); err != nil {
			_ = links.Close()
			return st, err
		}
		extUser[e] = u
		if i, ok := idx[u]; ok {
			st.Users[i].ExtensionIDs = append(st.Users[i].ExtensionIDs, e)
			if by == connectorID {
				st.Users[i].OwnedExtension = e
				owned[u] = e
			}
		}
	}
	if err := links.Close(); err != nil {
		return st, err
	}
	exts, err := p.DB.QueryContext(ctx, `SELECT id, number, kind = 'door' FROM extensions`)
	if err != nil {
		return st, err
	}
	for exts.Next() {
		var e LocalExt
		if err := exts.Scan(&e.ID, &e.Number, &e.Door); err != nil {
			_ = exts.Close()
			return st, err
		}
		e.UserID = extUser[e.ID]
		st.Exts[e.Number] = e
	}
	if err := exts.Close(); err != nil {
		return st, err
	}
	ownerOfExt := map[int64]int{}
	for u, e := range owned {
		ownerOfExt[e] = idx[u]
	}
	ms, err := p.DB.QueryContext(ctx, `SELECT group_id, extension_id, coalesce(source_connector_id, 0) FROM ring_group_members`)
	if err != nil {
		return st, err
	}
	for ms.Next() {
		var g, e, by int64
		if err := ms.Scan(&g, &e, &by); err != nil {
			_ = ms.Close()
			return st, err
		}
		if i, ok := ownerOfExt[e]; ok {
			st.Users[i].MemberOf = append(st.Users[i].MemberOf, g)
			if by == connectorID {
				st.Users[i].SyncedMemberOf = append(st.Users[i].SyncedMemberOf, g)
			}
		}
	}
	if err := ms.Close(); err != nil {
		return st, err
	}
	gs, err := p.DB.QueryContext(ctx, `SELECT id FROM ring_groups`)
	if err != nil {
		return st, err
	}
	for gs.Next() {
		var g int64
		if err := gs.Scan(&g); err != nil {
			_ = gs.Close()
			return st, err
		}
		st.RingGroups[g] = true
	}
	return st, gs.Close()
}

// PruneChanges implements Store: old changes, and finished runs past the
// same age.
func (p *PG) PruneChanges(ctx context.Context, before time.Time) (int64, error) {
	res, err := p.DB.ExecContext(ctx, `DELETE FROM directory_sync_changes WHERE created_at < $1`, before)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if _, err := p.DB.ExecContext(ctx, `DELETE FROM directory_sync_runs WHERE created_at < $1 AND status NOT IN ('queued', 'running')`, before); err != nil {
		return n, err
	}
	return n, nil
}

// TryLock implements Store: a session-level advisory lock on a dedicated
// connection, so a dead replica's lock goes with its connection (I-13).
func (p *PG) TryLock(ctx context.Context, key string) (func(), bool, error) {
	conn, err := p.DB.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	var ok bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtext($1::text))`, key).Scan(&ok); err != nil {
		_ = conn.Close()
		return nil, false, err
	}
	if !ok {
		_ = conn.Close()
		return nil, false, nil
	}
	return func() {
		uctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(uctx, `SELECT pg_advisory_unlock(hashtext($1::text))`, key); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = conn.Close()
	}, true, nil
}

func itoa64(id int64) string { return strconv.FormatInt(id, 10) }

func (o Op) touchesConfig() bool {
	return len(o.RingAdd)+len(o.RingRemove) > 0 || o.SetExtension || (o.Devices && (o.Kind == ActionDisable || o.Kind == ActionEnable))
}

// Apply implements Store: one operation, in one transaction, audited as the
// connector (one row per user-level change, never per field, never a value).
func (p *PG) Apply(ctx context.Context, actor string, c Connector, op Op) error {
	return p.tx(ctx, func(tx *sql.Tx) error {
		if op.touchesConfig() {
			if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(configLockKey)); err != nil {
				return err
			}
		}
		bump := false
		var err error
		switch op.Kind {
		case ActionCreate:
			bump, err = p.create(ctx, tx, actor, c, op)
		case ActionUpdate, ActionAdopt:
			bump, err = p.update(ctx, tx, actor, c, op)
		case ActionDisable:
			bump, err = p.disable(ctx, tx, actor, op)
		case ActionEnable:
			bump, err = p.enable(ctx, tx, actor, op)
		case "absent":
			_, err = tx.ExecContext(ctx, `UPDATE users SET absent_runs = $2 WHERE id = $1`, op.UserID, op.AbsentRuns)
		case "touch":
			_, err = tx.ExecContext(ctx, `UPDATE users SET absent_runs = 0 WHERE id = $1 AND source_connector_id = $2`, op.UserID, c.ID)
		case "managed":
			_, err = tx.ExecContext(ctx, `UPDATE users SET managed_fields = ARRAY(SELECT jsonb_array_elements_text($3::jsonb))
				WHERE id = $1 AND source_connector_id = $2`, op.UserID, c.ID, jsonOf(nonNilStrings(op.Managed)))
		default:
			err = fmt.Errorf("directory: unknown operation %q", op.Kind)
		}
		if err != nil {
			return err
		}
		if bump {
			return bumpRevision(ctx, tx)
		}
		return nil
	})
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func bumpRevision(ctx context.Context, tx *sql.Tx) error {
	var rev int64
	if err := tx.QueryRowContext(ctx, `UPDATE schema_info SET config_revision = config_revision + 1 RETURNING config_revision`).Scan(&rev); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `SELECT pg_notify($1, $2::text)`, notifyChannel, strconv.FormatInt(rev, 10))
	return err
}

func (p *PG) create(ctx context.Context, tx *sql.Tx, actor string, c Connector, op Op) (bool, error) {
	if op.Username == nil || op.Role == nil {
		return false, errors.New("directory: create without username or role")
	}
	email, display := "", ""
	if op.Email != nil {
		email = *op.Email
	}
	if op.Display != nil {
		display = *op.Display
	}
	var id int64
	// The password is a marker no hash equals ("An empty or unknown hash never
	// matches"), so the user cannot sign in locally until it can through its
	// directory (a later phase).
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO users (username, password_hash, role, display_name, email, enabled, must_change_password,
			source_connector_id, external_id, managed_fields)
		VALUES ($1, '!', $2, $3, $4, TRUE, FALSE, $5, $6, ARRAY(SELECT jsonb_array_elements_text($7::jsonb)))
		RETURNING id`,
		*op.Username, *op.Role, display, email, c.ID, op.ExternalID, jsonOf(nonNilStrings(op.Managed))).Scan(&id); err != nil {
		return false, err
	}
	bump, err := p.extAndRings(ctx, tx, c, id, op)
	if err != nil {
		return false, err
	}
	return bump, audit(ctx, tx, actor, "create", "user", itoa64(id))
}

// lockAdmins locks every enabled admin row and counts them, like the store's.
func lockAdmins(ctx context.Context, tx *sql.Tx) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM users WHERE role = 'admin' AND enabled FOR UPDATE`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	n := 0
	for rows.Next() {
		n++
	}
	return n, rows.Err()
}

func (p *PG) update(ctx context.Context, tx *sql.Tx, actor string, c Connector, op Op) (bool, error) {
	admins, err := lockAdmins(ctx, tx)
	if err != nil {
		return false, err
	}
	var (
		role    string
		enabled bool
		src     sql.NullInt64
	)
	if err := tx.QueryRowContext(ctx, `SELECT role, enabled, source_connector_id FROM users WHERE id = $1 FOR UPDATE`, op.UserID).Scan(&role, &enabled, &src); err != nil {
		return false, mapErr(err)
	}
	if op.Kind == ActionAdopt && src.Valid {
		return false, fmt.Errorf("%w: already linked", ErrNotApplicable)
	}
	if op.Kind == ActionUpdate && (!src.Valid || src.Int64 != c.ID) {
		return false, ErrNotFound
	}
	if op.Role != nil && role == "admin" && enabled && *op.Role != "admin" && admins <= 1 {
		return false, ErrLastAdmin
	}
	if op.Kind == ActionAdopt && role == "admin" {
		return false, errors.New("directory: a local admin is never adopted")
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE users SET
			username = COALESCE($2, username),
			email = COALESCE($3, email),
			display_name = COALESCE($4, display_name),
			role = COALESCE($5, role),
			managed_fields = CASE WHEN $6 THEN ARRAY(SELECT jsonb_array_elements_text($7::jsonb)) ELSE managed_fields END,
			source_connector_id = CASE WHEN $8 THEN $9 ELSE source_connector_id END,
			external_id = CASE WHEN $8 THEN $10 ELSE external_id END,
			absent_runs = 0
		WHERE id = $1`,
		op.UserID, op.Username, op.Email, op.Display, op.Role, op.SetManaged, jsonOf(nonNilStrings(op.Managed)),
		op.Kind == ActionAdopt, c.ID, op.ExternalID); err != nil {
		return false, err
	}
	bump, err := p.extAndRings(ctx, tx, c, op.UserID, op)
	if err != nil {
		return false, err
	}
	return bump, audit(ctx, tx, actor, op.Kind, "user", itoa64(op.UserID))
}

// extAndRings links the user's synced extension and brings its synced
// ring-group rows to the plan. Rows the connector does not own are never
// touched (I-4, I-10).
func (p *PG) extAndRings(ctx context.Context, tx *sql.Tx, c Connector, userID int64, op Op) (bool, error) {
	bump := false
	if op.SetExtension && op.Extension != 0 {
		if op.Override {
			if _, err := tx.ExecContext(ctx, `DELETE FROM user_extensions WHERE extension_id = $1 AND user_id <> $2`, op.Extension, userID); err != nil {
				return false, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE users SET extension_id = NULL WHERE extension_id = $1 AND id <> $2`, op.Extension, userID); err != nil {
				return false, err
			}
		}
		// The rows the old synced link put in ring groups go with it.
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM ring_group_members WHERE source_connector_id = $1 AND extension_id IN
				(SELECT extension_id FROM user_extensions WHERE user_id = $2 AND source_connector_id = $1 AND extension_id <> $3)`,
			c.ID, userID, op.Extension); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM user_extensions WHERE user_id = $1 AND source_connector_id = $2 AND extension_id <> $3`,
			userID, c.ID, op.Extension); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO user_extensions (user_id, extension_id, position, source_connector_id)
			VALUES ($1, $2, (SELECT coalesce(max(position) + 1, 0) FROM user_extensions WHERE user_id = $1), $3)
			ON CONFLICT (user_id, extension_id) DO UPDATE SET source_connector_id = EXCLUDED.source_connector_id`,
			userID, op.Extension, c.ID); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE users SET extension_id = $2 WHERE id = $1
				AND (extension_id IS NULL OR extension_id NOT IN (SELECT extension_id FROM user_extensions WHERE user_id = $1))`,
			userID, op.Extension); err != nil {
			return false, err
		}
		bump = true
	}
	if len(op.RingAdd)+len(op.RingRemove) == 0 {
		return bump, nil
	}
	var ext int64
	switch err := tx.QueryRowContext(ctx, `SELECT extension_id FROM user_extensions WHERE user_id = $1 AND source_connector_id = $2 ORDER BY position LIMIT 1`,
		userID, c.ID).Scan(&ext); {
	case errors.Is(err, sql.ErrNoRows):
		return bump, nil
	case err != nil:
		return false, err
	}
	for _, g := range op.RingAdd {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO ring_group_members (group_id, extension_id, position, source_connector_id)
			SELECT $1::bigint, $2::bigint, coalesce(max(position), 0) + 1, $3::bigint FROM ring_group_members WHERE group_id = $1::bigint
			ON CONFLICT (group_id, extension_id) DO NOTHING`, g, ext, c.ID); err != nil {
			return false, err
		}
	}
	if len(op.RingRemove) > 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM ring_group_members WHERE group_id = ANY($1::bigint[]) AND extension_id = $2 AND source_connector_id = $3`,
			op.RingRemove, ext, c.ID); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (p *PG) disable(ctx context.Context, tx *sql.Tx, actor string, op Op) (bool, error) {
	admins, err := lockAdmins(ctx, tx)
	if err != nil {
		return false, err
	}
	var (
		role    string
		enabled bool
	)
	if err := tx.QueryRowContext(ctx, `SELECT role, enabled FROM users WHERE id = $1 FOR UPDATE`, op.UserID).Scan(&role, &enabled); err != nil {
		return false, mapErr(err)
	}
	if !enabled {
		return false, nil
	}
	if role == "admin" && admins <= 1 {
		return false, ErrLastAdmin
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET enabled = FALSE, disabled_at = now(), absent_runs = $2 WHERE id = $1`, op.UserID, op.AbsentRuns); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = $1`, op.UserID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, op.UserID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `
		WITH g AS (UPDATE oauth_grants SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL RETURNING id)
		UPDATE oauth_tokens SET revoked_at = now() WHERE revoked_at IS NULL AND (user_id = $1 OR grant_id IN (SELECT id FROM g))`, op.UserID); err != nil {
		return false, err
	}
	bump := false
	if op.Devices {
		res, err := tx.ExecContext(ctx, `
			UPDATE devices SET enabled = FALSE, disabled_by_directory = TRUE, updated_at = now()
			WHERE enabled AND extension_id IN (SELECT extension_id FROM user_extensions WHERE user_id = $1)`, op.UserID)
		if err != nil {
			return false, err
		}
		n, _ := res.RowsAffected()
		bump = n > 0
	}
	return bump, audit(ctx, tx, actor, "disable", "user", itoa64(op.UserID))
}

func (p *PG) enable(ctx context.Context, tx *sql.Tx, actor string, op Op) (bool, error) {
	if _, err := tx.ExecContext(ctx, `UPDATE users SET enabled = TRUE, disabled_at = NULL, absent_runs = 0 WHERE id = $1`, op.UserID); err != nil {
		return false, err
	}
	bump := false
	if op.Devices {
		res, err := tx.ExecContext(ctx, `
			UPDATE devices SET enabled = TRUE, disabled_by_directory = FALSE, updated_at = now()
			WHERE disabled_by_directory AND extension_id IN (SELECT extension_id FROM user_extensions WHERE user_id = $1)`, op.UserID)
		if err != nil {
			return false, err
		}
		n, _ := res.RowsAffected()
		bump = n > 0
	}
	return bump, audit(ctx, tx, actor, "enable", "user", itoa64(op.UserID))
}
