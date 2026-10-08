package oauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/vkconn"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/valkey-io/valkey-go"
)

// TestResourcesAndMetadataURL fails if a resource identifier or its RFC
// 9728 metadata URL is not derived from the public URL, or a URL with a
// path is accepted as the public URL.
func TestResourcesAndMetadataURL(t *testing.T) {
	s, err := New(Options{PublicURL: "https://hello.example/"})
	if err != nil {
		t.Fatal(err)
	}
	api, mcp := s.Resources()
	if api != "https://hello.example/api/v1" || mcp != "https://hello.example/mcp" {
		t.Fatalf("Resources = %s, %s", api, mcp)
	}
	if got := s.MetadataURL(mcp); got != "https://hello.example/.well-known/oauth-protected-resource/mcp" {
		t.Fatalf("MetadataURL(mcp) = %s", got)
	}
	if got := s.MetadataURL(api); got != "https://hello.example/.well-known/oauth-protected-resource/api/v1" {
		t.Fatalf("MetadataURL(api) = %s", got)
	}
	for _, bad := range []string{"", "hello.example", "https://hello.example/x"} {
		if _, err := New(Options{PublicURL: bad}); err == nil {
			t.Errorf("New(%q) accepted", bad)
		}
	}
}

const public = "https://hello.example"

// harness is an authorization server on a fake store, with a client ID
// metadata document served over TLS on loopback (CIMDAllowPrivate).
type harness struct {
	t    *testing.T
	st   *fakeStore
	srv  *Server
	h    http.Handler
	logs *syncBuffer
	// cimd serves the client ID metadata document doc at clientID.
	cimd     *httptest.Server
	clientID string
	doc      map[string]any
	redirect string
}

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

