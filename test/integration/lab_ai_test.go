package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// labUI is the console's nginx, the lab's HELLO_PUBLIC_URL.
const labUI = "http://localhost:8080"

// TestLabAIAccess (spec ai-external-access S-19, S-20) drives external AI
// access through the lab's hello-ui proxy, as kw serves it: the 401
// challenge and both metadata documents on /mcp and /.well-known, the
// console's consent page (never framed), a service account's
// client-credentials token whose tools/list holds read tools only, and an
// MCP client that registers itself, is approved for read and calls a
// tool. Every credential is remembered for TestNoSecretsInLogs.
func TestLabAIAccess(t *testing.T) {
	lc := newLabClient(t)
	ctx := context.Background()
	mcpURL := labUI + "/mcp"

	resp, err := http.Post(mcpURL, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(resp.Header.Get("WWW-Authenticate"), `resource_metadata="`+labUI+`/.well-known/oauth-protected-resource/mcp"`) {
		t.Fatalf("/mcp without a token = %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
	for path, field := range map[string]string{
		"/.well-known/oauth-protected-resource/mcp": "resource",
		"/.well-known/oauth-authorization-server":   "issuer",
	} {
		resp, err := http.Get(labUI + path)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&doc)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || doc[field] == nil {
			t.Fatalf("GET %s = %d %v", path, resp.StatusCode, doc)
		}
	}
	resp, err = http.Get(labUI + "/oauth/consent?request=x")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(page), `<div id="root">`) ||
		resp.Header.Get("Content-Security-Policy") != "frame-ancestors 'none'" {
		t.Fatalf("GET /oauth/consent = %d, CSP %q: not the console's unframeable page", resp.StatusCode, resp.Header.Get("Content-Security-Policy"))
	}

	// A read-only service account through client credentials.
	var sa struct{ ID string }
	lc.must("POST", "/api/v1/service-accounts", map[string]any{"name": "lab-ai-" + randDigits(6), "role": "viewer", "scopes": []string{"read"}}, &sa, http.StatusCreated)
	t.Cleanup(func() {
		_ = lc.do("DELETE", "/api/v1/service-accounts/"+url.PathEscape(sa.ID), nil, nil, http.StatusNoContent)
	})
	var sec struct{ Secret string }
	lc.must("POST", "/api/v1/service-accounts/"+url.PathEscape(sa.ID)+"/secrets", map[string]any{}, &sec, http.StatusCreated)
	remember(sec.Secret)
	req, _ := http.NewRequest(http.MethodPost, labUI+"/oauth/token", strings.NewReader(url.Values{
		"grant_type": {"client_credentials"}, "scope": {"read"}, "resource": {mcpURL},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(sa.ID, sec.Secret)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&tok)
	_ = resp.Body.Close()
	remember(tok.AccessToken)
	if resp.StatusCode != http.StatusOK || tok.AccessToken == "" || tok.RefreshToken != "" {
		t.Fatalf("client credentials = %d (access token: %t, refresh token: %t)", resp.StatusCode, tok.AccessToken != "", tok.RefreshToken != "")
	}
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "lab-service", Version: "1"}, nil).Connect(ctx,
		&sdk.StreamableClientTransport{Endpoint: mcpURL, HTTPClient: &http.Client{Transport: bearerRT{tok.AccessToken}, Timeout: 10 * time.Second}, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatalf("connect as the service account: %v", err)
	}
	defer func() { _ = cs.Close() }()
	names := toolNames(t, cs)
	if !slices.Contains(names, "listExtensions") || slices.Contains(names, "createExtension") {
		t.Fatalf("service account tools/list = %v, want read tools only", names)
	}
	callTool(t, cs, "listExtensions", map[string]any{})

	// An MCP client that registers itself and is approved for read.
	creds := &credentials{}
	approvals := 0
	oauthClient := &http.Client{Transport: capture{http.DefaultTransport, creds}, Timeout: 10 * time.Second}
	handler, err := sdkauth.NewAuthorizationCodeHandler(&sdkauth.AuthorizationCodeHandlerConfig{
		DynamicClientRegistrationConfig: &sdkauth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			ClientName: "Hello lab agent", RedirectURIs: []string{"http://127.0.0.1:9/callback"},
			GrantTypes: []string{"authorization_code", "refresh_token"}, TokenEndpointAuthMethod: "none",
		}},
		AuthorizationCodeFetcher: approver(labUI, func(method, path string, body, out any, want int) error {
			return lc.do(method, path, body, out, want)
		}, creds, &approvals),
		RequestRefreshToken: true,
		Client:              oauthClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := sdk.NewClient(&sdk.Implementation{Name: "lab-agent", Version: "1"}, nil).Connect(ctx,
		&sdk.StreamableClientTransport{Endpoint: mcpURL, OAuthHandler: handler, HTTPClient: oauthClient, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatalf("connect with OAuth through the proxy: %v", err)
	}
	defer func() { _ = agent.Close() }()
	if approvals != 1 {
		t.Fatalf("%d consents, want 1", approvals)
	}
	if names := toolNames(t, agent); slices.Contains(names, "createExtension") {
		t.Fatalf("a read grant lists write tools: %v", names)
	}
	callTool(t, agent, "listExtensions", map[string]any{})
	if len(creds.all()) < 3 {
		t.Fatalf("captured %d credentials, want the code, verifier and tokens", len(creds.all()))
	}
}
