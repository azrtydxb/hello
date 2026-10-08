package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// SessionCookie is the name of the browser session cookie.
const SessionCookie = "hello_session"

// Actor is the authenticated principal of a request.
type Actor struct {
	UserID   int64
	Username string
	// TokenID is the API token that authenticated the request, or 0 for a
	// session or an OAuth credential.
	TokenID int64
	// Role is the owning user's current role, or the service account's.
	Role Role
	// Kind is how the actor authenticated.
	Kind Kind
	// Scopes are what the credential carries: AllScopes for legacy tokens,
	// AllScopes plus ScopeSession for sessions.
	Scopes Scopes
	// ClientID is the OAuth client of an OAuth or service credential.
	ClientID string
	// Audience is the resources an OAuth access token is bound to.
	Audience []string
	// ServiceName is the service account's name for KindService.
	ServiceName string
	// Via is how the change was made when not through a client: the audit
	// via of an agent request (ViaAssistant), else empty.
	Via string
}

// String is the audit form of the actor: "service:<name>" for a service
// account, "token:<id>" for an API token, "user:<username>" otherwise.
func (a Actor) String() string {
	switch {
	case a.Kind == KindService:
		return "service:" + a.ServiceName
	case a.TokenID != 0:
		return "token:" + strconv.FormatInt(a.TokenID, 10)
	}
	return "user:" + a.Username
}

// Lookup resolves stored credential hashes to actors. Both methods return
// ErrNoCredentials for an unknown, expired or revoked credential.
type Lookup interface {
	// SessionActor resolves an unexpired session.
	SessionActor(ctx context.Context, hash []byte) (Actor, error)
	// TokenActor resolves any bearer credential and records its use: the
	// prefix decides the kind (PrefixPersonal, PrefixAccess, ...), an
	// unprefixed token is a legacy API token.
	TokenActor(ctx context.Context, hash []byte) (Actor, error)
	// UserActor resolves the user an agent acts for, with their current
	// role; ErrNoCredentials when the user no longer exists.
	UserActor(ctx context.Context, id int64) (Actor, error)
}

type actorKey struct{}

// WithActor returns ctx carrying a.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom returns the actor Middleware put in ctx.
func ActorFrom(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorKey{}).(Actor)
	return a, ok
}

// Options configure Middleware.
type Options struct {
	// Resource is the resource identifier this middleware guards (the
	// /api/v1 or /mcp URL under HELLO_PUBLIC_URL); empty without it.
	Resource string
	// MetadataURL is the protected resource metadata URL that 401 and 403
	// challenges point to; empty without HELLO_PUBLIC_URL.
	MetadataURL string
	// Cookies admits the session cookie; /mcp takes bearer tokens only.
	Cookies bool
}

// Middleware admits a request carrying a valid bearer token or (with
// o.Cookies) session cookie, with the actor in its context, and answers 401
// otherwise. When an Authorization header is present it alone decides; the
// cookie is not consulted.
//
// An OAuth access token is admitted only for a resource in its audience:
// o.Resource, or, inside an MCP replay (ReplayFrom) by the same client, the
// MCP resource next to it (spec S-9). The session scope is only ever held
// by a session (spec S-5). A request without credentials whose context
// holds an agent (AgentFrom) is that user, read only. With o.MetadataURL set, every 401 carries the
// RFC 9728 challenge.
func Middleware(l Lookup, o Options, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var (
				a   Actor
				err error
			)
			ag, isAgent := AgentFrom(r.Context())
			switch h := r.Header.Get("Authorization"); {
			case h == "" && isAgent:
				// The agent marker rides in the context, never in a header,
				// so only code in this process can set it. The agent reads
				// as the user it works for, with their current role.
				a, err = l.UserActor(r.Context(), ag.UserID)
				a.Kind, a.Scopes, a.Via = KindAgent, Scopes{ScopeRead}, ViaAssistant
			case h != "":
				tok, ok := strings.CutPrefix(h, "Bearer ")
				if !ok || tok == "" {
					err = ErrNoCredentials
					break
				}
				a, err = l.TokenActor(r.Context(), HashToken(tok))
				if err != nil {
					break
				}
				if a.Kind == "" {
					a.Kind = KindLegacyToken
				}
				if a.Kind == KindLegacyToken && a.Scopes == nil {
					a.Scopes = slices.Clone(AllScopes)
				}
				// Only a browser session holds session: no token can
				// approve consent, whatever was stored for it.
				a.Scopes = slices.DeleteFunc(slices.Clone(a.Scopes), func(s Scope) bool { return s == ScopeSession })
				if !o.inAudience(r.Context(), a) {
					o.challenge(w, `error="invalid_token", error_description="the token is not for this resource", `)
					WriteError(w, http.StatusUnauthorized, "invalid_token", "the token is not valid for this resource")
					return
				}
			case !o.Cookies:
				err = ErrNoCredentials
			default:
				c, cerr := r.Cookie(SessionCookie)
				if cerr != nil || c.Value == "" {
					err = ErrNoCredentials
					break
				}
				a, err = l.SessionActor(r.Context(), HashToken(c.Value))
				a.Kind = KindSession
				a.Scopes = append(slices.Clone(AllScopes), ScopeSession)
			}
			switch {
			case errors.Is(err, ErrNoCredentials):
				o.challenge(w, "")
				WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
				return
			case err != nil:
				log.Error("authenticate request", "error", err)
				WriteError(w, http.StatusInternalServerError, "internal", "internal error")
				return
			}
			if a.Via == "" {
				a.Via = ViaFrom(r.Context())
			}
			next.ServeHTTP(w, r.WithContext(WithActor(withChallenge(r.Context(), o.MetadataURL), a)))
		})
	}
}

