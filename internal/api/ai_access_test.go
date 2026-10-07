package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/oauth"
	"github.com/azrtydxb/hello/internal/store"
)

// aiEnv is the API on a scratch database with the authorization server
// (DCR on) for HELLO_PUBLIC_URL http://localhost.
type aiEnv struct {
	*env
	as *oauth.Server
}

func newAIEnv(t *testing.T) *aiEnv {
	t.Helper()
	e := newEnv(t, noLive{})
	as, err := oauth.New(oauth.Options{Store: e.st, PublicURL: "http://localhost", DCR: true})
	if err != nil {
		t.Fatal(err)
	}
	e.srv.Close()
	e.srv = httptest.NewServer(Handler(Config{Store: e.st, Live: noLive{}, SIPDomain: testDomain, SessionTTL: sessionTTL, AI: as}))
	t.Cleanup(e.srv.Close)
	return &aiEnv{env: e, as: as}
}

// oauthPost posts a form to the authorization server.
func (e *aiEnv) oauthPost(path string, form url.Values, basic ...string) response {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if len(basic) == 2 {
		req.SetBasicAuth(basic[0], basic[1])
	}
	rec := httptest.NewRecorder()
	e.as.Handler().ServeHTTP(rec, req)
	return response{code: rec.Code, body: rec.Body.Bytes(), header: rec.Header()}
}

// oauthToken registers a public client, authorizes it for scope through
// the console's consent route with c's session and returns the token
// response.
func (e *aiEnv) oauthToken(c *client, scope string) map[string]any {
	t := e.t
	t.Helper()
	id, clientID, verifier := e.authorizeRequest(scope)
	view := c.must(http.StatusOK, "GET", "/api/v1/oauth/requests/"+id, nil).json(t)
	if view["clientName"] != "Agent" || view["verified"] != false {
		t.Fatalf("consent view = %v", view)
	}
	approved := c.must(http.StatusOK, "POST", "/api/v1/oauth/requests/"+id+"/approve",
		map[string]any{"scopes": strings.Fields(scope)}).json(t)
	u, _ := url.Parse(approved["redirect"].(string))
	tok := e.oauthPost("/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {u.Query().Get("code")},
		"client_id": {clientID}, "redirect_uri": {"http://127.0.0.1/cb"}, "code_verifier": {verifier}})
	if tok.code != http.StatusOK {
		t.Fatalf("token = %d %s", tok.code, tok.body)
	}
	out := tok.json(t)
	out["client_id"] = clientID
	return out
}

// authorizeRequest registers a public client and starts an authorization
// request for scope, returning the pending request id, the client id and
// the PKCE verifier.
func (e *aiEnv) authorizeRequest(scope string) (id, clientID, verifier string) {
	t := e.t
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(`{"redirect_uris":["http://127.0.0.1/cb"],"client_name":"Agent"}`))
	rec := httptest.NewRecorder()
	e.as.Handler().ServeHTTP(rec, req)
	reg := response{code: rec.Code, body: rec.Body.Bytes()}.json(t)
	clientID = reg["client_id"].(string)
	verifier = strings.Repeat("v", 50)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {"http://127.0.0.1/cb"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"scope": {scope}, "state": {"s"}, "resource": {"http://localhost/api/v1"}}
	rec = httptest.NewRecorder()
	e.as.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	return loc.Query().Get("request"), clientID, verifier
}

