package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/azrtydxb/hello/internal/api"
	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/oauth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	testPublic   = "https://hello.test"
	testMetadata = testPublic + "/.well-known/oauth-protected-resource/mcp"
	tokRead      = "hello_pat_read"
	tokWrite     = "hello_pat_write"
	tokAdmin     = "hello_pat_admin"
	tokOAuth     = "hello_at_mcp"
	tokOAuthAPI  = "hello_at_api"
	tokOAuthW    = "hello_at_write"
	tokSecrets   = "hello_pat_secrets"
	tokBroken    = "hello_pat_broken"
)

// fakeLookup resolves the test tokens.
type fakeLookup struct{}

var errStoreDown = io.ErrUnexpectedEOF

func actorFor(tok string) (auth.Actor, error) {
	switch tok {
	case tokRead:
		return auth.Actor{UserID: 1, Username: "ann", TokenID: 11, Role: auth.RoleViewer, Kind: auth.KindPersonalToken, Scopes: auth.Scopes{auth.ScopeRead}}, nil
	case tokWrite:
		return auth.Actor{UserID: 2, Username: "bob", TokenID: 12, Role: auth.RoleOperator, Kind: auth.KindPersonalToken, Scopes: auth.Scopes{auth.ScopeWrite}}, nil
	case tokAdmin:
		return auth.Actor{UserID: 3, Username: "cat", TokenID: 13, Role: auth.RoleAdmin, Kind: auth.KindPersonalToken, Scopes: auth.Scopes{auth.ScopeAdmin}}, nil
	case tokSecrets:
		return auth.Actor{UserID: 3, Username: "cat", TokenID: 14, Role: auth.RoleAdmin, Kind: auth.KindPersonalToken, Scopes: auth.Scopes{auth.ScopeSecrets}}, nil
	case tokOAuth:
		return auth.Actor{UserID: 1, Username: "ann", Role: auth.RoleViewer, Kind: auth.KindOAuth, Scopes: auth.Scopes{auth.ScopeRead}, ClientID: "https://client.test/cimd", Audience: []string{testPublic + "/mcp"}}, nil
	case tokOAuthW:
		return auth.Actor{UserID: 2, Username: "bob", Role: auth.RoleOperator, Kind: auth.KindOAuth, Scopes: auth.Scopes{auth.ScopeRead, auth.ScopeWrite}, ClientID: "https://client.test/cimd", Audience: []string{testPublic + "/mcp"}}, nil
	case tokOAuthAPI:
		return auth.Actor{UserID: 1, Username: "ann", Role: auth.RoleViewer, Kind: auth.KindOAuth, Scopes: auth.Scopes{auth.ScopeRead}, ClientID: "c", Audience: []string{testPublic + "/api/v1"}}, nil
	case tokBroken:
		return auth.Actor{}, errStoreDown
	}
	return auth.Actor{}, auth.ErrNoCredentials
}

var tokenByHash = func() map[string]string {
	m := map[string]string{}
	for _, t := range []string{tokRead, tokWrite, tokAdmin, tokOAuth, tokOAuthAPI, tokOAuthW, tokSecrets, tokBroken} {
		m[string(auth.HashToken(t))] = t
	}
	return m
}()

func (fakeLookup) SessionActor(context.Context, []byte) (auth.Actor, error) {
	return auth.Actor{UserID: 1, Username: "ann", Role: auth.RoleAdmin}, nil
}

func (fakeLookup) TokenActor(_ context.Context, hash []byte) (auth.Actor, error) {
	return actorFor(tokenByHash[string(hash)])
}

func obj(props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props}
}

var idParam = apispec.Param{Name: "id", In: "path", Required: true, Description: "The id.", Schema: map[string]any{"type": "integer"}}

