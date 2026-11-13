package api

// Tests for the CRM connector routes (spec integrations I-47, I-48). They run
// through Handler over the in-memory user store and crm.Mem with a scripted
// connector, so they need no services.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/crm"
)

type crmFake struct {
	pingErr  error
	exchange error
}

func (f *crmFake) Kind() string { return "fakecrm" }
func (f *crmFake) Capabilities() crm.Capabilities {
	return crm.Capabilities{Lookup: true, LogCalls: true, Recordings: true}
}
func (f *crmFake) Lookup(context.Context, crm.Conn, string) (*crm.Contact, error) { return nil, nil }
func (f *crmFake) LogCall(context.Context, crm.Conn, crm.CallLog) (string, error) {
	return "r1", nil
}
func (f *crmFake) UpdateCallLog(context.Context, crm.Conn, string, crm.CallLog) error { return nil }
func (f *crmFake) ResolveUser(context.Context, crm.Conn, string) (string, error)      { return "", nil }
func (f *crmFake) AuthURL(cfg crm.Config, redirect, state string) string {
	return "https://crm.example/oauth?client_id=" + cfg.ClientID + "&state=" + state
}
func (f *crmFake) Exchange(_ context.Context, _ crm.Config, _, code string) (crm.Tokens, error) {
	if f.exchange != nil || code != "good" {
		return crm.Tokens{}, &crm.APIError{Status: 400, Msg: "bad"}
	}
	return crm.Tokens{Access: "ACCESS-SECRET", Refresh: "REFRESH-SECRET", ExpiresAt: time.Now().Add(time.Hour), Account: "55", Scopes: []string{"s1"}}, nil
}
func (f *crmFake) Refresh(context.Context, crm.Config, string) (crm.Tokens, error) {
	return crm.Tokens{Access: "A2", ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func (f *crmFake) Ping(context.Context, crm.Conn) error { return f.pingErr }

type crmEnv struct {
	repo *crm.Mem
	svc  *crm.Service
	fake *crmFake
	srv  *httptest.Server
}

func newCRMEnv(t *testing.T, publicURL string) *crmEnv {
	t.Helper()
	repo := crm.NewMem()
	repo.UsersList = []crm.UserRef{{ID: uAlice, Username: "alice", Email: "alice@example.com", Extension: "101"}}
	fake := &crmFake{}
	tm := &crm.TokenManager{Repo: repo, Providers: func(k string) (crm.Connector, bool) { return fake, k == "fakecrm" }}
	svc := &crm.Service{Repo: repo, Tokens: tm, Lookup: &crm.Lookup{Repo: repo, Tokens: tm}, PublicURL: publicURL}
	f, _, _, _ := newAcctFake(t)
	h := Handler(Config{Store: evStore{f}, CRM: svc, Log: slog.New(slog.DiscardHandler), SessionTTL: time.Hour})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &crmEnv{repo: repo, svc: svc, fake: fake, srv: srv}
}

// do sends a request as who and returns the status and body; redirects are
// not followed.
func (e *crmEnv) do(t *testing.T, who, method, path string, body any) (int, []byte, http.Header) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, e.srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if who != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: who + cookieSuffix})
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

