package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/azrtydxb/hello/internal/ai"
)

// The AI core's persistence (spec ai-agent S-15–S-17, S-23, S-26):
// *Store implements ai.Store.
var _ ai.Store = (*Store)(nil)

// AddAIUsage adds u to day's ai_usage row.
func (s *Store) AddAIUsage(ctx context.Context, day time.Time, u ai.Usage) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO ai_usage (day, input_tokens, output_tokens, reasoning_tokens, calls)
		VALUES ($1::date, $2, $3, $4, $5)
		ON CONFLICT (day) DO UPDATE SET
			input_tokens = ai_usage.input_tokens + EXCLUDED.input_tokens,
			output_tokens = ai_usage.output_tokens + EXCLUDED.output_tokens,
			reasoning_tokens = ai_usage.reasoning_tokens + EXCLUDED.reasoning_tokens,
			calls = ai_usage.calls + EXCLUDED.calls`,
		day.UTC().Format(time.DateOnly), u.InputTokens, u.OutputTokens, u.ReasoningTokens, u.Calls)
	return err
}

// AIUsage is day's usage, zero when nothing ran.
func (s *Store) AIUsage(ctx context.Context, day time.Time) (ai.Usage, error) {
	var u ai.Usage
	err := s.db.QueryRowContext(ctx, `
		SELECT input_tokens, output_tokens, reasoning_tokens, calls FROM ai_usage WHERE day = $1::date`,
		day.UTC().Format(time.DateOnly)).Scan(&u.InputTokens, &u.OutputTokens, &u.ReasoningTokens, &u.Calls)
	if errors.Is(err, sql.ErrNoRows) {
		return ai.Usage{}, nil
	}
	return u, err
}

// sessionLocked marks a context whose caller already holds the session's
// advisory lock in an open transaction (PostAIMessage): CreateAITask, on its
// own connection, must not wait for that lock or the post deadlocks.
type sessionLocked struct{}

// CreateAITask inserts a queued task. A task for a session serialises on
// the session (a transaction-scoped advisory lock) so two posts cannot both
// pass the running-task check.
func (s *Store) CreateAITask(ctx context.Context, t ai.Task) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	session := sql.NullString{String: t.SessionID, Valid: t.SessionID != ""}
	if session.Valid {
		if held, _ := ctx.Value(sessionLocked{}).(string); held != t.SessionID {
			if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('hello:ai:session:' || $1::text))`, t.SessionID); err != nil {
				return err
			}
		}
		var busy bool
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (SELECT 1 FROM ai_tasks WHERE session_id = $1 AND status IN ('queued', 'running'))`,
			t.SessionID).Scan(&busy); err != nil {
			return err
		}
		if busy {
			return ai.ErrTaskRunning
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ai_tasks (id, kind, requested_by, session_id, replica, status, created_at)
		VALUES ($1, $2, $3, $4, $5, 'queued', $6)`,
		t.ID, t.Kind, t.RequestedBy, session, t.Replica, t.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

// StartAITask marks a queued task running.
func (s *Store) StartAITask(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE ai_tasks SET status = 'running', started_at = $2 WHERE id = $1 AND status = 'queued'`, id, at)
	return err
}

// FinishAITask ends a task that has not already been failed (by the stale
// check of another replica).
func (s *Store) FinishAITask(ctx context.Context, id string, status ai.TaskStatus, code, message string, result json.RawMessage, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE ai_tasks SET status = $2, error_code = NULLIF($3, ''), error_message = NULLIF($4, ''), result = $5, finished_at = $6
		WHERE id = $1 AND status IN ('queued', 'running')`,
		id, string(status), code, message, nullJSON(result), at)
	return err
}

func nullJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return []byte(b)
}

