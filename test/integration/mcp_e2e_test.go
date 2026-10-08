package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/azrtydxb/hello/internal/api"
	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/mcp"
	"github.com/azrtydxb/hello/internal/oauth"
)

// syncBuffer is a log sink safe for the server's goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// credentials collects every credential and withheld value the end-to-end
// run sees on the wire, for the log check.
type credentials struct {
	mu     sync.Mutex
	values []string
}

func (c *credentials) add(vals ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, v := range vals {
		if v != "" && !slices.Contains(c.values, v) {
			c.values = append(c.values, v)
		}
	}
	remember(vals...)
}

func (c *credentials) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.values)
}

// capture is the MCP client's transport for OAuth requests: it records the
// code, verifier and tokens of every token request and response.
type capture struct {
	base  http.RoundTripper
	creds *credentials
}

func (c capture) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil && strings.HasSuffix(r.URL.Path, "/oauth/token") {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(b))
		if f, err := url.ParseQuery(string(b)); err == nil {
			c.creds.add(f.Get("code"), f.Get("code_verifier"), f.Get("refresh_token"))
		}
	}
	resp, err := c.base.RoundTrip(r)
	if err != nil || !strings.HasSuffix(r.URL.Path, "/oauth/token") {
		return resp, err
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(b))
	var tok struct{ AccessToken, RefreshToken string }
	var raw map[string]any
	if json.Unmarshal(b, &raw) == nil {
		tok.AccessToken, _ = raw["access_token"].(string)
		tok.RefreshToken, _ = raw["refresh_token"].(string)
	}
	c.creds.add(tok.AccessToken, tok.RefreshToken)
	return resp, nil
}

// secretTap records x-hello-secret values in the API's answers to MCP
// replays: the values the MCP results must withhold.
type secretTap struct {
	next  http.Handler
	creds *credentials
}

func (s secretTap) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec := httptest.NewRecorder()
	s.next.ServeHTTP(rec, r)
	var body map[string]any
	if json.Unmarshal(rec.Body.Bytes(), &body) == nil {
		if v, ok := body["secret"].(string); ok {
			s.creds.add(v)
		}
	}
	for k, vs := range rec.Header() {
		w.Header()[k] = vs
	}
	w.WriteHeader(rec.Code)
	_, _ = w.Write(rec.Body.Bytes())
}

// bearerRT sends a fixed access token.
type bearerRT struct{ tok string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.tok)
	return http.DefaultTransport.RoundTrip(r)
}

// e2eHello is hello-control's HTTP surface as cmd/hello-control composes
// it with HELLO_PUBLIC_URL set, on a scratch database.
type e2eHello struct {
	public string
	log    *syncBuffer
	creds  *credentials
	admin  *http.Client
}