func newHarness(t *testing.T, mod func(*Options)) *harness {
	t.Helper()
	e := &harness{t: t, st: newFakeStore(), logs: &syncBuffer{}, redirect: "http://127.0.0.1/callback"}
	e.cimd = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=60")
		_ = json.NewEncoder(w).Encode(e.doc)
	}))
	t.Cleanup(e.cimd.Close)
	e.clientID = e.cimd.URL + "/client.json"
	e.doc = map[string]any{"client_id": e.clientID, "client_name": "Test Agent", "redirect_uris": []string{e.redirect}}
	o := Options{
		Store: e.st, PublicURL: public, CIMDAllowPrivate: true, Client: e.cimd.Client(),
		Log: slog.New(slog.NewJSONHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	if mod != nil {
		mod(&o)
	}
	srv, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	e.srv, e.h = srv, srv.Handler()
	return e
}

func (e *harness) get(path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func (e *harness) post(path string, form url.Values, basic ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if len(basic) == 2 {
		req.SetBasicAuth(url.QueryEscape(basic[0]), url.QueryEscape(basic[1]))
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func jsonBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("%d %q is not JSON", rec.Code, rec.Body)
	}
	return m
}

const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk-and-some-more"

func challengeOf(v string) string {
	s := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(s[:])
}

// authorizeQuery is a valid authorization request with overrides.
func (e *harness) authorizeQuery(over map[string]string) string {
	q := url.Values{
		"response_type": {"code"}, "client_id": {e.clientID}, "redirect_uri": {e.redirect},
		"code_challenge": {challengeOf(verifier)}, "code_challenge_method": {"S256"},
		"scope": {"read write"}, "state": {"st-1"},
	}
	for k, v := range over {
		if v == "" {
			q.Del(k)
		} else {
			q.Set(k, v)
		}
	}
	return "/oauth/authorize?" + q.Encode()
}

var alice = auth.Actor{
	UserID: 1, Username: "alice", Role: auth.RoleAdmin, Kind: auth.KindSession,
	Scopes: append(slices.Clone(auth.AllScopes), auth.ScopeSession),
}

// requestID runs a valid authorization request and returns its id.
func (e *harness) requestID(over map[string]string) string {
	e.t.Helper()
	rec := e.get(e.authorizeQuery(over))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusFound || loc == nil || loc.Path != "/oauth/consent" {
		e.t.Fatalf("authorize = %d %s, want a redirect to the consent page", rec.Code, rec.Header().Get("Location"))
	}
	if !strings.HasPrefix(rec.Header().Get("Location"), public+"/oauth/consent?request=") {
		e.t.Fatalf("consent redirect %s is not under the public URL", rec.Header().Get("Location"))
	}
	return loc.Query().Get("request")
}

// code runs authorize and approval with scopes and returns the code.
func (e *harness) code(over map[string]string, a auth.Actor, scopes auth.Scopes) string {
	e.t.Helper()
	redirect, err := e.srv.Approve(context.Background(), a, e.requestID(over), scopes)
	if err != nil {
		e.t.Fatal(err)
	}
	u, _ := url.Parse(redirect)
	return u.Query().Get("code")
}

func (e *harness) exchange(code string, over map[string]string) *httptest.ResponseRecorder {
	f := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {e.clientID},
		"redirect_uri": {e.redirect}, "code_verifier": {verifier},
	}
	for k, v := range over {
		f.Set(k, v)
	}
	return e.post("/oauth/token", f)
}

// tokens runs the whole flow and returns the token response.
func (e *harness) tokens(scopes auth.Scopes, over map[string]string) map[string]any {
	e.t.Helper()
	rec := e.exchange(e.code(over, alice, scopes), nil)
	if rec.Code != http.StatusOK {
		e.t.Fatalf("token = %d %s", rec.Code, rec.Body)
	}
	return jsonBody(e.t, rec)
}

// admitted reports whether token passes auth.Middleware for resource,
// optionally inside an MCP replay by client.
func (e *harness) admitted(token, resource string, replay *auth.Replay) bool {
	e.t.Helper()
	mw := auth.Middleware(e.st, auth.Options{Resource: resource, MetadataURL: e.srv.MetadataURL(resource)}, slog.New(slog.DiscardHandler))
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/extensions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if replay != nil {
		req = req.WithContext(auth.WithReplay(req.Context(), *replay))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code == http.StatusTeapot
}

// TestOAuthMetadata fails if either metadata document lacks a field of
// spec S-7 or names another issuer or resource, if a 401 lacks the
// resource_metadata challenge, or if registration is advertised with DCR
// off. (The 404s without HELLO_PUBLIC_URL are TestComposeWithoutPublicURL
// in cmd/hello-control.)
func TestOAuthMetadata(t *testing.T) {
	for _, dcr := range []bool{false, true} {
		e := newHarness(t, func(o *Options) { o.DCR = dcr })
		rec := e.get("/.well-known/oauth-authorization-server")
		if rec.Code != http.StatusOK {
			t.Fatalf("server metadata = %d", rec.Code)
		}
		var m struct {
			Issuer                string   `json:"issuer"`
			AuthorizationEndpoint string   `json:"authorization_endpoint"`
			TokenEndpoint         string   `json:"token_endpoint"`
			RevocationEndpoint    string   `json:"revocation_endpoint"`
			RegistrationEndpoint  string   `json:"registration_endpoint"`
			ResponseTypes         []string `json:"response_types_supported"`
			GrantTypes            []string `json:"grant_types_supported"`
			Challenge             []string `json:"code_challenge_methods_supported"`
			AuthMethods           []string `json:"token_endpoint_auth_methods_supported"`
			Scopes                []string `json:"scopes_supported"`
			CIMD                  bool     `json:"client_id_metadata_document_supported"`
			ISS                   bool     `json:"authorization_response_iss_parameter_supported"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		if m.Issuer != public || m.AuthorizationEndpoint != public+"/oauth/authorize" ||
			m.TokenEndpoint != public+"/oauth/token" || m.RevocationEndpoint != public+"/oauth/revoke" {
			t.Fatalf("endpoints = %+v", m)
		}
		if (m.RegistrationEndpoint != "") != dcr {
			t.Fatalf("DCR %v: registration_endpoint = %q", dcr, m.RegistrationEndpoint)
		}
		if !slices.Equal(m.ResponseTypes, []string{"code"}) || !slices.Equal(m.Challenge, []string{"S256"}) ||
			!slices.Equal(m.GrantTypes, []string{"authorization_code", "refresh_token", "client_credentials"}) ||
			!slices.Equal(m.AuthMethods, []string{"none", "client_secret_basic", "client_secret_post"}) ||
			!slices.Equal(m.Scopes, []string{"read", "write", "admin", "secrets"}) || !m.CIMD || !m.ISS {
			t.Fatalf("server metadata = %s", rec.Body)
		}
		for _, res := range []string{"/api/v1", "/mcp"} {
			rec := e.get("/.well-known/oauth-protected-resource" + res)
			var pr struct {
				Resource string   `json:"resource"`
				AS       []string `json:"authorization_servers"`
				Scopes   []string `json:"scopes_supported"`
				Bearer   []string `json:"bearer_methods_supported"`
				Name     string   `json:"resource_name"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &pr); err != nil || rec.Code != http.StatusOK {
				t.Fatalf("resource metadata %s = %d %s", res, rec.Code, rec.Body)
			}
			if pr.Resource != public+res || !slices.Equal(pr.AS, []string{public}) || len(pr.Scopes) != 4 ||
				!slices.Equal(pr.Bearer, []string{"header"}) || pr.Name == "" {
				t.Fatalf("resource metadata %s = %s", res, rec.Body)
			}
		}
		if rec := e.post("/oauth/register", nil); (rec.Code == http.StatusNotFound) == dcr {
			t.Fatalf("DCR %v: POST /oauth/register = %d", dcr, rec.Code)
		}
	}

	// A 401 from a protected resource names its metadata.
	e := newHarness(t, nil)
	for _, res := range []string{public + "/api/v1", public + "/mcp"} {
		mw := auth.Middleware(e.st, auth.Options{Resource: res, MetadataURL: e.srv.MetadataURL(res)}, slog.New(slog.DiscardHandler))
		rec := httptest.NewRecorder()
		mw(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
		want := `resource_metadata="` + e.srv.MetadataURL(res) + `", scope="read"`
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Header().Get("WWW-Authenticate"), want) {
			t.Fatalf("401 from %s: %d %q, want %s", res, rec.Code, rec.Header().Get("WWW-Authenticate"), want)
		}
	}
}

// TestAuthorizeAndConsent fails if a request without S256 PKCE, with an
// unregistered or inexact redirect URI, or with a foreign resource is
// accepted; if an error before redirect-URI validation redirects; if a
// client ID metadata document that redirects, exceeds 64 KiB, times out,
// resolves to a private address or names another client_id is accepted;
// if approval issues a scope the user narrowed away or outside
// GrantableScopes; if the redirect lacks iss or state; or if a bearer
// token can approve.
func TestAuthorizeAndConsent(t *testing.T) {
	e := newHarness(t, nil)

	// Errors before the redirect URI is validated stay on Hello's page.
	for name, over := range map[string]map[string]string{
		"no client":          {"client_id": ""},
		"unregistered":       {"client_id": "some-client"},
		"no redirect":        {"redirect_uri": ""},
		"other redirect":     {"redirect_uri": "https://evil.example/cb"},
		"redirect path":      {"redirect_uri": "http://127.0.0.1/callback/x"},
		"redirect not exact": {"redirect_uri": "http://127.0.0.1/callback?x=1"},
	} {
		rec := e.get(e.authorizeQuery(over))
		if rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" ||
			!strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Errorf("%s: %d Location=%q, want the error page", name, rec.Code, rec.Header().Get("Location"))
		}
	}

	// After it, errors go back to the client with state and iss.
	for name, c := range map[string]struct {
		over map[string]string
		code string
	}{
		"no pkce":       {map[string]string{"code_challenge": "", "code_challenge_method": ""}, "invalid_request"},
		"plain pkce":    {map[string]string{"code_challenge_method": "plain"}, "invalid_request"},
		"short pkce":    {map[string]string{"code_challenge": "abc"}, "invalid_request"},
		"token type":    {map[string]string{"response_type": "token"}, "unsupported_response_type"},
		"unknown scope": {map[string]string{"scope": "read root"}, "invalid_scope"},
		"session scope": {map[string]string{"scope": "read session"}, "invalid_scope"},
		"foreign res":   {map[string]string{"resource": "https://other.example/mcp"}, "invalid_target"},
	} {
		rec := e.get(e.authorizeQuery(c.over))
		loc, _ := url.Parse(rec.Header().Get("Location"))
		if rec.Code != http.StatusFound || loc == nil || !strings.HasPrefix(loc.String(), e.redirect+"?") {
			t.Errorf("%s: %d %q, want a redirect to the client", name, rec.Code, rec.Header().Get("Location"))
			continue
		}
		if q := loc.Query(); q.Get("error") != c.code || q.Get("state") != "st-1" || q.Get("iss") != public {
			t.Errorf("%s: redirect %s, want error=%s with state and iss", name, loc, c.code)
		}
	}

	// A loopback redirect may use any port; resource indicators are
	// normalised.
	id := e.requestID(map[string]string{"redirect_uri": "http://127.0.0.1:49152/callback", "resource": "HTTPS://Hello.Example:443/mcp/"})
	v, err := e.srv.Request(context.Background(), id)
	if err != nil || !slices.Equal(v.Resources, []string{public + "/mcp"}) || v.ClientName != "Test Agent" || !v.Verified {
		t.Fatalf("consent view = %+v, %v", v, err)
	}
	if v := e.requestID(map[string]string{"resource": ""}); v == "" {
		t.Fatal("no request id")
	}

	// Consent: only a session approves; scopes narrow, never widen, and
	// stay within the role's.
	id = e.requestID(nil)
	bearer := alice
	bearer.Kind, bearer.Scopes = auth.KindLegacyToken, slices.Clone(auth.AllScopes)
	if _, err := e.srv.Approve(context.Background(), bearer, id, auth.Scopes{auth.ScopeRead}); err == nil {
		t.Fatal("a bearer token approved consent")
	}
	if _, err := e.srv.Deny(context.Background(), bearer, id); err == nil {
		t.Fatal("a bearer token denied consent")
	}
	if _, err := e.srv.Approve(context.Background(), alice, id, auth.Scopes{auth.ScopeAdmin}); err == nil {
		t.Fatal("approval widened read write to admin")
	}
	operator := alice
	operator.Role = auth.RoleOperator
	id2 := e.requestID(map[string]string{"scope": "read secrets"})
	if _, err := e.srv.Approve(context.Background(), operator, id2, auth.Scopes{auth.ScopeRead, auth.ScopeSecrets}); err == nil {
		t.Fatal("an operator granted secrets")
	}
	redirect, err := e.srv.Approve(context.Background(), alice, id, auth.Scopes{auth.ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(redirect)
	if q := u.Query(); q.Get("code") == "" || q.Get("state") != "st-1" || q.Get("iss") != public || !strings.HasPrefix(redirect, e.redirect+"?") {
		t.Fatalf("approve redirect = %s", redirect)
	}
	if _, err := e.srv.Approve(context.Background(), alice, id, auth.Scopes{auth.ScopeRead}); err == nil {
		t.Fatal("a request was approved twice")
	}
	rec := e.exchange(u.Query().Get("code"), nil)
	if body := jsonBody(t, rec); rec.Code != http.StatusOK || body["scope"] != "read" {
		t.Fatalf("narrowed token = %d %v, want scope read", rec.Code, body)
	}

	redirect, err = e.srv.Deny(context.Background(), alice, id2)
	if err != nil {
		t.Fatal(err)
	}
	if u, _ := url.Parse(redirect); u.Query().Get("error") != "access_denied" || u.Query().Get("state") != "st-1" || u.Query().Get("iss") != public {
		t.Fatalf("deny redirect = %s", redirect)
	}
	if !slices.ContainsFunc(e.st.audit, func(s string) bool { return strings.HasPrefix(s, "user:alice approve") }) ||
		!slices.ContainsFunc(e.st.audit, func(s string) bool { return strings.HasPrefix(s, "user:alice deny") }) {
		t.Fatalf("consent audit rows missing: %v", e.st.audit)
	}
}

// TestCIMDFetch fails if a client ID metadata document that redirects,
// exceeds 64 KiB, times out, resolves to a private address, names another
// client_id or a confidential auth method is accepted, if a good one is
// refused, or if the cache ignores Cache-Control or exceeds 24 h.
func TestCIMDFetch(t *testing.T) {
	var (
		mu   sync.Mutex
		mode string
	)
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		m := mode
		mu.Unlock()
		id := "https://" + r.Host + r.URL.Path
		doc := map[string]any{"client_id": id, "client_name": "Agent", "redirect_uris": []string{"http://127.0.0.1/cb"}}
		switch m {
		case "redirect":
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
			return
		case "huge":
			doc["client_name"] = strings.Repeat("x", 70<<10)
		case "slow":
			time.Sleep(time.Second)
		case "other":
			doc["client_id"] = "https://evil.example/client.json"
		case "secret":
			doc["token_endpoint_auth_method"] = "client_secret_basic"
		case "badredirect":
			doc["redirect_uris"] = []string{"http://evil.example/cb"}
		}
		_ = json.NewEncoder(w).Encode(doc)
	}))
	defer ts.Close()
	id := ts.URL + "/client.json"
	now := time.Now()

	allow := newCIMDFetcher(ts.Client(), true, nil)
	allow.client.Timeout = 300 * time.Millisecond
	for _, m := range []string{"redirect", "huge", "slow", "other", "secret", "badredirect"} {
		mu.Lock()
		mode = m
		mu.Unlock()
		if _, err := allow.fetch(context.Background(), id, now); err == nil {
			t.Errorf("%s document accepted", m)
		}
	}
	mu.Lock()
	mode = ""
	mu.Unlock()
	c, err := allow.fetch(context.Background(), id, now)
	if err != nil || c.Name != "Agent" || c.Kind != ClientCIMD {
		t.Fatalf("good document = %+v, %v", c, err)
	}
	for _, bad := range []string{"http://x.example/c.json", "https://x.example", "https://x.example/", "https://u@x.example/c"} {
		if _, err := allow.fetch(context.Background(), bad, now); err == nil {
			t.Errorf("client_id %s accepted", bad)
		}
	}

	// The guarded dialer refuses loopback (the test server's address).
	guarded := newCIMDFetcher(nil, false, nil)
	tr := guarded.client.Transport.(*http.Transport)
	tr.TLSClientConfig = ts.Client().Transport.(*http.Transport).TLSClientConfig
	if _, err := guarded.fetch(context.Background(), id, now); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("loopback document = %v, want refused as private", err)
	}
	for addr, public := range map[string]bool{
		"10.1.2.3": false, "192.168.1.1": false, "172.16.0.1": false, "127.0.0.1": false, "169.254.1.1": false,
		"::1": false, "fe80::1": false, "fd00::1": false, "100.64.0.1": false, "0.0.0.0": false,
		"::ffff:10.0.0.1": false, "93.184.216.34": true, "2606:4700::1": true,
	} {
		if got := publicAddr(mustAddr(t, addr)); got != public {
			t.Errorf("publicAddr(%s) = %v", addr, got)
		}
	}

	h := http.Header{}
	for cc, want := range map[string]time.Duration{
		"max-age=60": time.Minute, "no-store": 0, "no-cache, max-age=60": 0,
		"public, max-age=999999": 24 * time.Hour, "": 0,
	} {
		h.Set("Cache-Control", cc)
		if got := cacheFor(h, now); got != want {
			t.Errorf("cacheFor(%q) = %v, want %v", cc, got, want)
		}
	}
	h.Del("Cache-Control")
	h.Set("Expires", now.Add(2*time.Hour).UTC().Format(http.TimeFormat))
	if got := cacheFor(h, now); got < time.Hour || got > 2*time.Hour {
		t.Errorf("cacheFor(Expires +2h) = %v", got)
	}
}

