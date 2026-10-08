package api

// The voice agent operations (spec voice-agents, plan Task 2): agents and
// their persona versions, their tools attachments and the per-agent halves
// of caller verification. Thin handlers: decode, call internal/voice, map
// the store's voice errors onto the document's statuses.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/internal/voice"
)

// Voice is the registry behind the voice routes; *voice.Registry implements
// it. nil answers 503, like every other optional dependency.
type Voice interface {
	ListAgents(ctx context.Context) ([]store.VoiceAgent, error)
	GetAgent(ctx context.Context, id int64) (store.VoiceAgent, []store.VoiceAttachment, error)
	CreateAgent(ctx context.Context, actor string, in voice.AgentInput, check store.Check) (store.VoiceAgent, error)
	UpdateAgent(ctx context.Context, actor string, id int64, in voice.AgentInput, check store.Check) (store.VoiceAgent, error)
	DeleteAgent(ctx context.Context, actor string, id int64, check store.Check) error
	ListVersions(ctx context.Context, agentID int64) ([]store.VoiceAgentVersion, error)
	RestoreVersion(ctx context.Context, actor string, id, revision int64, check store.Check) (store.VoiceAgent, error)
	PutTools(ctx context.Context, actor string, agentID int64, in []voice.AttachmentIn, check store.Check) ([]store.VoiceAttachment, error)

	MCPServers(ctx context.Context) ([]store.VoiceMCPServer, []string, error)
	MCPServer(ctx context.Context, id int64) (store.VoiceMCPServer, []string, error)
	CreateMCPServer(ctx context.Context, actor string, in voice.ServerInput, check store.Check) (store.VoiceMCPServer, error)
	UpdateMCPServer(ctx context.Context, actor string, id int64, in voice.ServerInput, check store.Check) (store.VoiceMCPServer, error)
	DeleteMCPServer(ctx context.Context, actor string, id int64, check store.Check) error
	Discover(ctx context.Context, id int64) ([]voice.DiscoveredTool, error)
	TestConnection(ctx context.Context, id int64) (voice.TestResult, error)
}

var _ Voice = (*voice.Registry)(nil)

// voiceHandler returns 503 when the registry is not wired (a deployment
// without the voice registry built; cmd always wires it).
func (s *server) voiceReady(w http.ResponseWriter) bool {
	if s.Voice == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "voice registry is not available")
		return false
	}
	return true
}

// voiceError maps a registry or store error onto the document's statuses.
func (s *server) voiceError(w http.ResponseWriter, what string, err error) {
	var ve *store.VoiceError
	var used *store.InUseError
	switch {
	case errors.As(err, &ve):
		writeError(w, ve.Status, ve.Code, ve.Msg)
	case errors.As(err, &used):
		writeError(w, http.StatusConflict, "conflict", used.Msg)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", what+": not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", what+": already exists")
	default:
		s.internal(w, what, err)
	}
}

// Agents.

type voiceAgentJSON struct {
	ID                      int64     `json:"id"`
	Name                    string    `json:"name"`
	Description             string    `json:"description"`
	Enabled                 bool      `json:"enabled"`
	SIPUser                 string    `json:"sipUser"`
	Extension               *string   `json:"extension"`
	Prompt                  string    `json:"prompt"`
	Greeting                string    `json:"greeting"`
	Language                string    `json:"language"`
	Voice                   string    `json:"voice"`
	VoiceReference          string    `json:"voiceReference"`
	Style                   string    `json:"style"`
	Temperature             *float64  `json:"temperature"`
	MaxCallSeconds          int       `json:"maxCallSeconds"`
	MaxConcurrent           int       `json:"maxConcurrent"`
	MaxToolCalls            int       `json:"maxToolCalls"`
	IdleTimeoutSeconds      int       `json:"idleTimeoutSeconds"`
	RecordTranscript        bool      `json:"recordTranscript"`
	TranscriptRetentionDays int       `json:"transcriptRetentionDays"`
	CallerVerification      string    `json:"callerVerification"`
	CallerAllowlist         []string  `json:"callerAllowlist"`
	PINSet                  bool      `json:"pinSet"`
	Revision                int64     `json:"revision"`
	CreatedAt               time.Time `json:"createdAt"`
	UpdatedAt               time.Time `json:"updatedAt"`
	Attachments             []voiceAttachmentJSON
}

