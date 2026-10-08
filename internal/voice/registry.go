package voice

// Package voice is the voice agent registry (spec voice-agents): agents and
// their persona versions, MCP servers with sealed credentials, attachments
// with tool allowlists, the egress-guarded discovery and test client, and
// the SIP call signature. The registry validates and orchestrates; the store
// commits every change with its audit row and the revision bumps.

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/config"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/azrtydxb/hello/internal/store"
)

// Store is the persistence the registry orchestrates; *store.Store
// implements it.
type Store interface {
	ListVoiceAgents(ctx context.Context) ([]store.VoiceAgent, error)
	GetVoiceAgent(ctx context.Context, id int64) (store.VoiceAgent, error)
	CreateVoiceAgent(ctx context.Context, actor string, in store.VoiceAgentInput, maxAgents int, check store.Check) (store.VoiceAgent, error)
	UpdateVoiceAgent(ctx context.Context, actor string, id int64, in store.VoiceAgentInput, check store.Check) (store.VoiceAgent, error)
	DeleteVoiceAgent(ctx context.Context, actor string, id int64, check store.Check) error

	ListVoiceAgentVersions(ctx context.Context, agentID int64) ([]store.VoiceAgentVersion, error)
	RestoreVoiceAgentVersion(ctx context.Context, actor string, id, revision int64, check store.Check) (store.VoiceAgent, error)

	PutVoiceAgentTools(ctx context.Context, actor string, agentID int64, atts []store.VoiceAttachmentInput, check store.Check) ([]store.VoiceAttachment, error)
	AgentAttachments(ctx context.Context, agentID int64) ([]store.VoiceAttachment, error)

	ListVoiceMCPServers(ctx context.Context) ([]store.VoiceMCPServer, []string, error)
	GetVoiceMCPServer(ctx context.Context, id int64) (store.VoiceMCPServer, []string, error)
	CreateVoiceMCPServer(ctx context.Context, actor string, in store.NewVoiceMCPServer, check store.Check) (store.VoiceMCPServer, error)
	UpdateVoiceMCPServer(ctx context.Context, actor string, id int64, in store.NewVoiceMCPServer, check store.Check) (store.VoiceMCPServer, error)
	DeleteVoiceMCPServer(ctx context.Context, actor string, id int64, check store.Check) error
	VoiceMCPServerCredential(ctx context.Context, id int64) (string, error)
	SetVoiceMCPCheck(ctx context.Context, id int64, status string) error

	VoiceRevision(ctx context.Context) (int64, error)
}

// Registry is the voice agent registry.
type Registry struct {
	st     Store
	box    *secret.Box
	cfg    config.Voice
	log    *slog.Logger
	tokens tokenCache
}

// New builds the registry.
func New(st Store, box *secret.Box, cfg config.Voice, log *slog.Logger) *Registry {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Registry{st: st, box: box, cfg: cfg, log: log}
}

// Egress returns the registry's egress guard.
func (r *Registry) Egress() *Egress {
	return &Egress{AllowPublic: r.cfg.AllowPublicMCP, AllowLoopback: r.cfg.AllowLoopback}
}

// VoiceRevision returns the global voice revision (the runtime ETag).
func (r *Registry) VoiceRevision(ctx context.Context) (int64, error) {
	return r.st.VoiceRevision(ctx)
}

// Agents (spec S-1 to S-3, S-36).

var (
	nameRe         = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	extensionRe    = regexp.MustCompile(`^[0-9]{2,10}$`)
	allowlistRe    = regexp.MustCompile(`^\+?[0-9]{2,20}$`)
	pinRe          = regexp.MustCompile(`^[0-9]{4,12}$`)
	verificationRe = regexp.MustCompile(`^(none|allowlist|pin|allowlist_or_pin)$`)
)

// AgentInput is one agent to save, as the API presents it: Pin is the
// plaintext PIN (write-only, hashed before it is stored; empty keeps the
// stored hash), SIPUser is accepted and ignored (spec S-1: never edited).
type AgentInput struct {
	Name                    string
	Description             string
	Enabled                 bool
	SIPUser                 string `json:"-"`
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
	Pin                     string
}

// fieldLimit is one string field's bound.
type fieldLimit struct {
	value string
	max   int
	what  string
}

