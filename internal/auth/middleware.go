package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
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
	// session.
	TokenID int64
}

// String is the audit form of the actor: "user:<username>" for a session,
// "token:<id>" for an API token.
func (a Actor) String() string {
	if a.TokenID != 0 {
		return "token:" + strconv.FormatInt(a.TokenID, 10)
	}
	return "user:" + a.Username
}

// Lookup resolves stored credential hashes to actors. Both methods return
// ErrNoCredentials for an unknown, expired or revoked credential.
type Lookup interface {
	// SessionActor resolves an unexpired session.
	SessionActor(ctx context.Context, hash []byte) (Actor, error)
	// TokenActor resolves an API token and records its use.
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

// Middleware admits a request carrying a valid bearer token or session
// cookie, with the actor in its context, and answers 401 otherwise. When an
// Authorization header is present it alone decides; the cookie is not
// consulted.
func Middleware(l Lookup, log *slog.Logger) func(http.Handler) http.Handler {
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
			default:
				c, cerr := r.Cookie(SessionCookie)
				if cerr != nil || c.Value == "" {
					err = ErrNoCredentials
					break
				}
				a, err = l.SessionActor(r.Context(), HashToken(c.Value))
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
