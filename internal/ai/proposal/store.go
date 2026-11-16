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

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/advisory"

	"github.com/azrtydxb/hello/internal/auth"
)

// ErrNotFound is a proposal id that names no row.
var ErrNotFound = errorString("proposal not found")

// SuppressionWindow is how long a dismissed fingerprint is not proposed
// again (spec S-14).
const SuppressionWindow = 7 * 24 * time.Hour

// MaxDismissText is the longest dismissal text, in characters.
const MaxDismissText = 500

// DBStore is the Store on PostgreSQL, on momo (internal/db). The statements
// are hand-written SQL with @name binds; a missing row is ErrNotFound as
// before, and any other error is the driver's (a *pgconn.PgError is still
// reachable with errors.As).
type DBStore struct {
	db *momo.DB
	v  *SchemaValidator
	// OnStatus, if set, is called after a proposal is stored or changes
	// status (for the proposals metric); it must not block.
	OnStatus func(source string, status Status)
}

var _ Store = (*DBStore)(nil)

// NewStore returns the proposal store. v supplies the document and the API
// handler apply replays through.
func NewStore(db *momo.DB, v *SchemaValidator) *DBStore { return &DBStore{db: db, v: v} }

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

const cols = `id, source, session_id, finding_id, fingerprint, title, rationale, actions, config_revision, status,
	created_by, created_at, updated_at, applied_by, applied_at, dismissed_by, dismissed_at, dismiss_reason, dismiss_text, failure,
	applied_via, batch_id`

// row is a cols row.
type row struct {
	ID             string `momo:"column:id"`
	Source         string
	SessionID      sql.NullString `momo:"column:session_id"`
	FindingID      sql.NullString `momo:"column:finding_id"`
	Fingerprint    string
	Title          string
	Rationale      string
	Actions        []byte
	ConfigRevision int64
	Status         string
	CreatedBy      sql.NullInt64
	CreatedAt      time.Time
	UpdatedAt      time.Time
	AppliedBy      sql.NullInt64
	AppliedAt      sql.NullTime
	DismissedBy    sql.NullInt64
	DismissedAt    sql.NullTime
	DismissReason  sql.NullString
	DismissText    sql.NullString
	Failure        []byte
	AppliedVia     sql.NullString
	BatchID        sql.NullString `momo:"column:batch_id"`
}

// one returns the single row of rows, or sql.ErrNoRows when there is none,
// the error QueryRow(...).Scan gave, so the not-found checks hold as before.
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

// scan is the single cols row of a query, as a Proposal; no row is
// sql.ErrNoRows.
func scan(rows []row, err error) (Proposal, error) {
	r, err := one(rows, err)
	if err != nil {
		return Proposal{}, err
	}
	return r.proposal()
}

