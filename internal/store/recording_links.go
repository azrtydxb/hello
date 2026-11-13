package store

// Recording links and the call and recording lookup (spec integrations I-43
// to I-45). A link is an opaque token whose SHA-256 is stored; it lets
// someone without a Hello session play one recording until it expires, is
// revoked, runs out of uses, or the recording is deleted (the foreign key
// cascades). Creating and revoking a link are audited in the same
// transaction. The lookup reads CDRs and recordings with optional filters and
// an allowed-extension restriction applied in SQL, so a page never contains a
// row the caller may not see.

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

// RecordingLink is a recording link row. The token is never stored.
type RecordingLink struct {
	ID          int64      `json:"id"`
	RecordingID int64      `json:"recordingId"`
	ExpiresAt   time.Time  `json:"expiresAt"`
	MaxUses     int        `json:"maxUses"`
	Uses        int        `json:"uses"`
	LastUsedAt  *time.Time `json:"lastUsedAt,omitempty"`
	CreatedBy   string     `json:"createdBy"`
	CreatedAt   time.Time  `json:"createdAt"`
	RevokedAt   *time.Time `json:"revokedAt,omitempty"`
}

const linkCols = `id, recording_id, expires_at, max_uses, uses, last_used_at, created_by, created_at, revoked_at`

func scanLink(r interface{ Scan(...any) error }) (RecordingLink, error) {
	var l RecordingLink
	var used, revoked sql.NullTime
	err := r.Scan(&l.ID, &l.RecordingID, &l.ExpiresAt, &l.MaxUses, &l.Uses, &used, &l.CreatedBy, &l.CreatedAt, &revoked)
	l.LastUsedAt, l.RevokedAt = nullTime(used), nullTime(revoked)
	return l, err
}

// CreateRecordingLink stores a link for a recording and audits it as
// create/recording_link by actor. hash is the SHA-256 of the token. A
// recording that does not exist is ErrNotFound.
func (s *Store) CreateRecordingLink(ctx context.Context, actor string, recordingID int64, hash []byte, expires time.Time, maxUses int) (RecordingLink, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RecordingLink{}, err
	}
	defer func() { _ = tx.Rollback() }()
	l, err := scanLink(tx.QueryRowContext(ctx, `
		INSERT INTO recording_links (token_hash, recording_id, expires_at, max_uses, created_by)
		VALUES ($1, $2, $3, $4, $5) RETURNING `+linkCols, hash, recordingID, expires.UTC(), maxUses, actor))
	if err != nil {
		return RecordingLink{}, mapErr(err)
	}
	if err := insertAudit(ctx, tx, actor, "create", "recording_link", strconv.FormatInt(l.ID, 10)); err != nil {
		return RecordingLink{}, err
	}
	return l, tx.Commit()
}

// GetRecordingLink returns one link.
func (s *Store) GetRecordingLink(ctx context.Context, id int64) (RecordingLink, error) {
	l, err := scanLink(s.q(ctx).QueryRowContext(ctx, `SELECT `+linkCols+` FROM recording_links WHERE id = $1`, id))
	return l, mapErr(err)
}

// RevokeRecordingLink revokes a link and audits it. Revoking a revoked link
// is not an error; a missing one is ErrNotFound.
func (s *Store) RevokeRecordingLink(ctx context.Context, actor string, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE recording_links SET revoked_at = COALESCE(revoked_at, now()) WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if err := requireRow(res); err != nil {
		return err
	}
	if err := insertAudit(ctx, tx, actor, "revoke", "recording_link", strconv.FormatInt(id, 10)); err != nil {
		return err
	}
	return tx.Commit()
}

// LinkResult is why UseRecordingLink refused a token.
type LinkResult string

const (
	LinkOK      LinkResult = "ok"
	LinkUnknown LinkResult = "unknown" // no such token
	LinkRevoked LinkResult = "revoked"
	LinkExpired LinkResult = "expired"
	LinkUsedUp  LinkResult = "used_up"
)

// UseRecordingLink redeems a token hash: when the link is live it counts the
// use (atomically, so max_uses holds under concurrency) and returns the
// recording's object key. Otherwise it reports why, so the caller can count
// the outcome; the caller answers every refusal alike.
func (s *Store) UseRecordingLink(ctx context.Context, hash []byte) (object string, id int64, res LinkResult, err error) {
	err = s.q(ctx).QueryRowContext(ctx, `
		UPDATE recording_links l SET uses = l.uses + 1, last_used_at = now()
		FROM recordings r
		WHERE l.token_hash = $1 AND r.id = l.recording_id AND l.revoked_at IS NULL AND l.expires_at > now()
			AND (l.max_uses = 0 OR l.uses < l.max_uses)
		RETURNING r.minio_object, l.id`, hash).Scan(&object, &id)
	if err == nil {
		return object, id, LinkOK, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", 0, "", err
	}
	var revoked sql.NullTime
	var expires time.Time
	var maxUses, uses int
	err = s.q(ctx).QueryRowContext(ctx,
		`SELECT revoked_at, expires_at, max_uses, uses FROM recording_links WHERE token_hash = $1`, hash).
		Scan(&revoked, &expires, &maxUses, &uses)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", 0, LinkUnknown, nil
	case err != nil:
		return "", 0, "", err
	case revoked.Valid:
		return "", 0, LinkRevoked, nil
	case !expires.After(time.Now()):
		return "", 0, LinkExpired, nil
	}
	return "", 0, LinkUsedUp, nil
}

