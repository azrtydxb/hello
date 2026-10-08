package store

// AIOps findings (spec ai-agent S-18, S-21): one open-or-acknowledged row
// per candidate id, dismissed rows that suppress it for 24 hours, and
// automatic resolution. The detectors and the explanation live in
// internal/ai/detect; this file is only the lifecycle's SQL.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Finding severities, status values and lifecycle windows.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"

	FindingOpen         = "open"
	FindingAcknowledged = "acknowledged"
	FindingDismissed    = "dismissed"
	FindingResolved     = "resolved"

	// FindingDismissSuppression is how long a dismissed finding is not
	// raised again unless its severity rises.
	FindingDismissSuppression = 24 * time.Hour
	// FindingResolveAfter is how long a candidate may be absent before its
	// finding resolves.
	FindingResolveAfter = 30 * time.Minute
)

// ErrFindingState is returned by a lifecycle action the finding's status
// does not allow (acknowledging a dismissed finding).
var ErrFindingState = errors.New("store: finding is not in a state that allows this")

func severityRank(s string) int {
	switch s {
	case SeverityCritical:
		return 2
	case SeverityWarning:
		return 1
	}
	return 0
}

// AIFindingInput is one candidate a detector produced.
type AIFindingInput struct {
	CandidateID, Type, Subject, Severity, Title string
	Evidence                                    json.RawMessage
}

// AIExplanation is the model's explanation of one finding.
type AIExplanation struct {
	Summary     string   `json:"summary"`
	LikelyCause string   `json:"likelyCause"`
	NextStep    string   `json:"nextStep"`
	RelatedIDs  []string `json:"relatedIds,omitempty"`
}

// AISeverityChange is one entry of a finding's severity history.
type AISeverityChange struct {
	Severity string    `json:"severity"`
	At       time.Time `json:"at"`
}

// AIFinding is a stored finding; the JSON is the API's AIFinding schema.
type AIFinding struct {
	ID              string             `json:"id"`
	CandidateID     string             `json:"candidateId"`
	Type            string             `json:"type"`
	Subject         string             `json:"subject"`
	Severity        string             `json:"severity"`
	Status          string             `json:"status"`
	Title           string             `json:"title"`
	Evidence        json.RawMessage    `json:"evidence"`
	Explanation     *AIExplanation     `json:"explanation"`
	Explained       bool               `json:"explained"`
	Rank            *int               `json:"rank"`
	FirstSeen       time.Time          `json:"firstSeen"`
	LastSeen        time.Time          `json:"lastSeen"`
	Occurrences     int64              `json:"occurrences"`
	SeverityHistory []AISeverityChange `json:"severityHistory"`
	AcknowledgedBy  *int64             `json:"acknowledgedBy"`
	AcknowledgedAt  *time.Time         `json:"acknowledgedAt"`
	DismissedBy     *int64             `json:"dismissedBy"`
	DismissedAt     *time.Time         `json:"dismissedAt"`
	DismissReason   *string            `json:"dismissReason"`
	ResolvedAt      *time.Time         `json:"resolvedAt"`
	ProposalID      *string            `json:"proposalId"`
}

const findingCols = `f.id, f.candidate_id, f.type, f.subject, f.severity, f.status, f.title, f.evidence,
	f.explanation, f.explained, f.rank, f.first_seen, f.last_seen, f.occurrences, f.severity_history,
	f.acknowledged_by, f.acknowledged_at, f.dismissed_by, f.dismissed_at, f.dismiss_reason, f.resolved_at,
	(SELECT p.id::text FROM ai_proposals p WHERE p.finding_id = f.id AND p.status = 'open'
	 ORDER BY p.created_at DESC LIMIT 1)`

func scanFinding(r rowScanner) (AIFinding, error) {
	var f AIFinding
	var expl, hist []byte
	err := r.Scan(&f.ID, &f.CandidateID, &f.Type, &f.Subject, &f.Severity, &f.Status, &f.Title, &f.Evidence,
		&expl, &f.Explained, &f.Rank, &f.FirstSeen, &f.LastSeen, &f.Occurrences, &hist,
		&f.AcknowledgedBy, &f.AcknowledgedAt, &f.DismissedBy, &f.DismissedAt, &f.DismissReason, &f.ResolvedAt, &f.ProposalID)
	if err != nil {
		return f, err
	}
	if expl != nil {
		f.Explanation = new(AIExplanation)
		if err := json.Unmarshal(expl, f.Explanation); err != nil {
			return f, err
		}
	}
	if err := json.Unmarshal(hist, &f.SeverityHistory); err != nil {
		return f, err
	}
	return f, nil
}

