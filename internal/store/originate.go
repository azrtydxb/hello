package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
)

// OriginateRecord is one click-to-call request (migration 00057); the live
// state of a running request is in Valkey and is copied here as it changes.
type OriginateRecord struct {
	ID             string
	IdempotencyKey string
	Extension      string
	Destination    string
	CallerID       string
	// Client is the OAuth client or token id the request came with; Actor
	// the audit actor.
	Client        string
	Actor         string
	State         string
	Reason        string
	CorrelationID string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

const originateCols = `id, idempotency_key, extension_number, destination, caller_id, client, actor,
	state, reason, cdr_correlation_id, created_at, updated_at`

func scanOriginate(r interface{ Scan(...any) error }) (OriginateRecord, error) {
	var o OriginateRecord
	err := r.Scan(&o.ID, &o.IdempotencyKey, &o.Extension, &o.Destination, &o.CallerID, &o.Client, &o.Actor,
		&o.State, &o.Reason, &o.CorrelationID, &o.CreatedAt, &o.UpdatedAt)
	return o, err
}

// CreateOriginate records a queued request.
func (s *Store) CreateOriginate(ctx context.Context, o OriginateRecord) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO originate_requests
		(id, idempotency_key, extension_number, destination, caller_id, client, actor, state)
		VALUES (@id, @idempotency_key, @extension_number, @destination, @caller_id, @client, @actor, 'queued')`,
		momo.Named("id", o.ID), momo.Named("idempotency_key", o.IdempotencyKey), momo.Named("extension_number", o.Extension),
		momo.Named("destination", o.Destination), momo.Named("caller_id", o.CallerID), momo.Named("client", o.Client), momo.Named("actor", o.Actor))
	return mapErr(err)
}

// GetOriginate reads one request; ErrNotFound when there is none.
func (s *Store) GetOriginate(ctx context.Context, id string) (OriginateRecord, error) {
	o, err := scanOriginate(s.db.QueryRowContext(ctx, `SELECT `+originateCols+` FROM originate_requests WHERE id = @id`, momo.Named("id", id)))
	if err != nil {
		return OriginateRecord{}, mapErr(err)
	}
	return o, nil
}

// SetOriginateState copies a request's state into its row.
func (s *Store) SetOriginateState(ctx context.Context, id, state, reason, correlationID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE originate_requests
		SET state = @state, reason = @reason,
			cdr_correlation_id = CASE WHEN @cdr_correlation_id::text = '' THEN cdr_correlation_id ELSE @cdr_correlation_id::text END, updated_at = now()
		WHERE id = @id`, momo.Named("id", id), momo.Named("state", state), momo.Named("reason", reason), momo.Named("cdr_correlation_id", correlationID))
	return mapErr(err)
}

// OpenOriginates lists requests that are not over and have not changed for
// at least idle, oldest first, at most limit.
func (s *Store) OpenOriginates(ctx context.Context, idle time.Duration, limit int) ([]OriginateRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+originateCols+` FROM originate_requests
		WHERE state NOT IN ('ended', 'failed') AND updated_at < now() - make_interval(secs => @idle)
		ORDER BY updated_at LIMIT @limit`, momo.Named("idle", idle.Seconds()), momo.Named("limit", limit))
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	var out []OriginateRecord
	for rows.Next() {
		o, err := scanOriginate(rows)
		if err != nil {
			return nil, mapErr(err)
		}
		out = append(out, o)
	}
	return out, mapErr(rows.Err())
}

// PurgeOriginates deletes finished requests older than age.
func (s *Store) PurgeOriginates(ctx context.Context, age time.Duration) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM originate_requests
		WHERE state IN ('ended', 'failed') AND updated_at < now() - make_interval(secs => @age)`, momo.Named("age", age.Seconds()))
	if err != nil {
		return 0, mapErr(err)
	}
	return res.RowsAffected()
}

// Click-to-call destination policies.
const (
	OriginateInherit    = "inherit"
	OriginateRestricted = "restricted"
)

// OriginateSettings is the administrator's click-to-call policy.
type OriginateSettings struct {
	// Destinations is inherit (the outbound routes decide) or restricted
	// (internal numbers plus numbers starting with a CountryPrefixes entry).
	Destinations      string   `json:"destinations"`
	CountryPrefixes   []string `json:"countryPrefixes"`
	AutoAnswerAllowed bool     `json:"autoAnswerAllowed"`
}

// GetOriginateSettings reads the policy.
func (s *Store) GetOriginateSettings(ctx context.Context) (OriginateSettings, error) {
	var out OriginateSettings
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT destinations, to_json(country_prefixes)::text, auto_answer_allowed
		FROM originate_settings`).Scan(&out.Destinations, &raw, &out.AutoAnswerAllowed)
	if err != nil {
		return OriginateSettings{}, mapErr(err)
	}
	if err := json.Unmarshal([]byte(raw), &out.CountryPrefixes); err != nil {
		return OriginateSettings{}, err
	}
	if out.CountryPrefixes == nil {
		out.CountryPrefixes = []string{}
	}
	return out, nil
}

// PutOriginateSettings replaces the policy and audits the change.
func (s *Store) PutOriginateSettings(ctx context.Context, actor string, in OriginateSettings) error {
	if in.CountryPrefixes == nil {
		in.CountryPrefixes = []string{}
	}
	return s.tx(ctx, func(tx *dbTx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE originate_settings
			SET destinations = @destinations, country_prefixes = @country_prefixes::text[], auto_answer_allowed = @auto_answer_allowed`,
			momo.Named("destinations", in.Destinations), momo.Named("country_prefixes", in.CountryPrefixes), momo.Named("auto_answer_allowed", in.AutoAnswerAllowed)); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "update", "originate_settings", "click-to-call")
	})
}
