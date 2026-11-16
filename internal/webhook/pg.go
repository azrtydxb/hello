package webhook

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

	"github.com/azrtydxb/hello/internal/auth"
	hellodb "github.com/azrtydxb/hello/internal/db"
	"github.com/azrtydxb/hello/internal/events"
)

// PG is the PostgreSQL store, on momo (internal/db).
//
// The statements are the hand-written SQL they were on database/sql, with
// @name binds instead of $n. Errors reach callers as before: ErrNotFound for
// a missing row, ErrInFlight for a redelivery of a leased delivery, and the
// driver's error (a *pgconn.PgError is still reachable with errors.As) for
// anything else. The advisory locks keep their keys, so replicas of an older
// and a newer version still exclude each other during a rolling update.
type PG struct{ DB *momo.DB }

// claimLock serialises claims across control replicas for the few
// milliseconds a claim takes, so the per-endpoint concurrency is exact.
const claimLock = 0x68656c6c6f776864 // "hellowhd"

// createLock is the text key of the lock that serialises endpoint creates,
// hashed by the server with hashtext (advisory.XactLockString).
const createLock = "hello:webhooks:create"

// one returns the single row of rows, or sql.ErrNoRows when there is none,
// the error QueryRow(...).Scan gave, so mapErr maps it as before.
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

const endpointCols = `e.id, e.name, e.url, to_json(e.events)::text AS events_json, to_json(e.extensions)::text AS extensions_json,
	e.enabled, e.allow_private, e.include_peer, e.include_message_body,
	e.disabled_reason, e.consecutive_failures, e.previous_secret_expires, e.last_success_at, e.last_failure_at, e.last_status,
	e.created_by, e.created_at, e.updated_at, e.secret_enc, e.previous_secret_enc,
	(SELECT count(*) FROM webhook_deliveries d WHERE d.endpoint_id = e.id AND d.status IN ('pending', 'retrying')) AS pending,
	(SELECT count(*) FROM webhook_deliveries d WHERE d.endpoint_id = e.id AND d.status = 'dead') AS dead`

// endpointScan is an endpointCols row.
type endpointScan struct {
	ID                    int64      `momo:"column:id"`
	Name                  string     `momo:"column:name"`
	URL                   string     `momo:"column:url"`
	EventsJSON            string     `momo:"column:events_json"`
	ExtensionsJSON        string     `momo:"column:extensions_json"`
	Enabled               bool       `momo:"column:enabled"`
	AllowPrivate          bool       `momo:"column:allow_private"`
	IncludePeer           bool       `momo:"column:include_peer"`
	IncludeMessageBody    bool       `momo:"column:include_message_body"`
	DisabledReason        string     `momo:"column:disabled_reason"`
	ConsecutiveFailures   int        `momo:"column:consecutive_failures"`
	PreviousSecretExpires *time.Time `momo:"column:previous_secret_expires"`
	LastSuccessAt         *time.Time `momo:"column:last_success_at"`
	LastFailureAt         *time.Time `momo:"column:last_failure_at"`
	LastStatus            *int       `momo:"column:last_status"`
	CreatedBy             string     `momo:"column:created_by"`
	CreatedAt             time.Time  `momo:"column:created_at"`
	UpdatedAt             time.Time  `momo:"column:updated_at"`
	SecretEnc             []byte     `momo:"column:secret_enc"`
	PreviousSecretEnc     []byte     `momo:"column:previous_secret_enc"`
	Pending               int64      `momo:"column:pending"`
	Dead                  int64      `momo:"column:dead"`
}

