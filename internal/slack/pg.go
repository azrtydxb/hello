package slack

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/events"
)

// PG is the PostgreSQL store.
type PG struct{ DB *sql.DB }

const destCols = `d.id, d.name, d.mode, d.channel, to_json(d.events)::text, to_json(d.extensions)::text, d.enabled, d.disabled_reason,
	d.consecutive_failures, d.last_success_at, d.last_failure_at, d.last_error, d.created_by, d.created_at, d.updated_at, d.secret_enc,
	(SELECT count(*) FROM slack_posts p WHERE p.destination_id = d.id AND p.status IN ('pending', 'retrying')),
	(SELECT count(*) FROM slack_posts p WHERE p.destination_id = d.id AND p.status = 'dead')`

type rowScanner interface{ Scan(dest ...any) error }

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

func audit(ctx context.Context, tx *sql.Tx, actor, action, resource, id string) error {
	var via any
	if a, ok := auth.ActorFrom(ctx); ok {
		switch {
		case a.ClientID != "":
			via = a.ClientID
		case a.Via != "":
			via = a.Via
		}
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO audit_events (actor, action, resource, resource_id, via) VALUES ($1, $2, $3, $4, $5)`,
		actor, action, resource, id, via)
	return err
}

func (p *PG) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return mapErr(err)
	}
	return tx.Commit()
}

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// NextID implements Store.
func (p *PG) NextID(ctx context.Context) (int64, error) {
	var id int64
	err := p.DB.QueryRowContext(ctx, `SELECT nextval(pg_get_serial_sequence('slack_destinations', 'id'))`).Scan(&id)
	return id, err
}

// Create implements Store.
func (p *PG) Create(ctx context.Context, actor string, d Destination) (Destination, error) {
	var out Destination
	err := p.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO slack_destinations (id, name, mode, secret_enc, channel, events, extensions, enabled, created_by)
			VALUES ($1, $2, $3, $4, $5, $6::text[], $7::text[], $8, $9)`,
			d.ID, d.Name, d.Mode, d.SecretEnc, d.Channel, pgArray(d.Events), pgArray(d.Extensions), d.Enabled, actor); err != nil {
			if isUnique(err) {
				return ErrConflict
			}
			return err
		}
		if err := audit(ctx, tx, actor, "create", "slack_destination", strconv.FormatInt(d.ID, 10)); err != nil {
			return err
		}
		var err error
		out, err = scanDest(tx.QueryRowContext(ctx, `SELECT `+destCols+` FROM slack_destinations d WHERE d.id = $1`, d.ID))
		return err
	})
	return out, err
}

// Get implements Store.
func (p *PG) Get(ctx context.Context, id int64) (Destination, error) {
	d, err := scanDest(p.DB.QueryRowContext(ctx, `SELECT `+destCols+` FROM slack_destinations d WHERE d.id = $1`, id))
	return d, mapErr(err)
}