// TestTokenLifecycle (store and API half) fails if a new personal token
// lacks its prefix or is stored in plaintext, a legacy token stops working
// or loses a scope, POST /tokens without scopes grants secrets, an expired
// or revoked credential is admitted, or an OAuth call's audit row lacks
// via.
func TestTokenLifecycle(t *testing.T) {
	e := newAIEnv(t)
	ctx := context.Background()
	c := e.login()

	created := c.must(http.StatusCreated, "POST", "/api/v1/tokens", map[string]any{"name": "ci"}).json(t)
	plain := created["token"].(string)
	if !strings.HasPrefix(plain, auth.PrefixPersonal) || created["kind"] != "personal" ||
		fmt.Sprint(created["scopes"]) != "[read write admin]" {
		t.Fatalf("created token = %v", created)
	}
	var rows int
	if err := e.db.QueryRowContext(ctx, `SELECT count(*) FROM api_tokens t WHERE position($1 in row_to_json(t)::text) > 0`, plain).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("token plaintext stored in %d rows (%v)", rows, err)
	}
	b := e.client()
	b.bearer = plain
	b.must(http.StatusOK, "GET", "/api/v1/auth/me", nil)
	if r := b.do("POST", "/api/v1/devices/1/rotate-secret", nil); r.code != http.StatusForbidden || !strings.Contains(string(r.body), "insufficient_scope") {
		t.Fatalf("a token without secrets reached a secrets route: %d %s", r.code, r.body)
	}
	// Scopes are named explicitly; never beyond the creator's.
	c.must(http.StatusBadRequest, "POST", "/api/v1/tokens", map[string]any{"name": "x", "scopes": []string{"session"}})
	c.must(http.StatusBadRequest, "POST", "/api/v1/tokens", map[string]any{"name": "x", "scopes": []string{"root"}})
	c.must(http.StatusBadRequest, "POST", "/api/v1/tokens", map[string]any{"name": "x", "expiresAt": "2001-01-01T00:00:00Z"})
	b.must(http.StatusBadRequest, "POST", "/api/v1/tokens", map[string]any{"name": "x", "scopes": []string{"secrets"}})
	sec := c.must(http.StatusCreated, "POST", "/api/v1/tokens", map[string]any{"name": "s", "scopes": []string{"secrets", "read"}}).json(t)
	if fmt.Sprint(sec["scopes"]) != "[secrets read]" {
		t.Fatalf("secrets token = %v", sec)
	}

	// Expiry and revocation take effect on the next request.
	if _, err := e.db.ExecContext(ctx, `UPDATE api_tokens SET expires_at = now() - interval '1 second' WHERE id = $1`, int64(created["id"].(float64))); err != nil {
		t.Fatal(err)
	}
	b.must(http.StatusUnauthorized, "GET", "/api/v1/auth/me", nil)

	// A legacy token keeps every scope.
	var uid int64
	if err := e.db.QueryRowContext(ctx, `SELECT id FROM users WHERE username = $1`, testUser).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	old, oldHash := auth.NewToken()
	if _, err := e.st.CreateToken(ctx, "test", uid, store.NewToken{Name: "old"}, oldHash); err != nil {
		t.Fatal(err)
	}
	l := e.client()
	l.bearer = old
	l.must(http.StatusNotFound, "POST", "/api/v1/devices/999/rotate-secret", nil)
	items := c.must(http.StatusOK, "GET", "/api/v1/tokens", nil).json(t)["items"].([]any)
	if len(items) != 3 || items[2].(map[string]any)["kind"] != "legacy" || bytes.Contains(c.must(http.StatusOK, "GET", "/api/v1/tokens", nil).body, []byte(plain)) {
		t.Fatalf("token list = %v", items)
	}

	// An OAuth call's audit row names the client in via.
	tok := e.oauthToken(c, "read write")
	o := e.client()
	o.bearer = tok["access_token"].(string)
	ext := o.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "150", "name": "Agent"}).json(t)
	var actor, via string
	if err := e.db.QueryRowContext(ctx, `SELECT actor, coalesce(via, '') FROM audit_events WHERE resource = 'extension' AND resource_id = $1`,
		fmt.Sprint(ext["id"])).Scan(&actor, &via); err != nil {
		t.Fatal(err)
	}
	if actor != "user:"+testUser || via != tok["client_id"] {
		t.Fatalf("audit actor %q via %q, want user:%s via %s", actor, via, testUser, tok["client_id"])
	}
	// A bearer token can never approve consent.
	o.must(http.StatusForbidden, "POST", "/api/v1/oauth/requests/x/approve", map[string]any{"scopes": []string{"read"}})
	// An OAuth token bound to the API is refused on nothing else; a
	// narrower one gets the step-up 403.
	ro := e.oauthToken(c, "read")
	r := e.client()
	r.bearer = ro["access_token"].(string)
	if res := r.do("POST", "/api/v1/extensions", map[string]string{"number": "151", "name": "x"}); res.code != http.StatusForbidden ||
		!strings.Contains(res.header.Get("WWW-Authenticate"), `scope="write"`) {
		t.Fatalf("read token writing = %d %q", res.code, res.header.Get("WWW-Authenticate"))
	}
}

