// Package fakemcp is a fake MCP server for Hello's tests: a streamable HTTP
// endpoint with annotated tools (spec S-6), a fake OAuth token endpoint for
// the client-credentials exchange (S-5), and a few misbehaving endpoints for
// the egress and protocol failure paths (S-7, S-8).
package fakemcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server is the fake MCP server under test.
type Server struct {
	*httptest.Server

	mu       sync.Mutex
	tokens   []TokenRequest
	tokenTTL int    // seconds; 0 uses 300
	secret   string // when set, /token demands this client secret
	bearer   string // when set, /mcp demands this bearer token
	hits     map[string]int
}

// RequireBearer makes /mcp demand the bearer token.
func (s *Server) RequireBearer(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bearer = token
}

// TokenRequest records one call of the fake token endpoint.
type TokenRequest struct {
	GrantType     string
	ClientID      string
	ClientSecret  string
	Scope         string
	Authorization string // the Authorization header, or ""
}

// Tokens returns the recorded token endpoint calls.
func (s *Server) TokenRequests() []TokenRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]TokenRequest{}, s.tokens...)
}

// SetTokenTTL sets the expires_in of future token responses (seconds).
func (s *Server) SetTokenTTL(seconds int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokenTTL = seconds
}

// RequireSecret makes /token demand the client secret.
func (s *Server) RequireSecret(secret string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secret = secret
}

// Hits returns how many times a path was requested.
func (s *Server) Hits(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

// New starts the fake MCP server; it closes with the test.
func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{hits: map[string]int{}}
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "fakemcp", Version: "1.0.0"}, nil)
	addTool(mcpServer, "lookup", "Look one thing up.", true)
	addTool(mcpServer, "create_thing", "Create one thing.", false)
	addTool(mcpServer, "update_record", "Change one record.", false)
	mux := http.NewServeMux()
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, nil)
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.count("/mcp")
		s.mu.Lock()
		want := s.bearer
		s.mu.Unlock()
		if want != "" && r.Header.Get("Authorization") != "Bearer "+want && r.Header.Get("X-Api-Key") != want {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		mcpHandler.ServeHTTP(w, r)
	}))
	mux.HandleFunc("/token", s.serveToken)
	mux.HandleFunc("/notmcp", func(w http.ResponseWriter, _ *http.Request) {
		s.count("/notmcp")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"not":"an mcp server"}`))
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		s.count("/redirect")
		http.Redirect(w, r, "/mcp", http.StatusFound)
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, _ *http.Request) {
		s.count("/big")
		_, _ = w.Write([]byte(strings.Repeat("x", (1<<20)+4096)))
	})
	s.Server = httptest.NewServer(mux)
	// /redirect needs the final URL, known only after Start.
	t.Cleanup(s.Close)
	return s
}

func (s *Server) count(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hits[path]++
}

// serveToken is the fake OAuth token endpoint (RFC 6749 client credentials).
func (s *Server) serveToken(w http.ResponseWriter, r *http.Request) {
	s.count("/token")
	_ = r.ParseForm()
	s.mu.Lock()
	s.tokens = append(s.tokens, TokenRequest{
		GrantType:     r.FormValue("grant_type"),
		ClientID:      r.FormValue("client_id"),
		ClientSecret:  r.FormValue("client_secret"),
		Scope:         r.FormValue("scope"),
		Authorization: r.Header.Get("Authorization"),
	})
	secret := s.secret
	ttl := s.tokenTTL
	s.mu.Unlock()
	if secret != "" && r.FormValue("client_secret") != secret {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
		return
	}
	ttlOut := ttl
	if ttlOut == 0 {
		ttlOut = 300
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "fake-access-token", "token_type": "Bearer", "expires_in": ttlOut,
	})
}

// addTool registers one tool with a readOnlyHint annotation.
func addTool(s *mcp.Server, name, description string, readOnly bool) {
	t := &mcp.Tool{
		Name:        name,
		Description: description,
		InputSchema: &jsonschema.Schema{Type: "object"},
	}
	if readOnly {
		t.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: true}
	}
	s.AddTool(t, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
}
