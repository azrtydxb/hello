package webhook

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/events"
)

// PG is the PostgreSQL store.
type PG struct{ DB *sql.DB }

// claimLock serialises claims across control replicas for the few
// milliseconds a claim takes, so the per-endpoint concurrency is exact.
const claimLock = 0x68656c6c6f776864 // "hellowhd"

const endpointCols = `e.id, e.name, e.url, to_json(e.events)::text, to_json(e.extensions)::text, e.enabled, e.allow_private,
	e.disabled_reason, e.consecutive_failures, e.previous_secret_expires, e.last_success_at, e.last_failure_at, e.last_status,
	e.created_by, e.created_at, e.updated_at, e.secret_enc, e.previous_secret_enc,
	(SELECT count(*) FROM webhook_deliveries d WHERE d.endpoint_id = e.id AND d.status IN ('pending', 'retrying')),
	(SELECT count(*) FROM webhook_deliveries d WHERE d.endpoint_id = e.id AND d.status = 'dead')`

type rowScanner interface{ Scan(dest ...any) error }

func scanEndpoint(r rowScanner) (Endpoint, error) {
	var (
		e                        Endpoint
		evs, exts                string
		prevExp, lastOK, lastBad sql.NullTime
		lastStatus               sql.NullInt64
	)
	if err := r.Scan(&e.ID, &e.Name, &e.URL, &evs, &exts, &e.Enabled, &e.AllowPrivate, &e.DisabledReason, &e.ConsecutiveFailures,
		&prevExp, &lastOK, &lastBad, &lastStatus, &e.CreatedBy, &e.CreatedAt, &e.UpdatedAt, &e.SecretEnc, &e.PreviousSecretEnc,
		&e.Pending, &e.Dead); err != nil {
		return Endpoint{}, err
	}
	_ = json.Unmarshal([]byte(evs), &e.Events)
	_ = json.Unmarshal([]byte(exts), &e.Extensions)
	e.Events, e.Extensions = nonNil(e.Events), nonNil(e.Extensions)
	e.PreviousSecretExpires = timePtr(prevExp)
	e.LastSuccessAt, e.LastFailureAt = timePtr(lastOK), timePtr(lastBad)
	if lastStatus.Valid {
		v := int(lastStatus.Int64)
		e.LastStatus = &v
	}
	return e, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func timePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

func mapErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// pgArray is a PostgreSQL text[] literal.
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

// idArray is a PostgreSQL bigint[] literal.
func idArray(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return "{" + strings.Join(parts, ",") + "}"
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

// NextEndpointID implements Store.
func (p *PG) NextEndpointID(ctx context.Context) (int64, error) {
	var id int64
	err := p.DB.QueryRowContext(ctx, `SELECT nextval(pg_get_serial_sequence('webhook_endpoints', 'id'))`).Scan(&id)
	return id, err
}

// CreateEndpoint implements Store.
func (p *PG) CreateEndpoint(ctx context.Context, actor string, e Endpoint) (Endpoint, bool, error) {
	var (
		out     Endpoint
		created bool
	)
	err := p.tx(ctx, func(tx *sql.Tx) error {
		// One create at a time, so two identical Zapier subscriptions
		// cannot both pass the existence check.
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('hello:webhooks:create'))`); err != nil {
			return err
		}
		row := tx.QueryRowContext(ctx, `SELECT `+endpointCols+` FROM webhook_endpoints e WHERE e.url = $1 AND e.events = $2::text[] ORDER BY e.id LIMIT 1`,
			e.URL, pgArray(e.Events))
		if got, err := scanEndpoint(row); err == nil {
			out = got
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO webhook_endpoints (id, name, url, secret_enc, events, extensions, enabled, allow_private, created_by)
			VALUES ($1, $2, $3, $4, $5::text[], $6::text[], $7, $8, $9)`,
			e.ID, e.Name, e.URL, e.SecretEnc, pgArray(e.Events), pgArray(e.Extensions), e.Enabled, e.AllowPrivate, actor); err != nil {
			return err
		}
		if err := audit(ctx, tx, actor, "create", "webhook_endpoint", strconv.FormatInt(e.ID, 10)); err != nil {
			return err
		}
		got, err := scanEndpoint(tx.QueryRowContext(ctx, `SELECT `+endpointCols+` FROM webhook_endpoints e WHERE e.id = $1`, e.ID))
		out, created = got, true
		return err
	})
	return out, created, err
}

