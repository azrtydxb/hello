package slack

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/azrtydxb/hello/internal/auth"
	hellodb "github.com/azrtydxb/hello/internal/db"
	"github.com/azrtydxb/hello/internal/events"
)

// PG is the PostgreSQL store, on momo (internal/db).
//
// The statements are the hand-written SQL they were on database/sql, with
// @name binds instead of $n. Rows are read through momo.RawRows with the
// same Scan destinations as before, and a single-row read goes through
// queryRow, which reports sql.ErrNoRows on an empty result as QueryRow did,
// so callers get the same errors: ErrNotFound, ErrConflict, and the driver's
// error (a *pgconn.PgError is still reachable with errors.As) for anything
// else.
type PG struct{ DB *momo.DB }

const destCols = `d.id, d.name, d.mode, d.channel, to_json(d.events)::text, to_json(d.extensions)::text, d.enabled, d.disabled_reason,
	d.consecutive_failures, d.last_success_at, d.last_failure_at, d.last_error, d.created_by, d.created_at, d.updated_at, d.secret_enc,
	(SELECT count(*) FROM slack_posts p WHERE p.destination_id = d.id AND p.status IN ('pending', 'retrying')),
	(SELECT count(*) FROM slack_posts p WHERE p.destination_id = d.id AND p.status = 'dead')`

type rowScanner interface{ Scan(dest ...any) error }

// scanFunc is a rowScanner.
type scanFunc func(dest ...any) error

// Scan implements rowScanner.
func (f scanFunc) Scan(dest ...any) error { return f(dest...) }

// queryRow is QueryRow on momo: Scan runs the statement, scans its first row
// and closes the rows, and reports sql.ErrNoRows when there is none. momo
// has no single-row raw read (plan gap G7).
func queryRow(ctx context.Context, q momo.Querier, query string, args ...any) rowScanner {
	return scanFunc(func(dest ...any) error {
		rows, err := momo.RawRows(ctx, q, query, args...)
		if err != nil {
			return err
		}
		if !rows.Next() {
			err := rows.Err()
			_ = rows.Close()
			if err != nil {
				return err
			}
			return sql.ErrNoRows
		}
		if err := rows.Scan(dest...); err != nil {
			_ = rows.Close()
			return err
		}
		return rows.Close()
	})
}

func scanDest(r rowScanner) (Destination, error) {
	var (
		d           Destination
		evs, exts   string
		lastOK, bad sql.NullTime
	)
	if err := r.Scan(&d.ID, &d.Name, &d.Mode, &d.Channel, &evs, &exts, &d.Enabled, &d.DisabledReason, &d.ConsecutiveFailures,
		&lastOK, &bad, &d.LastError, &d.CreatedBy, &d.CreatedAt, &d.UpdatedAt, &d.SecretEnc, &d.Pending, &d.Dead); err != nil {
		return Destination{}, err
	}
	_ = json.Unmarshal([]byte(evs), &d.Events)
	_ = json.Unmarshal([]byte(exts), &d.Extensions)
	if d.Events == nil {
		d.Events = []string{}
	}
	if d.Extensions == nil {
		d.Extensions = []string{}
	}
	d.LastSuccessAt, d.LastFailureAt = timePtr(lastOK), timePtr(bad)
	d.HasSecret = len(d.SecretEnc) > 0
	return d, nil
}

func timePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

func pgArray(v []string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, s := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

func idArray(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func mapErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func audit(ctx context.Context, tx momo.Querier, actor, action, resource, id string) error {
	var via any
	if a, ok := auth.ActorFrom(ctx); ok {
		switch {
		case a.ClientID != "":
			via = a.ClientID
		case a.Via != "":
			via = a.Via
		}
	}
	_, err := momo.Exec(ctx, tx,
		`INSERT INTO audit_events (actor, action, resource, resource_id, via) VALUES (@actor, @action, @resource, @resource_id, @via)`,
		momo.Named("actor", actor), momo.Named("action", action), momo.Named("resource", resource), momo.Named("resource_id", id),
		momo.Named("via", via))
	return err
}

// tx runs fn in a transaction: commit on nil, else roll back (ignoring the
// rollback's error, as before) and return fn's error through mapErr. The
// rollback gets a context without ctx's cancellation so that it still
// reaches the database when ctx ended the work.
func (p *PG) tx(ctx context.Context, fn func(momo.Querier) error) error {
	tx, err := p.DB.Begin(ctx, momo.TxOptions{})
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		return mapErr(err)
	}
	return tx.Commit(ctx)
}

// isUnique reports a unique violation. momo classifies it as
// momo.ErrUniqueViolation; the *pgconn.PgError check is the one this store
// always made, and still holds through momo's error.
func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.Is(err, momo.ErrUniqueViolation) || errors.As(err, &pe) && pe.Code == "23505"
}