// inAudience reports whether a's credential may be used on o.Resource.
// Credentials without an audience (sessions, API tokens) may be used
// anywhere the middleware admits them.
func (o Options) inAudience(ctx context.Context, a Actor) bool {
	if a.Kind != KindOAuth && a.Kind != KindService {
		return true
	}
	if o.Resource == "" {
		return false
	}
	if slices.Contains(a.Audience, o.Resource) {
		return true
	}
	rp, ok := ReplayFrom(ctx)
	if !ok || rp.ClientID != a.ClientID {
		return false
	}
	// The MCP server replays a tool call through the API with the
	// caller's token; the token is then bound to the MCP resource, which
	// sits next to the API under the public URL.
	base, cut := strings.CutSuffix(o.Resource, "/api/v1")
	return cut && slices.Contains(a.Audience, base+"/mcp")
}

// challenge sets the WWW-Authenticate challenge of a 401 or 403 when the
// resource has protected resource metadata (spec S-7): extra is any
// error parameters, each followed by ", ".
func (o Options) challenge(w http.ResponseWriter, extra string) {
	if o.MetadataURL == "" {
		return
	}
	w.Header().Set("WWW-Authenticate", `Bearer `+extra+`resource_metadata="`+o.MetadataURL+`", scope="read"`)
}

type challengeKey struct{}

func withChallenge(ctx context.Context, metadataURL string) context.Context {
	if metadataURL == "" {
		return ctx
	}
	return context.WithValue(ctx, challengeKey{}, metadataURL)
}

// Require admits a request whose actor has at least role minRole and holds
// scope s, answering 403 forbidden_role or 403 insufficient_scope (with the
// step-up challenge of spec S-5) otherwise. It runs inside Middleware; the
// role is the one the Lookup read for this request.
func Require(minRole Role, s Scope) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			a, ok := ActorFrom(r.Context())
			if !ok {
				WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
				return
			}
			if !a.Role.AtLeast(minRole) {
				WriteError(w, http.StatusForbidden, "forbidden_role", "this needs the "+string(minRole)+" role")
				return
			}
			if !a.Scopes.Has(s) {
				ch := `Bearer error="insufficient_scope", scope="` + string(s) + `"`
				if md, _ := r.Context().Value(challengeKey{}).(string); md != "" {
					ch += `, resource_metadata="` + md + `"`
				}
				w.Header().Set("WWW-Authenticate", ch)
				WriteError(w, http.StatusForbidden, "insufficient_scope", "this needs the "+string(s)+" scope")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// WriteError writes the API's error envelope.
func WriteError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

// FirstUserCreator creates the first management user.
type FirstUserCreator interface {
	// CountUsers reports how many users exist.
	CountUsers(ctx context.Context) (int, error)
	// CreateFirstUser inserts the user unless one with that name exists and
	// reports whether it did.
	CreateFirstUser(ctx context.Context, username, passwordHash string) (bool, error)
}

// BootstrapUsername is the user created from HELLO_BOOTSTRAP_ADMIN_PASSWORD.
const BootstrapUsername = "admin"

// Bootstrap creates user admin with password when no user exists and the
// password is set. It logs that it happened, never the password.
func Bootstrap(ctx context.Context, s FirstUserCreator, password string, log *slog.Logger) error {
	if password == "" {
		return nil
	}
	n, err := s.CountUsers(ctx)
	if err != nil || n > 0 {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	created, err := s.CreateFirstUser(ctx, BootstrapUsername, hash)
	if err != nil {
		return err
	}
	if created {
		log.Info("bootstrap admin user created", "username", BootstrapUsername)
	}
	return nil
}
