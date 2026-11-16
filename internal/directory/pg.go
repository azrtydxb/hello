package directory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/advisory"
	"github.com/azrtydxb/momo/driver/pgxdriver"
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

// PG is the PostgreSQL Store, on momo (internal/db).
//
// The statements are the hand-written SQL they were on database/sql, with
// @name binds instead of $n, and rows are scanned positionally as before
// (queryRow and queryEach over momo.RawRows). Errors reach callers as before:
// ErrNotFound for a missing row, the field error for a taken connector name,
// and the driver's error (a *pgconn.PgError is still reachable with
// errors.As) for anything else. The Provisioner still receives the
// transaction as a *sql.Tx (pgxdriver.UnwrapSQLTx), so DB must be a handle
// over a *sql.DB (internal/db.New).
type PG struct {
	DB  *momo.DB
	Box *secret.Box
	// Provisioner creates the extensions of range auto-provisioning (I-11);
	// nil refuses a run that would provision.
	Provisioner Provisioner
}

func credentialAAD(id int64) string { return "directory:" + strconv.FormatInt(id, 10) + ":credential" }

// tx runs fn in a transaction: committed when fn returns nil, rolled back
// when it returns an error, which is returned as is.
func (p *PG) tx(ctx context.Context, fn func(*momo.Tx) error) error {
	return p.DB.Tx(ctx, momo.TxOptions{}, fn)
}

// sqlTx is the *sql.Tx behind tx, for the Provisioner, which runs the
// configuration store's own statements in it.
func sqlTx(tx *momo.Tx) (*sql.Tx, error) {
	stx, ok := pgxdriver.UnwrapSQLTx(tx)
	if !ok {
		return nil, errors.New("directory: the provisioner needs a database/sql transaction (a momo handle from internal/db.New)")
	}
	return stx, nil
}

// row is one row of a query, run when it is scanned: QueryRow(...).Scan's
// contract on momo, sql.ErrNoRows when there is no row, so mapErr maps it
// as before (G7: momo has no single-row raw helper).
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

func audit(ctx context.Context, q momo.Querier, actor, action, resource, id string) error {
	_, err := momo.Exec(ctx, q,
		`INSERT INTO audit_events (actor, action, resource, resource_id, via) VALUES (@actor, @action, @resource, @resource_id, NULL)`,
		momo.Named("actor", actor), momo.Named("action", action), momo.Named("resource", resource), momo.Named("resource_id", id))
	return err
}

// Audit implements Store.
func (p *PG) Audit(ctx context.Context, actor, action, resource, id string) error {
	return audit(ctx, p.DB, actor, action, resource, id)
}

const connectorCols = `c.id, c.kind, c.name, c.enabled, c.mode, c.schedule_minutes, c.config::text, c.mappings::text, c.group_rules::text,
	c.options::text, c.auto_provision::text, c.credential_enc IS NOT NULL, c.credential_fp, c.status, c.last_run_at, c.last_success_at,
	c.last_error_code, c.last_error_detail, c.consecutive_failures, c.created_at, c.updated_at,
	(SELECT count(*) FROM users u WHERE u.source_connector_id = c.id), c.sync_mark::text, c.sync_fingerprint, c.last_full_run_at,
	c.paused_at, c.guard_since`

