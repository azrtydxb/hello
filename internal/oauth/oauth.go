// Package oauth is Hello's OAuth 2.1 authorization server for MCP clients
// and service accounts (spec ai-external-access S-7 to S-12): metadata,
// authorize, token, revoke, optional dynamic registration, client ID
// metadata documents, and the consent operations the console calls through
// /api/v1.
//
// No credential, code, verifier or secret is ever logged: log lines name
// the client and the outcome only.
package oauth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
)

// Options configure the authorization server.
type Options struct {
	Store Store
	// PublicURL is HELLO_PUBLIC_URL (scheme and host): the issuer and the
	// base of both resource identifiers.
	PublicURL string
	// AccessTTL is the access token lifetime; RefreshIdle and RefreshMax
	// bound a refresh token's idle time and its grant's age.
	AccessTTL, RefreshIdle, RefreshMax time.Duration
	// DCR enables POST /oauth/register; CIMDAllowPrivate lets client ID
	// metadata documents be fetched from private and loopback addresses.
	DCR, CIMDAllowPrivate bool
	Limiter               Limiter
	// Client fetches client ID metadata documents. nil uses a client that
	// refuses private, loopback and link-local addresses at dial time
	// (unless CIMDAllowPrivate); redirects, the timeout and the size cap
	// are enforced on any client.
	Client  *http.Client
	Metrics *Metrics
	Log     *slog.Logger
}

// Limiter is the per-key sliding-window throttle (Valkey, with an
// in-memory fallback).
type Limiter interface {
	// Allow records one hit on key and reports whether it is within limit
	// hits per window.
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
}

// ConsentView is what the consent screen shows for a pending request.
type ConsentView struct {
	ID          string
	ClientID    string
	ClientName  string
	ClientURI   string
	RedirectURI string
	// Verified is false for dynamically registered clients.
	Verified  bool
	Scopes    auth.Scopes
	Resources []string
	ExpiresAt time.Time
}

// Lifetimes the spec fixes.
const (
	requestTTL = 10 * time.Minute
	codeTTL    = 60 * time.Second
	// serviceTokenTTL is a client-credentials access token's lifetime.
	serviceTokenTTL = time.Hour
)

// Server is the authorization server.
type Server struct {
	o        Options
	public   string
	api, mcp string
	cimd     *cimdFetcher
	now      func() time.Time
}

// New returns the authorization server for o.PublicURL.
func New(o Options) (*Server, error) {
	u, err := url.Parse(o.PublicURL)
	if err != nil || u.Scheme == "" || u.Host == "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("oauth: PublicURL must be a scheme and host")
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.AccessTTL <= 0 {
		o.AccessTTL = time.Hour
	}
	if o.RefreshIdle <= 0 {
		o.RefreshIdle = 30 * 24 * time.Hour
	}
	if o.RefreshMax <= 0 {
		o.RefreshMax = 90 * 24 * time.Hour
	}
	if o.Limiter == nil {
		o.Limiter = NewLimiter(nil, o.Log)
	}
	pub := strings.TrimSuffix(o.PublicURL, "/")
	return &Server{
		o: o, public: pub, api: pub + "/api/v1", mcp: pub + "/mcp",
		cimd: newCIMDFetcher(o.Client, o.CIMDAllowPrivate, o.Metrics),
		now:  time.Now,
	}, nil
}

// Resources returns the two protected resource identifiers.
func (s *Server) Resources() (api, mcp string) { return s.api, s.mcp }

// MetadataURL returns the RFC 9728 protected resource metadata URL of
// resource: the well-known path with the resource's path appended.
func (s *Server) MetadataURL(resource string) string {
	return s.public + "/.well-known/oauth-protected-resource" + strings.TrimPrefix(resource, s.public)
}

// Issuer is the authorization server's issuer identifier.
func (s *Server) Issuer() string { return s.public }

// DCR reports whether dynamic client registration is on.
func (s *Server) DCR() bool { return s.o.DCR }

// Handler serves the well-known metadata documents and /oauth/*.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.serverMetadata)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", s.resourceMetadata(s.mcp, "Hello MCP server"))
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/api/v1", s.resourceMetadata(s.api, "Hello management API"))
	mux.HandleFunc("GET /oauth/authorize", s.authorize)
	mux.HandleFunc("POST /oauth/token", s.token)
	mux.HandleFunc("POST /oauth/revoke", s.revoke)
	if s.o.DCR {
		mux.HandleFunc("POST /oauth/register", s.register)
	}
	return mux
}

// InputError rejects a consent or service-account request; its message is
// safe to show to API clients.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

func inputErr(msg string) error { return &InputError{Msg: msg} }

// clientIP is the request's peer address, without the port. Forwarded
// headers are not trusted.
func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i > 0 {
		host = host[:i]
	}
	return strings.Trim(host, "[]")
}