func (e *crmEnv) want(t *testing.T, code int, who, method, path string, body any) map[string]any {
	t.Helper()
	got, b, _ := e.do(t, who, method, path, body)
	if got != code {
		t.Fatalf("%s %s as %s = %d %s, want %d", method, path, who, got, b, code)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func connPath(id any) string { return "/api/v1/integrations/crm/" + toS(id) }

func toS(v any) string {
	switch n := v.(type) {
	case float64:
		return strconv.FormatInt(int64(n), 10)
	case int64:
		return strconv.FormatInt(n, 10)
	}
	return v.(string)
}

// TestCRMConnectorLifecycle: create, read, update, test, connect through the
// OAuth callback and disconnect a connector. It fails if a client secret or a
// token appears in any response, the callback needs a session, a state can
// be replayed, a new client id keeps the old secret, or a viewer reaches the
// administration.
func TestCRMConnectorLifecycle(t *testing.T) {
	e := newCRMEnv(t, "https://hello.example")
	created := e.want(t, 201, "carol", "POST", "/api/v1/integrations/crm", map[string]any{
		"kind": "fakecrm", "name": "My CRM", "clientSecret": "CLIENT-SECRET", "config": map[string]string{"clientId": "cid"}})
	id := created["id"]
	if created["hasClientSecret"] != true || created["connection"] != nil || created["redirectUri"] != "https://hello.example/api/v1/integrations/crm/oauth/callback" {
		t.Fatalf("created = %v", created)
	}
	if caps := created["capabilities"].(map[string]any); caps["lookup"] != true || caps["ticket"] != false {
		t.Fatalf("capabilities = %v", caps)
	}
	e.want(t, 409, "carol", "POST", "/api/v1/integrations/crm", map[string]any{"kind": "fakecrm", "name": "My CRM"})
	e.want(t, 400, "carol", "POST", "/api/v1/integrations/crm", map[string]any{"kind": "nope", "name": "x"})
	e.want(t, 400, "carol", "POST", "/api/v1/integrations/crm", map[string]any{"name": "x"})
	e.want(t, 400, "carol", "POST", "/api/v1/integrations/crm", map[string]any{"kind": "fakecrm", "name": "y", "linkTtlHours": 0})
	e.want(t, 403, "dave", "GET", "/api/v1/integrations/crm", nil)
	e.want(t, 403, "alice", "GET", "/api/v1/integrations/crm", nil)
	e.want(t, 404, "carol", "GET", "/api/v1/integrations/crm/999", nil)
	e.want(t, 404, "carol", "GET", "/api/v1/integrations/crm/abc", nil)

	// Not connected yet: a test is refused, the list shows no connection.
	e.want(t, 409, "carol", "POST", connPath(id)+"/test", nil)
	list := e.want(t, 200, "carol", "GET", "/api/v1/integrations/crm", nil)
	if items := list["items"].([]any); len(items) != 1 {
		t.Fatalf("list = %v", list)
	}

	// Sign in: the authorize URL carries a state; the callback needs no
	// session and consumes the state.
	conn := e.want(t, 200, "carol", "POST", connPath(id)+"/connect", nil)
	au := conn["authorizeUrl"].(string)
	if !strings.HasPrefix(au, "https://crm.example/oauth?client_id=cid&state=") {
		t.Fatalf("authorizeUrl = %q", au)
	}
	state := au[strings.Index(au, "state=")+6:]
	code, _, hdr := e.do(t, "", "GET", "/api/v1/integrations/crm/oauth/callback?state=bogus&code=good", nil)
	if code != 302 || hdr.Get("Location") != "/integrations/crm?error=state" {
		t.Fatalf("bogus state = %d %s", code, hdr.Get("Location"))
	}
	code, _, hdr = e.do(t, "", "GET", "/api/v1/integrations/crm/oauth/callback?state="+state+"&code=bad", nil)
	if code != 302 || !strings.HasPrefix(hdr.Get("Location"), "/integrations/crm?error=failed&connector=") {
		t.Fatalf("bad code = %d %s", code, hdr.Get("Location"))
	}
	conn = e.want(t, 200, "carol", "POST", connPath(id)+"/connect", nil)
	au = conn["authorizeUrl"].(string)
	state = au[strings.Index(au, "state=")+6:]
	code, _, hdr = e.do(t, "", "GET", "/api/v1/integrations/crm/oauth/callback?state="+state+"&code=good", nil)
	if code != 302 || hdr.Get("Location") != "/integrations/crm?connected="+toS(id) || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("callback = %d %s %v", code, hdr.Get("Location"), hdr)
	}
	code, _, hdr = e.do(t, "", "GET", "/api/v1/integrations/crm/oauth/callback?state="+state+"&code=good", nil)
	if hdr.Get("Location") != "/integrations/crm?error=state" {
		t.Fatalf("a replayed state was accepted: %d %s", code, hdr.Get("Location"))
	}
	code, _, hdr = e.do(t, "", "GET", "/api/v1/integrations/crm/oauth/callback?error=access_denied", nil)
	if hdr.Get("Location") != "/integrations/crm?error=denied" {
		t.Fatalf("denied = %d %s", code, hdr.Get("Location"))
	}

	got := e.want(t, 200, "carol", "GET", connPath(id), nil)
	cn := got["connection"].(map[string]any)
	if cn["status"] != "connected" || cn["externalAccount"] != "55" {
		t.Fatalf("connection = %v", cn)
	}
	e.want(t, 200, "carol", "POST", connPath(id)+"/test", nil)
	e.fake.pingErr = &crm.APIError{Status: 403, Msg: "missing scope"}
	if r := e.want(t, 200, "carol", "POST", connPath(id)+"/test", nil); r["ok"] != false || !strings.Contains(r["error"].(string), "missing scope") {
		t.Fatalf("failed test = %v", r)
	}

	// Update: partial, and a new client id demands the secret again.
	upd := e.want(t, 200, "carol", "PATCH", connPath(id), map[string]any{"recordings": true, "linkTtlHours": 48, "fallbackOwner": "9"})
	if upd["recordings"] != true || upd["linkTtlHours"] != float64(48) || upd["fallbackOwner"] != "9" || upd["name"] != "My CRM" {
		t.Fatalf("updated = %v", upd)
	}
	e.want(t, 400, "carol", "PATCH", connPath(id), map[string]any{"config": map[string]string{"clientId": "other"}})
	e.want(t, 400, "carol", "PATCH", connPath(id), map[string]any{"kind": "other"})
	e.want(t, 404, "carol", "PATCH", "/api/v1/integrations/crm/999", map[string]any{"name": "z"})
	e.want(t, 200, "carol", "PATCH", connPath(id), map[string]any{"config": map[string]string{"clientId": "other"}, "clientSecret": "NEW-SECRET"})
	e.want(t, 400, "carol", "PATCH", connPath(id), map[string]any{"unknownField": 1})
	other := e.want(t, 201, "carol", "POST", "/api/v1/integrations/crm", map[string]any{"kind": "fakecrm", "name": "Second"})
	e.want(t, 409, "carol", "PATCH", connPath(other["id"]), map[string]any{"name": "My CRM"})

	// Nothing secret in any representation.
	for _, p := range []string{"/api/v1/integrations/crm", connPath(id)} {
		_, b, _ := e.do(t, "carol", "GET", p, nil)
		for _, s := range []string{"CLIENT-SECRET", "NEW-SECRET", "ACCESS-SECRET", "REFRESH-SECRET"} {
			if strings.Contains(string(b), s) {
				t.Fatalf("%s leaks %q: %s", p, s, b)
			}
		}
	}

	e.want(t, 204, "carol", "DELETE", connPath(id)+"/connection", nil)
	e.want(t, 404, "carol", "DELETE", connPath(id)+"/connection", nil)
	e.want(t, 204, "carol", "DELETE", connPath(id), nil)
	e.want(t, 404, "carol", "DELETE", connPath(id), nil)
	if _, err := e.repo.GetConnector(context.Background(), 1); err == nil {
		t.Fatal("the connector survived its deletion")
	}
}

// TestCRMConnectNeedsCredentialsAndPublicURL fails if the sign-in starts
// without a public URL, without a client id and secret, or for a connector
// that does not use OAuth.
func TestCRMConnectNeedsCredentialsAndPublicURL(t *testing.T) {
	e := newCRMEnv(t, "")
	c := e.want(t, 201, "carol", "POST", "/api/v1/integrations/crm", map[string]any{"kind": "fakecrm", "name": "A", "clientSecret": "s", "config": map[string]string{"clientId": "c"}})
	if _, has := c["redirectUri"]; has {
		t.Fatalf("a redirect URI without a public URL: %v", c)
	}
	e.want(t, 503, "carol", "POST", connPath(c["id"])+"/connect", nil)
	e.svc.PublicURL = "https://hello.example"
	b := e.want(t, 201, "carol", "POST", "/api/v1/integrations/crm", map[string]any{"kind": "fakecrm", "name": "B"})
	e.want(t, 409, "carol", "POST", connPath(b["id"])+"/connect", nil)
	e.want(t, 404, "carol", "POST", "/api/v1/integrations/crm/999/connect", nil)
	e.want(t, 404, "carol", "POST", "/api/v1/integrations/crm/999/test", nil)
	e.want(t, 404, "carol", "GET", "/api/v1/integrations/crm/999/logs", nil)
	e.want(t, 404, "carol", "GET", "/api/v1/integrations/crm/999/users", nil)
}

// TestCRMTestIsRateLimited fails if a connector can be tested more than six
// times a minute.
func TestCRMTestIsRateLimited(t *testing.T) {
	e := newCRMEnv(t, "https://hello.example")
	c := e.want(t, 201, "carol", "POST", "/api/v1/integrations/crm", map[string]any{"kind": "fakecrm", "name": "A", "clientSecret": "s", "config": map[string]string{"clientId": "c"}})
	id := int64(c["id"].(float64))
	if _, err := e.repo.SaveOrgConnection(context.Background(), id, crm.Tokens{Access: "a", Refresh: "r", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	for range 6 {
		e.want(t, 200, "carol", "POST", connPath(id)+"/test", nil)
	}
	code, _, hdr := e.do(t, "carol", "POST", connPath(id)+"/test", nil)
	if code != 429 || hdr.Get("Retry-After") == "" {
		t.Fatalf("7th test = %d %v", code, hdr)
	}
}

// TestCRMLogsAndRetry: the job list pages, filters and retries a dead job. It
// fails if a viewer reads the logs, a caller's number appears in a job, a
// live job can be retried, or a bad filter is accepted.
func TestCRMLogsAndRetry(t *testing.T) {
	e := newCRMEnv(t, "https://hello.example")
	c := e.want(t, 201, "carol", "POST", "/api/v1/integrations/crm", map[string]any{"kind": "fakecrm", "name": "A"})
	id := int64(c["id"].(float64))
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_ = e.repo.UpsertCall(ctx, nil, id, crm.LogJob{CallID: "call-" + strconv.Itoa(i), Direction: "inbound", EventType: "call.completed",
			Payload: []byte(`{"from":{"number":"+3211223344"}}`), OwnerExtension: "101"})
	}
	_ = e.repo.Skip(ctx, nil, id, crm.LogJob{CallID: "skipped-1", Direction: "inbound", EventType: "call.completed"}, "no owner")
	e.want(t, 403, "dave", "GET", connPath(id)+"/logs", nil)
	page := e.want(t, 200, "carol", "GET", connPath(id)+"/logs?limit=2", nil)
	if n := len(page["items"].([]any)); n != 2 || page["nextBefore"].(float64) == 0 {
		t.Fatalf("page = %v", page)
	}
	next := e.want(t, 200, "carol", "GET", connPath(id)+"/logs?limit=2&before="+toS(page["nextBefore"]), nil)
	if n := len(next["items"].([]any)); n != 2 || next["nextBefore"].(float64) == 0 {
		t.Fatalf("second page = %v", next)
	}
	sk := e.want(t, 200, "carol", "GET", connPath(id)+"/logs?status=skipped", nil)
	items := sk["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["lastError"] != "no owner" {
		t.Fatalf("skipped = %v", sk)
	}
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), "3211223344") {
		t.Fatalf("a caller's number is in the job list: %s", raw)
	}
	for _, q := range []string{"status=weird", "limit=0", "limit=500", "limit=x", "before=0", "before=x"} {
		e.want(t, 400, "carol", "GET", connPath(id)+"/logs?"+q, nil)
	}
	skID := items[0].(map[string]any)["id"]
	e.want(t, 200, "carol", "POST", "/api/v1/integrations/crm/logs/"+toS(skID)+"/retry", nil)
	e.want(t, 409, "carol", "POST", "/api/v1/integrations/crm/logs/"+toS(skID)+"/retry", nil) // pending now
	e.want(t, 404, "carol", "POST", "/api/v1/integrations/crm/logs/999/retry", nil)
	e.want(t, 403, "dave", "POST", "/api/v1/integrations/crm/logs/"+toS(skID)+"/retry", nil)
}