// CountActiveRecordingLinks counts the links that can still be used.
func (s *Store) CountActiveRecordingLinks(ctx context.Context) (int64, error) {
	var n int64
	err := s.q(ctx).QueryRowContext(ctx, `SELECT count(*) FROM recording_links
		WHERE revoked_at IS NULL AND expires_at > now() AND (max_uses = 0 OR uses < max_uses)`).Scan(&n)
	return n, err
}

// LookupFilter narrows the call and recording lookup. The zero value matches
// everything the restriction allows.
type LookupFilter struct {
	// Number matches a caller or dialled number: all non-digits are ignored,
	// and a number of eight or more digits also matches by its last eight
	// digits, so +32 11 111 222 and 011 111 222 meet (the trunk prefix 0 is
	// dropped in the international form).
	Number string
	// Extension keeps calls the extension was the source or destination of.
	Extension string
	// From and To bound the call's start (zero = unbounded; To exclusive).
	From, To time.Time
	// CallID matches the correlation id or the SIP Call-ID.
	CallID string
	// Recorded keeps only calls that have a recording.
	Recorded bool
	// Restrict limits results to calls involving one of Allowed; false means
	// no restriction (an empty Allowed with Restrict set matches nothing).
	Restrict bool
	Allowed  []string
}

// lookupWhere appends the filter's conditions over the cdr alias c and the
// recording alias r to a query and returns the clause and its arguments,
// numbered from len(args)+1.
func lookupWhere(f LookupFilter, args []any) (string, []any) {
	var w []string
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	text := func(v string) string { return arg(v) + "::text" }
	parties := func(cond func(col string) string) string {
		return "(" + cond("c.source") + " OR " + cond("c.destination") + " OR " + cond("c.original_destination") + ")"
	}
	if f.Restrict {
		a := arg(pgTextArray(f.Allowed))
		w = append(w, parties(func(col string) string { return col + " = ANY(" + a + "::text[])" }))
	}
	if f.Extension != "" {
		a := text(f.Extension)
		w = append(w, parties(func(col string) string { return col + " = " + a }))
	}
	if digits := onlyDigits(f.Number); digits != "" {
		d := text(digits)
		suf := ""
		if len(digits) >= 8 {
			suf = digits[len(digits)-8:]
		}
		s := text(suf)
		w = append(w, parties(func(col string) string {
			nd := "regexp_replace(" + col + ", '[^0-9]', '', 'g')"
			return "(" + nd + " = " + d + " OR (" + s + " <> '' AND right(" + nd + ", length(" + s + ")) = " + s + "))"
		}))
	}
	if !f.From.IsZero() {
		w = append(w, "COALESCE(c.start_time, r.created_at) >= "+arg(f.From.UTC())+"::timestamptz")
	}
	if !f.To.IsZero() {
		w = append(w, "COALESCE(c.start_time, r.created_at) < "+arg(f.To.UTC())+"::timestamptz")
	}
	if f.CallID != "" {
		a := text(f.CallID)
		w = append(w, "(c.correlation_id = "+a+" OR c.sip_call_id = "+a+" OR r.correlation_id = "+a+")")
	}
	if f.Recorded {
		w = append(w, "r.id IS NOT NULL")
	}
	if len(w) == 0 {
		return "TRUE", args
	}
	return strings.Join(w, " AND "), args
}