// voiceAgentJSON builds the response body of one agent. PINHash is never
// read into it (mutation check: dropping the credential-style filter in
// store.scanAgent — exposing pin_hash to callers — fails the
// pin-not-in-response assertions of TestVoiceCallerVerification).
func agentBody(a store.VoiceAgent, atts []store.VoiceAttachment) voiceAgentJSON {
	body := voiceAgentJSON{
		ID: a.ID, Name: a.Name, Description: a.Description, Enabled: a.Enabled, SIPUser: a.SIPUser,
		Extension: a.Extension, Prompt: a.Prompt, Greeting: a.Greeting, Language: a.Language,
		Voice: a.Voice, VoiceReference: a.VoiceReference, Style: a.Style, Temperature: a.Temperature,
		MaxCallSeconds: a.MaxCallSeconds, MaxConcurrent: a.MaxConcurrent, MaxToolCalls: a.MaxToolCalls,
		IdleTimeoutSeconds: a.IdleTimeoutSeconds, RecordTranscript: a.RecordTranscript,
		TranscriptRetentionDays: a.TranscriptRetentionDays, CallerVerification: a.CallerVerification,
		CallerAllowlist: a.CallerAllowlist, PINSet: a.PINHash != "", Revision: a.Revision,
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt, Attachments: []voiceAttachmentJSON{},
	}
	for _, at := range atts {
		body.Attachments = append(body.Attachments, attachmentBody(at))
	}
	return body
}

func (s *server) listVoiceAgents(w http.ResponseWriter, r *http.Request) {
	if !s.voiceReady(w) {
		return
	}
	as, err := s.Voice.ListAgents(r.Context())
	if err != nil {
		s.voiceError(w, "voice agents", err)
		return
	}
	out := make([]voiceAgentJSON, len(as))
	for i, a := range as {
		out[i] = agentBody(a, nil)
	}
	writeJSON(w, http.StatusOK, out)
}

// voiceAgentIn is the create and update body.
type voiceAgentIn struct {
	Name                    string   `json:"name"`
	Description             string   `json:"description"`
	Enabled                 *bool    `json:"enabled"`
	SIPUser                 string   `json:"sipUser"`
	Extension               *string  `json:"extension"`
	Prompt                  string   `json:"prompt"`
	Greeting                string   `json:"greeting"`
	Language                string   `json:"language"`
	Voice                   string   `json:"voice"`
	VoiceReference          string   `json:"voiceReference"`
	Style                   string   `json:"style"`
	Temperature             *float64 `json:"temperature"`
	MaxCallSeconds          int      `json:"maxCallSeconds"`
	MaxConcurrent           int      `json:"maxConcurrent"`
	MaxToolCalls            int      `json:"maxToolCalls"`
	IdleTimeoutSeconds      int      `json:"idleTimeoutSeconds"`
	RecordTranscript        bool     `json:"recordTranscript"`
	TranscriptRetentionDays int      `json:"transcriptRetentionDays"`
	CallerVerification      string   `json:"callerVerification"`
	CallerAllowlist         []string `json:"callerAllowlist"`
	Pin                     string   `json:"pin"`
}

func (in voiceAgentIn) input() voice.AgentInput {
	enabled := in.Enabled == nil || *in.Enabled
	return voice.AgentInput{
		Name: in.Name, Description: in.Description, Enabled: enabled, SIPUser: in.SIPUser,
		Extension: in.Extension, Prompt: in.Prompt, Greeting: in.Greeting, Language: in.Language,
		Voice: in.Voice, VoiceReference: in.VoiceReference, Style: in.Style, Temperature: in.Temperature,
		MaxCallSeconds: in.MaxCallSeconds, MaxConcurrent: in.MaxConcurrent, MaxToolCalls: in.MaxToolCalls,
		IdleTimeoutSeconds: in.IdleTimeoutSeconds, RecordTranscript: in.RecordTranscript,
		TranscriptRetentionDays: in.TranscriptRetentionDays, CallerVerification: in.CallerVerification,
		CallerAllowlist: in.CallerAllowlist, Pin: in.Pin,
	}
}

func (s *server) createVoiceAgent(w http.ResponseWriter, r *http.Request) {
	if !s.voiceReady(w) {
		return
	}
	var in voiceAgentIn
	if !decode(w, r, &in) {
		return
	}
	a, err := s.Voice.CreateAgent(r.Context(), actor(r).Username, in.input(), nil)
	if err != nil {
		s.voiceError(w, "voice agent", err)
		return
	}
	writeJSON(w, http.StatusCreated, agentBody(a, nil))
}

func (s *server) getVoiceAgent(w http.ResponseWriter, r *http.Request) {
	if !s.voiceReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	a, atts, err := s.Voice.GetAgent(r.Context(), id)
	if err != nil {
		s.voiceError(w, "voice agent", err)
		return
	}
	writeJSON(w, http.StatusOK, agentBody(a, atts))
}