// TestCRMUserMapping: users are listed with their CRM user; a manual mapping
// is set and cleared. It fails if an unknown user can be mapped, or the
// mapping needs less than an administrator.
func TestCRMUserMapping(t *testing.T) {
	e := newCRMEnv(t, "https://hello.example")
	c := e.want(t, 201, "carol", "POST", "/api/v1/integrations/crm", map[string]any{"kind": "fakecrm", "name": "A"})
	p := connPath(c["id"]) + "/users"
	list := e.want(t, 200, "carol", "GET", p, nil)
	u := list["items"].([]any)[0].(map[string]any)
	if u["username"] != "alice" || u["email"] != "alice@example.com" || u["extension"] != "101" || u["externalUserId"] != nil {
		t.Fatalf("users = %v", list)
	}
	uid := toS(u["userId"])
	e.want(t, 403, "dave", "PUT", p+"/"+uid, map[string]any{"externalUserId": "77"})
	e.want(t, 204, "carol", "PUT", p+"/"+uid, map[string]any{"externalUserId": "77"})
	u = e.want(t, 200, "carol", "GET", p, nil)["items"].([]any)[0].(map[string]any)
	if u["externalUserId"] != "77" || u["source"] != "manual" {
		t.Fatalf("mapped user = %v", u)
	}
	e.want(t, 204, "carol", "PUT", p+"/"+uid, map[string]any{"externalUserId": ""})
	e.want(t, 404, "carol", "PUT", p+"/999", map[string]any{"externalUserId": "1"})
	e.want(t, 404, "carol", "PUT", p+"/abc", map[string]any{"externalUserId": "1"})
	e.want(t, 400, "carol", "PUT", p+"/"+uid, map[string]any{})
	e.want(t, 400, "carol", "PUT", p+"/"+uid, map[string]any{"externalUserId": strings.Repeat("x", 65)})
}

