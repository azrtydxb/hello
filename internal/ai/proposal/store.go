package proposal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/azrtydxb/hello/internal/auth"
)

// ErrNotFound is a proposal id that names no row.
var ErrNotFound = errorString("proposal not found")

// SuppressionWindow is how long a dismissed fingerprint is not proposed
// again (spec S-14).
const SuppressionWindow = 7 * 24 * time.Hour

// MaxDismissText is the longest dismissal text, in characters.
const MaxDismissText = 500

// DBStore is the Store on PostgreSQL.
type DBStore struct {
	db *sql.DB
	v  *SchemaValidator
	// OnStatus, if set, is called after a proposal is stored or changes
	// status (for the proposals metric); it must not block.
	OnStatus func(source string, status Status)
}

var _ Store = (*DBStore)(nil)

// NewStore returns the proposal store. v supplies the document and the API
// handler apply replays through.
func NewStore(db *sql.DB, v *SchemaValidator) *DBStore { return &DBStore{db: db, v: v} }

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

const cols = `id, source, session_id, finding_id, fingerprint, title, rationale, actions, config_revision, status,
	created_by, created_at, updated_at, applied_by, applied_at, dismissed_by, dismissed_at, dismiss_reason, dismiss_text, failure`

type scanner interface{ Scan(...any) error }

func scan(r scanner) (Proposal, error) {
	var (
		p                      Proposal
		sess, find, reason, tx sql.NullString
		by, ab, db             sql.NullInt64
		aat, dat               sql.NullTime
		actions, failure       []byte
		status                 string
	)
	if err := r.Scan(&p.ID, &p.Source, &sess, &find, &p.Fingerprint, &p.Title, &p.Rationale, &actions, &p.ConfigRevision,
		&status, &by, &p.CreatedAt, &p.UpdatedAt, &ab, &aat, &db, &dat, &reason, &tx, &failure); err != nil {
		return p, err
	}
	p.Status = Status(status)
	if err := json.Unmarshal(actions, &p.Actions); err != nil {
		return p, fmt.Errorf("proposal %s actions: %w", p.ID, err)
	}
	if failure != nil {
		p.Failure = new(Failure)
		if err := json.Unmarshal(failure, p.Failure); err != nil {
			return p, fmt.Errorf("proposal %s failure: %w", p.ID, err)
		}
	}
	p.SessionID, p.FindingID = nullStr(sess), nullStr(find)
	p.DismissReason, p.DismissText = nullStr(reason), nullStr(tx)
	p.CreatedBy, p.AppliedBy, p.DismissedBy = nullInt(by), nullInt(ab), nullInt(db)
	p.AppliedAt, p.DismissedAt = nullTime(aat), nullTime(dat)
	return p, nil
}

func nullStr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}

func nullInt(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	return &n.Int64
}

