package store

// The voice runtime's read and write half (spec voice-agents S-19 to S-23):
// the view snapshot talking-agent polls, the runtime singleton its acks
// replace, the per-call reports joined to CDRs by correlation id, the prune
// advisory lock and the retention prune. The view types here mirror the
// runtime contracts; internal/voice adapts them to its wire shapes.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// VoiceRuntimeAgent is one enabled agent for the runtime view: the full
// registry row (PINHash included; the view builder is the only reader) plus
// its attachments with the credentials still sealed.
type VoiceRuntimeAgent struct {
	VoiceAgent
	Servers []VoiceRuntimeServer
}

// VoiceRuntimeServer is one attached MCP server with its credential still
// sealed (additional data "voice_mcp:<id>:cred"); the plain value exists
// only after the view unseals it.
type VoiceRuntimeServer struct {
	ID         int64
	Name       string
	URL        string
	Auth       string
	HeaderName string
	TokenURL   string
	ClientID   string
	Scope      string
	Credential []byte
	TimeoutMS  int
	Enabled    bool
	Tools      []VoiceToolAccess
}

// VoiceRuntimeState is the runtime singleton plus what its colour needs:
// the current voice revision, what the last ack recorded, and whether any
// enabled agent exists (silence only matters then, spec S-30).
type VoiceRuntimeState struct {
	// Revision is the global voice revision; AckRevision the one the last
	// ack loaded (0 when the runtime never acked).
	Revision, AckRevision int64
	LastSeen              *time.Time
	Version               string
	// Loaded is the acked agents array as JSON, nil when never acked.
	Loaded           []byte
	HasEnabledAgents bool
}

// VoiceRuntimeSnapshot reads the runtime view's inputs in one repeatable-read
// transaction, so the returned revision is the agents', servers' and
// attachments'.
func (s *Store) VoiceRuntimeSnapshot(ctx context.Context) (rev int64, agents []VoiceRuntimeAgent, err error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := tx.QueryRowContext(ctx, `SELECT revision FROM voice_revision WHERE id = 1`).Scan(&rev); err != nil {
		return 0, nil, fmt.Errorf("store: voice revision: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+agentCols+` FROM voice_agents WHERE enabled ORDER BY id`)
	if err != nil {
		return 0, nil, fmt.Errorf("store: voice runtime agents: %w", err)
	}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			if cerr := rows.Close(); cerr != nil {
				return 0, nil, fmt.Errorf("store: voice runtime agents: %w", cerr)
			}
			return 0, nil, err
		}
		agents = append(agents, VoiceRuntimeAgent{VoiceAgent: a})
	}
	if err := rows.Close(); err != nil {
		return 0, nil, fmt.Errorf("store: voice runtime agents: %w", err)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, fmt.Errorf("store: voice runtime agents: %w", err)
	}

	for i := range agents {
		agents[i].Servers, err = s.voiceRuntimeServers(ctx, tx, agents[i].ID)
		if err != nil {
			return 0, nil, err
		}
	}
	return rev, agents, tx.Commit()
}

