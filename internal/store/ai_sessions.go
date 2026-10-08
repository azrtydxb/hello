package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"slices"

	"github.com/azrtydxb/hello/internal/ai/assistant/chat"
)

// The assistant's sessions and messages (spec ai-agent S-8); Store
// implements chat.Store.

var _ chat.Store = (*Store)(nil)

// uuidText matches a UUID in text form; anything else is not a session id
// (and would be a PostgreSQL syntax error, not a 404).
var uuidText = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

const aiSessionCols = `id::text, owner, title, created_at, last_active_at`

func scanAISession(row interface{ Scan(...any) error }) (chat.Session, error) {
	var s chat.Session
	err := row.Scan(&s.ID, &s.Owner, &s.Title, &s.CreatedAt, &s.LastActiveAt)
	if errors.Is(err, sql.ErrNoRows) {
		return s, chat.ErrNotFound
	}
	return s, err
}

// CreateAISession starts a session owned by owner.
func (s *Store) CreateAISession(ctx context.Context, owner int64, title string) (chat.Session, error) {
	return scanAISession(s.db.QueryRowContext(ctx,
		`INSERT INTO ai_sessions (id, owner, title) VALUES (gen_random_uuid(), $1, $2) RETURNING `+aiSessionCols, owner, title))
}

// ListAISessions lists owner's sessions, or all with owner 0, most recently
// active first.
func (s *Store) ListAISessions(ctx context.Context, owner int64) ([]chat.Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+aiSessionCols+` FROM ai_sessions
		WHERE $1::bigint = 0 OR owner = $1::bigint ORDER BY last_active_at DESC, id`, owner)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []chat.Session{}
	for rows.Next() {
		v, err := scanAISession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetAISession returns one session.
func (s *Store) GetAISession(ctx context.Context, id string) (chat.Session, error) {
	if !uuidText.MatchString(id) {
		return chat.Session{}, chat.ErrNotFound
	}
	return scanAISession(s.db.QueryRowContext(ctx, `SELECT `+aiSessionCols+` FROM ai_sessions WHERE id = $1`, id))
}

// RenameAISession sets a session's title.
func (s *Store) RenameAISession(ctx context.Context, id, title string) (chat.Session, error) {
	if !uuidText.MatchString(id) {
		return chat.Session{}, chat.ErrNotFound
	}
	return scanAISession(s.db.QueryRowContext(ctx,
		`UPDATE ai_sessions SET title = $2 WHERE id = $1 RETURNING `+aiSessionCols, id, title))
}

// lockAISession serialises the writes of one session in tx; posts and
// deletes take it so a check for a running task cannot race a new task.
func lockAISession(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('hello:ai:session:' || $1::text))`, id)
	return err
}

const activeAITask = `SELECT EXISTS (SELECT 1 FROM ai_tasks WHERE session_id = $1 AND status IN ('queued', 'running'))`

// DeleteAISession deletes a session and, by cascade, its messages; refused
// while a task works for it.
func (s *Store) DeleteAISession(ctx context.Context, id string) error {
	if !uuidText.MatchString(id) {
		return chat.ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockAISession(ctx, tx, id); err != nil {
		return err
	}
	var busy bool
	if err := tx.QueryRowContext(ctx, activeAITask, id).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return chat.ErrTaskRunning
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM ai_sessions WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return chat.ErrNotFound
	}
	return tx.Commit()
}

const aiMessageCols = `id, session_id::text, role, content, tool_calls, citations, proposal_id::text, task_id::text, created_at`

func scanAIMessage(row interface{ Scan(...any) error }) (chat.Message, error) {
	var m chat.Message
	var calls, cites []byte
	if err := row.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &calls, &cites, &m.ProposalID, &m.TaskID, &m.CreatedAt); err != nil {
		return m, err
	}
	m.ToolCalls, m.Citations = []chat.ToolCall{}, []int{}
	if len(calls) > 0 {
		if err := json.Unmarshal(calls, &m.ToolCalls); err != nil {
			return m, err
		}
	}
	if len(cites) > 0 {
		if err := json.Unmarshal(cites, &m.Citations); err != nil {
			return m, err
		}
	}
	// A stored JSON null (a message added with nil slices) is still an
	// empty array in the API.
	if m.ToolCalls == nil {
		m.ToolCalls = []chat.ToolCall{}
	}
	if m.Citations == nil {
		m.Citations = []int{}
	}
	return m, nil
}

func queryAIMessages(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, query string, args ...any) ([]chat.Message, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []chat.Message{}
	for rows.Next() {
		m, err := scanAIMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AIMessages lists a session's messages, oldest first.
func (s *Store) AIMessages(ctx context.Context, sessionID string) ([]chat.Message, error) {
	if !uuidText.MatchString(sessionID) {
		return nil, chat.ErrNotFound
	}
	return queryAIMessages(ctx, s.db, `SELECT `+aiMessageCols+` FROM ai_messages WHERE session_id = $1 ORDER BY id`, sessionID)
}

// ActiveAITask returns the session's queued or running task, or nil.
func (s *Store) ActiveAITask(ctx context.Context, sessionID string) (*chat.Task, error) {
	if !uuidText.MatchString(sessionID) {
		return nil, chat.ErrNotFound
	}
	var t chat.Task
	var result []byte
	err := s.db.QueryRowContext(ctx, `SELECT id::text, kind, status, session_id::text, error_code, error_message,
			result, created_at, started_at, finished_at
		FROM ai_tasks WHERE session_id = $1 AND status IN ('queued', 'running')
		ORDER BY created_at DESC LIMIT 1`, sessionID).
		Scan(&t.ID, &t.Kind, &t.Status, &t.SessionID, &t.ErrorCode, &t.ErrorMessage, &result, &t.CreatedAt, &t.StartedAt, &t.FinishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(result) > 0 {
		t.Result = result
	} else {
		t.Result = json.RawMessage("null")
	}
	return &t, nil
}

// PostAIMessage stores a user message and starts its task while holding the
// session's lock, so a second message cannot start a second task (spec
// S-8). start runs inside the transaction: it records the task on its own
// connection, and the task sees the history read here, the new message
// last.
func (s *Store) PostAIMessage(ctx context.Context, sessionID, content, title string, window int, start chat.StartFunc) (int64, string, error) {
	if !uuidText.MatchString(sessionID) {
		return 0, "", chat.ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockAISession(ctx, tx, sessionID); err != nil {
		return 0, "", err
	}
	res, err := tx.ExecContext(ctx, `UPDATE ai_sessions SET last_active_at = now(),
		title = CASE WHEN title = '' THEN $2 ELSE title END WHERE id = $1`, sessionID, title)
	if err != nil {
		return 0, "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, "", chat.ErrNotFound
	}
	var busy bool
	if err := tx.QueryRowContext(ctx, activeAITask, sessionID).Scan(&busy); err != nil {
		return 0, "", err
	}
	if busy {
		return 0, "", chat.ErrTaskRunning
	}
	var msgID int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO ai_messages (session_id, role, content) VALUES ($1, 'user', $2) RETURNING id`,
		sessionID, content).Scan(&msgID); err != nil {
		return 0, "", err
	}
	history, err := queryAIMessages(ctx, tx, `SELECT `+aiMessageCols+` FROM ai_messages WHERE session_id = $1 ORDER BY id DESC LIMIT $2`,
		sessionID, window)
	if err != nil {
		return 0, "", err
	}
	slices.Reverse(history)
	taskID, err := start(context.WithValue(ctx, sessionLocked{}, sessionID), msgID, history)
	if err != nil {
		return 0, "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ai_messages SET task_id = $2 WHERE id = $1`, msgID, taskID); err != nil {
		return 0, "", err
	}
	return msgID, taskID, tx.Commit()
}

// AddAIMessage stores an assistant message and marks its session active.
func (s *Store) AddAIMessage(ctx context.Context, m chat.Message) (int64, error) {
	calls, err := json.Marshal(m.ToolCalls)
	if err != nil {
		return 0, err
	}
	cites, err := json.Marshal(m.Citations)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO ai_messages (session_id, role, content, tool_calls, citations, proposal_id, task_id)
		VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6, $7) RETURNING id`,
		m.SessionID, m.Role, m.Content, string(calls), string(cites), m.ProposalID, m.TaskID).Scan(&id)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ai_sessions SET last_active_at = now() WHERE id = $1`, m.SessionID); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// OpenAIFindings lists open and acknowledged findings, critical first, then
// most recently seen.
func (s *Store) OpenAIFindings(ctx context.Context, limit int) ([]chat.Finding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id::text, type, subject, severity, status, title, last_seen
		FROM ai_findings WHERE status IN ('open', 'acknowledged')
		ORDER BY CASE severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END, last_seen DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []chat.Finding{}
	for rows.Next() {
		var f chat.Finding
		if err := rows.Scan(&f.ID, &f.Type, &f.Subject, &f.Severity, &f.Status, &f.Title, &f.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