func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// pgTextArray formats v as a PostgreSQL text[] literal.
func pgTextArray(v []string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, s := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`)
	}
	b.WriteByte('}')
	return b.String()
}

// CallMatch is one call of the lookup, with its recording if it has one.
type CallMatch struct {
	ID                  int64      `json:"id"`
	CallID              string     `json:"callId"`
	SIPCallID           string     `json:"sipCallId"`
	Direction           string     `json:"direction"`
	Kind                string     `json:"kind"`
	Source              string     `json:"source"`
	Destination         string     `json:"destination"`
	OriginalDestination string     `json:"originalDestination"`
	StartTime           time.Time  `json:"startTime"`
	AnswerTime          *time.Time `json:"answerTime,omitempty"`
	EndTime             time.Time  `json:"endTime"`
	DurationMs          int64      `json:"durationMs"`
	BillableMs          int64      `json:"billableMs"`
	FinalStatus         int        `json:"finalStatus"`
	RecordingID         *int64     `json:"recordingId,omitempty"`
}

// LookupCalls returns up to limit calls matching f with id below before (0 =
// newest), newest first, and the cursor for the next page ("" at the end).
func (s *Store) LookupCalls(ctx context.Context, f LookupFilter, before int64, limit int) ([]CallMatch, string, error) {
	where, args := lookupWhere(f, []any{before, limit + 1})
	//nolint:gosec // G202: where is built from constants and numbered placeholders
	rows, err := s.q(ctx).QueryContext(ctx, `
		SELECT c.id, c.correlation_id, c.sip_call_id, c.direction, c.kind, c.source, c.destination, c.original_destination,
			c.start_time, c.answer_time, c.end_time, c.duration_ms, c.billable_ms, c.final_status, r.id
		FROM cdrs c LEFT JOIN recordings r ON r.correlation_id = c.correlation_id
		WHERE ($1::bigint = 0 OR c.id < $1::bigint) AND `+where+`
		ORDER BY c.id DESC LIMIT $2`, args...)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()
	out := []CallMatch{}
	for rows.Next() {
		var m CallMatch
		var answer sql.NullTime
		if err := rows.Scan(&m.ID, &m.CallID, &m.SIPCallID, &m.Direction, &m.Kind, &m.Source, &m.Destination, &m.OriginalDestination,
			&m.StartTime, &answer, &m.EndTime, &m.DurationMs, &m.BillableMs, &m.FinalStatus, &m.RecordingID); err != nil {
			return nil, "", err
		}
		m.AnswerTime = nullTime(answer)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = strconv.FormatInt(out[limit-1].ID, 10)
	}
	return out, next, nil
}

// LookupRecordings returns up to limit recordings whose call matches f, with
// id below before (0 = newest), newest first, and the next cursor. With
// Restrict set a recording with no CDR yet is never returned.
func (s *Store) LookupRecordings(ctx context.Context, f LookupFilter, before int64, limit int) ([]Recording, string, error) {
	where, args := lookupWhere(f, []any{before, limit + 1})
	//nolint:gosec // G202: where is built from constants and numbered placeholders
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+recordingCols+recordingFrom+`
		WHERE ($1::bigint = 0 OR r.id < $1::bigint) AND `+where+`
		ORDER BY r.id DESC LIMIT $2`, args...)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()
	out := []Recording{}
	for rows.Next() {
		rec, err := scanRecording(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = strconv.FormatInt(out[limit-1].ID, 10)
	}
	return out, next, nil
}

// RecordingParties returns the numbers a recording's call involved (source,
// destination and original destination of its CDR); empty when the CDR is not
// written yet.
func (s *Store) RecordingParties(ctx context.Context, id int64) ([]string, error) {
	var src, dst, orig sql.NullString
	err := s.q(ctx).QueryRowContext(ctx, `
		SELECT c.source, c.destination, c.original_destination
		FROM recordings r LEFT JOIN cdrs c ON c.correlation_id = r.correlation_id WHERE r.id = $1`, id).Scan(&src, &dst, &orig)
	if err != nil {
		return nil, mapErr(err)
	}
	var out []string
	for _, v := range []sql.NullString{src, dst, orig} {
		if v.Valid && v.String != "" {
			out = append(out, v.String)
		}
	}
	return out, nil
}

// CreateConnectorRecordingLink stores a link minted for a CRM connector
// (recording_links.connector_id), audited as create/recording_link by the
// connector's actor string. A recording that does not exist is ErrNotFound.
func (s *Store) CreateConnectorRecordingLink(ctx context.Context, connectorID, recordingID int64, hash []byte, expires time.Time) (RecordingLink, error) {
	actor := "crm:" + strconv.FormatInt(connectorID, 10)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RecordingLink{}, err
	}
	defer func() { _ = tx.Rollback() }()
	l, err := scanLink(tx.QueryRowContext(ctx, `
		INSERT INTO recording_links (token_hash, recording_id, expires_at, max_uses, created_by, connector_id)
		VALUES ($1, $2, $3, 0, $4, $5) RETURNING `+linkCols, hash, recordingID, expires.UTC(), actor, connectorID))
	if err != nil {
		return RecordingLink{}, mapErr(err)
	}
	if err := insertAudit(ctx, tx, actor, "create", "recording_link", strconv.FormatInt(l.ID, 10)); err != nil {
		return RecordingLink{}, err
	}
	return l, tx.Commit()
}

// RevokeConnectorRecordingLinks revokes every live link a CRM connector
// minted and audits it once. It returns how many it revoked.
func (s *Store) RevokeConnectorRecordingLinks(ctx context.Context, connectorID int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE recording_links SET revoked_at = now() WHERE connector_id = $1 AND revoked_at IS NULL`, connectorID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		if err := insertAudit(ctx, tx, "crm:"+strconv.FormatInt(connectorID, 10), "revoke_all", "recording_link", strconv.FormatInt(connectorID, 10)); err != nil {
			return 0, err
		}
	}
	return n, tx.Commit()
}