func (s *server) updateVoiceAgent(w http.ResponseWriter, r *http.Request) {
	if !s.voiceReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in voiceAgentIn
	if !decode(w, r, &in) {
		return
	}
	a, err := s.Voice.UpdateAgent(r.Context(), actor(r).Username, id, in.input(), nil)
	if err != nil {
		s.voiceError(w, "voice agent", err)
		return
	}
	writeJSON(w, http.StatusOK, agentBody(a, nil))
}

func (s *server) deleteVoiceAgent(w http.ResponseWriter, r *http.Request) {
	if !s.voiceReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Voice.DeleteAgent(r.Context(), actor(r).Username, id, nil); err != nil {
		s.voiceError(w, "voice agent", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Versions.

type voiceAgentVersionJSON struct {
	Revision  int64           `json:"revision"`
	Actor     string          `json:"actor"`
	CreatedAt time.Time       `json:"createdAt"`
	Persona   json.RawMessage `json:"persona"`
}

func (s *server) listVoiceAgentVersions(w http.ResponseWriter, r *http.Request) {
	if !s.voiceReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	vs, err := s.Voice.ListVersions(r.Context(), id)
	if err != nil {
		s.voiceError(w, "voice agent versions", err)
		return
	}
	out := make([]voiceAgentVersionJSON, len(vs))
	for i, v := range vs {
		out[i] = voiceAgentVersionJSON{Revision: v.Revision, Actor: v.Actor, CreatedAt: v.CreatedAt, Persona: v.Persona}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) restoreVoiceAgentVersion(w http.ResponseWriter, r *http.Request) {
	if !s.voiceReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rev, err := strconv.ParseInt(r.PathValue("v"), 10, 64)
	if err != nil || rev <= 0 {
		writeError(w, http.StatusNotFound, "not_found", "version: not found")
		return
	}
	a, err := s.Voice.RestoreVersion(r.Context(), actor(r).Username, id, rev, nil)
	if err != nil {
		s.voiceError(w, "voice agent version", err)
		return
	}
	writeJSON(w, http.StatusOK, agentBody(a, nil))
}

// Tools attachments.

type voiceToolIn struct {
	Name     string `json:"name"`
	ReadOnly bool   `json:"readOnly"`
	Confirm  *bool  `json:"confirm"`
	Write    *bool  `json:"write"`
}

type voiceAttachmentIn struct {
	ServerID int64         `json:"serverId"`
	Enabled  *bool         `json:"enabled"`
	Tools    []voiceToolIn `json:"tools"`
}

type voiceToolJSON struct {
	Name    string `json:"name"`
	Confirm bool   `json:"confirm"`
	Write   bool   `json:"write"`
}

type voiceAttachmentJSON struct {
	ServerID   int64           `json:"serverId"`
	ServerName string          `json:"serverName"`
	Enabled    bool            `json:"enabled"`
	Tools      []voiceToolJSON `json:"tools"`
}

func attachmentBody(at store.VoiceAttachment) voiceAttachmentJSON {
	out := voiceAttachmentJSON{ServerID: at.ServerID, ServerName: at.ServerName, Enabled: at.Enabled, Tools: []voiceToolJSON{}}
	for _, t := range at.Tools {
		out.Tools = append(out.Tools, voiceToolJSON{Name: t.Name, Confirm: t.Confirm, Write: t.Write})
	}
	return out
}

func (s *server) putVoiceAgentTools(w http.ResponseWriter, r *http.Request) {
	if !s.voiceReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Servers []voiceAttachmentIn `json:"servers"`
	}
	if !decode(w, r, &in) {
		return
	}
	atts := make([]voice.AttachmentIn, 0, len(in.Servers))
	for _, sa := range in.Servers {
		at := voice.AttachmentIn{ServerID: sa.ServerID, Enabled: sa.Enabled == nil || *sa.Enabled}
		for _, t := range sa.Tools {
			at.Tools = append(at.Tools, voice.ToolIn{Name: t.Name, ReadOnly: t.ReadOnly, Confirm: t.Confirm, Write: t.Write})
		}
		atts = append(atts, at)
	}
	saved, err := s.Voice.PutTools(r.Context(), actor(r).Username, id, atts, nil)
	if err != nil {
		s.voiceError(w, "voice agent tools", err)
		return
	}
	out := struct {
		Servers []voiceAttachmentJSON `json:"servers"`
	}{Servers: []voiceAttachmentJSON{}}
	for _, at := range saved {
		out.Servers = append(out.Servers, attachmentBody(at))
	}
	writeJSON(w, http.StatusOK, out)
}