// AITask returns a task, ai.ErrNotFound when there is none.
func (s *Store) AITask(ctx context.Context, id string) (ai.Task, error) {
	var (
		t                   ai.Task
		session, code       sql.NullString
		msg                 sql.NullString
		result              []byte
		started, finishedAt sql.NullTime
		status              string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, kind, requested_by, session_id, replica, status, error_code, error_message, result, created_at, started_at, finished_at
		FROM ai_tasks WHERE id = $1`, id).
		Scan(&t.ID, &t.Kind, &t.RequestedBy, &session, &t.Replica, &status, &code, &msg, &result, &t.CreatedAt, &started, &finishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ai.Task{}, ai.ErrNotFound
	}
	if err != nil {
		return ai.Task{}, err
	}
	t.SessionID, t.Status, t.ErrorCode, t.ErrorMessage = session.String, ai.TaskStatus(status), code.String, msg.String
	t.Result = result
	if started.Valid {
		t.StartedAt = &started.Time
	}
	if finishedAt.Valid {
		t.FinishedAt = &finishedAt.Time
	}
	return t, nil
}

// AITaskReplicas lists the replicas with unfinished tasks created before.
func (s *Store) AITaskReplicas(ctx context.Context, before time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT replica FROM ai_tasks WHERE status IN ('queued', 'running') AND created_at < $1`, before)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FailAITasks fails replica's unfinished tasks with code.
func (s *Store) FailAITasks(ctx context.Context, replica, code, message string, at time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE ai_tasks SET status = 'failed', error_code = $2, error_message = $3, finished_at = $4
		WHERE replica = $1 AND status IN ('queued', 'running')`, replica, code, message, at)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// TryAILock takes pg_try_advisory_lock(hashtext(key)) on a connection of
// its own, held until unlock (or until the process dies and PostgreSQL
// drops the connection).
func (s *Store) TryAILock(ctx context.Context, key string) (func(), bool, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	var ok bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtext($1::text))`, key).Scan(&ok); err != nil {
		_ = conn.Close()
		return nil, false, err
	}
	if !ok {
		_ = conn.Close()
		return nil, false, nil
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtext($1::text))`, key); err != nil {
			// The lock is the session's: discard the connection so it goes.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = conn.Close()
	}, true, nil
}

// LastAgentRun is agent's latest run, nil when it never ran.
func (s *Store) LastAgentRun(ctx context.Context, agent string) (*ai.AgentRun, error) {
	var (
		r        ai.AgentRun
		finished sql.NullTime
		outcome  sql.NullString
		errText  sql.NullString
		detail   []byte
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, agent, replica, started_at, finished_at, outcome, tokens, detail, error
		FROM ai_agent_runs WHERE agent = $1 ORDER BY started_at DESC, id DESC LIMIT 1`, agent).
		Scan(&r.ID, &r.Agent, &r.Replica, &r.StartedAt, &finished, &outcome, &r.Tokens, &detail, &errText)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if finished.Valid {
		r.FinishedAt = &finished.Time
	}
	r.Outcome, r.Error = ai.Outcome(outcome.String), errText.String
	if len(detail) > 0 {
		_ = json.Unmarshal(detail, &r.Detail)
	}
	return &r, nil
}

// AgentRunRequested reports a waiting run-now request.
func (s *Store) AgentRunRequested(ctx context.Context, agent string) (bool, error) {
	var ok bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM ai_agent_requests WHERE agent = $1)`, agent).Scan(&ok)
	return ok, err
}

// RequestAgentRun records (or keeps) agent's run-now request.
func (s *Store) RequestAgentRun(ctx context.Context, agent string, userID int64, at time.Time) (time.Time, error) {
	var when time.Time
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO ai_agent_requests (agent, requested_by, requested_at) VALUES ($1, NULLIF($2::bigint, 0), $3)
		ON CONFLICT (agent) DO UPDATE SET requested_by = EXCLUDED.requested_by
		RETURNING requested_at`, agent, userID, at).Scan(&when)
	return when, err
}

