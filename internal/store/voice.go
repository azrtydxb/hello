package store

// The voice agent registry's persistence (spec voice-agents): agents, their
// persona versions, MCP servers with sealed credentials and the agent
// attachments with their tool allowlists. Every change runs through
// configChange, so it is audited, checked against the routing
// configuration, bumps the configuration revision and notifies hello-sip —
// and, in the same transaction, bumps the global voice_revision the runtime
// API's ETag is built from (spec Data, plan Task 2).

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// VoiceError rejects a registry change with one of the spec's voice error
// codes (spec Interfaces); the API answers it as-is.
type VoiceError struct {
	Status int
	Code   string
	Msg    string
}

func (e *VoiceError) Error() string { return e.Msg }

func newVoiceError(status int, code, msg string) error {
	return &VoiceError{Status: status, Code: code, Msg: msg}
}

// VoiceAgent is one registered voice agent (spec S-1 to S-3, S-36). PINHash
// is the bcrypt hash of the caller-verification PIN; it never reaches an
// API response — only the runtime view builder (Task 5) reads it.
type VoiceAgent struct {
	ID                      int64
	Name                    string
	Description             string
	Enabled                 bool
	SIPUser                 string
	Extension               *string
	Prompt                  string
	Greeting                string
	Language                string
	Voice                   string
	VoiceReference          string
	Style                   string
	Temperature             *float64
	MaxCallSeconds          int
	MaxConcurrent           int
	MaxToolCalls            int
	IdleTimeoutSeconds      int
	RecordTranscript        bool
	TranscriptRetentionDays int
	CallerVerification      string
	CallerAllowlist         []string
	PINHash                 string
	Revision                int64
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

// VoiceAgentInput is a full replace of the agent's editable fields (PUT
// semantics). A zero limit takes the spec's default; nil Extension clears
// it. SIPUser is deliberately absent: it is generated once and never edited
// (spec S-1).
type VoiceAgentInput struct {
	Name                    string
	Description             string
	Enabled                 bool
	Extension               *string
	Prompt                  string
	Greeting                string
	Version                 string
	Language                string
	Voice                   string
	VoiceReference          string
	Style                   string
	Temperature             *float64
	MaxCallSeconds          int
	MaxConcurrent           int
	MaxToolCalls            int
	IdleTimeoutSeconds      int
	RecordTranscript        bool
	TranscriptRetentionDays int
	CallerVerification      string
	CallerAllowlist         []string
	PINHash                 string
}

// VoiceAgentVersion is one saved persona (spec S-4).
type VoiceAgentVersion struct {
	Revision  int64
	Persona   json.RawMessage
	Actor     string
	CreatedAt time.Time
}

// persona is the JSON shape of a saved version's persona fields.
type persona struct {
	Prompt      string   `json:"prompt"`
	Greeting    string   `json:"greeting"`
	Language    string   `json:"language"`
	Voice       string   `json:"voice"`
	VoiceRef    string   `json:"voiceReference"`
	Style       string   `json:"style"`
	Temperature *float64 `json:"temperature"`
}

func personaOf(in VoiceAgentInput) persona {
	return persona{
		Prompt: in.Prompt, Greeting: in.Greeting, Language: in.Language, Voice: in.Voice,
		VoiceRef: in.VoiceReference, Style: in.Style, Temperature: in.Temperature,
	}
}

// agentPersona is the persona of a stored agent row, for comparing a save
// against the previous persona.
func agentPersona(a VoiceAgent) persona {
	return persona{
		Prompt: a.Prompt, Greeting: a.Greeting, Language: a.Language, Voice: a.Voice,
		VoiceRef: a.VoiceReference, Style: a.Style, Temperature: a.Temperature,
	}
}

// personaMatches reports whether p equals in's persona fields, so an update
// that does not touch the persona does not add a version row.
func personaMatches(p persona, in persona) bool {
	return p.Prompt == in.Prompt && p.Greeting == in.Greeting && p.Language == in.Language &&
		p.Voice == in.Voice && p.VoiceRef == in.VoiceRef && p.Style == in.Style &&
		((p.Temperature == nil) == (in.Temperature == nil)) &&
		(p.Temperature == nil || *p.Temperature == *in.Temperature)
}

// bumpVoiceRevision moves the global voice revision counter in the change's
// own transaction (spec Data), so the runtime ETag changes exactly when the
// view does.
func bumpVoiceRevision(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE voice_revision SET revision = revision + 1 WHERE id = 1`)
	return err
}

// voiceAgentDefaults fills the spec's defaults for limits left at zero.
func (in *VoiceAgentInput) voiceAgentDefaults() {
	if in.MaxCallSeconds == 0 {
		in.MaxCallSeconds = 600
	}
	if in.MaxConcurrent == 0 {
		in.MaxConcurrent = 4
	}
	if in.MaxToolCalls == 0 {
		in.MaxToolCalls = 20
	}
	if in.IdleTimeoutSeconds == 0 {
		in.IdleTimeoutSeconds = 20
	}
	if in.TranscriptRetentionDays == 0 {
		in.TranscriptRetentionDays = 30
	}
}

// maxVoiceAgentVersions is how many persona versions are kept per agent
// (spec S-4).
const maxVoiceAgentVersions = 10

// voiceAgentInsertSQL is the INSERT of one agent; sip_user is a generated
// candidate the caller retries on a collision.
const voiceAgentInsertSQL = `
	INSERT INTO voice_agents (name, description, enabled, sip_user, extension, prompt, greeting, language,
		voice, voice_reference, style, temperature, max_call_seconds, max_concurrent, max_tool_calls,
		idle_timeout_seconds, record_transcript, transcript_retention_days, caller_verification, caller_allowlist,
		pin_hash, revision)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20::jsonb, $21, 1)
	RETURNING id, created_at, updated_at`

// genSIPUser draws one candidate sip_user. The migration's CHECK keeps
// sip_user to 3-15 digits, so the generated value is numeric (the spec's
// "va-<8 hex>" shape cannot be stored; recorded as a deviation in the plan).
func genSIPUser() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(8_000_000_000))
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(20_000_000_000+n.Int64(), 10), nil
}

// CreateVoiceAgent inserts an agent at revision 1 with a version 1 snapshot,
// refusing to pass maxAgents (spec S-2). It is one transaction: the row, the
// version snapshot, the audit row, both revision counters.
func (s *Store) CreateVoiceAgent(ctx context.Context, actor string, in VoiceAgentInput, maxAgents int, check Check) (VoiceAgent, error) {
	in.voiceAgentDefaults()
	allow, err := json.Marshal(zeroAllowlist(in.CallerAllowlist))
	if err != nil {
		return VoiceAgent{}, err
	}
	var a VoiceAgent
	err = s.configChangeID(ctx, actor, "create", "voice_agent", check, func(tx *sql.Tx) (string, error) {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM voice_agents`).Scan(&n); err != nil {
			return "", err
		}
		if n >= maxAgents {
			return "", newVoiceError(409, "voice_agent_limit",
				fmt.Sprintf("at the limit of %d voice agents (HELLO_VOICE_MAX_AGENTS)", maxAgents))
		}
		if err := checkExtensionFree(ctx, tx, in.Extension, 0); err != nil {
			return "", err
		}
		personaJSON, err := json.Marshal(personaOf(in))
		if err != nil {
			return "", err
		}
		for try := 0; try < 8; try++ {
			sip, err := genSIPUser()
			if err != nil {
				return "", err
			}
			err = tx.QueryRowContext(ctx, voiceAgentInsertSQL, in.Name, in.Description, in.Enabled, sip,
				in.Extension, in.Prompt, in.Greeting, in.Language, in.Voice, in.VoiceReference, in.Style,
				in.Temperature, in.MaxCallSeconds, in.MaxConcurrent, in.MaxToolCalls, in.IdleTimeoutSeconds,
				in.RecordTranscript, in.TranscriptRetentionDays, in.CallerVerification, string(allow),
				in.PINHash).Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt)
			if isUniqueSIPUser(err) {
				continue // a concurrent create drew the same digits: draw again
			}
			if err != nil {
				return "", err
			}
			a.SIPUser = sip
			break
		}
		if a.SIPUser == "" {
			return "", errors.New("store: cannot generate a free sip_user")
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO voice_agent_versions (agent_id, revision, persona, actor) VALUES ($1, 1, $2, $3)`,
			a.ID, personaJSON, actor); err != nil {
			return "", err
		}
		if err := bumpVoiceRevision(ctx, tx); err != nil {
			return "", err
		}
		return strconv.FormatInt(a.ID, 10), nil
	})
	if err != nil {
		return VoiceAgent{}, err
	}
	a, err = s.GetVoiceAgent(ctx, a.ID)
	return a, err
}

// isUniqueSIPUser reports whether err is a collision on the generated
// sip_user (and only there; a name or extension conflict is a real 409).
func isUniqueSIPUser(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "voice_agents_sip_user_key"
}

// checkExtensionFree refuses an extension another agent or an extension
// already holds: the two tables share one namespace (spec S-3).
func checkExtensionFree(ctx context.Context, tx *sql.Tx, ext *string, self int64) error {
	if ext == nil || *ext == "" {
		return nil
	}
	var taken bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM voice_agents WHERE extension = $1 AND id <> $2)`, *ext, self).Scan(&taken); err != nil {
		return err
	}
	if taken {
		return newVoiceError(409, "conflict", "extension is already an agent's extension")
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM extensions WHERE number = $1)`, *ext).Scan(&taken); err != nil {
		return err
	}
	if taken {
		return newVoiceError(409, "conflict", "extension is already an internal extension")
	}
	return nil
}

// zeroAllowlist maps a nil allowlist to the empty JSON array the column's
// default holds.
func zeroAllowlist(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

const agentCols = `id, name, description, enabled, sip_user, extension, prompt, greeting, language, voice,
	voice_reference, style, temperature, max_call_seconds, max_concurrent, max_tool_calls, idle_timeout_seconds,
	record_transcript, transcript_retention_days, caller_verification, caller_allowlist, pin_hash, revision,
	created_at, updated_at`

func scanAgent(r interface{ Scan(...any) error }) (VoiceAgent, error) {
	var (
		a     VoiceAgent
		ext   sql.NullString
		temp  sql.NullFloat64
		allow []byte
	)
	if err := r.Scan(&a.ID, &a.Name, &a.Description, &a.Enabled, &a.SIPUser, &ext, &a.Prompt, &a.Greeting,
		&a.Language, &a.Voice, &a.VoiceReference, &a.Style, &temp, &a.MaxCallSeconds, &a.MaxConcurrent,
		&a.MaxToolCalls, &a.IdleTimeoutSeconds, &a.RecordTranscript, &a.TranscriptRetentionDays,
		&a.CallerVerification, &allow, &a.PINHash, &a.Revision, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return a, err
	}
	a.Extension = voiceNullString(ext)
	a.CallerAllowlist = []string{}
	if len(allow) > 0 {
		if err := json.Unmarshal(allow, &a.CallerAllowlist); err != nil {
			return a, fmt.Errorf("store: agent %d caller_allowlist: %w", a.ID, err)
		}
	}
	if temp.Valid {
		v := temp.Float64
		a.Temperature = &v
	}
	return a, nil
}

// voiceNullString maps a sql.NullString to a nilable string.
func voiceNullString(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

// GetVoiceAgent returns one agent.
func (s *Store) GetVoiceAgent(ctx context.Context, id int64) (VoiceAgent, error) {
	a, err := scanAgent(s.db.QueryRowContext(ctx, `SELECT `+agentCols+` FROM voice_agents WHERE id = $1`, id))
	return a, mapErr(err)
}

// ListVoiceAgents returns every agent, oldest first.
func (s *Store) ListVoiceAgents(ctx context.Context) ([]VoiceAgent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+agentCols+` FROM voice_agents ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []VoiceAgent{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateVoiceAgent replaces the agent's editable fields (PUT semantics) and,
// in the same transaction: bumps the per-agent revision, snapshots the
// persona when it changed, prunes to the last 10 versions, hashes nothing
// (the caller passes the PIN already hashed), bumps the voice revision and
// writes the audit row. Enabled, limits and persona all move the runtime's
// view, so every save bumps the voice revision (spec S-4, Data).
func (s *Store) UpdateVoiceAgent(ctx context.Context, actor string, id int64, in VoiceAgentInput, check Check) (VoiceAgent, error) {
	in.voiceAgentDefaults()
	allow, err := json.Marshal(zeroAllowlist(in.CallerAllowlist))
	if err != nil {
		return VoiceAgent{}, err
	}
	err = s.configChangeID(ctx, actor, "update", "voice_agent", check, func(tx *sql.Tx) (string, error) {
		old, err := scanAgent(tx.QueryRowContext(ctx, `SELECT `+agentCols+` FROM voice_agents WHERE id = $1 FOR UPDATE`, id))
		if err != nil {
			return "", err
		}
		if err := checkExtensionFree(ctx, tx, in.Extension, id); err != nil {
			return "", err
		}
		var pinHash string
		if in.PINHash != "" {
			pinHash = in.PINHash
		} else {
			pinHash = old.PINHash
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE voice_agents SET name = $2, description = $3, enabled = $4, extension = $5, prompt = $6,
				greeting = $7, language = $8, voice = $9, voice_reference = $10, style = $11, temperature = $12,
				max_call_seconds = $13, max_concurrent = $14, max_tool_calls = $15, idle_timeout_seconds = $16,
				record_transcript = $17, transcript_retention_days = $18, caller_verification = $19,
				caller_allowlist = $20::jsonb, pin_hash = $21, revision = revision + 1, updated_at = now()
			WHERE id = $1`, id, in.Name, in.Description, in.Enabled, in.Extension, in.Prompt, in.Greeting,
			in.Language, in.Voice, in.VoiceReference, in.Style, in.Temperature, in.MaxCallSeconds,
			in.MaxConcurrent, in.MaxToolCalls, in.IdleTimeoutSeconds, in.RecordTranscript,
			in.TranscriptRetentionDays, in.CallerVerification, string(allow), pinHash); err != nil {
			return "", err
		}
		// A persona change snapshots a version; every save bumps the
		// revision (the runtime reloads on it).
		if !personaMatches(agentPersona(old), personaOf(in)) {
			pj, err := json.Marshal(personaOf(in))
			if err != nil {
				return "", err
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO voice_agent_versions (agent_id, revision, persona, actor)
				SELECT id, revision, $2, $3 FROM voice_agents WHERE id = $1`, id, pj, actor); err != nil {
				return "", err
			}
			if _, err := tx.ExecContext(ctx, `
				DELETE FROM voice_agent_versions v USING voice_agents a
				WHERE v.agent_id = a.id AND a.id = $1
				  AND v.revision < a.revision - $2`, id, maxVoiceAgentVersions-1); err != nil {
				return "", err
			}
		}
		if err := bumpVoiceRevision(ctx, tx); err != nil {
			return "", err
		}
		return strconv.FormatInt(id, 10), nil
	})
	if err != nil {
		return VoiceAgent{}, err
	}
	return s.GetVoiceAgent(ctx, id)
}