// GetEndpoint implements Store.
func (p *PG) GetEndpoint(ctx context.Context, id int64) (Endpoint, error) {
	e, err := scanEndpoint(p.DB.QueryRowContext(ctx, `SELECT `+endpointCols+` FROM webhook_endpoints e WHERE e.id = $1`, id))
	return e, mapErr(err)
}

// ListEndpoints implements Store.
func (p *PG) ListEndpoints(ctx context.Context) ([]Endpoint, error) {
	rows, err := p.DB.QueryContext(ctx, `SELECT `+endpointCols+` FROM webhook_endpoints e ORDER BY e.id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Endpoint{}
	for rows.Next() {
		e, err := scanEndpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpdateEndpoint implements Store.
func (p *PG) UpdateEndpoint(ctx context.Context, actor string, e Endpoint, resetHealth bool) (Endpoint, error) {
	var out Endpoint
	err := p.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE webhook_endpoints SET name = $2, url = $3, events = $4::text[], extensions = $5::text[], enabled = $6,
				allow_private = $7, disabled_reason = $8,
				consecutive_failures = CASE WHEN $9::boolean THEN 0 ELSE consecutive_failures END, updated_at = now()
			WHERE id = $1`,
			e.ID, e.Name, e.URL, pgArray(e.Events), pgArray(e.Extensions), e.Enabled, e.AllowPrivate, e.DisabledReason, resetHealth)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return sql.ErrNoRows
		}
		if err := audit(ctx, tx, actor, "update", "webhook_endpoint", strconv.FormatInt(e.ID, 10)); err != nil {
			return err
		}
		out, err = scanEndpoint(tx.QueryRowContext(ctx, `SELECT `+endpointCols+` FROM webhook_endpoints e WHERE e.id = $1`, e.ID))
		return err
	})
	return out, err
}

