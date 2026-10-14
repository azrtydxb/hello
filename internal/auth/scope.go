package auth

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Scope is a permission a credential carries (spec S-5).
type Scope string

// The scopes. ScopeAdmin implies ScopeWrite implies ScopeRead; ScopeSecrets
// and ScopeSession are orthogonal and held only when present.
const (
	ScopeRead    Scope = "read"
	ScopeWrite   Scope = "write"
	ScopeAdmin   Scope = "admin"
	ScopeSecrets Scope = "secrets"
	// ScopeSession is carried only by a browser session: consent approval
	// and denial need it, so no token can grant itself scopes.
	ScopeSession Scope = "session"
)

// AllScopes is every scope a user's credential can be granted. It leaves out
// ScopeSession.
var AllScopes = Scopes{ScopeRead, ScopeWrite, ScopeAdmin, ScopeSecrets}

// Scopes is the set of scopes a credential carries.
type Scopes []Scope

// Has reports whether ss grants s, following the hierarchy.
func (ss Scopes) Has(s Scope) bool {
	switch s {
	case ScopeRead:
		return slices.Contains(ss, ScopeRead) || ss.Has(ScopeWrite)
	case ScopeWrite:
		return slices.Contains(ss, ScopeWrite) || ss.Has(ScopeAdmin)
	case ScopeAdmin, ScopeSecrets, ScopeSession:
		return slices.Contains(ss, s)
	}
	return false
}

// String is the space-separated OAuth form.
func (ss Scopes) String() string {
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = string(s)
	}
	return strings.Join(parts, " ")
}

// ParseScopes parses a space-separated scope list. An unknown scope is an
// error; duplicates are dropped.
func ParseScopes(v string) (Scopes, error) {
	var out Scopes
	for _, f := range strings.Fields(v) {
		s := Scope(f)
		switch s {
		case ScopeRead, ScopeWrite, ScopeAdmin, ScopeSecrets, ScopeSession:
		default:
			return nil, fmt.Errorf("unknown scope %q", f)
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out, nil
}

// Role is a user's or service account's role (spec S-23).
type Role string

// The roles, from least to most privileged.
const (
	RoleViewer   Role = "viewer"
	RoleOperator Role = "operator"
	RoleAdmin    Role = "admin"
)

func (r Role) rank() int {
	switch r {
	case RoleViewer:
		return 1
	case RoleOperator:
		return 2
	case RoleAdmin:
		return 3
	}
	return 0
}

// AtLeast reports whether r is min or above. An unknown role is below
// every role.
func (r Role) AtLeast(minRole Role) bool {
	return r.rank() > 0 && r.rank() >= minRole.rank()
}

// ParseRole parses a role name.
func ParseRole(v string) (Role, error) {
	if r := Role(v); r.rank() > 0 {
		return r, nil
	}
	return "", fmt.Errorf("unknown role %q", v)
}

// GrantableScopes is the most a user or service account of role r may hold.
func GrantableScopes(r Role) Scopes {
	switch r {
	case RoleViewer:
		return Scopes{ScopeRead}
	case RoleOperator:
		return Scopes{ScopeRead, ScopeWrite}
	case RoleAdmin:
		return slices.Clone(AllScopes)
	}
	return nil
}

// Kind is how an actor authenticated.
type Kind string

// The credential kinds.
const (
	KindSession       Kind = "session"
	KindLegacyToken   Kind = "legacy_token"
	KindPersonalToken Kind = "personal_token"
	KindOAuth         Kind = "oauth"
	KindService       Kind = "service"
	// KindAgent is the in-product AI agent acting for a user (ai-agent S-6).
	KindAgent Kind = "agent"
)

// Credential prefixes (spec S-6), so secret scanners and log redaction can
// find them. Legacy API tokens have none.
const (
	PrefixPersonal     = "hello_pat_"
	PrefixAccess       = "hello_at_"
	PrefixRefresh      = "hello_rt_"
	PrefixClientSecret = "hello_cs_"
)

// NewPrefixed returns a new credential carrying prefix and the SHA-256
// hash of the whole credential, the only form that is stored.
func NewPrefixed(prefix string) (plain string, hash []byte) {
	t, _ := NewToken()
	plain = prefix + t
	return plain, HashToken(plain)
}

// Replay marks a request the MCP server replays through the API on behalf
// of an OAuth client.
type Replay struct{ ClientID string }

type replayKey struct{}

// WithReplay returns ctx marked as an MCP replay. Only the MCP server sets
// it; it never comes from a request.
func WithReplay(ctx context.Context, r Replay) context.Context {
	return context.WithValue(ctx, replayKey{}, r)
}

// ReplayFrom returns the replay marker WithReplay put in ctx.
func ReplayFrom(ctx context.Context) (Replay, bool) {
	r, ok := ctx.Value(replayKey{}).(Replay)
	return r, ok
}

// Agent marks a request the in-product AI agent replays for a user: the
// user the assistant is chatting with and the task it works for.
type Agent struct {
	UserID int64
	TaskID string
}

type agentKey struct{}

// WithAgent returns ctx marked as an agent replay. Only the AI package
// sets it; it never comes from a request.
func WithAgent(ctx context.Context, a Agent) context.Context {
	return context.WithValue(ctx, agentKey{}, a)
}

// AgentFrom returns the agent WithAgent put in ctx.
func AgentFrom(ctx context.Context) (Agent, bool) {
	a, ok := ctx.Value(agentKey{}).(Agent)
	return a, ok
}

// ViaAssistant is the audit via of a change made under an agent identity.
const ViaAssistant = "ai-assistant"

type viaKey struct{}

// WithVia returns ctx marking the audit via of the changes its request
// makes (for example "ai-proposal:<id>" while applying a proposal). Only
// in-process callers set it; it never comes from a request.
func WithVia(ctx context.Context, via string) context.Context {
	return context.WithValue(ctx, viaKey{}, via)
}

// ViaFrom returns the via WithVia put in ctx.
func ViaFrom(ctx context.Context) string {
	v, _ := ctx.Value(viaKey{}).(string)
	return v
}
