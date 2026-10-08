// Package mcp is Hello's MCP server (spec ai-external-access S-13 to S-16):
// tools, resources and prompts derived from the OpenAPI document, executed
// by replaying a request through the API with the caller's credentials,
// over stateless Streamable HTTP.
package mcp

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/azrtydxb/hello/internal/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Options configure the MCP server.
type Options struct {
	// API is the /api/v1 handler tool calls are replayed through.
	API  http.Handler
	Spec *apispec.Spec
	// PublicURL is HELLO_PUBLIC_URL; AllowedOrigins are
	// HELLO_MCP_ALLOWED_ORIGINS, trusted besides it.
	PublicURL      string
	AllowedOrigins []string
	Lookup         auth.Lookup
	// MetadataURL is the /mcp protected resource metadata URL.
	MetadataURL string
	Metrics     *Metrics
	Log         *slog.Logger
}

// New returns the /mcp handler. It derives the tools from o.Spec once and
// fails when an operation cannot become a tool, so a malformed document
// stops hello-control at start.
func New(o Options) (http.Handler, error) {
	if o.API == nil || o.Spec == nil {
		return nil, errors.New("mcp: API and Spec are required")
	}
	return newServer(o, o.Spec.Operations())
}

// newServer builds the handler over ops; tests pass fixture operations.
func newServer(o Options, ops []apispec.Operation) (*server, error) {
	tools, order, err := buildTools(ops)
	if err != nil {
		return nil, err
	}
	cop := http.NewCrossOriginProtection()
	origins := append([]string(nil), o.AllowedOrigins...)
	if o.PublicURL != "" {
		u, err := url.Parse(o.PublicURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("mcp: public URL %q is not an absolute URL", o.PublicURL)
		}
		origins = append(origins, u.Scheme+"://"+u.Host)
	}
	for _, origin := range origins {
		if err := cop.AddTrustedOrigin(strings.TrimSuffix(origin, "/")); err != nil {
			return nil, fmt.Errorf("mcp: allowed origin: %w", err)
		}
	}
	if o.Metrics == nil {
		o.Metrics = NewMetrics(nil)
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	byID := map[string]apispec.Operation{}
	for _, op := range ops {
		byID[op.ID] = op
	}
	s := &server{
		api:         o.API,
		lookup:      o.Lookup,
		tools:       tools,
		order:       order,
		ops:         byID,
		resource:    strings.TrimSuffix(o.PublicURL, "/") + "/mcp",
		metadataURL: o.MetadataURL,
		cop:         cop,
		metrics:     o.Metrics,
		log:         o.Log,
		handlers:    map[scopeKey]http.Handler{},
	}
	// The origin check runs in ServeHTTP, before authentication, as the
	// SDK's deprecation of StreamableHTTPOptions.CrossOriginProtection asks.
	s.opts = &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true}
	return s, nil
}