// fixtureOps are operations shaped as apispec returns them, for the paths
// the real API serves.
func fixtureOps() []apispec.Operation {
	ext := obj(map[string]any{"id": map[string]any{"type": "integer"}, "number": map[string]any{"type": "string"}})
	extBody := obj(map[string]any{"number": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}})
	return []apispec.Operation{
		{ID: "listExtensions", Method: "GET", Path: "/api/v1/extensions", Summary: "List extensions", Scope: auth.ScopeRead, Role: auth.RoleViewer,
			Output: obj(map[string]any{"items": map[string]any{"type": "array", "items": ext}})},
		{ID: "createExtension", Method: "POST", Path: "/api/v1/extensions", Summary: "Create an extension", Scope: auth.ScopeWrite, Role: auth.RoleOperator,
			Body: &apispec.Body{ContentTypes: []string{"application/json"}, JSON: extBody}, Output: ext},
		{ID: "getExtension", Method: "GET", Path: "/api/v1/extensions/{id}", Summary: "Get an extension", Scope: auth.ScopeRead, Role: auth.RoleViewer,
			Params: []apispec.Param{idParam}, Output: ext},
		{ID: "updateExtension", Method: "PUT", Path: "/api/v1/extensions/{id}", Summary: "Replace an extension", Scope: auth.ScopeWrite, Role: auth.RoleOperator,
			Params: []apispec.Param{idParam}, Body: &apispec.Body{ContentTypes: []string{"application/json"}, JSON: extBody}},
		{ID: "deleteExtension", Method: "DELETE", Path: "/api/v1/extensions/{id}", Summary: "Delete an extension", Scope: auth.ScopeWrite, Role: auth.RoleOperator,
			Params: []apispec.Param{idParam}},
		{ID: "createDevice", Method: "POST", Path: "/api/v1/devices", Summary: "Create a device", Scope: auth.ScopeWrite, Role: auth.RoleOperator,
			Body:    &apispec.Body{ContentTypes: []string{"application/json"}, JSON: obj(map[string]any{"extensionId": map[string]any{"type": "integer"}, "sipUsername": map[string]any{"type": "string"}})},
			Output:  obj(map[string]any{"id": map[string]any{"type": "integer"}, "secret": map[string]any{"type": "string", "x-hello-secret": true}}),
			Secrets: []string{"/secret"}},
		{ID: "drainNode", Method: "POST", Path: "/api/v1/cluster/nodes/{id}/drain", Summary: "Drain a node", Scope: auth.ScopeAdmin, Role: auth.RoleAdmin,
			Params: []apispec.Param{{Name: "id", In: "path", Required: true, Schema: map[string]any{"type": "string"}}, {Name: "force", In: "query", Schema: map[string]any{"type": "boolean"}}}},
		{ID: "listCDRs", Method: "GET", Path: "/api/v1/cdrs", Summary: "List CDRs", Scope: auth.ScopeRead, Role: auth.RoleViewer,
			Params: []apispec.Param{{Name: "limit", In: "query", Schema: map[string]any{"type": "integer"}}, {Name: "direction", In: "query", Schema: map[string]any{"type": "string"}}},
			Output: obj(map[string]any{"items": map[string]any{"type": "array"}})},
		{ID: "listPhones", Method: "GET", Path: "/api/v1/phones", Summary: "List phones", Scope: auth.ScopeRead, Role: auth.RoleViewer,
			Output: obj(map[string]any{"items": map[string]any{"type": "array", "items": obj(map[string]any{
				"mac": map[string]any{"type": "string"}, "url": map[string]any{"type": "string", "x-hello-secret": true}})}}),
			Secrets: []string{"/items/*/url"}},
		{ID: "getVersion", Method: "GET", Path: "/api/v1/version", Summary: "Version"},
		{ID: "login", Method: "POST", Path: "/api/v1/auth/login", Summary: "Log in", Exclude: "credentials are not handled by MCP"},
		{ID: "rotateDeviceSecret", Method: "POST", Path: "/api/v1/devices/{id}/rotate-secret", Summary: "Rotate", Scope: auth.ScopeSecrets, Role: auth.RoleAdmin,
			Params: []apispec.Param{idParam}, Exclude: "secrets are shown only in the console"},
	}
}

// apiCall is one request the fake API saw.
type apiCall struct {
	method, uri string
	header      http.Header
	replay      auth.Replay
	replayed    bool
	body        string
}

// recordingAPI wraps next, recording each request it receives.
type recordingAPI struct {
	mu    sync.Mutex
	calls []apiCall
	next  http.Handler
}

func (a *recordingAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(b))
	rp, ok := auth.ReplayFrom(r.Context())
	a.mu.Lock()
	a.calls = append(a.calls, apiCall{method: r.Method, uri: r.URL.RequestURI(), header: r.Header.Clone(), replay: rp, replayed: ok, body: string(b)})
	a.mu.Unlock()
	a.next.ServeHTTP(w, r)
}

func (a *recordingAPI) last(t *testing.T) apiCall {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.calls) == 0 {
		t.Fatal("the API saw no request")
	}
	return a.calls[len(a.calls)-1]
}

// echoAPI answers every GET with a JSON object naming its URI.
func echoAPI() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"uri": r.URL.RequestURI(), "method": r.Method})
	})
}

// newTestServer serves the MCP handler over ops in front of apiH.
func newTestServer(t *testing.T, apiH http.Handler, ops []apispec.Operation, mut func(*Options)) (*httptest.Server, *server) {
	t.Helper()
	o := Options{API: apiH, PublicURL: testPublic, AllowedOrigins: []string{"https://console.test"},
		Lookup: fakeLookup{}, MetadataURL: testMetadata}
	if mut != nil {
		mut(&o)
	}
	s, err := newServer(o, ops)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return ts, s
}

// bearer adds an Authorization header to every request.
type bearer struct {
	tok  string
	base http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.tok)
	return b.base.RoundTrip(r)
}

// connect opens an SDK client session as tok, negotiating version.
func connect(t *testing.T, url, tok, version string) *sdk.ClientSession {
	t.Helper()
	c := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	tr := &sdk.StreamableClientTransport{Endpoint: url, HTTPClient: &http.Client{Transport: bearer{tok, http.DefaultTransport}}}
	cs, err := c.Connect(context.Background(), tr, &sdk.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		t.Fatalf("connect as %s with %s: %v", tok, version, err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// reply is the status and headers of a raw POST.
type reply struct {
	StatusCode int
	Header     http.Header
}

// post sends one raw JSON-RPC body with headers.
func post(t *testing.T, url, body string, hdr map[string]string) (reply, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2025-11-25")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return reply{resp.StatusCode, resp.Header}, string(b)
}

func callBody(tool string, args any) string {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
	return string(b)
}

func loadSpec(t *testing.T) *apispec.Spec {
	t.Helper()
	spec, err := apispec.Load(api.OpenAPI())
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func resultText(t *testing.T, r *sdk.CallToolResult) string {
	t.Helper()
	if len(r.Content) != 1 {
		t.Fatalf("result has %d contents", len(r.Content))
	}
	tc, ok := r.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("content is %T", r.Content[0])
	}
	return tc.Text
}

// testAI is an authorization server for testPublic, so the API admits
// OAuth tokens bound to its resources.
func testAI(t testing.TB) *oauth.Server {
	t.Helper()
	as, err := oauth.New(oauth.Options{PublicURL: testPublic})
	if err != nil {
		t.Fatal(err)
	}
	return as
}

func (fakeLookup) UserActor(context.Context, int64) (auth.Actor, error) {
	return auth.Actor{}, auth.ErrNoCredentials
}