// SetSecrets implements Store.
func (p *PG) SetSecrets(ctx context.Context, actor string, id int64, current, previous []byte, previousExpires *time.Time) (Endpoint, error) {
	var out Endpoint
	err := p.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE webhook_endpoints SET secret_enc = $2, previous_secret_enc = $3, previous_secret_expires = $4, updated_at = now()
			WHERE id = $1`, id, current, previous, previousExpires)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return sql.ErrNoRows
		}
		if err := audit(ctx, tx, actor, "rotate-secret", "webhook_endpoint", strconv.FormatInt(id, 10)); err != nil {
			return err
		}
		out, err = scanEndpoint(tx.QueryRowContext(ctx, `SELECT `+endpointCols+` FROM webhook_endpoints e WHERE e.id = $1`, id))
		return err
	})
	return out, err
}

// DeleteEndpoint implements Store.
func (p *PG) DeleteEndpoint(ctx context.Context, actor string, id int64) error {
	return p.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM webhook_endpoints WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return sql.ErrNoRows
		}
		return audit(ctx, tx, actor, "delete", "webhook_endpoint", strconv.FormatInt(id, 10))
	})
}

// Enqueue implements Store: it runs in the event ingest transaction.
func (p *PG) Enqueue(ctx context.Context, tx *sql.Tx, e events.Entry) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, to_json(events)::text, to_json(extensions)::text FROM webhook_endpoints WHERE enabled`)
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
			f.Allowed = nil // an empty filter is every extension
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
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO webhook_deliveries (endpoint_id, event_id, event_type, status, next_attempt_at)
			VALUES ($1, $2, $3, 'pending', now())
			ON CONFLICT (endpoint_id, event_id) DO NOTHING`, id, e.ID, e.Type); err != nil {
			return err
		}
	}
	return nil
}

// Claim implements Store.
func (p *PG) Claim(ctx context.Context, now time.Time, limit, perEndpoint int, lease time.Duration) ([]Claim, error) {
	var out []Claim
	err := p.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(claimLock)); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `
			WITH busy AS (
				SELECT endpoint_id, count(*) AS n FROM webhook_deliveries WHERE claimed_until > $1 GROUP BY endpoint_id
			), due AS (
				SELECT d.id, d.endpoint_id, d.next_attempt_at,
					row_number() OVER (PARTITION BY d.endpoint_id ORDER BY d.next_attempt_at, d.id) AS rn
				FROM webhook_deliveries d JOIN webhook_endpoints e ON e.id = d.endpoint_id
				WHERE e.enabled AND d.status IN ('pending', 'retrying') AND d.next_attempt_at <= $1
					AND (d.claimed_until IS NULL OR d.claimed_until <= $1)
			)
			SELECT due.id FROM due LEFT JOIN busy ON busy.endpoint_id = due.endpoint_id
			WHERE due.rn <= $2::bigint - COALESCE(busy.n, 0)
			ORDER BY due.next_attempt_at, due.id LIMIT $3`, now, perEndpoint, limit)
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
		// Lease the rows another transaction is not changing right now.
		rows, err = tx.QueryContext(ctx, `
			UPDATE webhook_deliveries SET claimed_until = $2
			WHERE id IN (SELECT id FROM webhook_deliveries WHERE id = ANY($1::bigint[]) AND status IN ('pending', 'retrying') FOR UPDATE SKIP LOCKED)
			RETURNING id`, idArray(ids), now.Add(lease))
		if err != nil {
			return err
		}
		ids = ids[:0]
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
			SELECT d.id, d.endpoint_id, d.event_id, d.event_type, d.status, d.attempt, d.series_attempt,
				e.url, e.allow_private, e.secret_enc, e.previous_secret_enc, e.previous_secret_expires, ev.payload::text
			FROM webhook_deliveries d
			JOIN webhook_endpoints e ON e.id = d.endpoint_id
			LEFT JOIN integration_events ev ON ev.id = d.event_id
			WHERE d.id = ANY($1::bigint[]) ORDER BY d.next_attempt_at, d.id`, idArray(ids))
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var (
				c       Claim
				prevExp sql.NullTime
				payload sql.NullString
			)
			if err := rows.Scan(&c.ID, &c.EndpointID, &c.EventID, &c.EventType, &c.Status, &c.Attempts, &c.SeriesAttempt,
				&c.URL, &c.AllowPrivate, &c.SecretEnc, &c.PreviousSecretEnc, &prevExp, &payload); err != nil {
				return err
			}
			c.PreviousSecretExpires = timePtr(prevExp)
			if payload.Valid {
				c.Payload = []byte(payload.String)
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// Finish implements Store.
func (p *PG) Finish(ctx context.Context, f Finish) (FinishResult, error) {
	var res FinishResult
	err := p.tx(ctx, func(tx *sql.Tx) error {
		status, next := StatusSucceeded, f.Now
		switch f.Outcome {
		case OutcomeRetry:
			status, next = StatusRetrying, f.NextAttemptAt
		case OutcomeDead, OutcomeGone:
			status = StatusDead
		}
		var (
			endpointID int64
			attempt    int
			code       any
		)
		if f.Status != 0 {
			code = f.Status
		}
		err := tx.QueryRowContext(ctx, `
			UPDATE webhook_deliveries SET status = $2::text, attempt = attempt + 1, series_attempt = series_attempt + 1,
				next_attempt_at = $3, claimed_until = NULL, last_status = $4, last_error = $5, last_latency_ms = $6,
				delivered_at = CASE WHEN $2::text = 'succeeded' THEN $7::timestamptz ELSE delivered_at END, updated_at = $7
			WHERE id = $1 RETURNING endpoint_id, attempt`,
			f.DeliveryID, status, next, code, f.Error, f.LatencyMs, f.Now).Scan(&endpointID, &attempt)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO webhook_attempts (delivery_id, attempt, delivery_ref, at, http_status, error, latency_ms)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, f.DeliveryID, attempt, f.Ref, f.Now, code, f.Error, f.LatencyMs); err != nil {
			return err
		}
		if f.Internal {
			return nil
		}
		if f.Outcome == OutcomeSuccess {
			_, err := tx.ExecContext(ctx, `
				UPDATE webhook_endpoints SET consecutive_failures = 0, last_success_at = $2, last_status = $3 WHERE id = $1`,
				endpointID, f.Now, code)
			return err
		}
		var failures int
		var enabled bool
		if err := tx.QueryRowContext(ctx, `
			UPDATE webhook_endpoints SET consecutive_failures = consecutive_failures + 1, last_failure_at = $2, last_status = $3
			WHERE id = $1 RETURNING consecutive_failures, enabled`, endpointID, f.Now, code).Scan(&failures, &enabled); err != nil {
			return err
		}
		reason := ""
		switch {
		case f.Outcome == OutcomeGone:
			reason = ReasonGone
		case f.MaxFailures > 0 && failures >= f.MaxFailures:
			reason = ReasonFailures
		}
		if reason == "" || !enabled {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE webhook_endpoints SET enabled = false, disabled_reason = $2, updated_at = now() WHERE id = $1`,
			endpointID, reason); err != nil {
			return err
		}
		if err := audit(ctx, tx, "system:webhooks", "disable", "webhook_endpoint", strconv.FormatInt(endpointID, 10)); err != nil {
			return err
		}
		ep, err := scanEndpoint(tx.QueryRowContext(ctx, `SELECT `+endpointCols+` FROM webhook_endpoints e WHERE e.id = $1`, endpointID))
		if err != nil {
			return err
		}
		res = FinishResult{Disabled: true, Reason: reason, Endpoint: ep}
		return nil
	})
	return res, err
}

const deliveryCols = `d.id, d.endpoint_id, d.event_id, d.event_type, d.status, d.attempt, d.next_attempt_at, d.last_status, d.last_error,
	d.last_latency_ms, d.delivered_at, d.created_at, d.updated_at`

func scanDelivery(r rowScanner) (Delivery, error) {
	var (
		d          Delivery
		next       time.Time
		last, lat  sql.NullInt64
		deliveredA sql.NullTime
	)
	if err := r.Scan(&d.ID, &d.EndpointID, &d.EventID, &d.EventType, &d.Status, &d.Attempts, &next, &last, &d.LastError,
		&lat, &deliveredA, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return Delivery{}, err
	}
	if d.Status == StatusPending || d.Status == StatusRetrying {
		d.NextAttemptAt = &next
	}
	if last.Valid {
		v := int(last.Int64)
		d.LastStatus = &v
	}
	if lat.Valid {
		v := int(lat.Int64)
		d.LastLatencyMs = &v
	}
	d.DeliveredAt = timePtr(deliveredA)
	return d, nil
}

// ListDeliveries implements Store.
func (p *PG) ListDeliveries(ctx context.Context, q DeliveryQuery) ([]Delivery, error) {
	var (
		where []string
		args  []any
	)
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if q.EndpointID != 0 {
		where = append(where, "d.endpoint_id = "+arg(q.EndpointID))
	}
	if q.Status != "" {
		where = append(where, "d.status = "+arg(q.Status))
	}
	if q.Before != 0 {
		where = append(where, "d.id < "+arg(q.Before))
	}
	text := `SELECT ` + deliveryCols + ` FROM webhook_deliveries d`
	if len(where) > 0 {
		text += " WHERE " + strings.Join(where, " AND ")
	}
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	//nolint:gosec // G202: only fixed fragments and $n placeholders are joined; every value is bound.
	text += " ORDER BY d.id DESC LIMIT " + arg(limit)
	rows, err := p.DB.QueryContext(ctx, text, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Delivery{}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDelivery implements Store.
func (p *PG) GetDelivery(ctx context.Context, id int64) (DeliveryDetail, error) {
	d, err := scanDelivery(p.DB.QueryRowContext(ctx, `SELECT `+deliveryCols+` FROM webhook_deliveries d WHERE d.id = $1`, id))
	if err != nil {
		return DeliveryDetail{}, mapErr(err)
	}
	out := DeliveryDetail{Delivery: d, Log: []Attempt{}}
	var payload sql.NullString
	if err := p.DB.QueryRowContext(ctx, `SELECT payload::text FROM integration_events WHERE id = $1`, d.EventID).Scan(&payload); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return DeliveryDetail{}, err
	}
	if payload.Valid {
		out.Event = []byte(payload.String)
	}
	rows, err := p.DB.QueryContext(ctx, `SELECT attempt, delivery_ref, at, http_status, error, latency_ms FROM webhook_attempts WHERE delivery_id = $1 ORDER BY attempt`, id)
	if err != nil {
		return DeliveryDetail{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			a    Attempt
			code sql.NullInt64
		)
		if err := rows.Scan(&a.Attempt, &a.Ref, &a.At, &code, &a.Error, &a.LatencyMs); err != nil {
			return DeliveryDetail{}, err
		}
		if code.Valid {
			v := int(code.Int64)
			a.Status = &v
		}
		out.Log = append(out.Log, a)
	}
	return out, rows.Err()
}

// Redeliver implements Store.
func (p *PG) Redeliver(ctx context.Context, actor string, id int64, now time.Time) (Delivery, error) {
	var out Delivery
	err := p.tx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `
			UPDATE webhook_deliveries d SET status = 'pending', series_attempt = 0, next_attempt_at = $2, claimed_until = NULL, updated_at = $2
			WHERE d.id = $1 AND (d.claimed_until IS NULL OR d.claimed_until <= $2)
			RETURNING `+deliveryCols, id, now)
		d, err := scanDelivery(row)
		if errors.Is(err, sql.ErrNoRows) {
			var one int
			if e2 := tx.QueryRowContext(ctx, `SELECT 1 FROM webhook_deliveries WHERE id = $1`, id).Scan(&one); e2 == nil {
				return ErrInFlight
			}
			return sql.ErrNoRows
		}
		if err != nil {
			return err
		}
		out = d
		return audit(ctx, tx, actor, "redeliver", "webhook_delivery", strconv.FormatInt(id, 10))
	})
	return out, err
}

// RedeliverDead implements Store.
func (p *PG) RedeliverDead(ctx context.Context, actor string, endpointID int64, now time.Time) (int64, error) {
	var n int64
	err := p.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE webhook_deliveries SET status = 'pending', series_attempt = 0, next_attempt_at = $2, claimed_until = NULL, updated_at = $2
			WHERE endpoint_id = $1 AND status = 'dead'`, endpointID, now)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return audit(ctx, tx, actor, "redeliver-dead", "webhook_endpoint", strconv.FormatInt(endpointID, 10))
	})
	return n, err
}

