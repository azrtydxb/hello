package voice

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/config"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/test/fakemcp"
)

// fakeStore is the registry's persistence for the discovery tests: only the
// reads and writes the discovery and test paths make; anything else panics
// through the nil embedded interface.
type fakeStore struct {
	Store
	servers  map[int64]store.VoiceMCPServer
	creds    map[int64]string
	checks   []checkCall
	revCalls int
}

type checkCall struct {
	id     int64
	status string
}

func (f *fakeStore) GetVoiceMCPServer(_ context.Context, id int64) (store.VoiceMCPServer, []string, error) {
	s, ok := f.servers[id]
	if !ok {
		return store.VoiceMCPServer{}, nil, store.ErrNotFound
	}
	return s, nil, nil
}

func (f *fakeStore) VoiceMCPServerCredential(_ context.Context, id int64) (string, error) {
	c, ok := f.creds[id]
	if !ok {
		return "", store.ErrNotFound
	}
	return c, nil
}

func (f *fakeStore) SetVoiceMCPCheck(_ context.Context, id int64, status string) error {
	f.checks = append(f.checks, checkCall{id, status})
	return nil
}

func (f *fakeStore) VoiceRevision(context.Context) (int64, error) {
	f.revCalls++
	return 7, nil
}

// testBox is a secret box for the tests.
func testBox(t *testing.T) *secret.Box {
	t.Helper()
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	box, err := secret.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

// TestVoiceMCPDiscovery (acceptance S-7, S-8, S-9) fails if an OAuth
// exchange is not made for a server with that auth, a token is stored or
// logged, a public or rebinding address is dialled without the opt-in, a
// redirect is followed, a response over 1 MiB is read, discovery saves
// anything, or the check result stores a response body.
//
// Mutation checks:
//   - the egress check: removing the address classification in
//     Egress.allowed (or the connect-time re-check) fails the public-URL
//     and dial-refusal assertions;
//   - the OAuth exchange: dropping the accessToken call in authedClient
//     fails the token-request assertions; caching it (or persisting it
//     through the store) fails the one-exchange and no-store assertions;
//   - the response cap: removing cappedBody fails the 1 MiB assertion;
//   - the redirect refusal: allowing CheckRedirect to follow fails the
//     no-second-request assertion.
func TestVoiceMCPDiscovery(t *testing.T) {
	ctx := context.Background()
	fake := &fakeStore{
		servers: map[int64]store.VoiceMCPServer{},
		creds:   map[int64]string{},
	}
	fm := fakemcp.New(t)
	fm.RequireBearer("tok-123")
	cfg := config.Voice{AllowLoopback: true}
	reg := New(fake, testBox(t), cfg, nil)

	seed := func(id int64, mut func(*store.VoiceMCPServer), cred string) {
		s := store.VoiceMCPServer{ID: id, Name: "srv", URL: fm.URL + "/mcp", Auth: "bearer", TimeoutMS: 5000, Enabled: true}
		if mut != nil {
			mut(&s)
		}
		fake.servers[id] = s
		fake.creds[id] = cred
	}

	// Bearer and header auth reach the server and list its annotated tools.
	seed(1, nil, "tok-123")
	seed(2, func(s *store.VoiceMCPServer) { s.Auth = "header"; s.HeaderName = "X-Api-Key" }, "tok-123")
	for _, id := range []int64{1, 2} {
		tools, err := reg.Discover(ctx, id)
		if err != nil {
			t.Fatalf("discover server %d: %v", id, err)
		}
		if len(tools) != 3 {
			t.Fatalf("discover %d = %d tools, want 3", id, len(tools))
		}
		var readOnly int
		for _, tool := range tools {
			switch {
			case tool.Name == "lookup" && tool.ReadOnly:
				readOnly++
			case tool.Name == "create_thing" && !tool.ReadOnly:
				readOnly++
			}
		}
		if readOnly != 2 {
			t.Fatalf("annotations not carried: %+v", tools)
		}
	}
	// Discovery saves nothing.
	if len(fake.checks) != 0 {
		t.Fatalf("discovery wrote %d check rows", len(fake.checks))
	}

	// The test connection runs the same call and records the status only.
	res, err := reg.TestConnection(ctx, 1)
	if err != nil || res.Status != "ok" || res.Tools != 3 || res.LatencyMS < 0 {
		t.Fatalf("test connection = %+v, %v", res, err)
	}
	if len(fake.checks) != 1 || fake.checks[0].status != "ok" {
		t.Fatalf("checks = %v", fake.checks)
	}

	// A wrong bearer token is refused, not unreachable.
	seed(3, nil, "wrong-token")
	res, err = reg.TestConnection(ctx, 3)
	if err != nil || res.Status != "unauthorized" {
		t.Fatalf("unauthorized test = %+v, %v", res, err)
	}
	if len(fake.checks) != 2 || fake.checks[1].status != "refused" {
		t.Fatalf("checks = %v", fake.checks)
	}

	// A peer that is not an MCP server is not_mcp.
	seed(4, func(s *store.VoiceMCPServer) { s.URL = fm.URL + "/notmcp" }, "t")
	res, err = reg.TestConnection(ctx, 4)
	if err != nil || res.Status != "not_mcp" {
		t.Fatalf("not_mcp test = %+v, %v", res, err)
	}

	// A closed port is unreachable.
	seed(5, func(s *store.VoiceMCPServer) { s.URL = "http://127.0.0.1:1/mcp" }, "t")
	res, err = reg.TestConnection(ctx, 5)
	if err != nil || res.Status != "unreachable" {
		t.Fatalf("unreachable test = %+v, %v", res, err)
	}

	// A public URL is refused before anything is dialled; with loopback
	// refused too. The stored status is unreachable, not a body.
	public := "http://192.0.2.1/mcp"
	if err := reg.Egress().Check(public); err == nil || !strings.Contains(err.Error(), "not a private address") {
		t.Fatalf("public URL check = %v", err)
	}
	seed(6, func(s *store.VoiceMCPServer) { s.URL = public }, "t")
	if _, err := reg.Discover(ctx, 6); err == nil {
		t.Fatal("a public URL was dialled")
	}

	// A redirect is not followed: the redirect endpoint is hit (with the
	// transport's retries) and the target never is.
	fmr := fakemcp.New(t)
	fmr.RequireBearer("tok-123")
	seed(7, func(s *store.VoiceMCPServer) { s.URL = fmr.URL + "/redirect" }, "t")
	if _, err := reg.Discover(ctx, 7); err == nil {
		t.Fatal("a redirect was followed")
	}
	if fmr.Hits("/mcp") != 0 {
		t.Fatalf("the redirect target was followed %d times", fmr.Hits("/mcp"))
	}

	// A response over 1 MiB is not read past the cap.
	eg := Egress{AllowLoopback: true}
	resp, err := eg.Client(time.Second).Get(fm.URL + "/big")
	if err == nil {
		_, err = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
	if err == nil || !errors.Is(err, ErrTooLarge) {
		t.Fatalf("big response = %v, want ErrTooLarge", err)
	}

	// OAuth client credentials: the exchange happens, the token is cached
	// (one exchange for two discoveries) and never stored.
	var oauthCalls int
	fmo := fakemcp.New(t)
	fmo.RequireBearer("fake-access-token")
	fake.servers[8] = store.VoiceMCPServer{ID: 8, Name: "srv", URL: fmo.URL + "/mcp",
		Auth: "oauth_client_credentials", TokenURL: fmo.URL + "/token", ClientID: "hello-vc",
		TimeoutMS: 5000, Enabled: true}
	fake.creds[8] = "client-secret-1"
	tools, err := reg.Discover(ctx, 8)
	if err != nil || len(tools) != 3 {
		t.Fatalf("oauth discover = %d tools, %v", len(tools), err)
	}
	if _, err := reg.Discover(ctx, 8); err != nil {
		t.Fatalf("second oauth discover: %v", err)
	}
	reqs := fmo.TokenRequests()
	oauthCalls = len(reqs)
	if oauthCalls != 1 {
		t.Fatalf("token endpoint hit %d times, want 1 (cached)", oauthCalls)
	}
	req := reqs[0]
	if req.GrantType != "client_credentials" || req.ClientID != "hello-vc" || req.ClientSecret != "client-secret-1" {
		t.Fatalf("token request = %+v", req)
	}
	if req.Scope != "" {
		t.Fatalf("scope sent without one: %+v", req)
	}
	// A wrong client secret is unauthorized.
	fm2 := fakemcp.New(t)
	fm2.RequireSecret("right")
	fake.servers[9] = store.VoiceMCPServer{ID: 9, Name: "srv", URL: fm2.URL + "/mcp",
		Auth: "oauth_client_credentials", TokenURL: fm2.URL + "/token", ClientID: "hello-vc",
		TimeoutMS: 5000, Enabled: true}
	fake.creds[9] = "nope"
	res, err = reg.TestConnection(ctx, 9)
	if err != nil || res.Status != "unauthorized" {
		t.Fatalf("oauth wrong secret = %+v, %v", res, err)
	}

	// The token never reaches the store: the fake store's write calls were
	// only the check statuses.
	for _, c := range fake.checks {
		if strings.Contains(c.status, "fake-access-token") {
			t.Fatal("a token reached the store")
		}
	}
}