// voiceRuntimeServers reads one agent's enabled attachments with their
// sealed credentials, inside the snapshot's transaction.
func (s *Store) voiceRuntimeServers(ctx context.Context, tx *sql.Tx, agentID int64) ([]VoiceRuntimeServer, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT s.id, s.name, s.url, s.auth, s.header_name, s.token_url, s.client_id, s.oauth_scope,
		       s.credential, s.timeout_ms, s.enabled, m.enabled, m.tools
		FROM voice_agent_mcp m
		JOIN voice_mcp_servers s ON s.id = m.server_id
		WHERE m.agent_id = $1 AND m.enabled AND s.enabled
		ORDER BY m.server_id`, agentID)
	if err != nil {
		return nil, fmt.Errorf("store: voice runtime servers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []VoiceRuntimeServer
	for rows.Next() {
		var (
			v       VoiceRuntimeServer
			cred    []byte
			enabled bool
			tools   []byte
		)
		if err := rows.Scan(&v.ID, &v.Name, &v.URL, &v.Auth, &v.HeaderName, &v.TokenURL, &v.ClientID,
			&v.Scope, &cred, &v.TimeoutMS, &enabled, &enabled, &tools); err != nil {
			return nil, fmt.Errorf("store: scan voice runtime server: %w", err)
		}
		if cred != nil {
			v.Credential = cred
		}
		// The WHERE clause keeps only attachments whose server and
		// attachment are enabled.
		v.Enabled = true
		v.Tools = []VoiceToolAccess{}
		if err := json.Unmarshal(tools, &v.Tools); err != nil {
			return nil, fmt.Errorf("store: voice runtime server %d tools: %w", v.ID, err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// VoiceRuntimeState returns the runtime singleton next to the current voice
// revision in one read (spec S-22): the ETag and the colour must not mix a
// pre-ack revision with a post-ack singleton.
func (s *Store) VoiceRuntimeState(ctx context.Context) (VoiceRuntimeState, error) {
	var (
		st      VoiceRuntimeState
		loaded  []byte
		lastSee sql.NullTime
		version sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT v.revision, COALESCE(r.revision, 0), r.last_seen_at, r.version, COALESCE(r.loaded::text, ''),
		       EXISTS (SELECT 1 FROM voice_agents WHERE enabled)
		FROM voice_revision v LEFT JOIN voice_runtime r ON r.id = 1
		WHERE v.id = 1`).Scan(&st.Revision, &st.AckRevision, &lastSee, &version, &loaded, &st.HasEnabledAgents)
	if err != nil {
		return st, fmt.Errorf("store: voice runtime state: %w", err)
	}
	if lastSee.Valid {
		t := lastSee.Time
		st.LastSeen = &t
	}
	st.Version = version.String
	if string(loaded) != "" && string(loaded) != "null" {
		st.Loaded = []byte(loaded)
	}
	return st, nil
}

// SaveVoiceRuntimeAck replaces the runtime singleton (spec S-20): the acked
// revision, the runtime's version, the last-seen time and the acked agents
// array as JSON. The service account is kept for the audit trail, never for
// authorization.
func (s *Store) SaveVoiceRuntimeAck(ctx context.Context, accountID int64, at time.Time, rev int64, version string, loaded []byte) error {
	if loaded == nil {
		loaded = []byte("[]")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO voice_runtime (id, revision, last_seen_at, loaded, version, service_account_id)
		VALUES (1, $1, $2, $3::jsonb, $4, $5)
		ON CONFLICT (id) DO UPDATE SET
			revision = EXCLUDED.revision, last_seen_at = EXCLUDED.last_seen_at,
			loaded = EXCLUDED.loaded, version = EXCLUDED.version,
			service_account_id = EXCLUDED.service_account_id`,
		rev, at, string(loaded), version, nullAccountID(accountID))
	if err != nil {
		return fmt.Errorf("store: save voice runtime ack: %w", err)
	}
	return nil
}

// VoiceAgentPolicy reports how the agent reports calls (spec S-21, S-23);
// ErrNotFound for an unknown name.
func (s *Store) VoiceAgentPolicy(ctx context.Context, name string) (recordTranscript bool, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT record_transcript FROM voice_agents WHERE name = $1`, name).Scan(&recordTranscript)
	if err == sql.ErrNoRows {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("store: voice agent %s policy: %w", name, err)
	}
	return recordTranscript, nil
}

