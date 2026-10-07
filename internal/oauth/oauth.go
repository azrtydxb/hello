// Package oauth is Hello's OAuth 2.1 authorization server for MCP clients
// and service accounts (spec ai-external-access S-7 to S-12): metadata,
// authorize, token, revoke, optional dynamic registration, client ID
// metadata documents, and the consent operations the console calls through
// /api/v1.
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

// ErrNotImplemented is returned by the consent operations until plan
// ai-external-access Task 3 lands them.
var ErrNotImplemented = errors.New("oauth: not implemented")

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
	// Client fetches client ID metadata documents.
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

// Metrics are the hello_oauth_* series; Task 3 registers them.
type Metrics struct{}

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

// Server is the authorization server.
type Server struct {
	o        Options
	public   string
	api, mcp string
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
	pub := strings.TrimSuffix(o.PublicURL, "/")
	return &Server{o: o, public: pub, api: pub + "/api/v1", mcp: pub + "/mcp"}, nil
}

// Resources returns the two protected resource identifiers.
func (s *Server) Resources() (api, mcp string) { return s.api, s.mcp }

// MetadataURL returns the RFC 9728 protected resource metadata URL of
// resource: the well-known path with the resource's path appended.
func (s *Server) MetadataURL(resource string) string {
	return s.public + "/.well-known/oauth-protected-resource" + strings.TrimPrefix(resource, s.public)
}

// Handler serves the well-known metadata documents and /oauth/*.
//
// Contract only: Task 3 implements the endpoints; until then it answers
// 404, as a deployment without HELLO_PUBLIC_URL does.
func (s *Server) Handler() http.Handler { return http.NotFoundHandler() }

// Request returns the pending authorization request id for the consent
// screen.
func (s *Server) Request(_ context.Context, _ string) (ConsentView, error) {
	return ConsentView{}, ErrNotImplemented
}

// Approve grants the request id with scopes (a subset of the requested and
// of a's GrantableScopes) and returns the client redirect carrying the code.
func (s *Server) Approve(_ context.Context, _ auth.Actor, _ string, _ auth.Scopes) (string, error) {
	return "", ErrNotImplemented
}

// Deny refuses the request id and returns the client redirect carrying
// access_denied.
func (s *Server) Deny(_ context.Context, _ auth.Actor, _ string) (string, error) {
	return "", ErrNotImplemented
}