// Purge implements Store. An event is kept while a delivery that is not
// finished depends on it (events.Service's purge checks the same).
func (p *PG) Purge(ctx context.Context, succeededBefore, deadBefore time.Time, deadCap int) (PurgeResult, error) {
	var r PurgeResult
	res, err := p.DB.ExecContext(ctx, `DELETE FROM webhook_deliveries WHERE status = 'succeeded' AND updated_at < $1`, succeededBefore)
	if err != nil {
		return r, err
	}
	r.Succeeded, _ = res.RowsAffected()
	if res, err = p.DB.ExecContext(ctx, `DELETE FROM webhook_deliveries WHERE status = 'dead' AND updated_at < $1`, deadBefore); err != nil {
		return r, err
	}
	r.Dead, _ = res.RowsAffected()
	if deadCap > 0 {
		if res, err = p.DB.ExecContext(ctx, `
			DELETE FROM webhook_deliveries WHERE id IN (
				SELECT id FROM (
					SELECT id, row_number() OVER (PARTITION BY endpoint_id ORDER BY updated_at DESC, id DESC) AS rn
					FROM webhook_deliveries WHERE status = 'dead') x
				WHERE x.rn > $1)`, deadCap); err != nil {
			return r, err
		}
		r.Dropped, _ = res.RowsAffected()
	}
	return r, nil
}

// Counts implements Store.
func (p *PG) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	err := p.DB.QueryRowContext(ctx, `
		SELECT
			(SELECT count(*) FROM webhook_deliveries WHERE status IN ('pending', 'retrying')),
			(SELECT count(*) FROM webhook_deliveries WHERE status = 'dead'),
			(SELECT count(*) FROM webhook_endpoints WHERE NOT enabled)`).Scan(&c.Pending, &c.Dead, &c.DisabledEndpoints)
	return c, err
}

var _ Store = (*PG)(nil)