func appendHistory(hist []byte, severity string, at time.Time) ([]byte, error) {
	var h []AISeverityChange
	if err := json.Unmarshal(hist, &h); err != nil {
		return nil, err
	}
	return json.Marshal(append(h, AISeverityChange{Severity: severity, At: at}))
}

// UpsertAIFindings applies one run of the detectors at now (spec S-21):
// each input refreshes its live finding, raises or reopens one, or is
// suppressed by a dismissal of the last 24 hours it does not out-rank; live
// findings whose candidate has been absent for 30 minutes resolve, except
// those of a type in skipResolve (a detector that failed this run proves
// nothing about its candidates).
func (s *Store) UpsertAIFindings(ctx context.Context, now time.Time, in []AIFindingInput, skipResolve []string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, c := range in {
			if len(c.Evidence) == 0 {
				c.Evidence = json.RawMessage(`{}`)
			}
			if err := upsertFinding(ctx, tx, now, c); err != nil {
				return err
			}
		}
		if skipResolve == nil {
			skipResolve = []string{}
		}
		_, err := tx.ExecContext(ctx, `UPDATE ai_findings SET status = 'resolved', resolved_at = $1
			WHERE status IN ('open', 'acknowledged') AND last_seen < $2 AND NOT (type = ANY($3))`,
			now, now.Add(-FindingResolveAfter), skipResolve)
		return err
	})
}

func upsertFinding(ctx context.Context, tx *sql.Tx, now time.Time, c AIFindingInput) error {
	var id, sev, status string
	var hist []byte
	err := tx.QueryRowContext(ctx, `SELECT id, severity, status, severity_history FROM ai_findings
		WHERE candidate_id = $1 AND status IN ('open', 'acknowledged')`, c.CandidateID).Scan(&id, &sev, &status, &hist)
	if err == nil {
		if c.Severity != sev {
			if hist, err = appendHistory(hist, c.Severity, now); err != nil {
				return err
			}
			if severityRank(c.Severity) > severityRank(sev) {
				status = FindingOpen // a rise is a new alert, acknowledged or not
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE ai_findings SET severity = $2, status = $3::text, severity_history = $4,
			title = $5, evidence = $6, last_seen = $7, occurrences = occurrences + 1,
			explained = CASE WHEN severity <> $2 THEN false ELSE explained END,
			explanation = CASE WHEN severity <> $2 THEN NULL ELSE explanation END,
			acknowledged_by = CASE WHEN $3::text = 'open' THEN NULL ELSE acknowledged_by END,
			acknowledged_at = CASE WHEN $3::text = 'open' THEN NULL ELSE acknowledged_at END
			WHERE id = $1`, id, c.Severity, status, hist, c.Title, []byte(c.Evidence), now)
		return err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	err = tx.QueryRowContext(ctx, `SELECT id, severity, severity_history FROM ai_findings
		WHERE candidate_id = $1 AND status = 'dismissed' AND dismissed_at > $2
		ORDER BY dismissed_at DESC LIMIT 1`, c.CandidateID, now.Add(-FindingDismissSuppression)).Scan(&id, &sev, &hist)
	switch {
	case err == nil:
		if severityRank(c.Severity) <= severityRank(sev) {
			return nil // suppressed
		}
		if hist, err = appendHistory(hist, c.Severity, now); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE ai_findings SET status = 'open', severity = $2, severity_history = $3,
			title = $4, evidence = $5, last_seen = $6, occurrences = occurrences + 1, explained = false,
			dismissed_by = NULL, dismissed_at = NULL, dismiss_reason = NULL WHERE id = $1`,
			id, c.Severity, hist, c.Title, []byte(c.Evidence), now)
		return err
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	hist, err = json.Marshal([]AISeverityChange{{Severity: c.Severity, At: now}})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO ai_findings
		(id, candidate_id, type, subject, severity, title, evidence, first_seen, last_seen, severity_history)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $7, $8)`,
		c.CandidateID, c.Type, c.Subject, c.Severity, c.Title, []byte(c.Evidence), now, hist)
	return err
}

// AIFindingFilter selects findings for ListAIFindings; empty fields match all.
type AIFindingFilter struct {
	Status, Severity, Type string
	Limit                  int
}

// ListAIFindings returns findings, most severe first, then best ranked,
// then newest.
func (s *Store) ListAIFindings(ctx context.Context, f AIFindingFilter) ([]AIFinding, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+findingCols+` FROM ai_findings f
		WHERE ($1 = '' OR f.status = $1) AND ($2 = '' OR f.severity = $2) AND ($3 = '' OR f.type = $3)
		ORDER BY CASE f.severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END,
		         f.rank NULLS LAST, f.last_seen DESC, f.id LIMIT $4`, f.Status, f.Severity, f.Type, f.Limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []AIFinding{}
	for rows.Next() {
		x, err := scanFinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// GetAIFinding returns one finding; id is a UUID, anything else is
// ErrNotFound.
func (s *Store) GetAIFinding(ctx context.Context, id string) (AIFinding, error) {
	f, err := scanFinding(s.db.QueryRowContext(ctx, `SELECT `+findingCols+` FROM ai_findings f WHERE f.id::text = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return f, ErrNotFound
	}
	return f, err
}