// TestHubSpotEngagementHandshake: the iframe records its engagement; a user
// may do so for their own call only. It fails if a user records another
// extension's call, a malformed id is accepted, or the server's own
// engagement is replaced by a late one.
func TestHubSpotEngagementHandshake(t *testing.T) {
	e := newCRMEnv(t, "https://hello.example")
	hs := "/api/v1/integrations/hubspot/engagements"
	e.want(t, 404, "carol", "POST", hs, map[string]any{"callId": "c1", "engagementId": "123"}) // no HubSpot connector yet
	// The registered kind in this test is fakecrm; give the handshake a
	// HubSpot connector whose kind the fake also answers to.
	e.svc.Tokens.Providers = func(k string) (crm.Connector, bool) { return e.fake, k == "fakecrm" || k == "hubspot" }
	c := e.want(t, 201, "carol", "POST", "/api/v1/integrations/crm", map[string]any{"kind": "hubspot", "name": "HubSpot"})
	id := int64(c["id"].(float64))
	e.repo.Events = [][2]string{{"c1", "101"}, {"c2", "102"}}

	e.want(t, 400, "alice", "POST", hs, map[string]any{"callId": "c1", "engagementId": "abc"})
	e.want(t, 400, "alice", "POST", hs, map[string]any{"callId": "", "engagementId": "1"})
	e.want(t, 403, "alice", "POST", hs, map[string]any{"callId": "c2", "engagementId": "123"}) // bob's call
	e.want(t, 403, "noext", "POST", hs, map[string]any{"callId": "c1", "engagementId": "123"})
	if r := e.want(t, 200, "alice", "POST", hs, map[string]any{"callId": "c1", "engagementId": "123"}); r["adopted"] != true {
		t.Fatalf("own call = %v", r)
	}
	if r := e.want(t, 200, "carol", "POST", hs, map[string]any{"callId": "c9", "engagementId": "9"}); r["adopted"] != true {
		t.Fatalf("an admin may record any call: %v", r)
	}
	// A second, different engagement for the same call does not replace the
	// first.
	if r := e.want(t, 200, "alice", "POST", hs, map[string]any{"callId": "c1", "engagementId": "456"}); r["adopted"] != false {
		t.Fatalf("a late engagement replaced the first: %v", r)
	}
	if row, ok := e.repo.Log(id, "c1"); !ok || row.ExternalID != "123" {
		t.Fatalf("row = %+v", row)
	}
}