// ValidateAgent checks the persona and limit fields against the spec's
// ranges (S-1 to S-3, S-36). It returns a *store.VoiceError answering 400.
func ValidateAgent(in *AgentInput) error {
	if !nameRe.MatchString(in.Name) {
		return inputError("name must be 1 to 64 letters, digits, dot, underscore or hyphen")
	}
	if len(in.Description) > 2000 {
		return inputError("description must be at most 2000 characters")
	}
	for _, f := range []fieldLimit{
		{in.Prompt, 8000, "prompt"}, {in.Greeting, 500, "greeting"},
		{in.Language, 32, "language"}, {in.Voice, 64, "voice"},
		{in.VoiceReference, 256, "voiceReference"}, {in.Style, 500, "style"},
	} {
		if len(f.value) > f.max {
			return inputError(f.what + " is too long")
		}
	}
	if in.Language != "" {
		// A BCP 47 tag; golang.org/x/text is a dependency already, but a
		// light structural check keeps the store regex honest without
		// importing it here.
		if strings.ContainsAny(in.Language, " \t\n") || len(in.Language) > 32 {
			return inputError("language must be a BCP 47 tag")
		}
	}
	if in.Extension != nil && *in.Extension != "" && !extensionRe.MatchString(*in.Extension) {
		return inputError("extension must be 2 to 10 digits")
	}
	if in.Temperature != nil && (*in.Temperature < 0 || *in.Temperature > 2) {
		return inputError("temperature must be between 0 and 2")
	}
	if !verificationRe.MatchString(in.CallerVerification) {
		return inputError("callerVerification must be none, allowlist, pin or allowlist_or_pin")
	}
	if len(in.CallerAllowlist) > 100 {
		return inputError("callerAllowlist holds at most 100 numbers")
	}
	for _, n := range in.CallerAllowlist {
		if !allowlistRe.MatchString(n) {
			return inputError("callerAllowlist entries must be E.164 or extension numbers")
		}
	}
	if in.Pin != "" && !pinRe.MatchString(in.Pin) {
		return inputError("pin must be 4 to 12 digits")
	}
	if err := checkRange("maxCallSeconds", in.MaxCallSeconds, 30, 1800); err != nil {
		return err
	}
	if err := checkRange("maxConcurrent", in.MaxConcurrent, 1, 50); err != nil {
		return err
	}
	if err := checkRange("maxToolCalls", in.MaxToolCalls, 0, 200); err != nil {
		return err
	}
	if err := checkRange("idleTimeoutSeconds", in.IdleTimeoutSeconds, 5, 300); err != nil {
		return err
	}
	if err := checkRange("transcriptRetentionDays", in.TranscriptRetentionDays, 1, 365); err != nil {
		return err
	}
	return nil
}

func checkRange(what string, v, lo, hi int) error {
	if v != 0 && (v < lo || v > hi) {
		return inputError(fmt.Sprintf("%s must be between %d and %d", what, lo, hi))
	}
	return nil
}

func inputError(msg string) error {
	return &store.VoiceError{Status: 400, Code: "bad_request", Msg: msg}
}

// toStore converts an AgentInput, hashing the PIN when one was given.
func toStore(in AgentInput) (store.VoiceAgentInput, error) {
	if err := ValidateAgent(&in); err != nil {
		return store.VoiceAgentInput{}, err
	}
	out := store.VoiceAgentInput{
		Name: in.Name, Description: in.Description, Enabled: in.Enabled, Extension: in.Extension,
		Prompt: in.Prompt, Greeting: in.Greeting, Language: in.Language, Voice: in.Voice,
		VoiceReference: in.VoiceReference, Style: in.Style, Temperature: in.Temperature,
		MaxCallSeconds: in.MaxCallSeconds, MaxConcurrent: in.MaxConcurrent, MaxToolCalls: in.MaxToolCalls,
		IdleTimeoutSeconds: in.IdleTimeoutSeconds, RecordTranscript: in.RecordTranscript,
		TranscriptRetentionDays: in.TranscriptRetentionDays, CallerVerification: in.CallerVerification,
		CallerAllowlist: in.CallerAllowlist,
	}
	if in.Pin != "" {
		hash, err := auth.HashPassword(in.Pin)
		if err != nil {
			return store.VoiceAgentInput{}, err
		}
		out.PINHash = hash
	}
	return out, nil
}

// ListAgents returns every agent.
func (r *Registry) ListAgents(ctx context.Context) ([]store.VoiceAgent, error) {
	return r.st.ListVoiceAgents(ctx)
}