// TakeAgentRequest deletes agent's run-now request.
func (s *Store) TakeAgentRequest(ctx context.Context, agent string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM ai_agent_requests WHERE agent = $1`, agent)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// StartAgentRun inserts a run row.
func (s *Store) StartAgentRun(ctx context.Context, agent, replica string, at time.Time) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO ai_agent_runs (agent, replica, started_at) VALUES ($1, $2, $3) RETURNING id`, agent, replica, at).Scan(&id)
	return id, err
}

// FinishAgentRun closes a run row.
func (s *Store) FinishAgentRun(ctx context.Context, id int64, r ai.AgentRun) error {
	var detail any
	if len(r.Detail) > 0 {
		b, err := json.Marshal(r.Detail)
		if err != nil {
			return err
		}
		detail = b
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE ai_agent_runs SET finished_at = $2, outcome = $3, tokens = $4, detail = $5, error = NULLIF($6, '')
		WHERE id = $1`, id, r.FinishedAt, string(r.Outcome), r.Tokens, detail, r.Error)
	return err
}

// CloseOrphanRuns fails agent's runs that never finished.
func (s *Store) CloseOrphanRuns(ctx context.Context, agent string, at time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE ai_agent_runs SET finished_at = $2, outcome = 'failed', error = 'instance_stopped'
		WHERE agent = $1 AND finished_at IS NULL`, agent, at)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneAI deletes the AI rows past their S-26 retention.
func (s *Store) PruneAI(ctx context.Context, now time.Time) (map[string]int64, error) {
	steps := []struct {
		name, sql string
		arg       any
	}{
		{"tasks", `DELETE FROM ai_tasks WHERE created_at < $1`, now.Add(-ai.RetainTasks)},
		{"agent_runs", `DELETE FROM ai_agent_runs WHERE started_at < $1`, now.Add(-ai.RetainRuns)},
		{"samples", `DELETE FROM ai_samples WHERE at < $1`, now.Add(-ai.RetainSamples)},
		{"findings", `DELETE FROM ai_findings WHERE status IN ('resolved', 'dismissed')
			AND COALESCE(resolved_at, dismissed_at, last_seen) < $1`, now.Add(-ai.RetainFindings)},
		{"proposals", `DELETE FROM ai_proposals WHERE status IN ('applied', 'failed', 'stale', 'dismissed', 'superseded')
			AND updated_at < $1`, now.Add(-ai.RetainProposals)},
		{"sessions", `DELETE FROM ai_sessions WHERE last_active_at < $1`, now.Add(-ai.RetainSessions)},
		{"usage", `DELETE FROM ai_usage WHERE day < $1::date`, now.Add(-ai.RetainUsage).UTC().Format(time.DateOnly)},
	}
	out := map[string]int64{}
	for _, st := range steps {
		res, err := s.db.ExecContext(ctx, st.sql, st.arg)
		if err != nil {
			return out, fmt.Errorf("prune %s: %w", st.name, err)
		}
		out[st.name], _ = res.RowsAffected()
	}
	return out, nil
}

// AICounts counts live findings by severity, the health score over open
// ones (max(0, 10 - 3 x critical - warning)) and open proposals.
func (s *Store) AICounts(ctx context.Context) (ai.Counts, error) {
	var c ai.Counts
	var openCrit, openWarn int
	err := s.db.QueryRowContext(ctx, `
		SELECT
			count(*) FILTER (WHERE severity = 'info'),
			count(*) FILTER (WHERE severity = 'warning'),
			count(*) FILTER (WHERE severity = 'critical'),
			count(*) FILTER (WHERE severity = 'warning' AND status = 'open'),
			count(*) FILTER (WHERE severity = 'critical' AND status = 'open'),
			(SELECT count(*) FROM ai_proposals WHERE status = 'open')
		FROM ai_findings WHERE status IN ('open', 'acknowledged')`).
		Scan(&c.Info, &c.Warning, &c.Critical, &openWarn, &openCrit, &c.OpenProposals)
	c.HealthScore = max(0, 10-3*openCrit-openWarn)
	return c, err
}