// TestServiceAccountsAPI (the API half of TestServiceAccounts) fails if a
// secret is returned other than once, a third live secret is allowed, a
// service account's role is not enforced, or a disabled account still
// works on the next request.
func TestServiceAccountsAPI(t *testing.T) {
	e := newAIEnv(t)
	c := e.login()
	c.must(http.StatusBadRequest, "POST", "/api/v1/service-accounts", map[string]any{"name": "ci", "role": "viewer", "scopes": []string{"write"}})
	sa := c.must(http.StatusCreated, "POST", "/api/v1/service-accounts",
		map[string]any{"name": "ci", "description": "pipeline", "role": "operator", "scopes": []string{"read", "write"}}).json(t)
	id := sa["id"].(string)
	c.must(http.StatusConflict, "POST", "/api/v1/service-accounts", map[string]any{"name": "ci", "role": "viewer", "scopes": []string{"read"}})
	s1 := c.must(http.StatusCreated, "POST", "/api/v1/service-accounts/"+id+"/secrets", nil).json(t)
	secret := s1["secret"].(string)
	c.must(http.StatusCreated, "POST", "/api/v1/service-accounts/"+id+"/secrets", map[string]any{})
	c.must(http.StatusConflict, "POST", "/api/v1/service-accounts/"+id+"/secrets", nil)
	c.must(http.StatusBadRequest, "POST", "/api/v1/service-accounts/"+id+"/secrets", "{")
	got := c.must(http.StatusOK, "GET", "/api/v1/service-accounts/"+id, nil)
	list := c.must(http.StatusOK, "GET", "/api/v1/service-accounts", nil)
	if bytes.Contains(got.body, []byte(secret)) || bytes.Contains(list.body, []byte(secret)) || len(got.json(t)["secrets"].([]any)) != 2 {
		t.Fatalf("account read back = %s", got.body)
	}

	tok := e.oauthPost("/oauth/token", url.Values{"grant_type": {"client_credentials"}}, id, secret)
	if tok.code != http.StatusOK {
		t.Fatalf("client credentials = %d %s", tok.code, tok.body)
	}
	b := e.client()
	b.bearer = tok.json(t)["access_token"].(string)
	b.must(http.StatusOK, "GET", "/api/v1/extensions", nil)
	if r := b.do("GET", "/api/v1/tokens", nil); r.code != http.StatusForbidden {
		t.Fatalf("an operator service account read API tokens: %d", r.code)
	}
	c.must(http.StatusOK, "PATCH", "/api/v1/service-accounts/"+id, map[string]any{"enabled": false})
	c.must(http.StatusBadRequest, "PATCH", "/api/v1/service-accounts/"+id, "{")
	b.must(http.StatusUnauthorized, "GET", "/api/v1/extensions", nil)
	c.must(http.StatusNoContent, "DELETE", "/api/v1/service-accounts/"+id+"/secrets/"+fmt.Sprint(s1["id"]), nil)
	c.must(http.StatusNotFound, "DELETE", "/api/v1/service-accounts/"+id+"/secrets/"+fmt.Sprint(s1["id"]), nil)
	c.must(http.StatusNoContent, "DELETE", "/api/v1/service-accounts/"+id, nil)
	c.must(http.StatusNotFound, "GET", "/api/v1/service-accounts/"+id, nil)
	c.must(http.StatusNotFound, "DELETE", "/api/v1/service-accounts/"+id, nil)
	var n int
	if err := e.db.QueryRowContext(context.Background(), `SELECT count(*) FROM audit_events WHERE resource IN ('service_account', 'client_secret')`).Scan(&n); err != nil || n < 6 {
		t.Fatalf("service-account audit rows = %d (%v)", n, err)
	}
}