// GetAgent returns one agent with its attachments.
func (r *Registry) GetAgent(ctx context.Context, id int64) (store.VoiceAgent, []store.VoiceAttachment, error) {
	a, err := r.st.GetVoiceAgent(ctx, id)
	if err != nil {
		return store.VoiceAgent{}, nil, err
	}
	atts, err := r.st.AgentAttachments(ctx, id)
	return a, atts, err
}

// CreateAgent validates and stores a new agent (spec S-2's limit included).
func (r *Registry) CreateAgent(ctx context.Context, actor string, in AgentInput, check store.Check) (store.VoiceAgent, error) {
	si, err := toStore(in)
	if err != nil {
		return store.VoiceAgent{}, err
	}
	return r.st.CreateVoiceAgent(ctx, actor, si, r.cfg.MaxAgents, check)
}

// UpdateAgent validates and replaces an agent's editable fields.
func (r *Registry) UpdateAgent(ctx context.Context, actor string, id int64, in AgentInput, check store.Check) (store.VoiceAgent, error) {
	si, err := toStore(in)
	if err != nil {
		return store.VoiceAgent{}, err
	}
	return r.st.UpdateVoiceAgent(ctx, actor, id, si, check)
}

// DeleteAgent removes an unreferenced agent (spec Data: 409 naming the
// references).
func (r *Registry) DeleteAgent(ctx context.Context, actor string, id int64, check store.Check) error {
	return r.st.DeleteVoiceAgent(ctx, actor, id, check)
}

// ToolIn is one allowlist entry as the API presents it: ReadOnly is the
// server's readOnlyHint annotation (from discovery, S-7), and the flags are
// optional, defaulting to !ReadOnly (spec S-6): a read-only tool needs no
// spoken confirmation and no caller verification.
type ToolIn struct {
	Name     string
	ReadOnly bool
	Confirm  *bool
	Write    *bool
}

// AttachmentIn is one attachment of a PUT /voice/agents/{id}/tools.
type AttachmentIn struct {
	ServerID int64
	Enabled  bool
	Tools    []ToolIn
}

// ToolLimits validates an attachment list against the agent's caller
// verification mode and computes the confirm/write defaults (spec S-6,
// S-36): a read-only tool defaults to confirm false and write false; any
// other tool to confirm true and write true, and a write tool may not
// attach to an agent whose mode is none. The full tool name is
// <server>.<tool>; duplicates collide (409 voice_tool_conflict).
func ToolLimits(mode string, serverName string, tools []ToolIn) ([]store.VoiceToolAccess, error) {
	out := make([]store.VoiceToolAccess, 0, len(tools))
	seen := map[string]bool{}
	for _, t := range tools {
		if t.Name == "" {
			return nil, inputError("every tool needs a name")
		}
		full := serverName + "." + t.Name
		if seen[full] {
			return nil, &store.VoiceError{Status: 409, Code: "voice_tool_conflict",
				Msg: "tool " + full + " is allowlisted twice"}
		}
		seen[full] = true
		confirm, write := !t.ReadOnly, !t.ReadOnly
		if t.Confirm != nil {
			confirm = *t.Confirm
		}
		if t.Write != nil {
			write = *t.Write
		}
		if write && mode == "none" {
			return nil, &store.VoiceError{Status: 409, Code: "voice_verification_required",
				Msg: "tool " + full + " changes data; the agent has no caller verification"}
		}
		out = append(out, store.VoiceToolAccess{Name: t.Name, Confirm: confirm, Write: write})
	}
	return out, nil
}

// PutTools replaces an agent's attachments and allowlists (spec S-6): an
// empty list attaches nothing; every tool is defaulted and checked against
// the agent's caller verification mode.
func (r *Registry) PutTools(ctx context.Context, actor string, agentID int64, in []AttachmentIn, check store.Check) ([]store.VoiceAttachment, error) {
	a, err := r.st.GetVoiceAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	atts := make([]store.VoiceAttachmentInput, 0, len(in))
	for _, at := range in {
		if at.ServerID <= 0 {
			return nil, inputError("every attachment needs a serverId")
		}
		server, _, err := r.st.GetVoiceMCPServer(ctx, at.ServerID)
		if err != nil {
			return nil, err
		}
		tools, err := ToolLimits(a.CallerVerification, server.Name, at.Tools)
		if err != nil {
			return nil, err
		}
		atts = append(atts, store.VoiceAttachmentInput{ServerID: at.ServerID, Enabled: at.Enabled, Tools: tools})
	}
	return r.st.PutVoiceAgentTools(ctx, actor, agentID, atts, check)
}
