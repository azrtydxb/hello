package voice

// The MCP server registry and the egress-guarded discovery and test client
// (spec S-5 to S-9): bearer, header and OAuth client-credentials auth, the
// credential sealed at rest and write-only in the API, tools/list through
// the official Go SDK client, and the OAuth exchange with an in-memory
// token cache — tokens are never stored and never logged.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/store"
	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// statuses classify a failed discovery or test (spec S-9).
var (
	errRefused     = errors.New("refused")
	errUnreachable = errors.New("unreachable")
	errNotMCP      = errors.New("not_mcp")
)

// MCPServers lists every server with the agent names attaching each.
func (r *Registry) MCPServers(ctx context.Context) ([]store.VoiceMCPServer, []string, error) {
	return r.st.ListVoiceMCPServers(ctx)
}

// MCPServer returns one server with the agent names attaching it.
func (r *Registry) MCPServer(ctx context.Context, id int64) (store.VoiceMCPServer, []string, error) {
	return r.st.GetVoiceMCPServer(ctx, id)
}

// ServerInput is one MCP server to save, as the API presents it. Credential
// is the plaintext bearer token, header value or OAuth client secret:
// write-only, sealed on save, never returned (spec S-5).
type ServerInput struct {
	Name       string
	URL        string
	Auth       string
	HeaderName string
	TokenURL   string
	ClientID   string
	Scope      string
	Credential string
	TimeoutMS  int
	Enabled    bool
}

// validateServer checks the URL and auth material of a server input, and
// egress-checks the URL and the token URL under the guard (spec S-8).
func (r *Registry) validateServer(in *ServerInput) error {
	if !nameRe.MatchString(in.Name) {
		return inputError("name must be 1 to 64 letters, digits, dot, underscore or hyphen")
	}
	switch in.Auth {
	case "none", "bearer", "header", "oauth_client_credentials":
	default:
		return inputError("auth must be none, bearer, header or oauth_client_credentials")
	}
	if err := r.Egress().Check(in.URL); err != nil {
		return &store.VoiceError{Status: 400, Code: "mcp_endpoint_not_private", Msg: err.Error()}
	}
	if in.Auth == "header" && (in.HeaderName == "" || len(in.HeaderName) > 128) {
		return inputError("header auth needs a headerName")
	}
	if in.Auth == "oauth_client_credentials" {
		if in.TokenURL == "" || in.ClientID == "" {
			return inputError("oauth_client_credentials needs a tokenUrl and a clientId")
		}
		if err := r.Egress().Check(in.TokenURL); err != nil {
			return &store.VoiceError{Status: 400, Code: "mcp_endpoint_not_private", Msg: err.Error()}
		}
	}
	if in.TimeoutMS != 0 && (in.TimeoutMS < 100 || in.TimeoutMS > 30000) {
		return inputError("timeoutMs must be between 100 and 30000")
	}
	return nil
}

// toStoreServer converts and validates a server input.
func (r *Registry) toStoreServer(in ServerInput) (store.NewVoiceMCPServer, error) {
	if err := r.validateServer(&in); err != nil {
		return store.NewVoiceMCPServer{}, err
	}
	if in.TimeoutMS == 0 {
		in.TimeoutMS = 5000 // the schema's default
	}
	return store.NewVoiceMCPServer{
		Name: in.Name, URL: in.URL, Auth: in.Auth, HeaderName: in.HeaderName, TokenURL: in.TokenURL,
		ClientID: in.ClientID, Scope: in.Scope, Credential: in.Credential, TimeoutMS: in.TimeoutMS,
		Enabled: in.Enabled,
	}, nil
}

// CreateMCPServer validates and stores a server, sealing its credential.
func (r *Registry) CreateMCPServer(ctx context.Context, actor string, in ServerInput, check store.Check) (store.VoiceMCPServer, error) {
	si, err := r.toStoreServer(in)
	if err != nil {
		return store.VoiceMCPServer{}, err
	}
	return r.st.CreateVoiceMCPServer(ctx, actor, si, check)
}

// UpdateMCPServer validates and replaces a server's fields; an empty
// credential keeps the stored one (spec S-5).
func (r *Registry) UpdateMCPServer(ctx context.Context, actor string, id int64, in ServerInput, check store.Check) (store.VoiceMCPServer, error) {
	si, err := r.toStoreServer(in)
	if err != nil {
		return store.VoiceMCPServer{}, err
	}
	return r.st.UpdateVoiceMCPServer(ctx, actor, id, si, check)
}