func (s endpointScan) endpoint() Endpoint {
	e := Endpoint{ID: s.ID, Name: s.Name, URL: s.URL, Enabled: s.Enabled, AllowPrivate: s.AllowPrivate, IncludePeer: s.IncludePeer,
		IncludeMessageBody: s.IncludeMessageBody, DisabledReason: s.DisabledReason, ConsecutiveFailures: s.ConsecutiveFailures,
		PreviousSecretExpires: s.PreviousSecretExpires, LastSuccessAt: s.LastSuccessAt, LastFailureAt: s.LastFailureAt,
		LastStatus: s.LastStatus, CreatedBy: s.CreatedBy, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
		SecretEnc: s.SecretEnc, PreviousSecretEnc: s.PreviousSecretEnc, Pending: s.Pending, Dead: s.Dead}
	_ = json.Unmarshal([]byte(s.EventsJSON), &e.Events)
	_ = json.Unmarshal([]byte(s.ExtensionsJSON), &e.Extensions)
	e.Events, e.Extensions = nonNil(e.Events), nonNil(e.Extensions)
	return e
}

// getEndpoint reads one endpoint on q; a missing one is sql.ErrNoRows.
func getEndpoint(ctx context.Context, q momo.Querier, id int64) (Endpoint, error) {
	s, err := one(momo.Query[endpointScan](ctx, q, `SELECT `+endpointCols+` FROM webhook_endpoints e WHERE e.id = @id`, momo.Named("id", id)))
	if err != nil {
		return Endpoint{}, err
	}
	return s.endpoint(), nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
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

func audit(ctx context.Context, q momo.Querier, actor, action, resource, id string) error {
	var via any
	if a, ok := auth.ActorFrom(ctx); ok {
		switch {
		case a.ClientID != "":
			via = a.ClientID
		case a.Via != "":
			via = a.Via
		}
	}
	_, err := momo.Exec(ctx, q,
		`INSERT INTO audit_events (actor, action, resource, resource_id, via) VALUES (@actor, @action, @resource, @resource_id, @via)`,
		momo.Named("actor", actor), momo.Named("action", action), momo.Named("resource", resource), momo.Named("resource_id", id),
		momo.Named("via", via))
	return err
}

// auditInclude records each change of an endpoint's inbox options with its
// own audit row: sending customers' numbers or texts to a receiver is a
// privacy decision (spec messaging-channels M-23).
func auditInclude(ctx context.Context, q momo.Querier, actor string, id int64, wasPeer, peer, wasBody, body bool) error {
	rid := strconv.FormatInt(id, 10)
	if peer != wasPeer {
		if err := audit(ctx, q, actor, "include_peer "+strconv.FormatBool(peer), "webhook_endpoint", rid); err != nil {
			return err
		}
	}
	if body != wasBody {
		return audit(ctx, q, actor, "include_message_body "+strconv.FormatBool(body), "webhook_endpoint", rid)
	}
	return nil
}

// tx runs fn in a transaction: committed when fn returns nil, rolled back
// otherwise, with a missing row reported as ErrNotFound.
func (p *PG) tx(ctx context.Context, fn func(*momo.Tx) error) error {
	return mapErr(p.DB.Tx(ctx, momo.TxOptions{}, fn))
}

// NextEndpointID implements Store.
func (p *PG) NextEndpointID(ctx context.Context) (int64, error) {
	return one(momo.Scalars[int64](ctx, p.DB, `SELECT nextval(pg_get_serial_sequence('webhook_endpoints', 'id'))`))
}

// CreateEndpoint implements Store.
func (p *PG) CreateEndpoint(ctx context.Context, actor string, e Endpoint) (Endpoint, bool, error) {
	var (
		out     Endpoint
		created bool
	)
	err := p.tx(ctx, func(tx *momo.Tx) error {
		// One create at a time, so two identical Zapier subscriptions
		// cannot both pass the existence check.
		if err := advisory.XactLockString(ctx, tx, createLock); err != nil {
			return err
		}
		got, err := momo.Query[endpointScan](ctx, tx,
			`SELECT `+endpointCols+` FROM webhook_endpoints e WHERE e.url = @url AND e.events = @events::text[] ORDER BY e.id LIMIT 1`,
			momo.Named("url", e.URL), momo.Named("events", pgArray(e.Events)))
		if err != nil {
			return err
		}
		if len(got) > 0 {
			out = got[0].endpoint()
			return nil
		}
		if _, err := momo.Exec(ctx, tx, `
			INSERT INTO webhook_endpoints (id, name, url, secret_enc, events, extensions, enabled, allow_private, created_by,
				include_peer, include_message_body)
			VALUES (@id, @name, @url, @secret_enc, @events::text[], @extensions::text[], @enabled, @allow_private, @created_by,
				@include_peer, @include_message_body)`,
			momo.Named("id", e.ID), momo.Named("name", e.Name), momo.Named("url", e.URL), momo.Named("secret_enc", e.SecretEnc),
			momo.Named("events", pgArray(e.Events)), momo.Named("extensions", pgArray(e.Extensions)), momo.Named("enabled", e.Enabled),
			momo.Named("allow_private", e.AllowPrivate), momo.Named("created_by", actor),
			momo.Named("include_peer", e.IncludePeer), momo.Named("include_message_body", e.IncludeMessageBody)); err != nil {
			return err
		}
		if err := audit(ctx, tx, actor, "create", "webhook_endpoint", strconv.FormatInt(e.ID, 10)); err != nil {
			return err
		}
		if err := auditInclude(ctx, tx, actor, e.ID, false, e.IncludePeer, false, e.IncludeMessageBody); err != nil {
			return err
		}
		ep, err := getEndpoint(ctx, tx, e.ID)
		out, created = ep, true
		return err
	})
	return out, created, err
}

// GetEndpoint implements Store.
func (p *PG) GetEndpoint(ctx context.Context, id int64) (Endpoint, error) {
	e, err := getEndpoint(ctx, p.DB, id)
	return e, mapErr(err)
}

// ListEndpoints implements Store.
func (p *PG) ListEndpoints(ctx context.Context) ([]Endpoint, error) {
	scans, err := momo.Query[endpointScan](ctx, p.DB, `SELECT `+endpointCols+` FROM webhook_endpoints e ORDER BY e.id`)
	if err != nil {
		return nil, err
	}
	out := make([]Endpoint, 0, len(scans))
	for _, s := range scans {
		out = append(out, s.endpoint())
	}
	return out, nil
}

// UpdateEndpoint implements Store.
func (p *PG) UpdateEndpoint(ctx context.Context, actor string, e Endpoint, resetHealth bool) (Endpoint, error) {
	var out Endpoint
	err := p.tx(ctx, func(tx *momo.Tx) error {
		was, err := one(momo.Query[momo.Row2[bool, bool]](ctx, tx,
			`SELECT include_peer, include_message_body FROM webhook_endpoints WHERE id = @id FOR UPDATE`, momo.Named("id", e.ID)))
		if err != nil {
			return err
		}
		n, err := momo.Exec(ctx, tx, `
			UPDATE webhook_endpoints SET name = @name, url = @url, events = @events::text[], extensions = @extensions::text[], enabled = @enabled,
				allow_private = @allow_private, disabled_reason = @disabled_reason,
				consecutive_failures = CASE WHEN @reset_health::boolean THEN 0 ELSE consecutive_failures END, updated_at = now(),
				include_peer = @include_peer, include_message_body = @include_message_body
			WHERE id = @id`,
			momo.Named("id", e.ID), momo.Named("name", e.Name), momo.Named("url", e.URL), momo.Named("events", pgArray(e.Events)),
			momo.Named("extensions", pgArray(e.Extensions)), momo.Named("enabled", e.Enabled), momo.Named("allow_private", e.AllowPrivate),
			momo.Named("disabled_reason", e.DisabledReason), momo.Named("reset_health", resetHealth),
			momo.Named("include_peer", e.IncludePeer), momo.Named("include_message_body", e.IncludeMessageBody))
		if err != nil {
			return err
		}
		if n == 0 {
			return sql.ErrNoRows
		}
		if err := audit(ctx, tx, actor, "update", "webhook_endpoint", strconv.FormatInt(e.ID, 10)); err != nil {
			return err
		}
		if err := auditInclude(ctx, tx, actor, e.ID, was.C1, e.IncludePeer, was.C2, e.IncludeMessageBody); err != nil {
			return err
		}
		out, err = getEndpoint(ctx, tx, e.ID)
		return err
	})
	return out, err
}

// SetSecrets implements Store.
func (p *PG) SetSecrets(ctx context.Context, actor string, id int64, current, previous []byte, previousExpires *time.Time) (Endpoint, error) {
	var out Endpoint
	err := p.tx(ctx, func(tx *momo.Tx) error {
		n, err := momo.Exec(ctx, tx, `
			UPDATE webhook_endpoints SET secret_enc = @secret_enc, previous_secret_enc = @previous_secret_enc,
				previous_secret_expires = @previous_secret_expires, updated_at = now()
			WHERE id = @id`, momo.Named("id", id), momo.Named("secret_enc", current), momo.Named("previous_secret_enc", previous),
			momo.Named("previous_secret_expires", previousExpires))
		if err != nil {
			return err
		}
		if n == 0 {
			return sql.ErrNoRows
		}
		if err := audit(ctx, tx, actor, "rotate-secret", "webhook_endpoint", strconv.FormatInt(id, 10)); err != nil {
			return err
		}
		out, err = getEndpoint(ctx, tx, id)
		return err
	})
	return out, err
}

// DeleteEndpoint implements Store.
func (p *PG) DeleteEndpoint(ctx context.Context, actor string, id int64) error {
	return p.tx(ctx, func(tx *momo.Tx) error {
		n, err := momo.Exec(ctx, tx, `DELETE FROM webhook_endpoints WHERE id = @id`, momo.Named("id", id))
		if err != nil {
			return err
		}
		if n == 0 {
			return sql.ErrNoRows
		}
		return audit(ctx, tx, actor, "delete", "webhook_endpoint", strconv.FormatInt(id, 10))
	})
}

// enqueueScan is an enabled endpoint's filter.
type enqueueScan struct {
	ID             int64  `momo:"column:id"`
	EventsJSON     string `momo:"column:events_json"`
	ExtensionsJSON string `momo:"column:extensions_json"`
}

// Enqueue implements Store: it runs in the event ingest transaction, which
// the caller commits or rolls back.
func (p *PG) Enqueue(ctx context.Context, tx *sql.Tx, e events.Entry) error {
	q := hellodb.AdoptTx(p.DB, tx)
	scans, err := momo.Query[enqueueScan](ctx, q,
		`SELECT id, to_json(events)::text AS events_json, to_json(extensions)::text AS extensions_json FROM webhook_endpoints WHERE enabled`)
	if err != nil {
		return err
	}
	var ids []int64
	for _, s := range scans {
		var f events.Filter
		_ = json.Unmarshal([]byte(s.EventsJSON), &f.Types)
		_ = json.Unmarshal([]byte(s.ExtensionsJSON), &f.Allowed)
		if len(f.Allowed) == 0 {
			f.Allowed = nil // an empty filter is every extension
		}
		if f.Matches(e.Event) {
			ids = append(ids, s.ID)
		}
	}
	for _, id := range ids {
		if _, err := momo.Exec(ctx, q, `
			INSERT INTO webhook_deliveries (endpoint_id, event_id, event_type, status, next_attempt_at)
			VALUES (@endpoint_id, @event_id, @event_type, 'pending', now())
			ON CONFLICT (endpoint_id, event_id) DO NOTHING`,
			momo.Named("endpoint_id", id), momo.Named("event_id", e.ID), momo.Named("event_type", e.Type)); err != nil {
			return err
		}
	}
	return nil
}

// claimScan is a leased delivery with what sending it needs.
type claimScan struct {
	ID                    int64      `momo:"column:id"`
	EndpointID            int64      `momo:"column:endpoint_id"`
	EventID               string     `momo:"column:event_id"`
	EventType             string     `momo:"column:event_type"`
	Status                string     `momo:"column:status"`
	Attempt               int        `momo:"column:attempt"`
	SeriesAttempt         int        `momo:"column:series_attempt"`
	URL                   string     `momo:"column:url"`
	AllowPrivate          bool       `momo:"column:allow_private"`
	IncludePeer           bool       `momo:"column:include_peer"`
	IncludeMessageBody    bool       `momo:"column:include_message_body"`
	SecretEnc             []byte     `momo:"column:secret_enc"`
	PreviousSecretEnc     []byte     `momo:"column:previous_secret_enc"`
	PreviousSecretExpires *time.Time `momo:"column:previous_secret_expires"`
	Payload               *string    `momo:"column:payload"`
}

// Claim implements Store.
func (p *PG) Claim(ctx context.Context, now time.Time, limit, perEndpoint int, lease time.Duration) ([]Claim, error) {
	var out []Claim
	err := p.tx(ctx, func(tx *momo.Tx) error {
		if err := advisory.XactLock(ctx, tx, claimLock); err != nil {
			return err
		}
		ids, err := momo.Scalars[int64](ctx, tx, `
			WITH busy AS (
				SELECT endpoint_id, count(*) AS n FROM webhook_deliveries WHERE claimed_until > @now GROUP BY endpoint_id
			), due AS (
				SELECT d.id, d.endpoint_id, d.next_attempt_at,
					row_number() OVER (PARTITION BY d.endpoint_id ORDER BY d.next_attempt_at, d.id) AS rn
				FROM webhook_deliveries d JOIN webhook_endpoints e ON e.id = d.endpoint_id
				WHERE e.enabled AND d.status IN ('pending', 'retrying') AND d.next_attempt_at <= @now
					AND (d.claimed_until IS NULL OR d.claimed_until <= @now)
			)
			SELECT due.id FROM due LEFT JOIN busy ON busy.endpoint_id = due.endpoint_id
			WHERE due.rn <= @per_endpoint::bigint - COALESCE(busy.n, 0)
			ORDER BY due.next_attempt_at, due.id LIMIT @limit`,
			momo.Named("now", now), momo.Named("per_endpoint", perEndpoint), momo.Named("limit", limit))
		if err != nil || len(ids) == 0 {
			return err
		}
		// Lease the rows another transaction is not changing right now.
		ids, err = momo.Scalars[int64](ctx, tx, `
			UPDATE webhook_deliveries SET claimed_until = @claimed_until
			WHERE id IN (SELECT id FROM webhook_deliveries WHERE id = ANY(@ids::bigint[]) AND status IN ('pending', 'retrying') FOR UPDATE SKIP LOCKED)
			RETURNING id`, momo.Named("ids", idArray(ids)), momo.Named("claimed_until", now.Add(lease)))
		if err != nil || len(ids) == 0 {
			return err
		}
		scans, err := momo.Query[claimScan](ctx, tx, `
			SELECT d.id, d.endpoint_id, d.event_id, d.event_type, d.status, d.attempt, d.series_attempt,
				e.url, e.allow_private, e.include_peer, e.include_message_body, e.secret_enc, e.previous_secret_enc,
				e.previous_secret_expires, ev.payload::text AS payload
			FROM webhook_deliveries d
			JOIN webhook_endpoints e ON e.id = d.endpoint_id
			LEFT JOIN integration_events ev ON ev.id = d.event_id
			WHERE d.id = ANY(@ids::bigint[]) ORDER BY d.next_attempt_at, d.id`, momo.Named("ids", idArray(ids)))
		if err != nil {
			return err
		}
		for _, s := range scans {
			c := Claim{Delivery: Delivery{ID: s.ID, EndpointID: s.EndpointID, EventID: s.EventID, EventType: s.EventType,
				Status: s.Status, Attempts: s.Attempt}, SeriesAttempt: s.SeriesAttempt, URL: s.URL, AllowPrivate: s.AllowPrivate,
				IncludePeer: s.IncludePeer, IncludeMessageBody: s.IncludeMessageBody, SecretEnc: s.SecretEnc,
				PreviousSecretEnc: s.PreviousSecretEnc, PreviousSecretExpires: s.PreviousSecretExpires}
			if s.Payload != nil {
				c.Payload = []byte(*s.Payload)
			}
			out = append(out, c)
		}
		return nil
	})
	return out, err
}

// Finish implements Store.
func (p *PG) Finish(ctx context.Context, f Finish) (FinishResult, error) {
	var res FinishResult
	err := p.tx(ctx, func(tx *momo.Tx) error {
		status, next := StatusSucceeded, f.Now
		switch f.Outcome {
		case OutcomeRetry:
			status, next = StatusRetrying, f.NextAttemptAt
		case OutcomeDead, OutcomeGone:
			status = StatusDead
		}
		var code any
		if f.Status != 0 {
			code = f.Status
		}
		d, err := one(momo.Query[momo.Row2[int64, int]](ctx, tx, `
			UPDATE webhook_deliveries SET status = @status::text, attempt = attempt + 1, series_attempt = series_attempt + 1,
				next_attempt_at = @next_attempt_at, claimed_until = NULL, last_status = @last_status, last_error = @last_error,
				last_latency_ms = @last_latency_ms,
				delivered_at = CASE WHEN @status::text = 'succeeded' THEN @now::timestamptz ELSE delivered_at END, updated_at = @now
			WHERE id = @id RETURNING endpoint_id, attempt`,
			momo.Named("id", f.DeliveryID), momo.Named("status", status), momo.Named("next_attempt_at", next), momo.Named("last_status", code),
			momo.Named("last_error", f.Error), momo.Named("last_latency_ms", f.LatencyMs), momo.Named("now", f.Now)))
		if err != nil {
			return err
		}
		endpointID, attempt := d.C1, d.C2
		if _, err := momo.Exec(ctx, tx, `
			INSERT INTO webhook_attempts (delivery_id, attempt, delivery_ref, at, http_status, error, latency_ms)
			VALUES (@delivery_id, @attempt, @delivery_ref, @at, @http_status, @error, @latency_ms)`,
			momo.Named("delivery_id", f.DeliveryID), momo.Named("attempt", attempt), momo.Named("delivery_ref", f.Ref), momo.Named("at", f.Now),
			momo.Named("http_status", code), momo.Named("error", f.Error), momo.Named("latency_ms", f.LatencyMs)); err != nil {
			return err
		}
		if f.Internal {
			return nil
		}
		if f.Outcome == OutcomeSuccess {
			_, err := momo.Exec(ctx, tx, `
				UPDATE webhook_endpoints SET consecutive_failures = 0, last_success_at = @now, last_status = @last_status WHERE id = @id`,
				momo.Named("id", endpointID), momo.Named("now", f.Now), momo.Named("last_status", code))
			return err
		}
		ep, err := one(momo.Query[momo.Row2[int, bool]](ctx, tx, `
			UPDATE webhook_endpoints SET consecutive_failures = consecutive_failures + 1, last_failure_at = @now, last_status = @last_status
			WHERE id = @id RETURNING consecutive_failures, enabled`,
			momo.Named("id", endpointID), momo.Named("now", f.Now), momo.Named("last_status", code)))
		if err != nil {
			return err
		}
		failures, enabled := ep.C1, ep.C2
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
		if _, err := momo.Exec(ctx, tx, `UPDATE webhook_endpoints SET enabled = false, disabled_reason = @reason, updated_at = now() WHERE id = @id`,
			momo.Named("id", endpointID), momo.Named("reason", reason)); err != nil {
			return err
		}
		if err := audit(ctx, tx, "system:webhooks", "disable", "webhook_endpoint", strconv.FormatInt(endpointID, 10)); err != nil {
			return err
		}
		got, err := getEndpoint(ctx, tx, endpointID)
		if err != nil {
			return err
		}
		res = FinishResult{Disabled: true, Reason: reason, Endpoint: got}
		return nil
	})
	return res, err
}

const deliveryCols = `d.id, d.endpoint_id, d.event_id, d.event_type, d.status, d.attempt, d.next_attempt_at, d.last_status, d.last_error,
	d.last_latency_ms, d.delivered_at, d.created_at, d.updated_at`

// deliveryScan is a deliveryCols row.
type deliveryScan struct {
	ID            int64      `momo:"column:id"`
	EndpointID    int64      `momo:"column:endpoint_id"`
	EventID       string     `momo:"column:event_id"`
	EventType     string     `momo:"column:event_type"`
	Status        string     `momo:"column:status"`
	Attempt       int        `momo:"column:attempt"`
	NextAttemptAt time.Time  `momo:"column:next_attempt_at"`
	LastStatus    *int       `momo:"column:last_status"`
	LastError     string     `momo:"column:last_error"`
	LastLatencyMs *int       `momo:"column:last_latency_ms"`
	DeliveredAt   *time.Time `momo:"column:delivered_at"`
	CreatedAt     time.Time  `momo:"column:created_at"`
	UpdatedAt     time.Time  `momo:"column:updated_at"`
}

func (s deliveryScan) delivery() Delivery {
	d := Delivery{ID: s.ID, EndpointID: s.EndpointID, EventID: s.EventID, EventType: s.EventType, Status: s.Status, Attempts: s.Attempt,
		LastStatus: s.LastStatus, LastError: s.LastError, LastLatencyMs: s.LastLatencyMs, DeliveredAt: s.DeliveredAt,
		CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
	if d.Status == StatusPending || d.Status == StatusRetrying {
		next := s.NextAttemptAt
		d.NextAttemptAt = &next
	}
	return d
}

// ListDeliveries implements Store.
func (p *PG) ListDeliveries(ctx context.Context, q DeliveryQuery) ([]Delivery, error) {
	var (
		where []string
		args  []any
	)
	if q.EndpointID != 0 {
		where = append(where, "d.endpoint_id = @endpoint_id")
		args = append(args, momo.Named("endpoint_id", q.EndpointID))
	}
	if q.Status != "" {
		where = append(where, "d.status = @status")
		args = append(args, momo.Named("status", q.Status))
	}
	if q.Before != 0 {
		where = append(where, "d.id < @before")
		args = append(args, momo.Named("before", q.Before))
	}
	text := `SELECT ` + deliveryCols + ` FROM webhook_deliveries d`
	if len(where) > 0 {
		text += " WHERE " + strings.Join(where, " AND ")
	}
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	text += " ORDER BY d.id DESC LIMIT @limit"
	args = append(args, momo.Named("limit", limit))
	scans, err := momo.Query[deliveryScan](ctx, p.DB, text, args...)
	if err != nil {
		return nil, err
	}
	out := make([]Delivery, 0, len(scans))
	for _, s := range scans {
		out = append(out, s.delivery())
	}
	return out, nil
}

// attemptScan is a webhook_attempts row.
type attemptScan struct {
	Attempt     int       `momo:"column:attempt"`
	DeliveryRef string    `momo:"column:delivery_ref"`
	At          time.Time `momo:"column:at"`
	HTTPStatus  *int      `momo:"column:http_status"`
	Error       string    `momo:"column:error"`
	LatencyMs   int       `momo:"column:latency_ms"`
}

// GetDelivery implements Store.
func (p *PG) GetDelivery(ctx context.Context, id int64) (DeliveryDetail, error) {
	s, err := one(momo.Query[deliveryScan](ctx, p.DB, `SELECT `+deliveryCols+` FROM webhook_deliveries d WHERE d.id = @id`, momo.Named("id", id)))
	if err != nil {
		return DeliveryDetail{}, mapErr(err)
	}
	d := s.delivery()
	out := DeliveryDetail{Delivery: d, Log: []Attempt{}}
	payload, err := one(momo.Scalars[sql.NullString](ctx, p.DB, `SELECT payload::text FROM integration_events WHERE id = @id`, momo.Named("id", d.EventID)))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return DeliveryDetail{}, err
	}
	if payload.Valid {
		out.Event = []byte(payload.String)
	}
	attempts, err := momo.Query[attemptScan](ctx, p.DB,
		`SELECT attempt, delivery_ref, at, http_status, error, latency_ms FROM webhook_attempts WHERE delivery_id = @id ORDER BY attempt`,
		momo.Named("id", id))
	if err != nil {
		return DeliveryDetail{}, err
	}
	for _, a := range attempts {
		out.Log = append(out.Log, Attempt{Attempt: a.Attempt, Ref: a.DeliveryRef, At: a.At, Status: a.HTTPStatus, Error: a.Error, LatencyMs: a.LatencyMs})
	}
	return out, nil
}

// Redeliver implements Store.
func (p *PG) Redeliver(ctx context.Context, actor string, id int64, now time.Time) (Delivery, error) {
	var out Delivery
	err := p.tx(ctx, func(tx *momo.Tx) error {
		s, err := one(momo.Query[deliveryScan](ctx, tx, `
			UPDATE webhook_deliveries d SET status = 'pending', series_attempt = 0, next_attempt_at = @now, claimed_until = NULL, updated_at = @now
			WHERE d.id = @id AND (d.claimed_until IS NULL OR d.claimed_until <= @now)
			RETURNING `+deliveryCols, momo.Named("id", id), momo.Named("now", now)))
		if errors.Is(err, sql.ErrNoRows) {
			if found, e2 := momo.Scalars[int](ctx, tx, `SELECT 1 FROM webhook_deliveries WHERE id = @id`, momo.Named("id", id)); e2 == nil && len(found) > 0 {
				return ErrInFlight
			}
			return sql.ErrNoRows
		}
		if err != nil {
			return err
		}
		out = s.delivery()
		return audit(ctx, tx, actor, "redeliver", "webhook_delivery", strconv.FormatInt(id, 10))
	})
	return out, err
}

// RedeliverDead implements Store.
func (p *PG) RedeliverDead(ctx context.Context, actor string, endpointID int64, now time.Time) (int64, error) {
	var n int64
	err := p.tx(ctx, func(tx *momo.Tx) error {
		var err error
		n, err = momo.Exec(ctx, tx, `
			UPDATE webhook_deliveries SET status = 'pending', series_attempt = 0, next_attempt_at = @now, claimed_until = NULL, updated_at = @now
			WHERE endpoint_id = @endpoint_id AND status = 'dead'`, momo.Named("endpoint_id", endpointID), momo.Named("now", now))
		if err != nil {
			return err
		}
		return audit(ctx, tx, actor, "redeliver-dead", "webhook_endpoint", strconv.FormatInt(endpointID, 10))
	})
	return n, err
}

// Purge implements Store. An event is kept while a delivery that is not
// finished depends on it (events.Service's purge checks the same).
func (p *PG) Purge(ctx context.Context, succeededBefore, deadBefore time.Time, deadCap int) (PurgeResult, error) {
	var (
		r   PurgeResult
		err error
	)
	if r.Succeeded, err = momo.Exec(ctx, p.DB, `DELETE FROM webhook_deliveries WHERE status = 'succeeded' AND updated_at < @before`,
		momo.Named("before", succeededBefore)); err != nil {
		return PurgeResult{}, err
	}
	if r.Dead, err = momo.Exec(ctx, p.DB, `DELETE FROM webhook_deliveries WHERE status = 'dead' AND updated_at < @before`,
		momo.Named("before", deadBefore)); err != nil {
		return PurgeResult{Succeeded: r.Succeeded}, err
	}
	if deadCap > 0 {
		if r.Dropped, err = momo.Exec(ctx, p.DB, `
			DELETE FROM webhook_deliveries WHERE id IN (
				SELECT id FROM (
					SELECT id, row_number() OVER (PARTITION BY endpoint_id ORDER BY updated_at DESC, id DESC) AS rn
					FROM webhook_deliveries WHERE status = 'dead') x
				WHERE x.rn > @cap)`, momo.Named("cap", deadCap)); err != nil {
			return PurgeResult{Succeeded: r.Succeeded, Dead: r.Dead}, err
		}
	}
	return r, nil
}

// Counts implements Store.
func (p *PG) Counts(ctx context.Context) (Counts, error) {
	c, err := one(momo.Query[momo.Row3[int64, int64, int64]](ctx, p.DB, `
		SELECT
			(SELECT count(*) FROM webhook_deliveries WHERE status IN ('pending', 'retrying')),
			(SELECT count(*) FROM webhook_deliveries WHERE status = 'dead'),
			(SELECT count(*) FROM webhook_endpoints WHERE NOT enabled)`))
	return Counts{Pending: c.C1, Dead: c.C2, DisabledEndpoints: c.C3}, err
}

var _ Store = (*PG)(nil)