func nullTime(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

// advisoryKey serialises Upsert so two writers cannot both miss each
// other's open proposal.
const advisoryKey = `SELECT pg_advisory_xact_lock(hashtext('hello:ai:proposals'))`

// Upsert stores a validated draft (spec S-14): a fingerprint dismissed in
// the last 7 days gives ErrSuppressed; an open proposal with the same
// fingerprint is refreshed (title, rationale, before/after, revision); else
// a new one is inserted and older open proposals from the same session or
// finding that change the same targets become superseded.
func (s *DBStore) Upsert(ctx context.Context, d Draft) (string, error) {
	if len(d.Actions) == 0 || len(d.Actions) > MaxActions {
		return "", fmt.Errorf("a proposal has 1 to %d actions", MaxActions)
	}
	fp := Fingerprint(d.Source, d.Actions)
	actions, err := json.Marshal(d.Actions)
	if err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, advisoryKey); err != nil {
		return "", err
	}
	var rev int64
	if err := tx.QueryRowContext(ctx, `SELECT config_revision FROM schema_info`).Scan(&rev); err != nil {
		return "", err
	}
	var one int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM ai_proposals WHERE fingerprint = $1 AND status = 'dismissed'
		AND dismissed_at > now() - make_interval(secs => $2) LIMIT 1`, fp, SuppressionWindow.Seconds()).Scan(&one)
	if err == nil {
		return "", ErrSuppressed
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	var id string
	err = tx.QueryRowContext(ctx, `UPDATE ai_proposals SET title = $2, rationale = $3, actions = $4, config_revision = $5,
		updated_at = now() WHERE id = (SELECT id FROM ai_proposals WHERE fingerprint = $1 AND status = 'open'
		ORDER BY created_at LIMIT 1) RETURNING id`, fp, d.Title, d.Rationale, actions, rev).Scan(&id)
	switch {
	case err == nil:
		return id, tx.Commit()
	case !errors.Is(err, sql.ErrNoRows):
		return "", err
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO ai_proposals (id, source, session_id, finding_id, fingerprint, title, rationale,
		actions, config_revision, created_by) VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		d.Source, d.SessionID, d.FindingID, fp, d.Title, d.Rationale, actions, rev, d.CreatedBy).Scan(&id)
	if err != nil {
		return "", err
	}
	if err := supersede(ctx, tx, id, d); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	s.notify(d.Source, StatusOpen)
	return id, nil
}

func supersede(ctx context.Context, tx *sql.Tx, id string, d Draft) error {
	if d.SessionID == nil && d.FindingID == nil {
		return nil
	}
	mine := Targets(d.Actions)
	if len(mine) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, actions, source FROM ai_proposals WHERE status = 'open' AND id <> $1
		AND (($2::uuid IS NOT NULL AND session_id = $2) OR ($3::uuid IS NOT NULL AND finding_id = $3))`, id, d.SessionID, d.FindingID)
	if err != nil {
		return err
	}
	var older []string
	for rows.Next() {
		var oid, src string
		var raw []byte
		var as []Action
		if err := rows.Scan(&oid, &raw, &src); err != nil {
			_ = rows.Close()
			return err
		}
		if json.Unmarshal(raw, &as) == nil && intersects(mine, Targets(as)) {
			older = append(older, oid)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, oid := range older {
		if _, err := tx.ExecContext(ctx, `UPDATE ai_proposals SET status = 'superseded', updated_at = now() WHERE id = $1`, oid); err != nil {
			return err
		}
	}
	return nil
}

func (s *DBStore) notify(source string, st Status) {
	if s.OnStatus != nil {
		s.OnStatus(source, st)
	}
}

// Get returns one proposal.
func (s *DBStore) Get(ctx context.Context, id string) (Proposal, error) {
	if !uuidRE.MatchString(id) {
		return Proposal{}, ErrNotFound
	}
	p, err := scan(s.db.QueryRowContext(ctx, `SELECT `+cols+` FROM ai_proposals WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Proposal{}, ErrNotFound
	}
	return p, err
}

// List returns proposals newest first, optionally of one status and source.
func (s *DBStore) List(ctx context.Context, status Status, source string, limit int) ([]Proposal, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+cols+` FROM ai_proposals
		WHERE ($1 = '' OR status = $1) AND ($2 = '' OR source = $2) ORDER BY created_at DESC, id LIMIT $3`,
		string(status), source, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Proposal{}
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Counts returns the number of proposals by status.
func (s *DBStore) Counts(ctx context.Context) (map[Status]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, count(*) FROM ai_proposals GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[Status]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[Status(st)] = n
	}
	return out, rows.Err()
}

// lock reads a proposal for update, skipping a row another transaction
// holds: that one is being applied or dismissed, so it is not open to us.
func lock(ctx context.Context, tx *sql.Tx, id string) (Proposal, error) {
	if !uuidRE.MatchString(id) {
		return Proposal{}, ErrNotFound
	}
	p, err := scan(tx.QueryRowContext(ctx, `SELECT `+cols+` FROM ai_proposals WHERE id = $1 FOR UPDATE SKIP LOCKED`, id))
	if errors.Is(err, sql.ErrNoRows) {
		var one int
		if e := tx.QueryRowContext(ctx, `SELECT 1 FROM ai_proposals WHERE id = $1`, id).Scan(&one); e == nil {
			return Proposal{}, ErrNotOpen
		}
		return Proposal{}, ErrNotFound
	}
	if err != nil {
		return Proposal{}, err
	}
	if p.Status != StatusOpen {
		return p, ErrNotOpen
	}
	return p, nil
}

// audit writes the proposal's own audit row, naming the actor as the API's
// audit rows do (auth.Actor.String).
func audit(ctx context.Context, tx *sql.Tx, userID int64, action, id string) error {
	name := ""
	if a, ok := auth.ActorFrom(ctx); ok && a.UserID == userID {
		name = a.String()
	} else {
		var u string
		if err := tx.QueryRowContext(ctx, `SELECT username FROM users WHERE id = $1`, userID).Scan(&u); err != nil {
			u = fmt.Sprintf("%d", userID)
		}
		name = "user:" + u
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_events (actor, action, resource, resource_id) VALUES ($1, $2, 'ai_proposal', $3)`,
		name, action, id)
	return err
}

// Dismiss closes an open proposal (spec S-13).
func (s *DBStore) Dismiss(ctx context.Context, id string, userID int64, reason, text string) error {
	if !slices.Contains([]string{ReasonNotNeeded, ReasonWrong, ReasonLater, ReasonOther}, reason) {
		return fmt.Errorf("unknown dismissal reason %q", reason)
	}
	if utf8.RuneCountInString(text) > MaxDismissText {
		return fmt.Errorf("the dismissal text is longer than %d characters", MaxDismissText)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := lock(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ai_proposals SET status = 'dismissed', dismissed_by = $2, dismissed_at = now(),
		dismiss_reason = $3, dismiss_text = NULLIF($4, ''), updated_at = now() WHERE id = $1`, id, userID, reason, text); err != nil {
		return err
	}
	if err := audit(ctx, tx, userID, "dismiss", id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.notify(p.Source, StatusDismissed)
	return nil
}