func startE2EHello(t *testing.T, cimdClient *http.Client) *e2eHello {
	t.Helper()
	ctx := context.Background()
	_, st, _ := p2Store(t, ctx)
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	const password = "e2e-admin-password-0123" // gitleaks:allow (scratch database only)
	if err := auth.Bootstrap(ctx, st, password, log); err != nil {
		t.Fatal(err)
	}
	creds := &credentials{}
	creds.add(password)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	public := "http://localhost:" + strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	reg := prometheus.NewRegistry()
	as, err := oauth.New(oauth.Options{
		Store: st, PublicURL: public, DCR: true, CIMDAllowPrivate: true, Client: cimdClient,
		Metrics: oauth.NewMetrics(reg), Log: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	apiH := api.Handler(api.Config{Store: st, Live: noLive{}, SIPDomain: "hello.e2e", SessionTTL: time.Hour, AI: as, Log: log})
	spec, err := apispec.Load(api.OpenAPI())
	if err != nil {
		t.Fatal(err)
	}
	_, mcpResource := as.Resources()
	mcpH, err := mcp.New(mcp.Options{
		API: secretTap{apiH, creds}, Spec: spec, PublicURL: public, Lookup: st,
		MetadataURL: as.MetadataURL(mcpResource), Metrics: mcp.NewMetrics(reg), Log: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", apiH)
	mux.Handle("/mcp", mcpH)
	mux.Handle("/oauth/", as.Handler())
	mux.Handle("/.well-known/oauth-authorization-server", as.Handler())
	mux.Handle("/.well-known/oauth-protected-resource/", as.Handler())
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	jar, _ := cookiejar.New(nil)
	h := &e2eHello{public: public, log: logs, creds: creds, admin: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
	h.must(t, h.admin, "POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": password}, http.StatusNoContent, nil)
	return h
}

// must sends a JSON request with c and decodes the answer into out.
func (h *e2eHello) must(t *testing.T, c *http.Client, method, path string, body any, want int, out any) http.Header {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.public+path, rd)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s = %d %s, want %d", method, path, resp.StatusCode, b, want)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
	}
	return resp.Header
}

// apiCall is one JSON request as a logged-in console user.
type apiCall func(method, path string, body, out any, want int) error

// approver is the user at the browser: it follows the authorization URL to
// the consent page on public and approves every requested scope through
// the API with call (a console session), as the consent page does.
func approver(public string, call apiCall, creds *credentials, approvals *int) sdkauth.AuthorizationCodeFetcher {
	noFollow := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return func(ctx context.Context, args *sdkauth.AuthorizationArgs) (*sdkauth.AuthorizationResult, error) {
		u, err := url.Parse(args.URL)
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(args.URL, public+"/oauth/authorize?") {
			return nil, fmt.Errorf("authorization URL %s is not Hello's", args.URL)
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, args.URL, nil)
		resp, err := noFollow.Do(req)
		if err != nil {
			return nil, err
		}
		_ = resp.Body.Close()
		loc, _ := url.Parse(resp.Header.Get("Location"))
		if resp.StatusCode != http.StatusFound || loc == nil || loc.Path != "/oauth/consent" {
			return nil, fmt.Errorf("authorize = %d to %q, want 302 to the consent page", resp.StatusCode, resp.Header.Get("Location"))
		}
		id := url.PathEscape(loc.Query().Get("request"))
		if err := call("GET", "/api/v1/oauth/requests/"+id, nil, nil, http.StatusOK); err != nil {
			return nil, err
		}
		var approved struct{ Redirect string }
		if err := call("POST", "/api/v1/oauth/requests/"+id+"/approve", map[string]any{"scopes": strings.Fields(u.Query().Get("scope"))}, &approved, http.StatusOK); err != nil {
			return nil, err
		}
		back, err := url.Parse(approved.Redirect)
		if err != nil {
			return nil, err
		}
		q := back.Query()
		creds.add(q.Get("code"))
		*approvals++
		return &sdkauth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}, nil
	}
}

// call is h's admin session as an apiCall.
func (h *e2eHello) call(method, path string, body, out any, want int) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.public+path, rd)
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.admin.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		return fmt.Errorf("%s %s = %d %s, want %d", method, path, resp.StatusCode, b, want)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

func toolNames(t *testing.T, cs *sdk.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	return names
}

func callTool(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) (map[string]any, string) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	if res.IsError {
		t.Fatalf("%s: tool error %s", name, text.String())
	}
	out, _ := res.StructuredContent.(map[string]any)
	if out == nil {
		_ = json.Unmarshal([]byte(text.String()), &out)
	}
	return out, text.String()
}