// TestTokenEndpoint fails if a wrong PKCE verifier, a reused or expired
// code, or a mismatched redirect URI or client is accepted; if a reused
// code does not revoke the grant's tokens; if a refresh token is not
// rotated, a spent one does not revoke the grant, or a refresh widens
// scope; if the idle or absolute refresh limit is not enforced; if a token
// is accepted outside its audience or an MCP-bound token is refused inside
// a replay; or if responses lack no-store.
func TestTokenEndpoint(t *testing.T) {
	e := newHarness(t, nil)
	api, mcp := e.srv.Resources()

	// Wrong verifier, client or redirect: invalid_grant, and the code is
	// still good afterwards for the right request.
	code := e.code(nil, alice, auth.Scopes{auth.ScopeRead, auth.ScopeWrite})
	for name, over := range map[string]map[string]string{
		"verifier":     {"code_verifier": strings.Repeat("a", 50)},
		"client":       {"client_id": "https://other.example/c.json"},
		"redirect":     {"redirect_uri": "http://127.0.0.1/other"},
		"resource":     {"resource": "https://other.example/mcp"},
		"secret":       {"client_secret": "x"},
		"short verify": {"code_verifier": "abc"},
	} {
		rec := e.exchange(code, over)
		if rec.Code < 400 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s mismatch = %d %s (Cache-Control %q)", name, rec.Code, rec.Body, rec.Header().Get("Cache-Control"))
		}
	}
	rec := e.exchange(code, nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("exchange = %d %s", rec.Code, rec.Body)
	}
	tok := jsonBody(t, rec)
	at, rt := tok["access_token"].(string), tok["refresh_token"].(string)
	if !strings.HasPrefix(at, auth.PrefixAccess) || !strings.HasPrefix(rt, auth.PrefixRefresh) ||
		tok["token_type"] != "Bearer" || tok["scope"] != "read write" || tok["expires_in"] != float64(3600) {
		t.Fatalf("token response = %v", tok)
	}
	if !e.admitted(at, api, nil) || !e.admitted(at, mcp, nil) {
		t.Fatal("a token bound to both resources was refused")
	}

	// A second use of the code fails and revokes what the first issued.
	if rec := e.exchange(code, nil); rec.Code != http.StatusBadRequest || jsonBody(t, rec)["error"] != "invalid_grant" {
		t.Fatalf("reused code = %d %s", rec.Code, rec.Body)
	}
	if e.admitted(at, api, nil) {
		t.Fatal("a reused code left its access token working")
	}
	if rec := e.post("/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {e.clientID}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("refresh after code reuse = %d", rec.Code)
	}

	// An expired code is refused.
	code = e.code(nil, alice, auth.Scopes{auth.ScopeRead})
	e.st.mu.Lock()
	for _, r := range e.st.requests {
		if r.codeHash != nil && bytes.Equal(r.codeHash, auth.HashToken(code)) {
			r.codeExpires = time.Now().Add(-time.Second)
		}
	}
	e.st.mu.Unlock()
	if rec := e.exchange(code, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("expired code = %d", rec.Code)
	}

	// Audience: an MCP-bound token works on /mcp and on the API only
	// inside a replay by its own client.
	tok = e.tokens(auth.Scopes{auth.ScopeRead}, map[string]string{"resource": mcp})
	at, rt = tok["access_token"].(string), tok["refresh_token"].(string)
	switch {
	case !e.admitted(at, mcp, nil):
		t.Fatal("an MCP-bound token was refused on /mcp")
	case e.admitted(at, api, nil):
		t.Fatal("an MCP-bound token was admitted directly on /api/v1")
	case !e.admitted(at, api, &auth.Replay{ClientID: e.clientID}):
		t.Fatal("an MCP-bound token was refused inside its replay")
	case e.admitted(at, api, &auth.Replay{ClientID: "https://other.example/c.json"}):
		t.Fatal("an MCP-bound token was admitted inside another client's replay")
	}

	// Refresh rotates; narrowing is allowed, widening is not.
	refresh := func(rt, scope string) *httptest.ResponseRecorder {
		f := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {e.clientID}}
		if scope != "" {
			f.Set("scope", scope)
		}
		return e.post("/oauth/token", f)
	}
	if rec := refresh(rt, "read write"); rec.Code != http.StatusBadRequest || jsonBody(t, rec)["error"] != "invalid_scope" {
		t.Fatalf("widening refresh = %d %s", rec.Code, rec.Body)
	}
	rec = refresh(rt, "")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("refresh = %d %s", rec.Code, rec.Body)
	}
	next := jsonBody(t, rec)
	rt2, at2 := next["refresh_token"].(string), next["access_token"].(string)
	if rt2 == "" || rt2 == rt || at2 == at {
		t.Fatalf("refresh did not rotate: %v", next)
	}
	if !e.admitted(at2, mcp, nil) || e.admitted(at2, api, nil) {
		t.Fatal("a refreshed token lost or widened its audience")
	}
	// Reusing the spent refresh token revokes the grant.
	if rec := refresh(rt, ""); rec.Code != http.StatusBadRequest || jsonBody(t, rec)["error"] != "invalid_grant" {
		t.Fatalf("spent refresh token = %d %s", rec.Code, rec.Body)
	}
	if e.admitted(at2, mcp, nil) || refresh(rt2, "").Code == http.StatusOK {
		t.Fatal("refresh token reuse left the grant working")
	}
	// Another client cannot use the refresh token.
	tok = e.tokens(auth.Scopes{auth.ScopeRead}, nil)
	if rec := e.post("/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok["refresh_token"].(string)},
		"client_id": {"https://other.example/c.json"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("refresh by another client = %d", rec.Code)
	}

	// Idle and absolute refresh limits.
	idle := newHarness(t, func(o *Options) { o.RefreshIdle = 50 * time.Millisecond })
	tok = idle.tokens(auth.Scopes{auth.ScopeRead}, nil)
	time.Sleep(100 * time.Millisecond)
	if rec := idle.post("/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok["refresh_token"].(string)},
		"client_id": {idle.clientID}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("idle refresh token = %d", rec.Code)
	}
	abs := newHarness(t, func(o *Options) { o.RefreshMax = time.Hour })
	tok = abs.tokens(auth.Scopes{auth.ScopeRead}, nil)
	abs.st.mu.Lock()
	for _, g := range abs.st.grants {
		g.CreatedAt = time.Now().Add(-2 * time.Hour)
	}
	abs.st.mu.Unlock()
	if rec := abs.post("/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok["refresh_token"].(string)},
		"client_id": {abs.clientID}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("refresh past RefreshMax = %d", rec.Code)
	}
	if got := abs.srv.refreshExpiry(time.Now(), time.Now().Add(-59*time.Minute)); time.Until(got) > 2*time.Minute {
		t.Fatalf("refresh expiry %v exceeds the grant's absolute limit", got)
	}

	if rec := e.post("/oauth/token", url.Values{"grant_type": {"password"}, "client_id": {e.clientID}}); rec.Code != http.StatusBadRequest ||
		jsonBody(t, rec)["error"] != "unsupported_grant_type" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("password grant = %d %s", rec.Code, rec.Body)
	}
}

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// throttleLimiters are the limiters TestOAuthThrottle runs against.
func throttleLimiters(t *testing.T) map[string]Limiter {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	out := map[string]Limiter{"memory": NewLimiter(nil, log)}
	down, _ := vkconn.New(context.Background(), vkconn.Config{Addr: "127.0.0.1:1"}, log)
	if down != nil {
		t.Cleanup(down.Close)
		out["valkey down"] = NewLimiter(func() valkey.Client { return down }, log)
	}
	if addr := os.Getenv("HELLO_TEST_VALKEY_ADDR"); addr != "" {
		c, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, ForceSingleClient: true, SelectDB: 12}) // oauth's own DB: other packages FLUSHDB theirs in parallel (TestValkeyDBsPerPackage)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(c.Close)
		out["valkey"] = NewLimiter(func() valkey.Client { return c }, log)
	}
	return out
}