// DeleteVoiceAgent removes an agent that no route, ring group member or
// failure target names (409 naming the references, spec Data), with its
// versions and attachments (cascade).
func (s *Store) DeleteVoiceAgent(ctx context.Context, actor string, id int64, check Check) error {
	return s.configChangeID(ctx, actor, "delete", "voice_agent", check, func(tx *sql.Tx) (string, error) {
		a, err := scanAgent(tx.QueryRowContext(ctx, `SELECT `+agentCols+` FROM voice_agents WHERE id = $1`, id))
		if err != nil {
			return "", err
		}
		if err := voiceAgentReferences(ctx, tx, a); err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM voice_agents WHERE id = $1`, id); err != nil {
			return "", err
		}
		if err := bumpVoiceRevision(ctx, tx); err != nil {
			return "", err
		}
		return strconv.FormatInt(id, 10), nil
	})
}

// voiceAgentReferences refuses a delete an inbound route, a ring group
// member or a ring group failure target still names.
func voiceAgentReferences(ctx context.Context, tx *sql.Tx, a VoiceAgent) error {
	var refs []string
	rows, err := tx.QueryContext(ctx, `
		SELECT 'inbound route' AS what, r.name FROM inbound_routes r
		WHERE r.destination_kind = 'voice_agent' AND r.destination = $1
		UNION ALL
		SELECT 'ring group ' || g.name, '' FROM ring_group_members m
		JOIN ring_groups g ON g.id = m.group_id WHERE m.voice_agent_id = $2
		UNION ALL
		SELECT 'ring group ' || g.name || ' (failure target)', '' FROM ring_groups g
		WHERE g.failure_kind = 'voice_agent' AND g.failure_target = $3`, a.Name, a.ID, a.Name)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var what, name string
		if err := rows.Scan(&what, &name); err != nil {
			return err
		}
		refs = append(refs, strings.TrimSpace(what+" "+name))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(refs) > 0 {
		return inUse("voice agent "+strconv.Quote(a.Name), refs)
	}
	return nil
}

// Versions (spec S-4).

// ListVoiceAgentVersions returns an agent's saved personas, newest first.
func (s *Store) ListVoiceAgentVersions(ctx context.Context, agentID int64) ([]VoiceAgentVersion, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT revision, persona, actor, created_at FROM voice_agent_versions
		WHERE agent_id = $1 ORDER BY revision DESC`, agentID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []VoiceAgentVersion{}
	for rows.Next() {
		var (
			v           VoiceAgentVersion
			personaJSON []byte
		)
		if err := rows.Scan(&v.Revision, &personaJSON, &v.Actor, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.Persona = json.RawMessage(personaJSON)
		out = append(out, v)
	}
	return out, rows.Err()
}

// RestoreVoiceAgentVersion applies a saved persona as a new revision (a new
// revision, never a rewrite of history, spec S-4): the persona fields are
// replaced, the revision moves to the agent's next revision, a new version
// row snapshots the restored persona and the history keeps the source.
func (s *Store) RestoreVoiceAgentVersion(ctx context.Context, actor string, id, revision int64, check Check) (VoiceAgent, error) {
	err := s.configChangeID(ctx, actor, "restore", "voice_agent", check, func(tx *sql.Tx) (string, error) {
		if _, err := tx.ExecContext(ctx, `SELECT id FROM voice_agents WHERE id = $1 FOR UPDATE`, id); err != nil {
			return "", err
		}
		var p persona
		if err := tx.QueryRowContext(ctx,
			`SELECT persona FROM voice_agent_versions WHERE agent_id = $1 AND revision = $2`,
			id, revision).Scan(&p); err != nil {
			return "", mapErr(err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE voice_agents SET prompt = $2, greeting = $3, language = $4, voice = $5, voice_reference = $6,
				style = $7, temperature = $8, revision = revision + 1, updated_at = now()
			WHERE id = $1`, id, p.Prompt, p.Greeting, p.Language, p.Voice, p.VoiceRef, p.Style,
			p.Temperature); err != nil {
			return "", err
		}
		pj, err := json.Marshal(p)
		if err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO voice_agent_versions (agent_id, revision, persona, actor)
			SELECT id, revision, $2, $3 FROM voice_agents WHERE id = $1`, id, pj, actor); err != nil {
			return "", err
		}
		// The prune keeps the newest maxVoiceAgentVersions-1 rows beside the
		// source, so the restored snapshot always survives its own prune.
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM voice_agent_versions v
			WHERE v.agent_id = $1 AND v.revision <> $2 AND v.revision IN (
				SELECT revision FROM (
					SELECT revision, row_number() OVER (ORDER BY revision DESC) rn
					FROM voice_agent_versions
					WHERE agent_id = $1 AND revision <> $2
				) r WHERE rn > $3)`,
			id, revision, maxVoiceAgentVersions-1); err != nil {
			return "", err
		}
		if err := bumpVoiceRevision(ctx, tx); err != nil {
			return "", err
		}
		return strconv.FormatInt(id, 10), nil
	})
	if err != nil {
		return VoiceAgent{}, err
	}
	return s.GetVoiceAgent(ctx, id)
}

// Attachments and tool allowlists (spec S-6, S-36).

// VoiceToolAccess is one allowlisted tool: the name without the server
// prefix, whether the spoken confirmation is required first, and whether the
// tool changes data (and so needs caller verification).
type VoiceToolAccess struct {
	Name    string
	Confirm bool
	Write   bool
}

// VoiceAttachment is one agent's attachment of one MCP server, as saved.
type VoiceAttachment struct {
	ServerID   int64
	ServerName string
	Enabled    bool
	Tools      []VoiceToolAccess
}

// VoiceAttachmentInput is one attachment of a PUT; the registry has already
// computed the confirm/write defaults and checked the verification rule.
type VoiceAttachmentInput struct {
	ServerID int64
	Enabled  bool
	Tools    []VoiceToolAccess
}

// PutVoiceAgentTools replaces the agent's attachments (PUT semantics): the
// rows are deleted and re-inserted in the agent's change transaction, so the
// allowlist, the revision, the audit row and the voice revision move
// together. It answers the attachments as saved, with each server's name.
func (s *Store) PutVoiceAgentTools(ctx context.Context, actor string, agentID int64, atts []VoiceAttachmentInput, check Check) ([]VoiceAttachment, error) {
	err := s.configChangeID(ctx, actor, "update", "voice_agent_tools", check, func(tx *sql.Tx) (string, error) {
		if _, err := tx.ExecContext(ctx, `SELECT id FROM voice_agents WHERE id = $1 FOR UPDATE`, agentID); err != nil {
			return "", mapErr(err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM voice_agent_mcp WHERE agent_id = $1`, agentID); err != nil {
			return "", err
		}
		for _, at := range atts {
			tools, err := json.Marshal(at.Tools)
			if err != nil {
				return "", err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO voice_agent_mcp (agent_id, server_id, enabled, tools)
				VALUES ($1, $2, $3, $4::jsonb)
				ON CONFLICT (agent_id, server_id) DO UPDATE SET enabled = EXCLUDED.enabled, tools = EXCLUDED.tools`,
				agentID, at.ServerID, at.Enabled, string(tools)); err != nil {
				return "", err
			}
		}
		if err := bumpVoiceRevision(ctx, tx); err != nil {
			return "", err
		}
		return strconv.FormatInt(agentID, 10), nil
	})
	if err != nil {
		return nil, err
	}
	return s.AgentAttachments(ctx, agentID)
}

// AgentAttachments returns an agent's attachments with each server's name,
// oldest attachment first.
func (s *Store) AgentAttachments(ctx context.Context, agentID int64) ([]VoiceAttachment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.server_id, s.name, m.enabled, m.tools FROM voice_agent_mcp m
		JOIN voice_mcp_servers s ON s.id = m.server_id
		WHERE m.agent_id = $1 ORDER BY m.server_id`, agentID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []VoiceAttachment{}
	for rows.Next() {
		var (
			at    VoiceAttachment
			tools []byte
		)
		if err := rows.Scan(&at.ServerID, &at.ServerName, &at.Enabled, &tools); err != nil {
			return nil, err
		}
		at.Tools = []VoiceToolAccess{}
		if err := json.Unmarshal(tools, &at.Tools); err != nil {
			return nil, fmt.Errorf("store: attachment %d/%d tools: %w", agentID, at.ServerID, err)
		}
		out = append(out, at)
	}
	return out, rows.Err()
}

// MCP servers (spec S-5).

// VoiceMCPServer is one registered MCP server, without its credential
// material: CredentialSet says whether one is stored.
type VoiceMCPServer struct {
	ID              int64
	Name            string
	URL             string
	Auth            string // none, bearer, header, oauth_client_credentials
	HeaderName      string
	TokenURL        string
	ClientID        string
	Scope           string
	CredentialSet   bool
	TimeoutMS       int
	Enabled         bool
	LastCheckAt     *time.Time
	LastCheckStatus string
}

const serverCols = `id, name, url, auth, header_name, token_url, client_id, oauth_scope,
	credential IS NOT NULL AS credential_set, timeout_ms, enabled, last_check_at, last_check_status`

func scanServer(r interface{ Scan(...any) error }) (VoiceMCPServer, error) {
	var (
		v  VoiceMCPServer
		ch sql.NullTime
		st sql.NullString
	)
	if err := r.Scan(&v.ID, &v.Name, &v.URL, &v.Auth, &v.HeaderName, &v.TokenURL, &v.ClientID, &v.Scope,
		&v.CredentialSet, &v.TimeoutMS, &v.Enabled, &ch, &st); err != nil {
		return v, err
	}
	if ch.Valid {
		t := ch.Time
		v.LastCheckAt = &t
	}
	v.LastCheckStatus = ""
	if st.Valid {
		v.LastCheckStatus = st.String
	}
	return v, nil
}

// ListVoiceMCPServers returns every server, oldest first, each with the
// agent names that attach it.
func (s *Store) ListVoiceMCPServers(ctx context.Context) ([]VoiceMCPServer, []string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+serverCols+` FROM voice_mcp_servers ORDER BY id`)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []VoiceMCPServer{}
	for rows.Next() {
		v, err := scanServer(rows)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	used, err := s.voiceMCPUsedBy(ctx, 0)
	return out, used, err
}

// GetVoiceMCPServer returns one server with the agent names that attach it.
func (s *Store) GetVoiceMCPServer(ctx context.Context, id int64) (VoiceMCPServer, []string, error) {
	v, err := scanServer(s.db.QueryRowContext(ctx, `SELECT `+serverCols+` FROM voice_mcp_servers WHERE id = $1`, id))
	if err != nil {
		return VoiceMCPServer{}, nil, mapErr(err)
	}
	used, err := s.voiceMCPUsedBy(ctx, id)
	return v, used, err
}

// voiceMCPUsedBy lists the agents attaching serverID, or every
// server-to-agent pair when serverID is 0.
func (s *Store) voiceMCPUsedBy(ctx context.Context, serverID int64) ([]string, error) {
	q := `SELECT s.name || ':' || a.name FROM voice_agent_mcp m
		JOIN voice_mcp_servers s ON s.id = m.server_id
		JOIN voice_agents a ON a.id = m.agent_id`
	args := []any{}
	if serverID > 0 {
		q += ` WHERE m.server_id = $1`
		args = append(args, serverID)
	}
	q += ` ORDER BY 1`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []string{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// NewVoiceMCPServer is a server to store. Credential is the plaintext
// bearer token, header value or OAuth client secret; the store seals it
// before the transaction commits, and a response never carries it (S-5).
type NewVoiceMCPServer struct {
	Name       string
	URL        string
	Auth       string
	HeaderName string
	TokenURL   string
	ClientID   string
	Scope      string
	Credential string
	TimeoutMS  int
	Enabled    bool
}

// voiceMCPAAD is the additional data every MCP credential is sealed under:
// a ciphertext moved to another row does not open (spec S-5).
func voiceMCPAAD(id int64) string { return fmt.Sprintf("voice_mcp:%d:cred", id) }

// CreateVoiceMCPServer inserts a server and seals its credential under its
// own id's additional data, in one transaction. Missing credential material
// for the chosen auth is a 400; a duplicate name a 409.
func (s *Store) CreateVoiceMCPServer(ctx context.Context, actor string, in NewVoiceMCPServer, check Check) (VoiceMCPServer, error) {
	if in.Auth != "none" && in.Credential == "" {
		return VoiceMCPServer{}, newVoiceError(400, "bad_request", in.Auth+" auth needs a credential")
	}
	var id int64
	err := s.configChangeID(ctx, actor, "create", "voice_mcp_server", check, func(tx *sql.Tx) (string, error) {
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO voice_mcp_servers (name, url, auth, header_name, token_url, client_id, oauth_scope,
				credential, timeout_ms, enabled)
			VALUES ($1, $2, $3, $4, $5, $6, $7, NULL, $8, $9)
			RETURNING id`, in.Name, in.URL, in.Auth, in.HeaderName, in.TokenURL, in.ClientID, in.Scope,
			in.TimeoutMS, in.Enabled).Scan(&id); err != nil {
			return "", err
		}
		if in.Credential != "" {
			if s.box == nil {
				return "", errors.New("store: no secret box")
			}
			sealed, err := s.box.Seal(in.Credential, voiceMCPAAD(id))
			if err != nil {
				return "", err
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE voice_mcp_servers SET credential = $2 WHERE id = $1`, id, sealed); err != nil {
				return "", err
			}
		}
		if err := bumpVoiceRevision(ctx, tx); err != nil {
			return "", err
		}
		return strconv.FormatInt(id, 10), nil
	})
	if err != nil {
		return VoiceMCPServer{}, err
	}
	v, _, err := s.GetVoiceMCPServer(ctx, id)
	return v, err
}

// UpdateVoiceMCPServer replaces the server's fields; an empty credential
// keeps the stored one (spec S-5: PUT without a credential keeps it, the
// sealed bytes included) and the voice revision still moves (the runtime
// reloads on it).
func (s *Store) UpdateVoiceMCPServer(ctx context.Context, actor string, id int64, in NewVoiceMCPServer, check Check) (VoiceMCPServer, error) {
	if in.Auth != "none" && in.Credential == "" {
		kept, err := s.VoiceMCPServerCredential(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return VoiceMCPServer{}, newVoiceError(400, "bad_request", in.Auth+" auth needs a credential")
		}
		if err != nil {
			return VoiceMCPServer{}, err
		}
		if kept == "" {
			return VoiceMCPServer{}, newVoiceError(400, "bad_request", in.Auth+" auth needs a credential")
		}
		in.Credential = ""
	}
	err := s.configChangeID(ctx, actor, "update", "voice_mcp_server", check, func(tx *sql.Tx) (string, error) {
		if err := bumpVoiceRevision(ctx, tx); err != nil {
			return "", err
		}
		var cred any
		if in.Credential != "" {
			if s.box == nil {
				return "", errors.New("store: no secret box")
			}
			sealed, err := s.box.Seal(in.Credential, voiceMCPAAD(id))
			if err != nil {
				return "", err
			}
			cred = sealed
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE voice_mcp_servers SET name = $2, url = $3, auth = $4, header_name = $5, token_url = $6,
				client_id = $7, oauth_scope = $8, credential = COALESCE($9, credential), timeout_ms = $10,
				enabled = $11
			WHERE id = $1`, id, in.Name, in.URL, in.Auth, in.HeaderName, in.TokenURL, in.ClientID, in.Scope,
			cred, in.TimeoutMS, in.Enabled)
		if err != nil {
			return "", err
		}
		return strconv.FormatInt(id, 10), requireRow(res)
	})
	if err != nil {
		return VoiceMCPServer{}, err
	}
	v, _, err := s.GetVoiceMCPServer(ctx, id)
	return v, err
}

// VoiceMCPServerCredential opens the server's sealed credential. It exists
// for the registry's discovery and test call and the runtime view builder
// (Task 5) — the only places an unsealed credential exists (spec S-5).
func (s *Store) VoiceMCPServerCredential(ctx context.Context, id int64) (string, error) {
	var sealed []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT credential FROM voice_mcp_servers WHERE id = $1`, id).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if len(sealed) == 0 {
		return "", nil
	}
	if s.box == nil {
		return "", errors.New("store: no secret box")
	}
	return s.box.Open(sealed, voiceMCPAAD(id))
}

// SetVoiceMCPCheck records a discovery or test result: the time and one of
// ok, refused, unreachable — never the response body (spec S-9).
func (s *Store) SetVoiceMCPCheck(ctx context.Context, id int64, status string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE voice_mcp_servers SET last_check_at = now(), last_check_status = $2 WHERE id = $1`, id, status)
	return err
}

// DeleteVoiceMCPServer removes a server no agent attaches (409 naming the
// agents, spec S-5's restrict).
func (s *Store) DeleteVoiceMCPServer(ctx context.Context, actor string, id int64, check Check) error {
	return s.configChangeID(ctx, actor, "delete", "voice_mcp_server", check, func(tx *sql.Tx) (string, error) {
		used, name, err := s.voiceMCPUsedByTx(ctx, tx, id)
		if err != nil {
			return "", err
		}
		if len(used) > 0 {
			return "", inUse("mcp server "+strconv.Quote(name), used)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM voice_mcp_servers WHERE id = $1`, id)
		if err != nil {
			return "", err
		}
		if err := requireRow(res); err != nil {
			return "", err
		}
		if err := bumpVoiceRevision(ctx, tx); err != nil {
			return "", err
		}
		return strconv.FormatInt(id, 10), nil
	})
}

// voiceMCPUsedByTx is the used-by list and the server's name inside the
// change transaction.
func (s *Store) voiceMCPUsedByTx(ctx context.Context, tx *sql.Tx, serverID int64) ([]string, string, error) {
	var name string
	if err := tx.QueryRowContext(ctx,
		`SELECT name FROM voice_mcp_servers WHERE id = $1`, serverID).Scan(&name); err != nil {
		return nil, "", mapErr(err)
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT a.name FROM voice_agent_mcp m JOIN voice_agents a ON a.id = m.agent_id
		WHERE m.server_id = $1 ORDER BY a.name`, serverID)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()
	out := []string{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, "", err
		}
		out = append(out, u)
	}
	return out, name, rows.Err()
}

// VoiceRevision returns the global voice revision counter (the runtime
// view's ETag, spec S-19).
func (s *Store) VoiceRevision(ctx context.Context) (int64, error) {
	var rev int64
	err := s.db.QueryRowContext(ctx, `SELECT revision FROM voice_revision WHERE id = 1`).Scan(&rev)
	return rev, err
}
