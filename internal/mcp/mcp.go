// Package mcp is Hello's MCP server (spec ai-external-access S-13 to S-16):
// tools, resources and prompts derived from the OpenAPI document, executed
// by replaying a request through the API with the caller's credentials,
// over stateless Streamable HTTP.
package mcp

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/version"
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

// Metrics are the hello_mcp_* series; Task 5 registers them.
type Metrics struct{}

// New returns the /mcp handler.
//
// Contract only: plan ai-external-access Task 5 adds authentication, the
// tools, resources and prompts; until then it serves an empty stateless
// server.
func New(o Options) (http.Handler, error) {
	if o.API == nil || o.Spec == nil {
		return nil, errors.New("mcp: API and Spec are required")
	}
	srv := sdk.NewServer(&sdk.Implementation{Name: "hello", Version: version.Version}, nil)
	return sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv },
		&sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true}), nil
}