func (r row) proposal() (Proposal, error) {
	p := Proposal{ID: r.ID, Source: r.Source, Fingerprint: r.Fingerprint, Title: r.Title, Rationale: r.Rationale,
		ConfigRevision: r.ConfigRevision, Status: Status(r.Status), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	if err := json.Unmarshal(r.Actions, &p.Actions); err != nil {
		return p, fmt.Errorf("proposal %s actions: %w", p.ID, err)
	}
	if r.Failure != nil {
		p.Failure = new(Failure)
		if err := json.Unmarshal(r.Failure, p.Failure); err != nil {
			return p, fmt.Errorf("proposal %s failure: %w", p.ID, err)
		}
	}
	p.SessionID, p.FindingID = nullStr(r.SessionID), nullStr(r.FindingID)
	p.DismissReason, p.DismissText = nullStr(r.DismissReason), nullStr(r.DismissText)
	p.AppliedVia, p.BatchID = nullStr(r.AppliedVia), nullStr(r.BatchID)
	p.CreatedBy, p.AppliedBy, p.DismissedBy = nullInt(r.CreatedBy), nullInt(r.AppliedBy), nullInt(r.DismissedBy)
	p.AppliedAt, p.DismissedAt = nullTime(r.AppliedAt), nullTime(r.DismissedAt)
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
// other's open proposal: pg_advisory_xact_lock(hashtext(advisoryKey)), the
// key every Hello version takes.
const advisoryKey = "hello:ai:proposals"

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
	tx, err := s.db.Begin(ctx, momo.TxOptions{})
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := advisory.XactLockString(ctx, tx, advisoryKey); err != nil {
		return "", err
	}
	rev, err := revision(ctx, tx)
	if err != nil {
		return "", err
	}
	_, err = suppressed(ctx, tx, fp)
	if err == nil {
		return "", ErrSuppressed
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id, err := refresh(ctx, tx, fp, d, actions, rev)
	switch {
	case err == nil:
		return id, tx.Commit(ctx)
	case !errors.Is(err, sql.ErrNoRows):
		return "", err
	}
	id, err = insert(ctx, tx, fp, d, actions, rev)
	if err != nil {
		return "", err
	}
	if err := supersede(ctx, tx, id, d); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	s.notify(d.Source, StatusOpen)
	return id, nil
}

// revision is the configuration revision (schema_info's one row).
func revision(ctx context.Context, q momo.Querier) (int64, error) {
	return one(momo.Scalars[int64](ctx, q, `SELECT config_revision FROM schema_info`))
}

// suppressed finds a dismissal of fp in the suppression window;
// sql.ErrNoRows when there is none.
func suppressed(ctx context.Context, q momo.Querier, fp string) (int, error) {
	return one(momo.Scalars[int](ctx, q, `SELECT 1 FROM ai_proposals WHERE fingerprint = @fp AND status = 'dismissed'
		AND dismissed_at > now() - make_interval(secs => @secs) LIMIT 1`, momo.Named("fp", fp), momo.Named("secs", SuppressionWindow.Seconds())))
}

// refresh updates the oldest open proposal with fingerprint fp to d and
// returns its id; sql.ErrNoRows when there is none.
func refresh(ctx context.Context, q momo.Querier, fp string, d Draft, actions []byte, rev int64) (string, error) {
	return one(momo.Scalars[string](ctx, q, `UPDATE ai_proposals SET title = @title, rationale = @rationale, actions = @actions,
		config_revision = @rev, updated_at = now() WHERE id = (SELECT id FROM ai_proposals WHERE fingerprint = @fp AND status = 'open'
		ORDER BY created_at LIMIT 1) RETURNING id`, momo.Named("fp", fp), momo.Named("title", d.Title), momo.Named("rationale", d.Rationale),
		momo.Named("actions", actions), momo.Named("rev", rev)))
}

// insert stores d as a new open proposal and returns its id.
func insert(ctx context.Context, q momo.Querier, fp string, d Draft, actions []byte, rev int64) (string, error) {
	return one(momo.Scalars[string](ctx, q, `INSERT INTO ai_proposals (id, source, session_id, finding_id, fingerprint, title, rationale,
		actions, config_revision, created_by) VALUES (gen_random_uuid(), @source, @session, @finding, @fp, @title, @rationale, @actions, @rev,
		@by) RETURNING id`, momo.Named("source", d.Source), momo.Named("session", d.SessionID), momo.Named("finding", d.FindingID),
		momo.Named("fp", fp), momo.Named("title", d.Title), momo.Named("rationale", d.Rationale), momo.Named("actions", actions),
		momo.Named("rev", rev), momo.Named("by", d.CreatedBy)))
}

func supersede(ctx context.Context, tx momo.Querier, id string, d Draft) error {
	if d.SessionID == nil && d.FindingID == nil {
		return nil
	}
	mine := Targets(d.Actions)
	if len(mine) == 0 {
		return nil
	}
	rows, err := momo.Query[momo.Row3[string, []byte, string]](ctx, tx, `SELECT id, actions, source FROM ai_proposals
		WHERE status = 'open' AND id <> @id
		AND ((@session::uuid IS NOT NULL AND session_id = @session) OR (@finding::uuid IS NOT NULL AND finding_id = @finding))`,
		momo.Named("id", id), momo.Named("session", d.SessionID), momo.Named("finding", d.FindingID))
	if err != nil {
		return err
	}
	var older []string
	for _, r := range rows {
		var as []Action
		if json.Unmarshal(r.C2, &as) == nil && intersects(mine, Targets(as)) {
			older = append(older, r.C1)
		}
	}
	for _, oid := range older {
		if _, err := momo.Exec(ctx, tx, `UPDATE ai_proposals SET status = 'superseded', updated_at = now() WHERE id = @id`,
			momo.Named("id", oid)); err != nil {
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
	p, err := scan(momo.Query[row](ctx, s.db, `SELECT `+cols+` FROM ai_proposals WHERE id = @id`, momo.Named("id", id)))
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
	rows, err := momo.Query[row](ctx, s.db, `SELECT `+cols+` FROM ai_proposals
		WHERE (@status::text = '' OR status = @status) AND (@source::text = '' OR source = @source) ORDER BY created_at DESC, id LIMIT @limit`,
		momo.Named("status", string(status)), momo.Named("source", source), momo.Named("limit", limit))
	if err != nil {
		return nil, err
	}
	out := make([]Proposal, 0, len(rows))
	for _, r := range rows {
		p, err := r.proposal()
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Counts returns the number of proposals by status.
func (s *DBStore) Counts(ctx context.Context) (map[Status]int, error) {
	rows, err := momo.Query[momo.Row2[string, int]](ctx, s.db, `SELECT status, count(*) FROM ai_proposals GROUP BY status`)
	if err != nil {
		return nil, err
	}
	out := map[Status]int{}
	for _, r := range rows {
		out[Status(r.C1)] = r.C2
	}
	return out, nil
}

// lock reads a proposal for update, skipping a row another transaction
// holds: that one is being applied or dismissed, so it is not open to us.
func lock(ctx context.Context, tx momo.Querier, id string) (Proposal, error) {
	if !uuidRE.MatchString(id) {
		return Proposal{}, ErrNotFound
	}
	p, err := scan(momo.Query[row](ctx, tx, `SELECT `+cols+` FROM ai_proposals WHERE id = @id FOR UPDATE SKIP LOCKED`, momo.Named("id", id)))
	if errors.Is(err, sql.ErrNoRows) {
		if _, e := one(momo.Scalars[int](ctx, tx, `SELECT 1 FROM ai_proposals WHERE id = @id`, momo.Named("id", id))); e == nil {
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
// audit rows do (auth.Actor.String), with via when the change was not the
// actor's own click (the assistant applying for them: "ai:<session>").
func audit(ctx context.Context, tx momo.Querier, userID int64, action, id, via string) error {
	name := ""
	if a, ok := auth.ActorFrom(ctx); ok && a.UserID == userID {
		name = a.String()
	} else {
		u, err := one(momo.Scalars[string](ctx, tx, `SELECT username FROM users WHERE id = @id`, momo.Named("id", userID)))
		if err != nil {
			u = fmt.Sprintf("%d", userID)
		}
		name = "user:" + u
	}
	_, err := momo.Exec(ctx, tx, `INSERT INTO audit_events (actor, action, resource, resource_id, via)
		VALUES (@actor, @action, 'ai_proposal', @id, NULLIF(@via, ''))`,
		momo.Named("actor", name), momo.Named("action", action), momo.Named("id", id), momo.Named("via", via))
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
	tx, err := s.db.Begin(ctx, momo.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	p, err := lock(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := markDismissed(ctx, tx, id, userID, reason, text); err != nil {
		return err
	}
	if err := audit(ctx, tx, userID, "dismiss", id, ""); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.notify(p.Source, StatusDismissed)
	return nil
}

// markDismissed records the dismissal of proposal id.
func markDismissed(ctx context.Context, q momo.Querier, id string, userID int64, reason, text string) error {
	_, err := momo.Exec(ctx, q, `UPDATE ai_proposals SET status = 'dismissed', dismissed_by = @by, dismissed_at = now(),
		dismiss_reason = @reason, dismiss_text = NULLIF(@text, ''), updated_at = now() WHERE id = @id`,
		momo.Named("id", id), momo.Named("by", userID), momo.Named("reason", reason), momo.Named("text", text))
	return err
}