// TestCRMRoutesAnswer503WithoutService fails if the CRM routes panic or
// answer anything but 503 when the service is not wired.
func TestCRMRoutesAnswer503WithoutService(t *testing.T) {
	f, _, _, _ := newAcctFake(t)
	srv := httptest.NewServer(Handler(Config{Store: evStore{f}, Log: slog.New(slog.DiscardHandler), SessionTTL: time.Hour}))
	t.Cleanup(srv.Close)
	e := &crmEnv{srv: srv}
	for _, rt := range []struct{ m, p string }{
		{"GET", "/api/v1/integrations/crm"}, {"POST", "/api/v1/integrations/crm"}, {"GET", "/api/v1/integrations/crm/1"},
		{"PATCH", "/api/v1/integrations/crm/1"}, {"DELETE", "/api/v1/integrations/crm/1"}, {"POST", "/api/v1/integrations/crm/1/connect"},
		{"DELETE", "/api/v1/integrations/crm/1/connection"}, {"POST", "/api/v1/integrations/crm/1/test"},
		{"GET", "/api/v1/integrations/crm/1/logs"}, {"POST", "/api/v1/integrations/crm/logs/1/retry"},
		{"GET", "/api/v1/integrations/crm/1/users"}, {"PUT", "/api/v1/integrations/crm/1/users/1"},
		{"POST", "/api/v1/integrations/hubspot/engagements"}, {"GET", "/api/v1/integrations/crm/oauth/callback"},
		{"POST", "/api/v1/integrations/salesforce/engagements"}, {"GET", "/api/v1/integrations/crm/1/setup"},
	} {
		who := "carol"
		if strings.HasSuffix(rt.p, "engagements") {
			who = "alice"
		}
		if strings.HasSuffix(rt.p, "callback") {
			who = ""
		}
		if code, b, _ := e.do(t, who, rt.m, rt.p, map[string]any{}); code != 503 {
			t.Errorf("%s %s = %d %s, want 503", rt.m, rt.p, code, b)
		}
	}
}

