package voice

import (
	"context"
	"strconv"
)

// View is what talking-agent reads from GET /api/v1/voice-runtime/agents
// (spec S-19, shared contract 5): every enabled agent with its persona,
// limits and unsealed MCP credentials, under one revision.
type View struct {
	Tenant   string         `json:"tenant"`
	Revision int64          `json:"revision"`
	Agents   []RuntimeAgent `json:"agents"`
}

// ETag is the strong entity tag of the view: the quoted global voice
// revision, so it changes exactly when the view does.
func (v View) ETag() string { return ETag(v.Revision) }

// ETag returns the strong entity tag for a voice revision, for example "42".
func ETag(revision int64) string { return `"` + strconv.FormatInt(revision, 10) + `"` }

// RuntimeAgent is one agent in the view.
type RuntimeAgent struct {
	Name     string `json:"name"`
	SIPUser  string `json:"sipUser"`
	Revision int64  `json:"revision"`

	Prompt         string   `json:"prompt"`
	Greeting       string   `json:"greeting"`
	Language       string   `json:"language"`
	Voice          string   `json:"voice"`
	VoiceReference string   `json:"voiceReference"`
	Style          string   `json:"style"`
	Temperature    *float64 `json:"temperature"`

	MaxCallSeconds     int `json:"maxCallSeconds"`
	MaxConcurrent      int `json:"maxConcurrent"`
	MaxToolCalls       int `json:"maxToolCalls"`
	IdleTimeoutSeconds int `json:"idleTimeoutSeconds"`

	RecordTranscript bool `json:"recordTranscript"`

	// CallerVerification is none, allowlist, pin or allowlist_or_pin (S-36).
	CallerVerification string   `json:"callerVerification"`
	CallerAllowlist    []string `json:"callerAllowlist"`
	PINHash            string   `json:"pinHash"`

	Servers []RuntimeServer `json:"servers"`
}

// RuntimeServer is an MCP server attached to an agent. Credential is the
// unsealed secret and exists only in a View; it is never logged.
type RuntimeServer struct {
	Name       string       `json:"name"`
	URL        string       `json:"url"`
	Auth       string       `json:"auth"` // none, bearer, header, oauth_client_credentials
	TimeoutMS  int          `json:"timeoutMs"`
	HeaderName string       `json:"headerName,omitempty"`
	Credential string       `json:"credential,omitempty"`
	TokenURL   string       `json:"tokenUrl,omitempty"`
	ClientID   string       `json:"clientId,omitempty"`
	Scope      string       `json:"scope,omitempty"`
	Audience   string       `json:"audience,omitempty"`
	Tools      []ToolAccess `json:"tools"`
}

// ToolAccess is one allowlisted tool of an attachment (S-6): the tool name
// without the server prefix, whether talking-agent reads the action aloud and
// needs a spoken yes first, and whether the tool changes data (which needs
// caller verification).
type ToolAccess struct {
	Name    string `json:"name"`
	Confirm bool   `json:"confirm"`
	Write   bool   `json:"write"`
}

// Runtime builds the view for talking-agent. Task 5 implements it; it is the
// only place an unsealed credential is produced.
type Runtime interface {
	RuntimeView(ctx context.Context) (View, error)
}
