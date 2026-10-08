package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/azrtydxb/hello/internal/api"
	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/oauth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
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

// TestOAuthMCPMetrics fails if a token issue, a token failure, a client ID
// metadata document fetch, an MCP request or a tool call does not move its
// metric (both families on one registry, as hello-control composes them), a tool call is not logged with tool, actor,
// client, status and duration, or a credential, a client secret, an
// argument or a withheld value reaches the log (spec S-20).
func TestOAuthMCPMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	logs := &syncBuffer{}
	st := &apiStore{}
	om := oauth.NewMetrics(reg)
	as := oauthHalf(t, om, slog.New(slog.NewJSONHandler(logs, nil)))
	ts, _ := newTestServer(t, api.Handler(api.Config{AI: testAI(t), Store: st}), fixtureOps(), func(o *Options) {
		o.Metrics = m
		o.Log = slog.New(slog.NewJSONHandler(logs, nil))
	})
	ctx := context.Background()
	cs := connect(t, ts.URL, tokOAuth, "2025-11-25")
	if _, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "listExtensions", Arguments: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "getExtension", Arguments: map[string]any{"id": 99}}); err != nil {
		t.Fatal(err)
	}
	ws := connect(t, ts.URL, tokWrite, "2025-11-25")
	if _, err := ws.CallTool(ctx, &sdk.CallToolParams{Name: "createDevice", Arguments: map[string]any{
		"body": map[string]any{"extensionId": 7, "sipUsername": "argument-marker"}}}); err != nil {
		t.Fatal(err)
	}
	post(t, ts.URL, callBody("listExtensions", nil), nil)

	for _, c := range []struct {
		c    prometheus.Collector
		want float64
		name string
	}{
		{m.ToolCalls.WithLabelValues("listExtensions", "ok"), 1, "tool ok"},
		{m.ToolCalls.WithLabelValues("getExtension", "error"), 1, "tool error"},
		{m.ToolCalls.WithLabelValues("createDevice", "ok"), 1, "createDevice ok"},
		{m.Requests.WithLabelValues("tools/call", "ok"), 3, "requests ok"},
		{m.Requests.WithLabelValues("tools/call", "unauthorized"), 0, "unauthenticated tools/call before parsing"},
		{m.Requests.WithLabelValues("other", "unauthorized"), 1, "unauthorized"},
	} {
		if got := testutil.ToFloat64(c.c); got != c.want {
			t.Errorf("%s = %v, want %v", c.name, got, c.want)
		}
	}
	if got := testutil.ToFloat64(m.Requests.WithLabelValues("initialize", "ok")); got < 2 {
		t.Errorf("initialize requests = %v", got)
	}
	if n := testutil.CollectAndCount(m.ToolSeconds); n != 1 {
		t.Errorf("tool_call_seconds series = %d", n)
	}
	if n, _ := testutil.GatherAndCount(reg, "hello_mcp_requests_total", "hello_mcp_tool_calls_total", "hello_mcp_tool_call_seconds"); n == 0 {
		t.Error("metrics not registered")
	}

	out := logs.String()
	for _, want := range []string{`"tool":"listExtensions"`, `"actor":"user:ann"`, `"client":"https://client.test/cimd"`, `"status":200`, `"status":404`, `"duration"`, `"actor":"token:12"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s:\n%s", want, out)
		}
	}
	st.mu.Lock()
	secret := st.secrets[0]
	st.mu.Unlock()
	for _, c := range []struct {
		c    prometheus.Collector
		want float64
		name string
	}{
		{om.TokensIssued.WithLabelValues("client_credentials"), 1, "oauth tokens issued"},
		{om.TokenFailures.WithLabelValues("invalid_client"), 1, "oauth token failures"},
		{om.CIMDFetches.WithLabelValues("ok"), 1, "oauth cimd fetches"},
	} {
		if got := testutil.ToFloat64(c.c); got != c.want {
			t.Errorf("%s = %v, want %v", c.name, got, c.want)
		}
	}
	for _, leak := range []string{tokOAuth, tokWrite, "argument-marker", secret, "Ann", svcSecret, as.issued} {
		if strings.Contains(out, leak) {
			t.Errorf("log contains %q", leak)
		}
	}
}

const svcSecret = "hello_svc_secret-marker"

// oauthStore is the authorization server's store with what the token and
// authorize endpoints need for one client credentials issue and one client
// ID metadata document fetch.
type oauthStore struct {
	oauth.Store
}

func (oauthStore) Client(context.Context, string) (oauth.Client, error) {
	return oauth.Client{}, oauth.ErrNotFound
}

func (oauthStore) SaveCIMDClient(context.Context, oauth.Client) error { return nil }

func (oauthStore) ClientCredentials(_ context.Context, id string, hash []byte, issue func(oauth.Client) (oauth.Token, error)) error {
	if id != "svc" || !bytes.Equal(hash, auth.HashToken(svcSecret)) {
		return oauth.ErrInvalidGrant
	}
	_, err := issue(oauth.Client{ID: id, Kind: oauth.ClientService, Role: auth.RoleOperator, Scopes: auth.Scopes{auth.ScopeRead}})
	return err
}

// oauthRun is the OAuth half's outcome: the access token it was issued.
type oauthRun struct{ issued string }

// oauthHalf drives the authorization server once through a token issue, a
// token failure and a metadata document fetch, logging to log.
func oauthHalf(t *testing.T, m *oauth.Metrics, log *slog.Logger) oauthRun {
	t.Helper()
	cimd := httptest.NewTLSServer(nil)
	cimd.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": cimd.URL + "/client.json", "client_name": "Agent",
			"redirect_uris": []string{"http://127.0.0.1/cb"}})
	})
	t.Cleanup(cimd.Close)
	as, err := oauth.New(oauth.Options{Store: oauthStore{}, PublicURL: testPublic, CIMDAllowPrivate: true,
		Client: cimd.Client(), Metrics: m, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	h := as.Handler()
	token := func(user, pass string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader("grant_type=client_credentials"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetBasicAuth(user, pass)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	ok := token("svc", svcSecret)
	if ok.Code != http.StatusOK {
		t.Fatalf("client credentials = %d %s", ok.Code, ok.Body)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(ok.Body.Bytes(), &tok); err != nil || tok.AccessToken == "" {
		t.Fatalf("token response %s (%v)", ok.Body, err)
	}
	if w := token("svc", "wrong"); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong secret = %d %s", w.Code, w.Body)
	}
	q := url.Values{"client_id": {cimd.URL + "/client.json"}, "redirect_uri": {"http://127.0.0.1/cb"}, "response_type": {"code"}}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil))
	return oauthRun{issued: tok.AccessToken}
}