// TestGrantsAPI fails if the grants list misses the caller's grant, a
// deleted grant's token keeps working, the settings route does not report
// the resources, or the daily prune keeps a stale registered client or
// drops a fresh or used one (spec S-10).
func TestGrantsAPI(t *testing.T) {
	e := newAIEnv(t)
	c := e.login()
	tok := e.oauthToken(c, "read")
	gs := c.must(http.StatusOK, "GET", "/api/v1/oauth/grants", nil).json(t)["items"].([]any)
	if len(gs) != 1 || gs[0].(map[string]any)["clientId"] != tok["client_id"] {
		t.Fatalf("grants = %v", gs)
	}
	b := e.client()
	b.bearer = tok["access_token"].(string)
	b.must(http.StatusOK, "GET", "/api/v1/extensions", nil)
	c.must(http.StatusNoContent, "DELETE", "/api/v1/oauth/grants/"+fmt.Sprint(gs[0].(map[string]any)["id"]), nil)
	c.must(http.StatusNotFound, "DELETE", "/api/v1/oauth/grants/"+fmt.Sprint(gs[0].(map[string]any)["id"]), nil)

	// Consent refusals: a bad scope list, an unknown request, and a
	// denial that sends the client back with access_denied.
	c.must(http.StatusBadRequest, "POST", "/api/v1/oauth/requests/x/approve", map[string]any{"scopes": []string{"root"}})
	c.must(http.StatusNotFound, "POST", "/api/v1/oauth/requests/nope/approve", map[string]any{"scopes": []string{"read"}})
	c.must(http.StatusNotFound, "POST", "/api/v1/oauth/requests/nope/deny", nil)
	pending, _, _ := e.authorizeRequest("read")
	denied := c.must(http.StatusOK, "POST", "/api/v1/oauth/requests/"+pending+"/deny", nil).json(t)
	if !strings.Contains(denied["redirect"].(string), "error=access_denied") {
		t.Fatalf("deny redirect = %v", denied)
	}
	b.must(http.StatusUnauthorized, "GET", "/api/v1/extensions", nil)
	set := c.must(http.StatusOK, "GET", "/api/v1/ai/settings", nil).json(t)
	if set["enabled"] != true || set["mcpUrl"] != "http://localhost/mcp" || set["dcr"] != true {
		t.Fatalf("ai settings = %v", set)
	}

	// The daily prune drops a registered client that never completed a
	// grant within a day, and keeps fresh and used ones.
	ctx := context.Background()
	if _, err := e.db.ExecContext(ctx, `INSERT INTO oauth_clients (client_id, kind, name, created_at) VALUES
		('hello_dcr_stale', 'dcr', 'x', now() - interval '2 days'), ('hello_dcr_fresh', 'dcr', 'y', now()),
		('hello_dcr_idle', 'dcr', 'z', now() - interval '60 days')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.ExecContext(ctx, `UPDATE oauth_clients SET last_used_at = now() - interval '31 days' WHERE client_id = 'hello_dcr_idle'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.PruneOAuth(ctx, time.Now().Add(-7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var left []string
	rows, err := e.db.QueryContext(ctx, `SELECT client_id FROM oauth_clients ORDER BY client_id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		left = append(left, id)
	}
	_ = rows.Close()
	if strings.Contains(strings.Join(left, " "), "stale") || strings.Contains(strings.Join(left, " "), "idle") ||
		!strings.Contains(strings.Join(left, " "), "hello_dcr_fresh") || !strings.Contains(strings.Join(left, " "), tok["client_id"].(string)) {
		t.Fatalf("clients after prune = %v", left)
	}
}