// TestGrantsAndRevocation fails if revoking a refresh token or deleting a
// grant leaves any of its tokens working, a user can see or revoke another
// user's grant without admin, or a revocation writes no audit row.
func TestGrantsAndRevocation(t *testing.T) {
	e := newHarness(t, nil)
	ctx := context.Background()
	api, _ := e.srv.Resources()

	// /oauth/revoke on a refresh token kills the grant.
	tok := e.tokens(auth.Scopes{auth.ScopeRead}, nil)
	if rec := e.post("/oauth/revoke", url.Values{"token": {tok["refresh_token"].(string)}, "client_id": {e.clientID}}); rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d %s", rec.Code, rec.Body)
	}
	if e.admitted(tok["access_token"].(string), api, nil) {
		t.Fatal("revoking the refresh token left the access token working")
	}
	// Unknown tokens are not an error (RFC 7009); an access token alone.
	if rec := e.post("/oauth/revoke", url.Values{"token": {"nope"}, "client_id": {e.clientID}}); rec.Code != http.StatusOK {
		t.Fatalf("revoke unknown = %d", rec.Code)
	}
	tok = e.tokens(auth.Scopes{auth.ScopeRead}, nil)
	e.post("/oauth/revoke", url.Values{"token": {tok["access_token"].(string)}, "client_id": {e.clientID}})
	if e.admitted(tok["access_token"].(string), api, nil) {
		t.Fatal("a revoked access token works")
	}

	// Grants: bob sees only his; he cannot revoke alice's; an admin can.
	bob := auth.Actor{UserID: 2, Username: "bob", Role: auth.RoleViewer, Kind: auth.KindSession,
		Scopes: append(slices.Clone(auth.AllScopes), auth.ScopeSession)}
	aliceTok := e.tokens(auth.Scopes{auth.ScopeRead}, nil)
	redirect, err := e.srv.Approve(ctx, bob, e.requestID(map[string]string{"scope": "read"}), auth.Scopes{auth.ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(redirect)
	if rec := e.exchange(u.Query().Get("code"), nil); rec.Code != http.StatusOK {
		t.Fatalf("bob's exchange = %d", rec.Code)
	}
	bobs, err := e.srv.Grants(ctx, bob)
	if err != nil || len(bobs) != 1 || bobs[0].UserID != 2 {
		t.Fatalf("bob's grants = %+v, %v", bobs, err)
	}
	all, _ := e.srv.Grants(ctx, alice)
	var aliceGrant int64
	for _, g := range all {
		if g.UserID == 1 {
			aliceGrant = g.ID
		}
	}
	if len(all) < 2 || aliceGrant == 0 {
		t.Fatalf("admin grants = %+v", all)
	}
	if err := e.srv.RevokeGrant(ctx, bob, aliceGrant); err == nil {
		t.Fatal("bob revoked alice's grant")
	}
	if !e.admitted(aliceTok["access_token"].(string), api, nil) {
		t.Fatal("alice's token stopped after bob's refused revoke")
	}
	before := len(e.st.audit)
	if err := e.srv.RevokeGrant(ctx, alice, aliceGrant); err != nil {
		t.Fatal(err)
	}
	if e.admitted(aliceTok["access_token"].(string), api, nil) {
		t.Fatal("deleting a grant left its token working")
	}
	if len(e.st.audit) == before || !strings.Contains(e.st.audit[len(e.st.audit)-1], "revoke oauth_grant") {
		t.Fatalf("grant revocation wrote no audit row: %v", e.st.audit[before:])
	}
	if err := e.srv.RevokeGrant(ctx, bob, bobs[0].ID); err != nil {
		t.Fatalf("bob revoking his own grant: %v", err)
	}
}

// TestServiceAccounts (authorization server half) fails if a service
// account holds a scope outside its role, a secret is returned other than
// once or a third live one is allowed, a client-credentials token gets a
// scope outside the account's or a refresh token, or a disabled account or
// revoked secret still works on the next request.
func TestServiceAccounts(t *testing.T) {
	e := newHarness(t, nil)
	ctx := context.Background()
	api, _ := e.srv.Resources()
	if _, err := e.srv.CreateServiceAccount(ctx, alice, NewServiceAccount{Name: "ci", Role: auth.RoleOperator,
		Scopes: auth.Scopes{auth.ScopeRead, auth.ScopeAdmin}, Enabled: true}); err == nil {
		t.Fatal("an operator account was given admin")
	}
	if _, err := e.srv.CreateServiceAccount(ctx, alice, NewServiceAccount{Name: "ci", Role: auth.RoleViewer,
		Scopes: auth.Scopes{auth.ScopeSession}, Enabled: true}); err == nil {
		t.Fatal("a service account was given session")
	}
	sa, err := e.srv.CreateServiceAccount(ctx, alice, NewServiceAccount{Name: "ci", Role: auth.RoleOperator,
		Scopes: auth.Scopes{auth.ScopeRead, auth.ScopeWrite}, Enabled: true})
	if err != nil || !strings.HasPrefix(sa.ID, "hello_sa_") {
		t.Fatalf("create = %+v, %v", sa, err)
	}
	if _, err := e.srv.CreateServiceAccount(ctx, alice, NewServiceAccount{Name: "ci", Role: auth.RoleViewer,
		Scopes: auth.Scopes{auth.ScopeRead}}); err == nil {
		t.Fatal("a duplicate name was accepted")
	}
	s1, sec1, err := e.srv.AddSecret(ctx, alice, sa.ID, nil)
	if err != nil || !strings.HasPrefix(s1, auth.PrefixClientSecret) {
		t.Fatalf("secret = %q, %v", s1, err)
	}
	s2, _, err := e.srv.AddSecret(ctx, alice, sa.ID, nil)
	if err != nil || s2 == s1 {
		t.Fatal("second secret refused or repeated")
	}
	if _, _, err := e.srv.AddSecret(ctx, alice, sa.ID, nil); err == nil {
		t.Fatal("a third live secret was allowed")
	}
	got, _ := e.srv.ServiceAccount(ctx, sa.ID)
	if b, _ := json.Marshal(got); bytes.Contains(b, []byte(s1)) || len(got.Secrets) != 2 {
		t.Fatalf("account read back = %s", b)
	}

	cc := func(id, secret, scope string, basic bool) *httptest.ResponseRecorder {
		f := url.Values{"grant_type": {"client_credentials"}}
		if scope != "" {
			f.Set("scope", scope)
		}
		if basic {
			return e.post("/oauth/token", f, id, secret)
		}
		f.Set("client_id", id)
		f.Set("client_secret", secret)
		return e.post("/oauth/token", f)
	}
	rec := cc(sa.ID, s1, "", true)
	body := jsonBody(t, rec)
	if rec.Code != http.StatusOK || body["refresh_token"] != nil || body["scope"] != "read write" {
		t.Fatalf("client credentials = %d %v", rec.Code, body)
	}
	at := body["access_token"].(string)
	if !e.admitted(at, api, nil) {
		t.Fatal("a client-credentials token was refused")
	}
	if rec := cc(sa.ID, s2, "read", false); rec.Code != http.StatusOK || jsonBody(t, rec)["scope"] != "read" {
		t.Fatalf("client_secret_post with scope read = %d %s", rec.Code, rec.Body)
	}
	if rec := cc(sa.ID, s1, "admin", true); rec.Code != http.StatusBadRequest || jsonBody(t, rec)["error"] != "invalid_scope" {
		t.Fatalf("scope beyond the account = %d %s", rec.Code, rec.Body)
	}
	if rec := cc(sa.ID, "hello_cs_wrong", "", true); rec.Code != http.StatusUnauthorized || jsonBody(t, rec)["error"] != "invalid_client" {
		t.Fatalf("wrong secret = %d %s", rec.Code, rec.Body)
	}

	// Revoking a secret stops it and the tokens issued so far.
	if err := e.srv.RevokeSecret(ctx, alice, sa.ID, sec1.ID); err != nil {
		t.Fatal(err)
	}
	if e.admitted(at, api, nil) || cc(sa.ID, s1, "", true).Code == http.StatusOK {
		t.Fatal("a revoked secret or its token still works")
	}
	// Disabling the account stops its other secret's tokens too.
	at = jsonBody(t, cc(sa.ID, s2, "", true))["access_token"].(string)
	off := false
	if _, err := e.srv.UpdateServiceAccount(ctx, alice, sa.ID, ServiceAccountChange{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if e.admitted(at, api, nil) || cc(sa.ID, s2, "", true).Code == http.StatusOK {
		t.Fatal("a disabled account still works")
	}
	// A role change must still fit the scopes.
	viewer := auth.RoleViewer
	if _, err := e.srv.UpdateServiceAccount(ctx, alice, sa.ID, ServiceAccountChange{Role: &viewer}); err == nil {
		t.Fatal("demoting the account below its scopes was accepted")
	}
	if !slices.ContainsFunc(e.st.audit, func(s string) bool { return strings.HasPrefix(s, "service:ci issue") }) {
		t.Fatalf("client-credentials issue not audited as the service: %v", e.st.audit)
	}
}

// TestDynamicRegistration fails if /oauth/register registers a
// confidential client or accepts a non-loopback http redirect, or if a
// registered client is not marked unverified. (Its absence with DCR off is
// in TestOAuthMetadata; the cleanup rules are store.TestOAuthStore.)
func TestDynamicRegistration(t *testing.T) {
	e := newHarness(t, func(o *Options) { o.DCR = true })
	reg := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(body))
		req.RemoteAddr = "192.0.2.1:1234"
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	for name, body := range map[string]string{
		"confidential":  `{"redirect_uris":["https://a.example/cb"],"token_endpoint_auth_method":"client_secret_basic"}`,
		"http":          `{"redirect_uris":["http://a.example/cb"]}`,
		"no redirects":  `{"client_name":"x"}`,
		"custom scheme": `{"redirect_uris":["myapp:/cb"]}`,
		"implicit":      `{"redirect_uris":["https://a.example/cb"],"response_types":["token"]}`,
		"password":      `{"redirect_uris":["https://a.example/cb"],"grant_types":["password"]}`,
		"not json":      `nope`,
	} {
		if rec := reg(body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s registration = %d %s", name, rec.Code, rec.Body)
		}
	}
	rec := reg(`{"redirect_uris":["http://127.0.0.1/callback"],"client_name":"Claude Code"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register = %d %s", rec.Code, rec.Body)
	}
	body := jsonBody(t, rec)
	id, _ := body["client_id"].(string)
	if !strings.HasPrefix(id, "hello_dcr_") || body["token_endpoint_auth_method"] != "none" || body["client_secret"] != nil {
		t.Fatalf("registration = %v", body)
	}
	e.clientID = id
	v, err := e.srv.Request(context.Background(), e.requestID(nil))
	if err != nil || v.Verified || v.ClientName != "Claude Code" {
		t.Fatalf("consent view of a registered client = %+v, %v", v, err)
	}
}

// TestOAuthThrottle fails if the eleventh failed client authentication in
// a minute from one client and IP, or the eleventh registration in an hour
// from one IP, is not 429 with Retry-After — in memory, with Valkey down,
// and on Valkey when HELLO_TEST_VALKEY_ADDR is set.
func TestOAuthThrottle(t *testing.T) {
	for name, lim := range throttleLimiters(t) {
		t.Run(name, func(t *testing.T) {
			e := newHarness(t, func(o *Options) { o.DCR = true; o.Limiter = lim })
			run := strconv.FormatInt(time.Now().UnixNano(), 36)
			client := "hello_sa_throttle_" + run
			for i := 1; i <= 11; i++ {
				rec := e.post("/oauth/token", url.Values{"grant_type": {"client_credentials"}}, client, "hello_cs_wrong")
				want := http.StatusUnauthorized
				if i == 11 {
					want = http.StatusTooManyRequests
				}
				if rec.Code != want {
					t.Fatalf("failed authentication %d = %d, want %d", i, rec.Code, want)
				}
				if i == 11 && rec.Header().Get("Retry-After") == "" {
					t.Fatal("429 without Retry-After")
				}
			}
			for i := 1; i <= 11; i++ {
				req := httptest.NewRequest(http.MethodPost, "/oauth/register",
					strings.NewReader(`{"redirect_uris":["http://127.0.0.1/cb"]}`))
				req.RemoteAddr = "[2001:db8::" + run[len(run)-4:] + "]:40000"
				rec := httptest.NewRecorder()
				e.h.ServeHTTP(rec, req)
				want := http.StatusCreated
				if i == 11 {
					want = http.StatusTooManyRequests
				}
				if rec.Code != want {
					t.Fatalf("registration %d = %d, want %d", i, rec.Code, want)
				}
			}
		})
	}
}

// TestNoSecretsInOAuthLogs fails if a code, verifier, access or refresh
// token, or client secret appears in a log line of a whole flow.
func TestNoSecretsInOAuthLogs(t *testing.T) {
	e := newHarness(t, nil)
	code := e.code(nil, alice, auth.Scopes{auth.ScopeRead})
	tok := jsonBody(t, e.exchange(code, nil))
	rt := tok["refresh_token"].(string)
	next := jsonBody(t, e.post("/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {e.clientID}}))
	e.post("/oauth/revoke", url.Values{"token": {next["refresh_token"].(string)}, "client_id": {e.clientID}})
	e.exchange(code, nil) // reuse: logged as a warning
	sa, _ := e.srv.CreateServiceAccount(context.Background(), alice, NewServiceAccount{Name: "x", Role: auth.RoleViewer, Scopes: auth.Scopes{auth.ScopeRead}, Enabled: true})
	secret, _, _ := e.srv.AddSecret(context.Background(), alice, sa.ID, nil)
	cc := jsonBody(t, e.post("/oauth/token", url.Values{"grant_type": {"client_credentials"}}, sa.ID, secret))
	logs := e.logs.String()
	if !strings.Contains(logs, "oauth token issued") {
		t.Fatalf("no issue log line: %s", logs)
	}
	for name, v := range map[string]any{
		"code": code, "verifier": verifier, "access": tok["access_token"], "refresh": rt,
		"rotated": next["access_token"], "secret": secret, "service token": cc["access_token"],
	} {
		if s, _ := v.(string); s == "" || strings.Contains(logs, s) {
			t.Errorf("%s (%q) appears in the logs", name, s)
		}
	}
}

// TestOAuthMetrics (the OAuth half of spec S-20) fails if a token issue,
// a token failure or a client ID metadata document fetch does not move
// its hello_oauth_* series.
func TestOAuthMetrics(t *testing.T) {
	m := NewMetrics(prometheus.NewRegistry())
	e := newHarness(t, func(o *Options) { o.Metrics = m })
	e.tokens(auth.Scopes{auth.ScopeRead}, nil)
	e.post("/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"nope"}, "client_id": {e.clientID}})
	if got := testutil.ToFloat64(m.TokensIssued.WithLabelValues("authorization_code")); got != 1 {
		t.Errorf("tokens issued = %v", got)
	}
	if got := testutil.ToFloat64(m.TokenFailures.WithLabelValues("invalid_grant")); got != 1 {
		t.Errorf("token failures = %v", got)
	}
	if got := testutil.ToFloat64(m.CIMDFetches.WithLabelValues("ok")); got != 1 {
		t.Errorf("cimd fetches = %v", got)
	}
}