// TestMCPEndToEnd (spec ai-external-access S-22) drives hello-control's
// OAuth authorization server and MCP endpoint with the MCP SDK's client
// and its authorization-code handler, step by step: the 401 and both
// metadata documents, a client ID metadata document served by the test,
// consent approved through the API with a session, the code exchange,
// tools/list read-only and then (after a step-up consent) with write, an
// extension created and read back through tools, a device whose secret the
// result withholds, hello://registrations, a refresh, revocation of the
// grant and the refusal that follows. Its log must hold none of the
// credentials or withheld values it saw (S-20).
func TestMCPEndToEnd(t *testing.T) {
	// The client ID metadata document, on https as CIMD requires.
	var cimdURL string
	cimd := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/client.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"client_id": cimdURL, "client_name": "Hello end-to-end", "client_uri": "https://example.test",
			"redirect_uris": []string{"http://127.0.0.1/callback"}, "token_endpoint_auth_method": "none",
			"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
		})
	}))
	t.Cleanup(cimd.Close)
	cimdURL = cimd.URL + "/client.json"
	h := startE2EHello(t, cimd.Client())
	mcpURL := h.public + "/mcp"
	ctx := context.Background()

	// 1. The 401 names the protected resource metadata.
	resp, err := http.Post(mcpURL, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	prm := h.public + "/.well-known/oauth-protected-resource/mcp"
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(resp.Header.Get("WWW-Authenticate"), `resource_metadata="`+prm+`"`) {
		t.Fatalf("step 1: /mcp without a token = %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
	// 2. Both metadata documents.
	var rm, asm map[string]any
	h.must(t, http.DefaultClient, "GET", "/.well-known/oauth-protected-resource/mcp", nil, http.StatusOK, &rm)
	h.must(t, http.DefaultClient, "GET", "/.well-known/oauth-authorization-server", nil, http.StatusOK, &asm)
	if rm["resource"] != mcpURL || asm["issuer"] != h.public || asm["client_id_metadata_document_supported"] != true {
		t.Fatalf("step 2: metadata %v / %v", rm, asm)
	}

	// 3-5. Authorize the CIMD client, approve read, exchange the code.
	approvals := 0
	oauthClient := &http.Client{Transport: capture{http.DefaultTransport, h.creds}, Timeout: 10 * time.Second}
	handler, err := sdkauth.NewAuthorizationCodeHandler(&sdkauth.AuthorizationCodeHandlerConfig{
		ClientIDMetadataDocumentConfig: &sdkauth.ClientIDMetadataDocumentConfig{URL: cimdURL},
		RedirectURL:                    "http://127.0.0.1:9/callback",
		AuthorizationCodeFetcher:       approver(h.public, h.call, h.creds, &approvals),
		RequestRefreshToken:            true,
		Client:                         oauthClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "hello-e2e", Version: "1"}, nil)
	cs, err := client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: mcpURL, OAuthHandler: handler, HTTPClient: oauthClient, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatalf("step 3-5: connect with OAuth: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	if approvals != 1 {
		t.Fatalf("step 3-5: %d consents, want 1", approvals)
	}

	// 6. Read-only tools first.
	names := toolNames(t, cs)
	if !slices.Contains(names, "listExtensions") || slices.Contains(names, "createExtension") {
		t.Fatalf("step 6: read tools = %v", names)
	}

	// 7. A write tool steps up: the handler asks for write, the user
	// approves it, and the call succeeds.
	number := randDigits(6)
	created, _ := callTool(t, cs, "createExtension", map[string]any{"body": map[string]any{"number": number, "name": "MCP e2e"}})
	if approvals != 2 {
		t.Fatalf("step 7: %d consents after a write call, want 2 (a step-up)", approvals)
	}
	if !slices.Contains(toolNames(t, cs), "createExtension") {
		t.Fatal("step 7: tools/list lacks createExtension with write")
	}
	id, _ := created["id"].(float64)
	if id == 0 {
		t.Fatalf("step 7: createExtension = %v", created)
	}
	got, _ := callTool(t, cs, "getExtension", map[string]any{"id": int64(id)})
	if got["number"] != number {
		t.Fatalf("step 7: getExtension = %v, want number %s", got, number)
	}
	// A device's secret is withheld from the result.
	_, text := callTool(t, cs, "createDevice", map[string]any{"body": map[string]any{"extensionId": int64(id), "sipUsername": "e2e" + number}})
	if !strings.Contains(text, mcp.Withheld) {
		t.Fatalf("step 7: createDevice result does not withhold the secret: %s", text)
	}
	for _, v := range h.creds.all() {
		if strings.Contains(text, v) {
			t.Fatal("step 7: a credential or secret is in an MCP result")
		}
	}

	// 8. A resource.
	rr, err := cs.ReadResource(ctx, &sdk.ReadResourceParams{URI: "hello://registrations"})
	if err != nil || len(rr.Contents) == 0 {
		t.Fatalf("step 8: hello://registrations = %v, %v", rr, err)
	}

	// 9. Refresh: a new access token works, the old refresh token is spent.
	ts, err := handler.TokenSource(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := ts.Token()
	if err != nil || tok.RefreshToken == "" {
		t.Fatalf("step 9: token source: %v (refresh token present: %t)", err, tok != nil && tok.RefreshToken != "")
	}
	h.creds.add(tok.AccessToken, tok.RefreshToken)
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok.RefreshToken}, "client_id": {cimdURL}, "resource": {mcpURL}}
	var refreshed struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	rresp, err := oauthClient.PostForm(h.public+"/oauth/token", form)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(rresp.Body).Decode(&refreshed)
	_ = rresp.Body.Close()
	if rresp.StatusCode != http.StatusOK || refreshed.AccessToken == "" || refreshed.RefreshToken == tok.RefreshToken {
		t.Fatalf("step 9: refresh = %d (rotated: %t)", rresp.StatusCode, refreshed.RefreshToken != tok.RefreshToken)
	}
	fresh, err := sdk.NewClient(&sdk.Implementation{Name: "hello-e2e", Version: "1"}, nil).Connect(ctx,
		&sdk.StreamableClientTransport{Endpoint: mcpURL, HTTPClient: &http.Client{Transport: bearerRT{refreshed.AccessToken}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatalf("step 9: connect with the refreshed token: %v", err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	callTool(t, fresh, "listExtensions", map[string]any{})

	// 10. Revoke the client's grants through the API, as Connected apps does.
	var grants struct {
		Items []struct {
			ID       int64  `json:"id"`
			ClientID string `json:"clientId"`
		} `json:"items"`
	}
	h.must(t, h.admin, "GET", "/api/v1/oauth/grants", nil, http.StatusOK, &grants)
	revoked := 0
	for _, g := range grants.Items {
		if g.ClientID == cimdURL {
			h.must(t, h.admin, "DELETE", "/api/v1/oauth/grants/"+strconv.FormatInt(g.ID, 10), nil, http.StatusNoContent, nil)
			revoked++
		}
	}
	if revoked == 0 {
		t.Fatalf("step 10: no grant for %s in %v", cimdURL, grants.Items)
	}

	// 11. The next call is refused.
	_, err = fresh.CallTool(ctx, &sdk.CallToolParams{Name: "listExtensions", Arguments: map[string]any{}})
	if err == nil || !strings.Contains(err.Error(), "401") && !strings.Contains(strings.ToLower(err.Error()), "unauthorized") {
		t.Fatalf("step 11: a call after revocation = %v, want 401", err)
	}

	// S-20: no credential, code, verifier or withheld value in the log.
	logs := h.log.String()
	if !strings.Contains(logs, "oauth token issued") {
		t.Fatal("the log holds no OAuth lines: the check below would be vacuous")
	}
	for _, v := range h.creds.all() {
		if strings.Contains(logs, v) {
			t.Errorf("a credential or withheld value (%d chars, %s…) appears in hello-control's log", len(v), prefixOf(v))
		}
	}
}

// prefixOf is a credential's non-secret prefix, for a failure message.
func prefixOf(v string) string {
	for _, p := range []string{auth.PrefixAccess, auth.PrefixRefresh, auth.PrefixClientSecret, auth.PrefixPersonal} {
		if strings.HasPrefix(v, p) {
			return p
		}
	}
	return "?"
}