// sfFake is a connector that signs in with client credentials, describes its
// own setup and checks record ids, as the Salesforce connector does.
type sfFake struct {
	crmFake
	authErr error
}

func (f *sfFake) Kind() string { return "salesforce" }
func (f *sfFake) Authenticate(context.Context, crm.Config) (crm.Tokens, error) {
	if f.authErr != nil {
		return crm.Tokens{}, f.authErr
	}
	return crm.Tokens{Access: "ACCESS-SECRET", ExpiresAt: time.Now().Add(time.Hour), Account: "acme.my.salesforce.com"}, nil
}
func (f *sfFake) ServerSide(cfg map[string]string) bool { return cfg["serverLogging"] == "true" }
func (f *sfFake) ValidRef(r string) bool                { return strings.HasPrefix(r, "00T") }
func (f *sfFake) Setup(_ crm.Config, public string) []crm.SetupItem {
	return []crm.SetupItem{{Name: "adapterUrl", Label: "CTI adapter URL", Kind: "url", Value: public + "/integrations/salesforce/softphone"}}
}

// TestSalesforceClientCredentialsAndSetup: a client-credentials connector is
// connected by the Connect call itself, a refusal is a 400 on the secret, the setup values need the admin, and the softphone's record
// handshake checks the record id and the caller's ownership. It fails if
// Connect answers an authorize URL for it, a secret or token leaks, a
// non-admin reads the setup, or a malformed record id is stored.
func TestSalesforceClientCredentialsAndSetup(t *testing.T) {
	e := newCRMEnv(t, "https://hello.example")
	sf := &sfFake{}
	e.svc.Tokens.Providers = func(k string) (crm.Connector, bool) { return sf, k == "salesforce" }
	e.want(t, 404, "alice", "POST", "/api/v1/integrations/salesforce/engagements", map[string]any{"callId": "c1", "recordId": "00T000000000001"}) // no connector yet
	c := e.want(t, 201, "carol", "POST", "/api/v1/integrations/crm", map[string]any{"kind": "salesforce", "name": "Salesforce",
		"clientSecret": "CLIENT-SECRET", "config": map[string]string{"clientId": "cid", "serverLogging": "true"}})
	p := connPath(c["id"])

	setup := e.want(t, 200, "carol", "GET", p+"/setup", nil)
	it := setup["items"].([]any)[0].(map[string]any)
	if it["name"] != "adapterUrl" || it["value"] != "https://hello.example/integrations/salesforce/softphone" {
		t.Fatalf("setup = %v", setup)
	}
	e.want(t, 403, "alice", "GET", p+"/setup", nil)
	e.want(t, 404, "carol", "GET", connPath(int64(99))+"/setup", nil)

	sf.authErr = &crm.APIError{Status: 400, Msg: "invalid_client: invalid client credentials"}
	code, body, _ := e.do(t, "carol", "POST", p+"/connect", nil)
	if code != 400 || !strings.Contains(string(body), "invalid_client") {
		t.Fatalf("refused connect = %d %s", code, body)
	}
	sf.authErr = nil
	r := e.want(t, 200, "carol", "POST", p+"/connect", nil)
	if r["connected"] != true || r["authorizeUrl"] != "" {
		t.Fatalf("connect = %v", r)
	}
	got := e.want(t, 200, "carol", "GET", p, nil)
	if conn, _ := got["connection"].(map[string]any); conn["status"] != "connected" || conn["externalAccount"] != "acme.my.salesforce.com" {
		t.Fatalf("connection = %v", got["connection"])
	}
	raw, _ := json.Marshal(got)
	for _, secret := range []string{"ACCESS-SECRET", "CLIENT-SECRET"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("the connector leaks %q", secret)
		}
	}

	hs := "/api/v1/integrations/salesforce/engagements"
	e.repo.Events = [][2]string{{"c1", "101"}, {"c2", "102"}}
	e.want(t, 400, "alice", "POST", hs, map[string]any{"callId": "c1"})
	e.want(t, 400, "alice", "POST", hs, map[string]any{"callId": "c1", "recordId": strings.Repeat("0", 19)})
	e.want(t, 400, "alice", "POST", hs, map[string]any{"callId": "c1", "recordId": "003AAAAAAAAAAAA"})
	e.want(t, 403, "alice", "POST", hs, map[string]any{"callId": "c2", "recordId": "00T000000000001"})
	if r := e.want(t, 200, "alice", "POST", hs, map[string]any{"callId": "c1", "recordId": "00T000000000001"}); r["adopted"] != true {
		t.Fatalf("own call = %v", r)
	}
	if row, ok := e.repo.Log(int64(c["id"].(float64)), "c1"); !ok || row.ExternalID != "00T000000000001" {
		t.Fatalf("row = %+v", row)
	}
}