// DeleteMCPServer removes a server no agent attaches.
func (r *Registry) DeleteMCPServer(ctx context.Context, actor string, id int64, check store.Check) error {
	return r.st.DeleteVoiceMCPServer(ctx, actor, id, check)
}

// DiscoveredTool is one tool of a discovery response.
type DiscoveredTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	ReadOnly    bool            `json:"readOnly"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
}

// TestResult is one test connection result (spec S-9): ok, unauthorized,
// unreachable or not_mcp, with latency and tool count.
type TestResult struct {
	Status    string `json:"status"`
	LatencyMS int64  `json:"latencyMs"`
	Tools     int    `json:"toolCount"`
}

// oauthToken is a cached access token: memory only, never persisted, never
// logged (spec S-5).
type oauthToken struct {
	token  string
	expiry time.Time
}

// tokenCache caches OAuth access tokens per server.
type tokenCache struct {
	mu       sync.Mutex
	byServer map[int64]oauthToken
}

func (c *tokenCache) get(id int64) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.byServer[id]
	if !ok || time.Now().After(t.expiry) {
		return "", false
	}
	return t.token, true
}

func (c *tokenCache) put(id int64, token string, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byServer == nil {
		c.byServer = make(map[int64]oauthToken)
	}
	c.byServer[id] = oauthToken{token: token, expiry: time.Now().Add(ttl - 60*time.Second)}
}

// accessToken returns the server's OAuth access token, exchanging the
// sealed client secret at the token URL (RFC 6749 client credentials) when
// the cache has none.
func (r *Registry) accessToken(ctx context.Context, s store.VoiceMCPServer, cred string) (string, error) {
	if t, ok := r.tokens.get(s.ID); ok {
		return t, nil
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {s.ClientID},
		"client_secret": {cred},
	}
	if s.Scope != "" {
		form.Set("scope", s.Scope)
	}
	hc := r.Egress().clientOf(s.TimeoutMS)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", errUnreachable
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
	if err != nil {
		return "", errUnreachable
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return "", errRefused
	case resp.StatusCode != http.StatusOK:
		return "", errUnreachable
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", errUnreachable
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || tok.AccessToken == "" {
		return "", errNotMCP
	}
	ttl := 5 * time.Minute
	if tok.ExpiresIn > 0 {
		ttl = time.Duration(tok.ExpiresIn) * time.Second
	}
	r.tokens.put(s.ID, tok.AccessToken, ttl)
	return tok.AccessToken, nil
}

// statusRecorder records the last response status a client saw, so a 401
// or 403 from the MCP endpoint classifies as refused even though the SDK
// folds the status into its own error.
type statusRecorder struct {
	mu     sync.Mutex
	latest int
}

func (s *statusRecorder) record(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latest = code
}

func (s *statusRecorder) status() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latest
}

// authedClient builds the guarded HTTP client for a server call, adding the
// server's auth: a bearer token, a named header, or an OAuth access token
// from the client-credentials exchange. The recorder rides along.
func (r *Registry) authedClient(ctx context.Context, s store.VoiceMCPServer, cred string, rec *statusRecorder) (*http.Client, error) {
	hc := r.Egress().clientOf(s.TimeoutMS)
	base := hc.Transport
	hc.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		resp, err := base.RoundTrip(req)
		if err == nil && rec != nil {
			rec.record(resp.StatusCode)
		}
		return resp, err
	})
	switch s.Auth {
	case "none":
		return hc, nil
	case "bearer":
		return withHeader(hc, "Authorization", "Bearer "+cred), nil
	case "header":
		return withHeader(hc, s.HeaderName, cred), nil
	case "oauth_client_credentials":
		tok, err := r.accessToken(ctx, s, cred)
		if err != nil {
			return nil, err
		}
		return withHeader(hc, "Authorization", "Bearer "+tok), nil
	}
	return nil, errUnreachable
}

// withHeader adds a static header to every request of hc.
func withHeader(hc *http.Client, name, value string) *http.Client {
	base := hc.Transport
	hc.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		req2 := req.Clone(req.Context())
		req2.Header.Set(name, value)
		return base.RoundTrip(req2)
	})
	return hc
}

// roundTripperFunc adapts a function to an http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// session runs fn against the server with the SDK client, unsealing the
// credential and guarding the whole call. Errors are classified; the
// credential never reaches one.
func (r *Registry) session(ctx context.Context, id int64, fn func(cs *mcp.ClientSession) error) error {
	s, _, err := r.st.GetVoiceMCPServer(ctx, id)
	if err != nil {
		return err
	}
	cred, err := r.st.VoiceMCPServerCredential(ctx, s.ID)
	if err != nil {
		return err
	}
	rec := &statusRecorder{}
	hc, err := r.authedClient(ctx, s, cred, rec)
	if err != nil {
		return err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "hello-control", Version: "1"}, nil)
	tr := &mcp.StreamableClientTransport{
		Endpoint:             s.URL,
		HTTPClient:           hc,
		MaxEventSize:         maxResponseBytes,
		DisableStandaloneSSE: true,
	}
	cs, err := client.Connect(ctx, tr, nil)
	if err != nil {
		return classify(err, rec)
	}
	defer func() { _ = cs.Close() }()
	return classify(fn(cs), rec)
}

// classify maps a client error to the test statuses (S-9): an HTTP 401 or
// 403 is refused, a guard or transport failure unreachable, anything else
// the peer not being an MCP server.
func classify(err error, rec *statusRecorder) error {
	if err == nil {
		return nil
	}
	var ve *store.VoiceError
	if errors.As(err, &ve) {
		return err
	}
	if errors.Is(err, errRefused) || errors.Is(err, errUnreachable) || errors.Is(err, errNotMCP) {
		return err
	}
	if rec != nil {
		switch s := rec.status(); {
		case s == http.StatusUnauthorized || s == http.StatusForbidden:
			return errRefused
		case s >= 400:
			return errUnreachable
		}
	}
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return errUnreachable
	}
	return errNotMCP
}

// Discover calls the server's tools/list (spec S-7) and returns its tools
// with their annotations. Nothing is saved by discovery.
func (r *Registry) Discover(ctx context.Context, id int64) ([]DiscoveredTool, error) {
	var out []DiscoveredTool
	err := r.session(ctx, id, func(cs *mcp.ClientSession) error {
		res, err := cs.ListTools(ctx, nil)
		if err != nil {
			return err
		}
		out = make([]DiscoveredTool, 0, len(res.Tools))
		for _, t := range res.Tools {
			dt := DiscoveredTool{Name: t.Name, Description: t.Description}
			if t.InputSchema != nil {
				if raw, merr := json.Marshal(t.InputSchema); merr == nil {
					dt.InputSchema = raw
				}
			}
			if t.Annotations != nil {
				dt.ReadOnly = t.Annotations.ReadOnlyHint
				if raw, merr := json.Marshal(t.Annotations); merr == nil {
					dt.Annotations = raw
				}
			}
			out = append(out, dt)
		}
		return nil
	})
	return out, err
}

// checkStatus is the stored half of a check (spec S-9): the status only,
// never the response body.
func (r *Registry) checkStatus(ctx context.Context, id int64) (string, error) {
	_, err := r.Discover(ctx, id)
	switch {
	case err == nil:
		return "ok", nil
	case errors.Is(err, errRefused):
		return "refused", nil
	default:
		return "unreachable", nil
	}
}

// TestConnection runs the discovery call as a test (spec S-9), records the
// check status (never the body) and reports status, latency and tool count.
// A failed test is a result, not an error.
func (r *Registry) TestConnection(ctx context.Context, id int64) (TestResult, error) {
	start := time.Now()
	tools, err := r.Discover(ctx, id)
	res := TestResult{LatencyMS: time.Since(start).Milliseconds()}
	if err == nil {
		res.Status, res.Tools = "ok", len(tools)
		return res, r.st.SetVoiceMCPCheck(ctx, id, "ok")
	}
	switch {
	case errors.Is(err, errRefused):
		res.Status = "unauthorized"
	case errors.Is(err, errUnreachable):
		res.Status = "unreachable"
	default:
		res.Status = "not_mcp"
	}
	status, serr := r.checkStatus(ctx, id)
	if serr == nil && status != "" {
		_ = r.st.SetVoiceMCPCheck(ctx, id, status)
	}
	return res, nil
}