func scanConnector(r scanner) (Connector, error) {
	var (
		c                             Connector
		cfg, maps, rules, opts, autoP string
		lastRun, lastOK, lastFull     sql.NullTime
		paused, guard                 sql.NullTime
		mark                          string
	)
	if err := r.Scan(&c.ID, &c.Kind, &c.Name, &c.Enabled, &c.Mode, &c.ScheduleMinutes, &cfg, &maps, &rules, &opts, &autoP,
		&c.HasCredential, &c.CredentialFP, &c.Status, &lastRun, &lastOK, &c.LastErrorCode, &c.LastErrorDetail,
		&c.ConsecutiveFailure, &c.CreatedAt, &c.UpdatedAt, &c.LinkedUsers, &mark, &c.Sync.Fingerprint, &lastFull,
		&paused, &guard); err != nil {
		return c, err
	}
	if mark != "" && mark != "{}" && mark != "null" {
		var m SyncMark
		if json.Unmarshal([]byte(mark), &m) == nil && m.Method != "" {
			c.Sync.Mark = &m
		}
	}
	if lastFull.Valid {
		c.LastFullRunAt, c.Sync.LastFullAt = &lastFull.Time, &lastFull.Time
	}
	if paused.Valid {
		c.PausedAt = &paused.Time
	}
	if guard.Valid {
		c.GuardSince = &guard.Time
	}
	c.Config = json.RawMessage(cfg)
	for _, d := range []struct {
		raw string
		to  any
	}{{maps, &c.Mappings}, {rules, &c.GroupRules}, {opts, &c.Options}, {autoP, &c.AutoProvision}} {
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

// unique reports a unique violation: momo.ErrUniqueViolation, or the
// *pgconn.PgError 23505 check this store always made (it still holds
// through momo's error).
func unique(err error) bool {
	var pe *pgconn.PgError
	return errors.Is(err, momo.ErrUniqueViolation) || errors.As(err, &pe) && pe.Code == "23505"
}

// ListConnectors implements Store.
func (p *PG) ListConnectors(ctx context.Context) ([]Connector, error) {
	out := []Connector{}
	err := queryEach(ctx, p.DB, func(r scanner) error {
		c, err := scanConnector(r)
		if err != nil {
			return err
		}
		out = append(out, c)
		return nil
	}, `SELECT `+connectorCols+` FROM directory_connectors c ORDER BY c.id`)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetConnector implements Store.
func (p *PG) GetConnector(ctx context.Context, id int64) (Connector, error) {
	c, err := scanConnector(queryRow(ctx, p.DB, `SELECT `+connectorCols+` FROM directory_connectors c WHERE c.id = @id`, momo.Named("id", id)))
	return c, mapErr(err)
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// SaveConnector implements Store.
func (p *PG) SaveConnector(ctx context.Context, actor string, c Connector, secretValue *string, fp string) (Connector, error) {
	var id int64
	err := p.tx(ctx, func(tx *momo.Tx) error {
		wasEnabled := false
		cols := []any{momo.Named("name", c.Name), momo.Named("enabled", c.Enabled), momo.Named("mode", c.Mode),
			momo.Named("schedule_minutes", c.ScheduleMinutes), momo.Named("config", string(c.Config)), momo.Named("mappings", jsonOf(c.Mappings)),
			momo.Named("group_rules", jsonOf(c.GroupRules)), momo.Named("options", jsonOf(c.Options)), momo.Named("auto_provision", jsonOf(c.AutoProvision))}
		if c.ID == 0 {
			if err := queryRow(ctx, tx, `
				INSERT INTO directory_connectors (kind, name, enabled, mode, schedule_minutes, config, mappings, group_rules, options, auto_provision)
				VALUES (@kind, @name, @enabled, @mode, @schedule_minutes, @config::jsonb, @mappings::jsonb, @group_rules::jsonb, @options::jsonb, @auto_provision::jsonb) RETURNING id`,
				append(cols, momo.Named("kind", c.Kind))...).Scan(&id); err != nil {
				return err
			}
			if err := audit(ctx, tx, actor, "create", "directory_connector", strconv.FormatInt(id, 10)); err != nil {
				return err
			}
		} else {
			id = c.ID
			if err := queryRow(ctx, tx, `SELECT enabled FROM directory_connectors WHERE id = @id FOR UPDATE`, momo.Named("id", id)).Scan(&wasEnabled); err != nil {
				return mapErr(err)
			}
			if _, err := momo.Exec(ctx, tx, `
				UPDATE directory_connectors SET name = @name, enabled = @enabled, mode = @mode, schedule_minutes = @schedule_minutes, config = @config::jsonb,
					mappings = @mappings::jsonb, group_rules = @group_rules::jsonb, options = @options::jsonb, auto_provision = @auto_provision::jsonb, updated_at = now()
				WHERE id = @id`,
				append(cols, momo.Named("id", id))...); err != nil {
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
			if _, err := momo.Exec(ctx, tx, `UPDATE directory_connectors SET credential_enc = @sealed, credential_fp = @fp WHERE id = @id`,
				momo.Named("id", id), momo.Named("sealed", sealed), momo.Named("fp", fp)); err != nil {
				return err
			}
			return audit(ctx, tx, actor, "set-credential", "directory_connector", strconv.FormatInt(id, 10))
		}
		return nil
	})
	if err != nil {
		if unique(err) {
			return Connector{}, fieldErr("name", "a connector with this name already exists")
		}
		return Connector{}, err
	}
	return p.GetConnector(ctx, id)
}

// DeleteConnector implements Store.
func (p *PG) DeleteConnector(ctx context.Context, actor string, id int64, release bool) error {
	return p.tx(ctx, func(tx *momo.Tx) error {
		if err := advisory.XactLock(ctx, tx, configLockKey); err != nil {
			return err
		}
		idArg := momo.Named("id", id)
		var one int
		if err := queryRow(ctx, tx, `SELECT 1 FROM directory_connectors WHERE id = @id FOR UPDATE`, idArg).Scan(&one); err != nil {
			return mapErr(err)
		}
		var n int
		if err := queryRow(ctx, tx, `SELECT count(*) FROM users WHERE source_connector_id = @id`, idArg).Scan(&n); err != nil {
			return err
		}
		if n > 0 && !release {
			return ErrHasUsers
		}
		// Released users become local users: they keep their rows, lose the
		// lock on their fields, and have no password until an administrator
		// sets one.
		if _, err := momo.Exec(ctx, tx, `UPDATE users SET source_connector_id = NULL, external_id = NULL, managed_fields = '{}',
			absent_runs = 0 WHERE source_connector_id = @id`, idArg); err != nil {
			return err
		}
		if _, err := momo.Exec(ctx, tx, `UPDATE user_extensions SET source_connector_id = NULL WHERE source_connector_id = @id`, idArg); err != nil {
			return err
		}
		if _, err := momo.Exec(ctx, tx, `UPDATE ring_group_members SET source_connector_id = NULL WHERE source_connector_id = @id`, idArg); err != nil {
			return err
		}
		if _, err := momo.Exec(ctx, tx, `DELETE FROM directory_connectors WHERE id = @id`, idArg); err != nil {
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
	if err := queryRow(ctx, p.DB, `SELECT credential_enc FROM directory_connectors WHERE id = @id`, momo.Named("id", id)).Scan(&enc); err != nil {
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
	err := queryRow(ctx, p.DB, `
		UPDATE directory_connectors SET
			last_run_at = @at,
			status = CASE WHEN @ok::boolean THEN 'ok' ELSE 'failing' END,
			last_success_at = CASE WHEN @ok::boolean THEN @at::timestamptz ELSE last_success_at END,
			consecutive_failures = CASE WHEN @ok::boolean THEN 0 ELSE consecutive_failures + 1 END,
			last_error_code = CASE WHEN @ok::boolean THEN '' ELSE @code END,
			last_error_detail = CASE WHEN @ok::boolean THEN '' ELSE @detail END
		WHERE id = @id RETURNING consecutive_failures`,
		momo.Named("id", id), momo.Named("ok", ok), momo.Named("code", code), momo.Named("detail", detail), momo.Named("at", at)).Scan(&n)
	return n, mapErr(err)
}

// SaveSyncState implements Store.
func (p *PG) SaveSyncState(ctx context.Context, id int64, s SyncState) error {
	mark := "{}"
	if s.Mark != nil {
		mark = jsonOf(s.Mark)
	}
	n, err := momo.Exec(ctx, p.DB, `UPDATE directory_connectors SET sync_mark = @mark::jsonb, sync_fingerprint = @fingerprint, last_full_run_at = @last_full WHERE id = @id`,
		momo.Named("id", id), momo.Named("mark", mark), momo.Named("fingerprint", s.Fingerprint), momo.Named("last_full", s.LastFullAt))
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

const runCols = `id, connector_id, kind, status, requested_by, created_at, started_at, finished_at, replica, counts::text, diff_hash, error_code, error_detail, incremental, full_reason`

func scanRun(r scanner) (Run, error) {
	var (
		x       Run
		started sql.NullTime
		done    sql.NullTime
		counts  string
	)
	if err := r.Scan(&x.ID, &x.ConnectorID, &x.Kind, &x.Status, &x.RequestedBy, &x.CreatedAt, &started, &done, &x.Replica, &counts,
		&x.DiffHash, &x.ErrorCode, &x.ErrorDetail, &x.Incremental, &x.FullReason); err != nil {
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
	x, err := scanRun(queryRow(ctx, p.DB, `
		INSERT INTO directory_sync_runs (connector_id, kind, status, requested_by, started_at, replica, counts, incremental, full_reason)
		VALUES (@connector_id, @kind, @status, @requested_by, @started_at, @replica, @counts::jsonb, @incremental, @full_reason) RETURNING `+runCols,
		momo.Named("connector_id", r.ConnectorID), momo.Named("kind", r.Kind), momo.Named("status", r.Status), momo.Named("requested_by", r.RequestedBy),
		momo.Named("started_at", r.StartedAt), momo.Named("replica", r.Replica), momo.Named("counts", jsonOf(orEmpty(r.Counts))),
		momo.Named("incremental", r.Incremental), momo.Named("full_reason", r.FullReason)))
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
	x, err := scanRun(queryRow(ctx, p.DB, `SELECT `+runCols+` FROM directory_sync_runs WHERE id = @id`, momo.Named("id", id)))
	return x, mapErr(err)
}

// ListRuns implements Store.
func (p *PG) ListRuns(ctx context.Context, connectorID int64, limit int) ([]Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	out := []Run{}
	err := queryEach(ctx, p.DB, func(r scanner) error {
		x, err := scanRun(r)
		if err != nil {
			return err
		}
		out = append(out, x)
		return nil
	}, `SELECT `+runCols+` FROM directory_sync_runs WHERE connector_id = @connector_id ORDER BY id DESC LIMIT @limit`,
		momo.Named("connector_id", connectorID), momo.Named("limit", limit))
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateRun implements Store.
func (p *PG) UpdateRun(ctx context.Context, r Run) error {
	_, err := momo.Exec(ctx, p.DB, `
		UPDATE directory_sync_runs SET kind = @kind, status = @status, requested_by = @requested_by, started_at = @started_at, finished_at = @finished_at,
			replica = @replica, counts = @counts::jsonb, diff_hash = @diff_hash, error_code = @error_code, error_detail = @error_detail,
			incremental = @incremental, full_reason = @full_reason WHERE id = @id`,
		momo.Named("id", r.ID), momo.Named("kind", r.Kind), momo.Named("status", r.Status), momo.Named("requested_by", r.RequestedBy),
		momo.Named("started_at", r.StartedAt), momo.Named("finished_at", r.FinishedAt), momo.Named("replica", r.Replica),
		momo.Named("counts", jsonOf(orEmpty(r.Counts))), momo.Named("diff_hash", r.DiffHash), momo.Named("error_code", r.ErrorCode),
		momo.Named("error_detail", r.ErrorDetail), momo.Named("incremental", r.Incremental), momo.Named("full_reason", r.FullReason))
	return err
}

// ClaimQueued implements Store.
func (p *PG) ClaimQueued(ctx context.Context, connectorID int64, replica string) (*Run, error) {
	x, err := scanRun(queryRow(ctx, p.DB, `
		UPDATE directory_sync_runs SET status = 'running', started_at = now(), replica = @replica
		WHERE id = (SELECT id FROM directory_sync_runs WHERE connector_id = @connector_id AND status = 'queued' ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING `+runCols, momo.Named("connector_id", connectorID), momo.Named("replica", replica)))
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
	n, err := momo.Exec(ctx, p.DB, `
		UPDATE directory_sync_runs SET status = 'failed', error_code = @code, error_detail = 'the replica running it went away', finished_at = now()
		WHERE connector_id = @connector_id AND status = 'running'`, momo.Named("connector_id", connectorID), momo.Named("code", CodeReplicaLost))
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// LastScheduled implements Store.
func (p *PG) LastScheduled(ctx context.Context, connectorID int64) (time.Time, bool, error) {
	var t sql.NullTime
	if err := queryRow(ctx, p.DB, `SELECT max(created_at) FROM directory_sync_runs WHERE connector_id = @connector_id AND kind = 'scheduled'`,
		momo.Named("connector_id", connectorID)).Scan(&t); err != nil {
		return time.Time{}, false, err
	}
	return t.Time, t.Valid, nil
}

// HasApplied implements Store.
func (p *PG) HasApplied(ctx context.Context, connectorID int64) (bool, error) {
	var ok bool
	err := queryRow(ctx, p.DB, `SELECT EXISTS (SELECT 1 FROM directory_sync_runs
		WHERE connector_id = @connector_id AND status = 'succeeded' AND kind IN ('apply', 'scheduled'))`, momo.Named("connector_id", connectorID)).Scan(&ok)
	return ok, err
}

// insertChange is the statement ReplaceChanges and RecordPush run per
// change (they prepared it once on database/sql; the text is unchanged).
const insertChange = `INSERT INTO directory_sync_changes (run_id, action, user_id, external_id, username, field_diffs, reason)
	VALUES (@run_id, @action, @user_id, @external_id, @username, @field_diffs::jsonb, @reason)`

func addChange(ctx context.Context, tx *momo.Tx, runID int64, c Change) error {
	_, err := momo.Exec(ctx, tx, insertChange, momo.Named("run_id", runID), momo.Named("action", c.Action), momo.Named("user_id", c.UserID),
		momo.Named("external_id", c.ExternalID), momo.Named("username", c.Username), momo.Named("field_diffs", jsonOf(nonNilDiffs(c.FieldDiffs))),
		momo.Named("reason", c.Reason))
	return err
}

// ReplaceChanges implements Store.
func (p *PG) ReplaceChanges(ctx context.Context, runID int64, cs []Change) error {
	return p.tx(ctx, func(tx *momo.Tx) error {
		if _, err := momo.Exec(ctx, tx, `DELETE FROM directory_sync_changes WHERE run_id = @run_id`, momo.Named("run_id", runID)); err != nil {
			return err
		}
		for _, c := range cs {
			if err := addChange(ctx, tx, runID, c); err != nil {
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
	out := []Change{}
	err := queryEach(ctx, p.DB, func(r scanner) error {
		var (
			c     Change
			uid   sql.NullInt64
			diffs string
		)
		if err := r.Scan(&c.ID, &c.RunID, &c.Action, &uid, &c.ExternalID, &c.Username, &diffs, &c.Reason, &c.CreatedAt); err != nil {
			return err
		}
		if uid.Valid {
			c.UserID = &uid.Int64
		}
		c.FieldDiffs = map[string]Diff{}
		_ = json.Unmarshal([]byte(diffs), &c.FieldDiffs)
		c.Overridable = c.Action == ActionConflict && c.Reason == ReasonExtensionLinked
		out = append(out, c)
		return nil
	}, `
		SELECT id, run_id, action, user_id, external_id, username, field_diffs::text, reason, created_at
		FROM directory_sync_changes WHERE run_id = @run_id AND id > @after AND (@action::text = '' OR action = @action::text) ORDER BY id LIMIT @limit`,
		momo.Named("run_id", runID), momo.Named("after", f.After), momo.Named("action", f.Action), momo.Named("limit", limit))
	if err != nil {
		return nil, err
	}
	return out, nil
}

// LoadState implements Store.
func (p *PG) LoadState(ctx context.Context, connectorID int64) (State, error) {
	st := State{Exts: map[string]LocalExt{}, RingGroups: map[int64]bool{}}
	idx := map[int64]int{}
	if err := queryEach(ctx, p.DB, func(r scanner) error {
		var (
			u       LocalUser
			managed string
		)
		if err := r.Scan(&u.ID, &u.Username, &u.Email, &u.DisplayName, &u.Role, &u.Enabled, &u.ConnectorID, &u.ExternalID,
			&u.DisabledByDirectory, &u.AbsentRuns, &managed, &u.ProvisionedNumber, &u.BreakGlass); err != nil {
			return err
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
		return nil
	}, `
		SELECT id, username, email, display_name, role, enabled, coalesce(source_connector_id, 0), coalesce(external_id, ''),
			disabled_at IS NOT NULL, absent_runs, to_json(managed_fields)::text, coalesce(provisioned_number, ''), break_glass
		FROM users ORDER BY id`); err != nil {
		return st, err
	}
	owned := map[int64]int64{} // user -> owned extension
	extUser := map[int64]int64{}
	if err := queryEach(ctx, p.DB, func(r scanner) error {
		var u, e, by int64
		if err := r.Scan(&u, &e, &by); err != nil {
			return err
		}
		extUser[e] = u
		if i, ok := idx[u]; ok {
			st.Users[i].ExtensionIDs = append(st.Users[i].ExtensionIDs, e)
			if by == connectorID {
				st.Users[i].OwnedExtension = e
				owned[u] = e
			}
		}
		return nil
	}, `SELECT user_id, extension_id, coalesce(source_connector_id, 0) FROM user_extensions ORDER BY user_id, position`); err != nil {
		return st, err
	}
	if err := queryEach(ctx, p.DB, func(r scanner) error {
		var e LocalExt
		if err := r.Scan(&e.ID, &e.Number, &e.Door); err != nil {
			return err
		}
		e.UserID = extUser[e.ID]
		st.Exts[e.Number] = e
		return nil
	}, `SELECT id, number, kind = 'door' FROM extensions`); err != nil {
		return st, err
	}
	ownerOfExt := map[int64]int{}
	for u, e := range owned {
		ownerOfExt[e] = idx[u]
	}
	if err := queryEach(ctx, p.DB, func(r scanner) error {
		var g, e, by int64
		if err := r.Scan(&g, &e, &by); err != nil {
			return err
		}
		if i, ok := ownerOfExt[e]; ok {
			st.Users[i].MemberOf = append(st.Users[i].MemberOf, g)
			if by == connectorID {
				st.Users[i].SyncedMemberOf = append(st.Users[i].SyncedMemberOf, g)
			}
		}
		return nil
	}, `SELECT group_id, extension_id, coalesce(source_connector_id, 0) FROM ring_group_members`); err != nil {
		return st, err
	}
	if err := queryEach(ctx, p.DB, func(r scanner) error {
		var g int64
		if err := r.Scan(&g); err != nil {
			return err
		}
		st.RingGroups[g] = true
		return nil
	}, `SELECT id FROM ring_groups`); err != nil {
		return st, err
	}
	return st, p.loadTaken(ctx, connectorID, &st)
}

// loadTaken fills State.Taken with the numbers of the connector's range that
// the dial space claims, when auto-provisioning is on.
func (p *PG) loadTaken(ctx context.Context, connectorID int64, st *State) error {
	st.Taken = map[string]bool{}
	if p.Provisioner == nil {
		return nil
	}
	c, err := p.GetConnector(ctx, connectorID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !c.AutoProvision.active() {
		return nil
	}
	tx, err := p.DB.Begin(ctx, momo.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	stx, err := sqlTx(tx)
	if err != nil {
		return err
	}
	taken, err := p.Provisioner.Taken(ctx, stx, c.AutoProvision.RangeStart, c.AutoProvision.RangeEnd)
	if err != nil {
		return err
	}
	st.Taken = taken
	return nil
}

// PruneChanges implements Store: old changes, and finished runs past the
// same age.
func (p *PG) PruneChanges(ctx context.Context, before time.Time) (int64, error) {
	n, err := momo.Exec(ctx, p.DB, `DELETE FROM directory_sync_changes WHERE created_at < @before`, momo.Named("before", before))
	if err != nil {
		return 0, err
	}
	if _, err := momo.Exec(ctx, p.DB, `DELETE FROM directory_sync_runs WHERE created_at < @before AND status NOT IN ('queued', 'running')`,
		momo.Named("before", before)); err != nil {
		return n, err
	}
	return n, nil
}

// TryLock implements Store: a session-level advisory lock on a dedicated
// connection, so a dead replica's lock goes with its connection (I-13). The
// key is hashtext(key), as before; an unlock that fails discards the
// connection instead of returning it to the pool with the lock held.
func (p *PG) TryLock(ctx context.Context, key string) (func(), bool, error) {
	pinner, ok := p.DB.Driver().(momo.Pinner)
	if !ok {
		return nil, false, errors.New("directory: the database handle cannot pin a connection")
	}
	pc, err := pinner.Pin(ctx)
	if err != nil {
		return nil, false, err
	}
	conn := p.DB.OnConn(pc)
	got, err := advisory.TryLockString(ctx, conn, key)
	if err != nil {
		_ = pc.Release()
		return nil, false, err
	}
	if !got {
		_ = pc.Release()
		return nil, false, nil
	}
	return func() {
		uctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := advisory.UnlockString(uctx, conn, key); err != nil {
			if d, ok := pc.(momo.Discarder); ok {
				_ = d.Discard()
				return
			}
		}
		_ = pc.Release()
	}, true, nil
}

func itoa64(id int64) string { return strconv.FormatInt(id, 10) }

func (o Op) touchesConfig(c Connector) bool {
	return len(o.RingAdd)+len(o.RingRemove) > 0 || o.SetExtension || o.Provision != nil ||
		(o.Devices && (o.Kind == ActionDisable || o.Kind == ActionEnable)) ||
		(c.AutoProvision.Enabled && o.Kind == ActionUpdate && (o.Display != nil || o.Email != nil))
}

// Apply implements Store: one operation, in one transaction, audited as the
// connector (one row per user-level change, never per field, never a value).
func (p *PG) Apply(ctx context.Context, actor string, c Connector, op Op) (Applied, error) {
	var got Applied
	err := p.tx(ctx, func(tx *momo.Tx) error {
		got = Applied{}
		if op.touchesConfig(c) {
			if err := advisory.XactLock(ctx, tx, configLockKey); err != nil {
				return err
			}
		}
		bump := false
		var err error
		switch op.Kind {
		case ActionCreate:
			bump, err = p.create(ctx, tx, actor, c, op, &got)
		case ActionUpdate, ActionAdopt:
			bump, err = p.update(ctx, tx, actor, c, op, &got)
		case ActionDisable:
			bump, err = p.disable(ctx, tx, actor, op)
		case ActionEnable:
			bump, err = p.enable(ctx, tx, actor, op)
		case "absent":
			_, err = momo.Exec(ctx, tx, `UPDATE users SET absent_runs = @absent_runs WHERE id = @id`,
				momo.Named("id", op.UserID), momo.Named("absent_runs", op.AbsentRuns))
		case "touch":
			_, err = momo.Exec(ctx, tx, `UPDATE users SET absent_runs = 0 WHERE id = @id AND source_connector_id = @connector_id`,
				momo.Named("id", op.UserID), momo.Named("connector_id", c.ID))
		case "managed":
			_, err = momo.Exec(ctx, tx, `UPDATE users SET managed_fields = ARRAY(SELECT jsonb_array_elements_text(@managed::jsonb))
				WHERE id = @id AND source_connector_id = @connector_id`,
				momo.Named("id", op.UserID), momo.Named("connector_id", c.ID), momo.Named("managed", jsonOf(nonNilStrings(op.Managed))))
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
	return got, err
}

// provision allocates and creates the user's extension through the
// Provisioner (inside tx, under the configuration lock), keeps the number on
// the user so a re-run never renumbers, and turns op into a link of it.
func (p *PG) provision(ctx context.Context, tx *momo.Tx, actor string, c Connector, userID int64, op *Op, got *Applied) error {
	if op.Provision == nil {
		return nil
	}
	if p.Provisioner == nil {
		return errors.New("directory: auto-provisioning is not available on this node")
	}
	stx, err := sqlTx(tx)
	if err != nil {
		return err
	}
	pr := op.Provision
	res, err := p.Provisioner.Provision(ctx, stx, actor, ProvisionRequest{ConnectorID: c.ID, Preferred: pr.Preferred,
		RangeStart: pr.Range.RangeStart, RangeEnd: pr.Range.RangeEnd, Name: ExtensionName(pr.Name, ""), Email: pr.Email,
		Voicemail: pr.Range.Voicemail, Device: pr.Range.Device})
	if err != nil {
		return err
	}
	if err := setProvisioned(ctx, tx, userID, res.Number); err != nil {
		return err
	}
	op.Extension, op.SetExtension = res.ExtensionID, true
	got.Extension = res.Number
	return audit(ctx, tx, actor, "provision", "user", itoa64(userID))
}

// setProvisioned keeps the number provisioning gave the user.
func setProvisioned(ctx context.Context, tx *momo.Tx, userID int64, number string) error {
	_, err := momo.Exec(ctx, tx, `UPDATE users SET provisioned_number = @number WHERE id = @id`, momo.Named("id", userID), momo.Named("number", number))
	return err
}

// followUser keeps the extension auto-provisioning created for the user in
// step with it while provisioning is on: its name follows the display name and
// its voicemail email follows the email, each only while it still has the
// value provisioning gave it (an administrator's own edit wins).
func (p *PG) followUser(ctx context.Context, tx *momo.Tx, c Connector, userID int64, op Op, oldName, oldEmail string) (bool, error) {
	if !c.AutoProvision.Enabled || (op.Display == nil && op.Email == nil) {
		return false, nil
	}
	const owned = `SELECT e.id FROM extensions e JOIN user_extensions ue ON ue.extension_id = e.id
		WHERE ue.user_id = @user_id AND ue.source_connector_id = @connector_id AND e.provisioned_by = @connector_id`
	bump := false
	if op.Display != nil && *op.Display != "" && *op.Display != oldName {
		n, err := momo.Exec(ctx, tx, `UPDATE extensions SET name = @name, updated_at = now()
			WHERE id IN (`+owned+`) AND name = @old`, momo.Named("user_id", userID), momo.Named("connector_id", c.ID),
			momo.Named("name", ExtensionName(*op.Display, "")), momo.Named("old", ExtensionName(oldName, "")))
		if err != nil {
			return false, err
		}
		bump = n > 0
	}
	if op.Email != nil && *op.Email != oldEmail {
		n, err := momo.Exec(ctx, tx, `UPDATE voicemail_boxes SET email = @email, updated_at = now()
			WHERE extension_id IN (`+owned+`) AND email = @old`, momo.Named("user_id", userID), momo.Named("connector_id", c.ID),
			momo.Named("email", *op.Email), momo.Named("old", oldEmail))
		if err != nil {
			return false, err
		}
		bump = bump || n > 0
	}
	return bump, nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func bumpRevision(ctx context.Context, tx *momo.Tx) error {
	var rev int64
	if err := queryRow(ctx, tx, `UPDATE schema_info SET config_revision = config_revision + 1 RETURNING config_revision`).Scan(&rev); err != nil {
		return err
	}
	return pgxdriver.Notify(ctx, tx, notifyChannel, strconv.FormatInt(rev, 10))
}

func (p *PG) create(ctx context.Context, tx *momo.Tx, actor string, c Connector, op Op, got *Applied) (bool, error) {
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
	// matches"): a synced user signs in only through its directory, by OIDC
	// or an LDAP bind (I-29).
	if err := queryRow(ctx, tx, `
		INSERT INTO users (username, password_hash, role, display_name, email, enabled, must_change_password,
			source_connector_id, external_id, managed_fields)
		VALUES (@username, '!', @role, @display_name, @email, TRUE, FALSE, @connector_id, @external_id, ARRAY(SELECT jsonb_array_elements_text(@managed::jsonb)))
		RETURNING id`,
		momo.Named("username", *op.Username), momo.Named("role", *op.Role), momo.Named("display_name", display), momo.Named("email", email),
		momo.Named("connector_id", c.ID), momo.Named("external_id", op.ExternalID), momo.Named("managed", jsonOf(nonNilStrings(op.Managed)))).Scan(&id); err != nil {
		return false, err
	}
	if err := audit(ctx, tx, actor, "create", "user", itoa64(id)); err != nil {
		return false, err
	}
	if err := p.provision(ctx, tx, actor, c, id, &op, got); err != nil {
		return false, err
	}
	return p.extAndRings(ctx, tx, c, id, op)
}

// lockAdmins locks every enabled admin row and counts them, like the store's.
func lockAdmins(ctx context.Context, tx *momo.Tx) (int, error) {
	n := 0
	err := queryEach(ctx, tx, func(scanner) error {
		n++
		return nil
	}, `SELECT id FROM users WHERE role = 'admin' AND enabled FOR UPDATE`)
	return n, err
}

func (p *PG) update(ctx context.Context, tx *momo.Tx, actor string, c Connector, op Op, got *Applied) (bool, error) {
	admins, err := lockAdmins(ctx, tx)
	if err != nil {
		return false, err
	}
	var (
		role, oldName, oldEmail string
		enabled, breakGlass     bool
		src                     sql.NullInt64
	)
	if err := queryRow(ctx, tx, `SELECT role, enabled, source_connector_id, CASE WHEN display_name <> '' THEN display_name ELSE username END, email, break_glass
		FROM users WHERE id = @id FOR UPDATE`, momo.Named("id", op.UserID)).Scan(&role, &enabled, &src, &oldName, &oldEmail, &breakGlass); err != nil {
		return false, mapErr(err)
	}
	if breakGlass {
		return false, ErrProtected
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
	if _, err := momo.Exec(ctx, tx, `
		UPDATE users SET
			username = COALESCE(@username, username),
			email = COALESCE(@email, email),
			display_name = COALESCE(@display_name, display_name),
			role = COALESCE(@role, role),
			managed_fields = CASE WHEN @set_managed::boolean THEN ARRAY(SELECT jsonb_array_elements_text(@managed::jsonb)) ELSE managed_fields END,
			source_connector_id = CASE WHEN @adopt::boolean THEN @connector_id ELSE source_connector_id END,
			external_id = CASE WHEN @adopt::boolean THEN @external_id ELSE external_id END,
			-- An adopted user signs in through the directory from now on: its
			-- local password stops working (I-29).
			password_hash = CASE WHEN @adopt::boolean THEN '!' ELSE password_hash END,
			must_change_password = CASE WHEN @adopt::boolean THEN FALSE ELSE must_change_password END,
			absent_runs = 0
		WHERE id = @id`,
		momo.Named("id", op.UserID), momo.Named("username", op.Username), momo.Named("email", op.Email), momo.Named("display_name", op.Display),
		momo.Named("role", op.Role), momo.Named("set_managed", op.SetManaged), momo.Named("managed", jsonOf(nonNilStrings(op.Managed))),
		momo.Named("adopt", op.Kind == ActionAdopt), momo.Named("connector_id", c.ID), momo.Named("external_id", op.ExternalID)); err != nil {
		return false, err
	}
	if err := audit(ctx, tx, actor, op.Kind, "user", itoa64(op.UserID)); err != nil {
		return false, err
	}
	followed, err := p.followUser(ctx, tx, c, op.UserID, op, oldName, oldEmail)
	if err != nil {
		return false, err
	}
	if err := p.provision(ctx, tx, actor, c, op.UserID, &op, got); err != nil {
		return false, err
	}
	bump, err := p.extAndRings(ctx, tx, c, op.UserID, op)
	if err != nil {
		return false, err
	}
	return bump || followed, nil
}

// extAndRings links the user's synced extension and brings its synced
// ring-group rows to the plan. Rows the connector does not own are never
// touched (I-4, I-10).
func (p *PG) extAndRings(ctx context.Context, tx *momo.Tx, c Connector, userID int64, op Op) (bool, error) {
	user, conn, ext := momo.Named("user_id", userID), momo.Named("connector_id", c.ID), momo.Named("extension_id", op.Extension)
	bump := false
	if op.SetExtension && op.Extension != 0 {
		if op.Override {
			if _, err := momo.Exec(ctx, tx, `DELETE FROM user_extensions WHERE extension_id = @extension_id AND user_id <> @user_id`, ext, user); err != nil {
				return false, err
			}
			if _, err := momo.Exec(ctx, tx, `UPDATE users SET extension_id = NULL WHERE extension_id = @extension_id AND id <> @user_id`, ext, user); err != nil {
				return false, err
			}
		}
		// The rows the old synced link put in ring groups go with it.
		if _, err := momo.Exec(ctx, tx, `
			DELETE FROM ring_group_members WHERE source_connector_id = @connector_id AND extension_id IN
				(SELECT extension_id FROM user_extensions WHERE user_id = @user_id AND source_connector_id = @connector_id AND extension_id <> @extension_id)`,
			conn, user, ext); err != nil {
			return false, err
		}
		if _, err := momo.Exec(ctx, tx, `DELETE FROM user_extensions WHERE user_id = @user_id AND source_connector_id = @connector_id AND extension_id <> @extension_id`,
			user, conn, ext); err != nil {
			return false, err
		}
		if _, err := momo.Exec(ctx, tx, `
			INSERT INTO user_extensions (user_id, extension_id, position, source_connector_id)
			VALUES (@user_id, @extension_id, (SELECT coalesce(max(position) + 1, 0) FROM user_extensions WHERE user_id = @user_id), @connector_id)
			ON CONFLICT (user_id, extension_id) DO UPDATE SET source_connector_id = EXCLUDED.source_connector_id`,
			user, ext, conn); err != nil {
			return false, err
		}
		if _, err := momo.Exec(ctx, tx, `
			UPDATE users SET extension_id = @extension_id WHERE id = @user_id
				AND (extension_id IS NULL OR extension_id NOT IN (SELECT extension_id FROM user_extensions WHERE user_id = @user_id))`,
			user, ext); err != nil {
			return false, err
		}
		bump = true
	}
	if len(op.RingAdd)+len(op.RingRemove) == 0 {
		return bump, nil
	}
	var linked int64
	switch err := queryRow(ctx, tx, `SELECT extension_id FROM user_extensions WHERE user_id = @user_id AND source_connector_id = @connector_id ORDER BY position LIMIT 1`,
		user, conn).Scan(&linked); {
	case errors.Is(err, sql.ErrNoRows):
		return bump, nil
	case err != nil:
		return false, err
	}
	for _, g := range op.RingAdd {
		if _, err := momo.Exec(ctx, tx, `
			INSERT INTO ring_group_members (group_id, extension_id, position, source_connector_id)
			SELECT @group_id::bigint, @extension_id::bigint, coalesce(max(position), 0) + 1, @connector_id::bigint FROM ring_group_members WHERE group_id = @group_id::bigint
			ON CONFLICT (group_id, extension_id) DO NOTHING`, momo.Named("group_id", g), momo.Named("extension_id", linked), conn); err != nil {
			return false, err
		}
	}
	if len(op.RingRemove) > 0 {
		if _, err := momo.Exec(ctx, tx, `DELETE FROM ring_group_members WHERE group_id = ANY(@groups::bigint[]) AND extension_id = @extension_id AND source_connector_id = @connector_id`,
			momo.Named("groups", op.RingRemove), momo.Named("extension_id", linked), conn); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (p *PG) disable(ctx context.Context, tx *momo.Tx, actor string, op Op) (bool, error) {
	admins, err := lockAdmins(ctx, tx)
	if err != nil {
		return false, err
	}
	var (
		role       string
		enabled    bool
		breakGlass bool
	)
	user := momo.Named("user_id", op.UserID)
	if err := queryRow(ctx, tx, `SELECT role, enabled, break_glass FROM users WHERE id = @user_id FOR UPDATE`, user).Scan(&role, &enabled, &breakGlass); err != nil {
		return false, mapErr(err)
	}
	if breakGlass {
		return false, ErrProtected
	}
	if !enabled {
		return false, nil
	}
	if role == "admin" && admins <= 1 {
		return false, ErrLastAdmin
	}
	if _, err := momo.Exec(ctx, tx, `UPDATE users SET enabled = FALSE, disabled_at = now(), absent_runs = @absent_runs WHERE id = @user_id`,
		user, momo.Named("absent_runs", op.AbsentRuns)); err != nil {
		return false, err
	}
	if _, err := momo.Exec(ctx, tx, `DELETE FROM sessions WHERE user_id = @user_id`, user); err != nil {
		return false, err
	}
	if _, err := momo.Exec(ctx, tx, `UPDATE api_tokens SET revoked_at = now() WHERE user_id = @user_id AND revoked_at IS NULL`, user); err != nil {
		return false, err
	}
	if _, err := momo.Exec(ctx, tx, `
		WITH g AS (UPDATE oauth_grants SET revoked_at = now() WHERE user_id = @user_id AND revoked_at IS NULL RETURNING id)
		UPDATE oauth_tokens SET revoked_at = now() WHERE revoked_at IS NULL AND (user_id = @user_id OR grant_id IN (SELECT id FROM g))`, user); err != nil {
		return false, err
	}
	bump := false
	if op.Devices {
		n, err := momo.Exec(ctx, tx, `
			UPDATE devices SET enabled = FALSE, disabled_by_directory = TRUE, updated_at = now()
			WHERE enabled AND extension_id IN (SELECT extension_id FROM user_extensions WHERE user_id = @user_id)`, user)
		if err != nil {
			return false, err
		}
		bump = n > 0
	}
	return bump, audit(ctx, tx, actor, "disable", "user", itoa64(op.UserID))
}

func (p *PG) enable(ctx context.Context, tx *momo.Tx, actor string, op Op) (bool, error) {
	user := momo.Named("user_id", op.UserID)
	if _, err := momo.Exec(ctx, tx, `UPDATE users SET enabled = TRUE, disabled_at = NULL, absent_runs = 0 WHERE id = @user_id`, user); err != nil {
		return false, err
	}
	bump := false
	if op.Devices {
		n, err := momo.Exec(ctx, tx, `
			UPDATE devices SET enabled = TRUE, disabled_by_directory = FALSE, updated_at = now()
			WHERE disabled_by_directory AND extension_id IN (SELECT extension_id FROM user_extensions WHERE user_id = @user_id)`, user)
		if err != nil {
			return false, err
		}
		bump = n > 0
	}
	return bump, audit(ctx, tx, actor, "enable", "user", itoa64(op.UserID))
}

// RecordPush implements Store: the changes go on the connector's SCIM run of
// the hour, made when missing; its counts grow by the changes.
func (p *PG) RecordPush(ctx context.Context, connectorID int64, cs []Change, at time.Time) error {
	if len(cs) == 0 {
		return nil
	}
	return p.tx(ctx, func(tx *momo.Tx) error {
		var (
			runID  int64
			counts string
		)
		conn, when := momo.Named("connector_id", connectorID), momo.Named("at", at)
		err := queryRow(ctx, tx, `
			SELECT id, counts::text FROM directory_sync_runs
			WHERE connector_id = @connector_id AND kind = 'scim' AND status = 'succeeded' AND created_at >= date_trunc('hour', @at::timestamptz)
			ORDER BY id DESC LIMIT 1 FOR UPDATE`, conn, when).Scan(&runID, &counts)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			counts = "{}"
			if err := queryRow(ctx, tx, `
				INSERT INTO directory_sync_runs (connector_id, kind, status, requested_by, created_at, started_at, finished_at, counts)
				VALUES (@connector_id, 'scim', 'succeeded', 'scim', @at, @at, @at, '{}') RETURNING id`, conn, when).Scan(&runID); err != nil {
				return err
			}
		case err != nil:
			return err
		}
		n := map[string]int{}
		_ = json.Unmarshal([]byte(counts), &n)
		for _, c := range cs {
			if err := addChange(ctx, tx, runID, c); err != nil {
				return err
			}
			n[c.Action]++
		}
		_, err = momo.Exec(ctx, tx, `UPDATE directory_sync_runs SET counts = @counts::jsonb, finished_at = @at WHERE id = @id`,
			momo.Named("id", runID), momo.Named("counts", jsonOf(n)), when)
		return err
	})
}

// PushDisables implements Store.
func (p *PG) PushDisables(ctx context.Context, connectorID int64, since time.Time) (int, error) {
	var n int
	err := queryRow(ctx, p.DB, `
		SELECT count(*) FROM directory_sync_changes ch JOIN directory_sync_runs r ON r.id = ch.run_id
		WHERE r.connector_id = @connector_id AND r.kind = 'scim' AND r.status = 'succeeded' AND ch.action = 'disable' AND ch.created_at >= @since`,
		momo.Named("connector_id", connectorID), momo.Named("since", since)).Scan(&n)
	return n, err
}

// SetPaused implements Store.
func (p *PG) SetPaused(ctx context.Context, connectorID int64, paused bool, at time.Time) error {
	q := `UPDATE directory_connectors SET paused_at = NULL, guard_since = @at, updated_at = now() WHERE id = @id`
	if paused {
		q = `UPDATE directory_connectors SET paused_at = @at, updated_at = now() WHERE id = @id`
	}
	n, err := momo.Exec(ctx, p.DB, q, momo.Named("id", connectorID), momo.Named("at", at))
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