// NextID implements Store.
func (p *PG) NextID(ctx context.Context) (int64, error) {
	var id int64
	err := queryRow(ctx, p.DB, `SELECT nextval(pg_get_serial_sequence('slack_destinations', 'id'))`).Scan(&id)
	return id, err
}

// Create implements Store.
func (p *PG) Create(ctx context.Context, actor string, d Destination) (Destination, error) {
	var out Destination
	err := p.tx(ctx, func(tx momo.Querier) error {
		if _, err := momo.Exec(ctx, tx, `
			INSERT INTO slack_destinations (id, name, mode, secret_enc, channel, events, extensions, enabled, created_by)
			VALUES (@id, @name, @mode, @secret_enc, @channel, @events::text[], @extensions::text[], @enabled, @created_by)`,
			momo.Named("id", d.ID), momo.Named("name", d.Name), momo.Named("mode", d.Mode), momo.Named("secret_enc", d.SecretEnc),
			momo.Named("channel", d.Channel), momo.Named("events", pgArray(d.Events)), momo.Named("extensions", pgArray(d.Extensions)),
			momo.Named("enabled", d.Enabled), momo.Named("created_by", actor)); err != nil {
			if isUnique(err) {
				return ErrConflict
			}
			return err
		}
		if err := audit(ctx, tx, actor, "create", "slack_destination", strconv.FormatInt(d.ID, 10)); err != nil {
			return err
		}
		var err error
		out, err = scanDest(queryRow(ctx, tx, `SELECT `+destCols+` FROM slack_destinations d WHERE d.id = @id`, momo.Named("id", d.ID)))
		return err
	})
	return out, err
}

// Get implements Store.
func (p *PG) Get(ctx context.Context, id int64) (Destination, error) {
	d, err := scanDest(queryRow(ctx, p.DB, `SELECT `+destCols+` FROM slack_destinations d WHERE d.id = @id`, momo.Named("id", id)))
	return d, mapErr(err)
}