// AIHealthScore is max(0, 10 - 3 x critical - warning) over open findings
// (spec S-21).
func (s *Store) AIHealthScore(ctx context.Context) (int, error) {
	var critical, warning int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE severity = 'critical'),
		count(*) FILTER (WHERE severity = 'warning') FROM ai_findings WHERE status = 'open'`).Scan(&critical, &warning)
	return HealthScore(critical, warning), err
}

// HealthScore is the dashboard score, computed by code and never by the model.
func HealthScore(critical, warning int) int { return max(0, 10-3*critical-warning) }

// AIExplained is one finding's explanation to store.
type AIExplained struct {
	ID          string
	Explanation AIExplanation
	Rank        *int
}

// SetAIExplanations stores the model's explanations and ranks. Only open
// findings are touched; the ranks of the other open findings are cleared,
// so a rank always belongs to the latest ranking.
func (s *Store) SetAIExplanations(ctx context.Context, xs []AIExplained) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE ai_findings SET rank = NULL WHERE status = 'open'`); err != nil {
			return err
		}
		for _, x := range xs {
			b, err := json.Marshal(x.Explanation)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE ai_findings SET explanation = $2, explained = true, rank = $3
				WHERE id::text = $1 AND status = 'open'`, x.ID, b, x.Rank); err != nil {
				return err
			}
		}
		return nil
	})
}

// AcknowledgeAIFinding marks an open finding acknowledged (ErrFindingState
// otherwise) and audits it.
func (s *Store) AcknowledgeAIFinding(ctx context.Context, actor string, userID int64, id string) (AIFinding, error) {
	return s.findingAction(ctx, actor, "acknowledge", id, `UPDATE ai_findings SET status = 'acknowledged',
		acknowledged_by = $2, acknowledged_at = now() WHERE id::text = $1 AND status = 'open'`, userID)
}

// DismissAIFinding dismisses an open or acknowledged finding with a reason
// (ErrFindingState otherwise) and audits it; the candidate is then not
// raised for 24 hours unless its severity rises.
func (s *Store) DismissAIFinding(ctx context.Context, actor string, userID int64, id, reason string) (AIFinding, error) {
	return s.findingAction(ctx, actor, "dismiss", id, `UPDATE ai_findings SET status = 'dismissed',
		dismissed_by = $2, dismissed_at = now(), dismiss_reason = $3, rank = NULL
		WHERE id::text = $1 AND status IN ('open', 'acknowledged')`, userID, reason)
}

func (s *Store) findingAction(ctx context.Context, actor, action, id, query string, args ...any) (AIFinding, error) {
	var out AIFinding
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, query, append([]any{id}, args...)...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var exists bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM ai_findings WHERE id::text = $1)`, id).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return ErrNotFound
			}
			return ErrFindingState
		}
		if err := insertAudit(ctx, tx, actor, action, "ai_finding", id); err != nil {
			return err
		}
		out, err = scanFinding(tx.QueryRowContext(ctx, `SELECT `+findingCols+` FROM ai_findings f WHERE f.id::text = $1`, id))
		return err
	})
	return out, err
}

// AIReadIdentity is the user a detector's proposals read as: the oldest
// admin (a deleted user's reads are refused, so it is looked up per run).
// ErrNotFound when there is none.
func (s *Store) AIReadIdentity(ctx context.Context) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE role = 'admin' ORDER BY id LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}
