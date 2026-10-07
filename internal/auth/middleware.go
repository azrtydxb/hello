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
func Middleware(l Lookup, o Options, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var (
				a   Actor
				err error
			)
			switch h := r.Header.Get("Authorization"); {
			case h != "":
				tok, ok := strings.CutPrefix(h, "Bearer ")
				if !ok || tok == "" {
					err = ErrNoCredentials
					break
				}
				a, err = l.TokenActor(r.Context(), HashToken(tok))
				if a.Kind == "" {
					a.Kind = KindLegacyToken
				}
				if a.Kind == KindLegacyToken && a.Scopes == nil {
					a.Scopes = slices.Clone(AllScopes)
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
				WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
				return
			case err != nil:
				log.Error("authenticate request", "error", err)
				WriteError(w, http.StatusInternalServerError, "internal", "internal error")
				return
			}
			next.ServeHTTP(w, r.WithContext(WithActor(r.Context(), a)))
		})
	}
}

// Require admits a request whose actor has at least role minRole and holds
// scope s, answering 403 forbidden_role or 403 insufficient_scope otherwise.
// It runs inside Middleware.
//
// Contract only: enforcement lands with the authorization server (plan
// ai-external-access, Task 3); until then it admits every request, so
// sessions and API tokens behave exactly as before.
func Require(minRole Role, s Scope) func(http.Handler) http.Handler {
	_, _ = minRole, s
	return func(next http.Handler) http.Handler { return next }
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