// List implements Store.
func (p *PG) List(ctx context.Context) ([]Destination, error) {
	rows, err := momo.RawRows(ctx, p.DB, `SELECT `+destCols+` FROM slack_destinations d ORDER BY d.name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Destination{}
	for rows.Next() {
		d, err := scanDest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Update implements Store.
func (p *PG) Update(ctx context.Context, actor string, d Destination, resetHealth bool) (Destination, error) {
	var out Destination
	err := p.tx(ctx, func(tx momo.Querier) error {
		n, err := momo.Exec(ctx, tx, `
			UPDATE slack_destinations SET name = @name, channel = @channel, events = @events::text[], extensions = @extensions::text[],
				enabled = @enabled, disabled_reason = @disabled_reason, secret_enc = COALESCE(@secret_enc::bytea, secret_enc),
				consecutive_failures = CASE WHEN @reset::boolean THEN 0 ELSE consecutive_failures END,
				last_error = CASE WHEN @reset::boolean THEN '' ELSE last_error END, updated_at = now()
			WHERE id = @id`,
			momo.Named("id", d.ID), momo.Named("name", d.Name), momo.Named("channel", d.Channel), momo.Named("events", pgArray(d.Events)),
			momo.Named("extensions", pgArray(d.Extensions)), momo.Named("enabled", d.Enabled), momo.Named("disabled_reason", d.DisabledReason),
			momo.Named("secret_enc", nullBytes(d.SecretEnc)), momo.Named("reset", resetHealth))
		if err != nil {
			if isUnique(err) {
				return ErrConflict
			}
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		if err := audit(ctx, tx, actor, "update", "slack_destination", strconv.FormatInt(d.ID, 10)); err != nil {
			return err
		}
		out, err = scanDest(queryRow(ctx, tx, `SELECT `+destCols+` FROM slack_destinations d WHERE d.id = @id`, momo.Named("id", d.ID)))
		return err
	})
	return out, err
}

func nullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// Delete implements Store.
func (p *PG) Delete(ctx context.Context, actor string, id int64) error {
	return p.tx(ctx, func(tx momo.Querier) error {
		n, err := momo.Exec(ctx, tx, `DELETE FROM slack_destinations WHERE id = @id`, momo.Named("id", id))
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return audit(ctx, tx, actor, "delete", "slack_destination", strconv.FormatInt(id, 10))
	})
}

// Enqueue implements Store. Its statements run in tx, the event ingest
// transaction, through db.AdoptTx: the caller still commits or rolls back.
func (p *PG) Enqueue(ctx context.Context, tx *sql.Tx, e events.Entry) error {
	switch e.Type {
	case events.CallMissed, events.VoicemailReceived:
	default:
		return nil
	}
	q := hellodb.AdoptTx(p.DB, tx)
	rows, err := momo.RawRows(ctx, q, `SELECT id, to_json(events)::text, to_json(extensions)::text FROM slack_destinations WHERE enabled`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var (
			id        int64
			evs, exts string
			f         events.Filter
		)
		if err := rows.Scan(&id, &evs, &exts); err != nil {
			_ = rows.Close()
			return err
		}
		_ = json.Unmarshal([]byte(evs), &f.Types)
		_ = json.Unmarshal([]byte(exts), &f.Allowed)
		if len(f.Allowed) == 0 {
			f.Allowed = nil
		}
		if f.Matches(e.Event) {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	if len(ids) == 0 {
		return nil
	}
	payload, err := json.Marshal(e)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := momo.Exec(ctx, q, `
			INSERT INTO slack_posts (destination_id, event_id, event_type, status, next_attempt_at, payload)
			VALUES (@destination_id, @event_id, @event_type, 'pending', now(), @payload::jsonb)
			ON CONFLICT (destination_id, event_id) DO NOTHING`,
			momo.Named("destination_id", id), momo.Named("event_id", e.ID), momo.Named("event_type", e.Type),
			momo.Named("payload", string(payload))); err != nil {
			return err
		}
	}
	return nil
}

// Claim implements Store.
func (p *PG) Claim(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]Claim, error) {
	var out []Claim
	err := p.tx(ctx, func(tx momo.Querier) error {
		rows, err := momo.RawRows(ctx, tx, `
			UPDATE slack_posts SET claimed_until = @until
			WHERE id IN (
				SELECT sp.id FROM slack_posts sp JOIN slack_destinations d ON d.id = sp.destination_id
				WHERE d.enabled AND sp.status IN ('pending', 'retrying') AND sp.next_attempt_at <= @now
					AND (sp.claimed_until IS NULL OR sp.claimed_until <= @now)
				ORDER BY sp.next_attempt_at, sp.id LIMIT @limit FOR UPDATE OF sp SKIP LOCKED)
			RETURNING id`, momo.Named("now", now), momo.Named("until", now.Add(lease)), momo.Named("limit", limit))
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()
		if len(ids) == 0 {
			return nil
		}
		rows, err = momo.RawRows(ctx, tx, `
			SELECT sp.id, sp.destination_id, sp.event_id, sp.event_type, sp.status, sp.attempts, sp.series_attempt, sp.payload::text,
				d.mode, d.channel, d.secret_enc
			FROM slack_posts sp JOIN slack_destinations d ON d.id = sp.destination_id
			WHERE sp.id = ANY(@ids::bigint[]) ORDER BY sp.next_attempt_at, sp.id`, momo.Named("ids", idArray(ids)))
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var c Claim
			var payload string
			if err := rows.Scan(&c.ID, &c.DestinationID, &c.EventID, &c.EventType, &c.Status, &c.Attempts, &c.SeriesAttempt, &payload,
				&c.Mode, &c.Channel, &c.SecretEnc); err != nil {
				return err
			}
			c.Payload = []byte(payload)
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// Finish implements Store.
func (p *PG) Finish(ctx context.Context, f Finish) error {
	return p.tx(ctx, func(tx momo.Querier) error {
		status, next := StatusSucceeded, f.Now
		switch f.Outcome {
		case OutcomeRetry:
			status, next = StatusRetrying, f.NextAttemptAt
		case OutcomeDead, OutcomeDisable:
			status = StatusDead
		}
		var destID int64
		if err := queryRow(ctx, tx, `
			UPDATE slack_posts SET status = @status::text, attempts = attempts + 1, series_attempt = series_attempt + 1,
				next_attempt_at = @next, claimed_until = NULL, last_error = @error,
				delivered_at = CASE WHEN @status::text = 'succeeded' THEN @now::timestamptz ELSE delivered_at END, updated_at = @now::timestamptz
			WHERE id = @id RETURNING destination_id`,
			momo.Named("id", f.PostID), momo.Named("status", status), momo.Named("next", next), momo.Named("error", f.Error),
			momo.Named("now", f.Now)).Scan(&destID); err != nil {
			return err
		}
		if f.Outcome == OutcomeSuccess {
			_, err := momo.Exec(ctx, tx, `UPDATE slack_destinations SET consecutive_failures = 0, last_success_at = @now, last_error = '' WHERE id = @id`,
				momo.Named("id", destID), momo.Named("now", f.Now))
			return err
		}
		var failures int
		var enabled bool
		if err := queryRow(ctx, tx, `
			UPDATE slack_destinations SET consecutive_failures = consecutive_failures + 1, last_failure_at = @now, last_error = @error
			WHERE id = @id RETURNING consecutive_failures, enabled`,
			momo.Named("id", destID), momo.Named("now", f.Now), momo.Named("error", f.Error)).Scan(&failures, &enabled); err != nil {
			return err
		}
		reason := f.Reason
		if f.Outcome != OutcomeDisable {
			reason = ""
			if f.MaxFailures > 0 && failures >= f.MaxFailures {
				reason = ReasonFailures
			}
		}
		if reason == "" || !enabled {
			return nil
		}
		if _, err := momo.Exec(ctx, tx, `UPDATE slack_destinations SET enabled = false, disabled_reason = @reason, updated_at = now() WHERE id = @id`,
			momo.Named("id", destID), momo.Named("reason", reason)); err != nil {
			return err
		}
		return audit(ctx, tx, "system:slack", "disable", "slack_destination", strconv.FormatInt(destID, 10))
	})
}

const postCols = `id, destination_id, event_id, event_type, status, attempts, next_attempt_at, last_error, delivered_at, created_at, updated_at`

func scanPost(r rowScanner) (Post, error) {
	var (
		x         Post
		next, del sql.NullTime
	)
	if err := r.Scan(&x.ID, &x.DestinationID, &x.EventID, &x.EventType, &x.Status, &x.Attempts, &next, &x.LastError, &del, &x.CreatedAt, &x.UpdatedAt); err != nil {
		return Post{}, err
	}
	if x.Status == StatusPending || x.Status == StatusRetrying {
		x.NextAttemptAt = timePtr(next)
	}
	x.DeliveredAt = timePtr(del)
	return x, nil
}

// ListPosts implements Store.
func (p *PG) ListPosts(ctx context.Context, q PostQuery) ([]Post, error) {
	rows, err := momo.RawRows(ctx, p.DB, `
		SELECT `+postCols+` FROM slack_posts
		WHERE (@dest::bigint = 0 OR destination_id = @dest::bigint) AND (@status::text = '' OR status = @status::text)
			AND (@before::bigint = 0 OR id < @before::bigint)
		ORDER BY id DESC LIMIT @limit`,
		momo.Named("dest", q.DestinationID), momo.Named("status", q.Status), momo.Named("before", q.Before), momo.Named("limit", q.Limit))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Post{}
	for rows.Next() {
		x, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Retry implements Store.
func (p *PG) Retry(ctx context.Context, actor string, id int64, now time.Time) (Post, error) {
	var out Post
	err := p.tx(ctx, func(tx momo.Querier) error {
		x, err := scanPost(queryRow(ctx, tx, `
			UPDATE slack_posts SET status = 'pending', series_attempt = 0, next_attempt_at = @now, claimed_until = NULL, updated_at = @now
			WHERE id = @id AND status = 'dead' RETURNING `+postCols, momo.Named("id", id), momo.Named("now", now)))
		if errors.Is(err, sql.ErrNoRows) {
			var exists bool
			if e := queryRow(ctx, tx, `SELECT EXISTS (SELECT 1 FROM slack_posts WHERE id = @id)`, momo.Named("id", id)).Scan(&exists); e == nil && exists {
				return ErrConflict
			}
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = x
		return audit(ctx, tx, actor, "retry", "slack_post", strconv.FormatInt(id, 10))
	})
	return out, err
}

// Purge implements Store.
func (p *PG) Purge(ctx context.Context, succeededBefore, deadBefore time.Time) (int64, error) {
	var n int64
	for _, q := range []struct {
		st string
		t  time.Time
	}{{StatusSucceeded, succeededBefore}, {StatusDead, deadBefore}} {
		c, err := momo.Exec(ctx, p.DB, `DELETE FROM slack_posts WHERE status = @status AND updated_at < @before`,
			momo.Named("status", q.st), momo.Named("before", q.t))
		if err != nil {
			return n, err
		}
		n += c
	}
	return n, nil
}

var _ Store = (*PG)(nil)