// SaveVoiceAgentCall stores a call report idempotently on its correlation
// id (spec S-21) and reports whether it was new. The report may arrive
// before hello-sip writes the CDR; the CDR detail joins, so the row stands
// alone until then.
func (s *Store) SaveVoiceAgentCall(ctx context.Context, correlationID, agentName, outcome, summary string,
	toolCalls []byte, tokensIn, tokensOut int64, transcript *string, at time.Time) (bool, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO voice_agent_calls (correlation_id, agent_name, outcome, summary, tool_calls, tokens_in, tokens_out, transcript, reported_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9)
		ON CONFLICT (correlation_id) DO NOTHING
		RETURNING correlation_id`,
		correlationID, agentName, outcome, summary, jsonOrNull(toolCalls), tokensIn, tokensOut, transcript, at).Scan(&id)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: save voice call %s: %w", correlationID, err)
	}
	return true, nil
}

// VoiceAgentCall is the report row of one call to an agent, the CDR
// detail's voiceAgent object (spec S-23). The transcript itself never
// leaves the store here: TranscriptPresent says whether the agent recorded
// one, and the transcript is served only through the runtime's own
// retention path.
type VoiceAgentCall struct {
	AgentName           string
	Outcome             string
	Summary             string
	ToolCalls           []VoiceToolCall
	TokensIn, TokensOut int64
	TranscriptPresent   bool
	ReportedAt          time.Time
}

// VoiceToolCall is one tool call of a stored report.
type VoiceToolCall struct {
	Tool      string         `json:"tool"`
	OK        bool           `json:"ok"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// VoiceAgentCallByCorrelation returns the report stored for a correlation
// id; ErrNotFound when no report arrived (yet).
func (s *Store) VoiceAgentCallByCorrelation(ctx context.Context, correlationID string) (VoiceAgentCall, error) {
	var (
		c          VoiceAgentCall
		tools      []byte
		transcript *string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT agent_name, outcome, summary, COALESCE(tool_calls::text, '[]'), tokens_in, tokens_out,
		       transcript, reported_at
		FROM voice_agent_calls WHERE correlation_id = $1`, correlationID).
		Scan(&c.AgentName, &c.Outcome, &c.Summary, &tools, &c.TokensIn, &c.TokensOut, &transcript, &c.ReportedAt)
	if err == sql.ErrNoRows {
		return c, ErrNotFound
	}
	if err != nil {
		return c, fmt.Errorf("store: voice call %s: %w", correlationID, err)
	}
	c.ToolCalls = []VoiceToolCall{}
	if len(tools) > 0 {
		if err := json.Unmarshal(tools, &c.ToolCalls); err != nil {
			return c, fmt.Errorf("store: voice call %s tool calls: %w", correlationID, err)
		}
	}
	c.TranscriptPresent = transcript != nil && *transcript != ""
	return c, nil
}

// TryVoiceLock takes pg_try_advisory_lock(hashtext(key)) on a connection of
// its own, held until unlock (or until the process dies and PostgreSQL
// drops the connection).
func (s *Store) TryVoiceLock(ctx context.Context, key string) (func(), bool, error) {
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

// VoicePrune clears transcripts past their agent's retention and deletes
// reports past the CDR retention (spec S-23); it returns the rows touched.
func (s *Store) VoicePrune(ctx context.Context, now time.Time, cdrRetention time.Duration) (int64, error) {
	var n int64
	res, err := s.db.ExecContext(ctx, `
		UPDATE voice_agent_calls c SET transcript = NULL
		FROM voice_agents a
		WHERE a.name = c.agent_name AND c.transcript IS NOT NULL
		  AND c.reported_at < $1::timestamptz - (a.transcript_retention_days * interval '1 day')`, now)
	if err != nil {
		return 0, fmt.Errorf("store: voice transcript prune: %w", err)
	}
	n, _ = res.RowsAffected()
	res, err = s.db.ExecContext(ctx,
		`DELETE FROM voice_agent_calls WHERE reported_at < $1`, now.Add(-cdrRetention))
	if err != nil {
		return n, fmt.Errorf("store: voice call prune: %w", err)
	}
	m, _ := res.RowsAffected()
	return n + m, nil
}

// nullAccountID maps "no account known" to SQL NULL (the service account
// column is a foreign key; the audit trail keeps what exists).
func nullAccountID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

// jsonOrNull maps an empty JSON payload to SQL NULL ($n::jsonb refuses ”).
func jsonOrNull(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}