// List implements Store.
func (p *PG) List(ctx context.Context) ([]Destination, error) {
	rows, err := p.DB.QueryContext(ctx, `SELECT `+destCols+` FROM slack_destinations d ORDER BY d.name`)
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
	err := p.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE slack_destinations SET name = $2, channel = $3, events = $4::text[], extensions = $5::text[],
				enabled = $6, disabled_reason = $7, secret_enc = COALESCE($8, secret_enc),
				consecutive_failures = CASE WHEN $9 THEN 0 ELSE consecutive_failures END,
				last_error = CASE WHEN $9 THEN '' ELSE last_error END, updated_at = now()
			WHERE id = $1`,
			d.ID, d.Name, d.Channel, pgArray(d.Events), pgArray(d.Extensions), d.Enabled, d.DisabledReason, nullBytes(d.SecretEnc), resetHealth)
		if err != nil {
			if isUnique(err) {
				return ErrConflict
			}
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if err := audit(ctx, tx, actor, "update", "slack_destination", strconv.FormatInt(d.ID, 10)); err != nil {
			return err
		}
		out, err = scanDest(tx.QueryRowContext(ctx, `SELECT `+destCols+` FROM slack_destinations d WHERE d.id = $1`, d.ID))
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
	return p.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM slack_destinations WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return audit(ctx, tx, actor, "delete", "slack_destination", strconv.FormatInt(id, 10))
	})
}

// Enqueue implements Store.
func (p *PG) Enqueue(ctx context.Context, tx *sql.Tx, e events.Entry) error {
	switch e.Type {
	case events.CallMissed, events.VoicemailReceived:
	default:
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, to_json(events)::text, to_json(extensions)::text FROM slack_destinations WHERE enabled`)
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
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO slack_posts (destination_id, event_id, event_type, status, next_attempt_at, payload)
			VALUES ($1, $2, $3, 'pending', now(), $4::jsonb)
			ON CONFLICT (destination_id, event_id) DO NOTHING`, id, e.ID, e.Type, string(payload)); err != nil {
			return err
		}
	}
	return nil
}

// Claim implements Store.
func (p *PG) Claim(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]Claim, error) {
	var out []Claim
	err := p.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			UPDATE slack_posts SET claimed_until = $2
			WHERE id IN (
				SELECT sp.id FROM slack_posts sp JOIN slack_destinations d ON d.id = sp.destination_id
				WHERE d.enabled AND sp.status IN ('pending', 'retrying') AND sp.next_attempt_at <= $1
					AND (sp.claimed_until IS NULL OR sp.claimed_until <= $1)
				ORDER BY sp.next_attempt_at, sp.id LIMIT $3 FOR UPDATE OF sp SKIP LOCKED)
			RETURNING id`, now, now.Add(lease), limit)
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
		rows, err = tx.QueryContext(ctx, `
			SELECT sp.id, sp.destination_id, sp.event_id, sp.event_type, sp.status, sp.attempts, sp.series_attempt, sp.payload::text,
				d.mode, d.channel, d.secret_enc
			FROM slack_posts sp JOIN slack_destinations d ON d.id = sp.destination_id
			WHERE sp.id = ANY($1::bigint[]) ORDER BY sp.next_attempt_at, sp.id`, idArray(ids))
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
	return p.tx(ctx, func(tx *sql.Tx) error {
		status, next := StatusSucceeded, f.Now
		switch f.Outcome {
		case OutcomeRetry:
			status, next = StatusRetrying, f.NextAttemptAt
		case OutcomeDead, OutcomeDisable:
			status = StatusDead
		}
		var destID int64
		if err := tx.QueryRowContext(ctx, `
			UPDATE slack_posts SET status = $2::text, attempts = attempts + 1, series_attempt = series_attempt + 1,
				next_attempt_at = $3, claimed_until = NULL, last_error = $4,
				delivered_at = CASE WHEN $2::text = 'succeeded' THEN $5::timestamptz ELSE delivered_at END, updated_at = $5
			WHERE id = $1 RETURNING destination_id`, f.PostID, status, next, f.Error, f.Now).Scan(&destID); err != nil {
			return err
		}
		if f.Outcome == OutcomeSuccess {
			_, err := tx.ExecContext(ctx, `UPDATE slack_destinations SET consecutive_failures = 0, last_success_at = $2, last_error = '' WHERE id = $1`, destID, f.Now)
			return err
		}
		var failures int
		var enabled bool
		if err := tx.QueryRowContext(ctx, `
			UPDATE slack_destinations SET consecutive_failures = consecutive_failures + 1, last_failure_at = $2, last_error = $3
			WHERE id = $1 RETURNING consecutive_failures, enabled`, destID, f.Now, f.Error).Scan(&failures, &enabled); err != nil {
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
		if _, err := tx.ExecContext(ctx, `UPDATE slack_destinations SET enabled = false, disabled_reason = $2, updated_at = now() WHERE id = $1`, destID, reason); err != nil {
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
	rows, err := p.DB.QueryContext(ctx, `
		SELECT `+postCols+` FROM slack_posts
		WHERE ($1 = 0 OR destination_id = $1) AND ($2 = '' OR status = $2) AND ($3 = 0 OR id < $3)
		ORDER BY id DESC LIMIT $4`, q.DestinationID, q.Status, q.Before, q.Limit)
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
	err := p.tx(ctx, func(tx *sql.Tx) error {
		x, err := scanPost(tx.QueryRowContext(ctx, `
			UPDATE slack_posts SET status = 'pending', series_attempt = 0, next_attempt_at = $2, claimed_until = NULL, updated_at = $2
			WHERE id = $1 AND status = 'dead' RETURNING `+postCols, id, now))
		if errors.Is(err, sql.ErrNoRows) {
			var exists bool
			if e := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM slack_posts WHERE id = $1)`, id).Scan(&exists); e == nil && exists {
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
		res, err := p.DB.ExecContext(ctx, `DELETE FROM slack_posts WHERE status = $1 AND updated_at < $2`, q.st, q.t)
		if err != nil {
			return n, err
		}
		c, _ := res.RowsAffected()
		n += c
	}
	return n, nil
}

var _ Store = (*PG)(nil)
