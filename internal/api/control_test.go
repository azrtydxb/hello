package api

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // RFC 3261 digest HA1 is defined over MD5.
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/valkey-io/valkey-go"
)

const (
	testDomain   = "hello.test"
	testUser     = "alice"
	testPassword = "alice-Password-1"
	sessionTTL   = time.Hour
)

// env is one API under test, on its own scratch database.
type env struct {
	t   *testing.T
	srv *httptest.Server
	db  *sql.DB
	st  *store.Store
	// ext101 and ext102 hold the extensions newPBXEnv created, by number.
	ext101 map[string]any
	ext102 map[string]any
}

// newEnv migrates a scratch database, creates user alice and serves the API.
// It skips without HELLO_TEST_DATABASE_URL.
func newEnv(t *testing.T, live Live) *env {
	t.Helper()
	return newEnvConfig(t, Config{Live: live}, nil)
}

// newEnvConfig is newEnv with more of the API's dependencies: cfg's Store,
// SIPDomain and SessionTTL are filled in, and box seals trunk passwords.
func newEnvConfig(t *testing.T, cfg Config, box *secret.Box) *env {
	t.Helper()
	dsn := os.Getenv("HELLO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HELLO_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", scratchDatabase(t, ctx, dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := migrate.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	st := store.New(db).WithSecretBox(box).WithProv(cfg.Prov.Settings)
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(ctx, "test", testUser, hash); err != nil {
		t.Fatal(err)
	}
	cfg.Store, cfg.SIPDomain, cfg.SessionTTL = st, testDomain, sessionTTL
	if box != nil {
		cfg.ProvStore = st
	}
	srv := httptest.NewServer(Handler(cfg))
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, db: db, st: st}
}

// scratchDatabase creates a throwaway database next to the one dsn names and
// returns its DSN (as test/integration does).
func scratchDatabase(t *testing.T, ctx context.Context, dsn string) string {
	t.Helper()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("hello_api_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop scratch database %s: %v", name, err)
		}
	})
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		t.Fatalf("HELLO_TEST_DATABASE_URL must be a postgres:// URL")
	}
	u.Path = "/" + name
	return u.String()
}

// client is an HTTP client with an optional bearer token and a cookie jar.
type client struct {
	e      *env
	hc     *http.Client
	bearer string
	header http.Header
}

func (e *env) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{e: e, hc: &http.Client{Jar: jar}, header: http.Header{}}
}

type response struct {
	code   int
	body   []byte
	header http.Header
}

func (r response) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("response %d is not a JSON object: %q", r.code, r.body)
	}
	return m
}

func (c *client) do(method, path string, body any) response {
	c.e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			c.e.t.Fatal(err)
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, c.e.srv.URL+path, rd)
	if err != nil {
		c.e.t.Fatal(err)
	}
	for k, v := range c.header {
		req.Header[k] = v
	}
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		c.e.t.Fatal(err)
	}
	return response{code: resp.StatusCode, body: b, header: resp.Header}
}

// must performs a request and fails unless it answers want.
func (c *client) must(want int, method, path string, body any) response {
	c.e.t.Helper()
	r := c.do(method, path, body)
	if r.code != want {
		c.e.t.Fatalf("%s %s = %d %s, want %d", method, path, r.code, r.body, want)
	}
	return r
}

// login returns a client with alice's session.
func (e *env) login() *client {
	e.t.Helper()
	c := e.client()
	c.must(http.StatusNoContent, "POST", "/api/v1/auth/login", map[string]string{"username": testUser, "password": testPassword})
	return c
}

// noLive is empty live state, for tests that do not exercise Valkey.
type noLive struct{}

func (noLive) AllBindings(context.Context) ([]livestate.Binding, error) { return nil, nil }
func (noLive) Calls(context.Context) ([]livestate.Call, error)          { return nil, nil }
func (noLive) DeviceStates(context.Context) ([]livestate.DeviceState, error) {
	return nil, nil
}

func TestAuthRequired(t *testing.T) {
	e := newEnv(t, noLive{})
	ctx := context.Background()

	// An expired session and a revoked token must not authenticate.
	var userID int64
	if err := e.db.QueryRowContext(ctx, `SELECT id FROM users WHERE username = $1`, testUser).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	expired, expiredHash := auth.NewToken()
	if err := e.st.CreateSession(ctx, userID, expiredHash, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	revoked, revokedHash := auth.NewToken()
	tok, err := e.st.CreateToken(ctx, "test", userID, store.NewToken{Name: "revoked"}, revokedHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.DeleteToken(ctx, "test", userID, tok.ID); err != nil {
		t.Fatal(err)
	}
	good, goodHash := auth.NewToken()
	if _, err := e.st.CreateToken(ctx, "test", userID, store.NewToken{Name: "good"}, goodHash); err != nil {
		t.Fatal(err)
	}

	srvURL, _ := url.Parse(e.srv.URL)
	anonymous := map[string]func() *client{
		"no credentials": e.client,
		"unknown bearer": func() *client { c := e.client(); c.bearer = "not-a-token"; return c },
		"revoked bearer": func() *client { c := e.client(); c.bearer = revoked; return c },
		"unknown cookie": func() *client {
			c := e.client()
			c.hc.Jar.SetCookies(srvURL, []*http.Cookie{{Name: auth.SessionCookie, Value: "forged"}})
			return c
		},
		"expired cookie": func() *client {
			c := e.client()
			c.hc.Jar.SetCookies(srvURL, []*http.Cookie{{Name: auth.SessionCookie, Value: expired}})
			return c
		},
		"basic auth": func() *client { c := e.client(); c.header.Set("Authorization", "Basic YWxpY2U6eA=="); return c },
	}
	authed := e.client()
	authed.bearer = good

	for _, op := range documentedOps(t) {
		method, path, _ := strings.Cut(op, " ")
		path = concrete(path)
		if want, public := publicOps[op]; public {
			if r := e.client().do(method, path, nil); r.code != want {
				t.Errorf("public %s = %d, want %d", op, r.code, want)
			}
			continue
		}
		for name, mk := range anonymous {
			r := mk().do(method, path, nil)
			if r.code != http.StatusUnauthorized {
				t.Errorf("%s with %s = %d, want 401", op, name, r.code)
				continue
			}
			if r.json(t)["error"].(map[string]any)["code"] != "unauthorized" {
				t.Errorf("%s with %s: body %s", op, name, r.body)
			}
		}
		if method == "DELETE" || strings.HasSuffix(path, "/logout") {
			continue // keep the good token and its state for the other routes
		}
		if r := authed.do(method, path, nil); r.code == http.StatusUnauthorized {
			t.Errorf("%s with a valid token = 401", op)
		}
	}
}

func TestLoginSession(t *testing.T) {
	e := newEnv(t, noLive{})
	ctx := context.Background()

	for name, body := range map[string]map[string]string{
		"wrong password": {"username": testUser, "password": "nope"},
		"unknown user":   {"username": "mallory", "password": testPassword},
	} {
		r := e.client().do("POST", "/api/v1/auth/login", body)
		if r.code != http.StatusUnauthorized || r.header.Get("Set-Cookie") != "" {
			t.Fatalf("%s: login = %d, Set-Cookie %q; want 401 without a cookie", name, r.code, r.header.Get("Set-Cookie"))
		}
	}

	c := e.client()
	r := c.must(http.StatusNoContent, "POST", "/api/v1/auth/login", map[string]string{"username": testUser, "password": testPassword})
	cookies := (&http.Response{Header: r.header}).Cookies()
	if len(cookies) != 1 || cookies[0].Name != auth.SessionCookie {
		t.Fatalf("login cookies = %v, want one %s", cookies, auth.SessionCookie)
	}
	ck := cookies[0]
	if !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/" || ck.Secure {
		t.Fatalf("cookie HttpOnly=%v SameSite=%v Path=%q Secure=%v; want HttpOnly, Strict, /, not Secure over HTTP",
			ck.HttpOnly, ck.SameSite, ck.Path, ck.Secure)
	}
	if raw, err := base64.RawURLEncoding.DecodeString(ck.Value); err != nil || len(raw) != 32 {
		t.Fatalf("session value is not 32 random bytes: %v", err)
	}
	if got := c.must(http.StatusOK, "GET", "/api/v1/auth/me", nil).json(t)["username"]; got != testUser {
		t.Fatalf("me = %v, want %s", got, testUser)
	}

	// Only the SHA-256 of the cookie value is stored, expiring after the TTL.
	var (
		stored  []byte
		expires time.Time
		n       int
	)
	if err := e.db.QueryRowContext(ctx, `SELECT token_hash, expires_at, count(*) OVER () FROM sessions`).Scan(&stored, &expires, &n); err != nil {
		t.Fatal(err)
	}
	if want := sha256.Sum256([]byte(ck.Value)); n != 1 || !bytes.Equal(stored, want[:]) {
		t.Fatalf("sessions: %d rows, hash %x; want one row holding sha256 of the cookie", n, stored)
	}
	if d := time.Until(expires); d < sessionTTL-time.Minute || d > sessionTTL+time.Minute {
		t.Fatalf("session expires in %v, want about %v", d, sessionTTL)
	}
	var plainRows int
	if err := e.db.QueryRowContext(ctx, `SELECT count(*) FROM sessions s WHERE position($1 in row_to_json(s)::text) > 0`, ck.Value).Scan(&plainRows); err != nil || plainRows != 0 {
		t.Fatalf("session plaintext found in %d rows (%v)", plainRows, err)
	}

	// Behind an HTTPS proxy the cookie is Secure.
	sc := e.client()
	sc.header.Set("X-Forwarded-Proto", "https")
	r = sc.must(http.StatusNoContent, "POST", "/api/v1/auth/login", map[string]string{"username": testUser, "password": testPassword})
	if cs := (&http.Response{Header: r.header}).Cookies(); len(cs) != 1 || !cs[0].Secure {
		t.Fatalf("HTTPS login cookie = %v, want Secure", cs)
	}

	// Logout ends the session: the cookie no longer authenticates even if
	// replayed.
	c.must(http.StatusNoContent, "POST", "/api/v1/auth/logout", nil)
	replay := e.client()
	srvURL, _ := url.Parse(e.srv.URL)
	replay.hc.Jar.SetCookies(srvURL, []*http.Cookie{{Name: auth.SessionCookie, Value: ck.Value}})
	replay.must(http.StatusUnauthorized, "GET", "/api/v1/auth/me", nil)
	if err := e.db.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE token_hash = $1`, stored).Scan(&n); err != nil || n != 0 {
		t.Fatalf("session rows after logout = %d (%v), want 0", n, err)
	}

	// Expired sessions are pruned.
	if _, err := e.db.ExecContext(ctx, `UPDATE sessions SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if pruned, err := e.st.PruneSessions(ctx); err != nil || pruned != 1 {
		t.Fatalf("pruned %d (%v), want the HTTPS session", pruned, err)
	}
}

func TestAPITokenHashed(t *testing.T) {
	e := newEnv(t, noLive{})
	ctx := context.Background()
	c := e.login()

	created := c.must(http.StatusCreated, "POST", "/api/v1/tokens", map[string]string{"name": "ci"}).json(t)
	plain, _ := created["token"].(string)
	rest, prefixed := strings.CutPrefix(plain, auth.PrefixPersonal)
	if raw, err := base64.RawURLEncoding.DecodeString(rest); !prefixed || err != nil || len(raw) != 32 {
		t.Fatalf("token %q is not hello_pat_ and 32 random bytes", plain)
	}
	for _, k := range []string{"id", "name", "createdAt", "lastUsedAt"} {
		if _, ok := created[k]; !ok {
			t.Fatalf("create response missing %q: %v", k, created)
		}
	}

	// Only the SHA-256 is stored; the plaintext is nowhere in the table.
	var stored []byte
	if err := e.db.QueryRowContext(ctx, `SELECT token_hash FROM api_tokens`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if want := sha256.Sum256([]byte(plain)); !bytes.Equal(stored, want[:]) {
		t.Fatalf("stored hash %x is not sha256 of the token", stored)
	}
	var plainRows int
	if err := e.db.QueryRowContext(ctx, `SELECT count(*) FROM api_tokens t WHERE position($1 in row_to_json(t)::text) > 0`, plain).Scan(&plainRows); err != nil || plainRows != 0 {
		t.Fatalf("token plaintext found in %d rows (%v)", plainRows, err)
	}

	// The token authenticates as a bearer and records its use; listing never
	// shows it again.
	b := e.client()
	b.bearer = plain
	if got := b.must(http.StatusOK, "GET", "/api/v1/auth/me", nil).json(t)["username"]; got != testUser {
		t.Fatalf("me via token = %v", got)
	}
	l := c.must(http.StatusOK, "GET", "/api/v1/tokens", nil)
	if bytes.Contains(l.body, []byte(plain)) || bytes.Contains(l.body, []byte(`"token"`)) {
		t.Fatalf("token list reveals the token: %s", l.body)
	}
	items := l.json(t)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["lastUsedAt"] == nil {
		t.Fatalf("token list = %s, want one token with lastUsedAt set", l.body)
	}

	// Revoking it stops it working; revoking again is 404.
	id := fmt.Sprint(created["id"])
	c.must(http.StatusNoContent, "DELETE", "/api/v1/tokens/"+id, nil)
	b.must(http.StatusUnauthorized, "GET", "/api/v1/auth/me", nil)
	c.must(http.StatusNotFound, "DELETE", "/api/v1/tokens/"+id, nil)
	c.must(http.StatusBadRequest, "POST", "/api/v1/tokens", map[string]string{"name": ""})
}

func TestDeviceSecretShownOnce(t *testing.T) {
	e := newEnv(t, noLive{})
	ctx := context.Background()
	c := e.login()

	ext := c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "101", "name": "Reception"}).json(t)
	extID := ext["id"]
	c.must(http.StatusConflict, "POST", "/api/v1/extensions", map[string]string{"number": "101", "name": "Duplicate"})

	created := c.must(http.StatusCreated, "POST", "/api/v1/devices", map[string]any{"extensionId": extID, "sipUsername": "101-desk"}).json(t)
	secret, _ := created["secret"].(string)
	if raw, err := base64.RawURLEncoding.DecodeString(secret); err != nil || len(raw) != 24 {
		t.Fatalf("secret %q is not 24 random bytes base64url", secret)
	}
	if created["enabled"] != true || created["sipUsername"] != "101-desk" || created["extensionId"] != extID || created["sipDomain"] != testDomain {
		t.Fatalf("created device = %v", created)
	}
	id := fmt.Sprint(created["id"])
	c.must(http.StatusConflict, "POST", "/api/v1/devices", map[string]any{"extensionId": extID, "sipUsername": "101-desk"})

	checkHidden := func(secret string) {
		t.Helper()
		for _, path := range []string{"/api/v1/devices/" + id, "/api/v1/devices"} {
			r := c.must(http.StatusOK, "GET", path, nil)
			if bytes.Contains(r.body, []byte(secret)) || bytes.Contains(r.body, []byte(`"secret"`)) {
				t.Fatalf("GET %s reveals the secret: %s", path, r.body)
			}
		}
		r := c.must(http.StatusOK, "PATCH", "/api/v1/devices/"+id, map[string]any{"enabled": true})
		if bytes.Contains(r.body, []byte(`"secret"`)) {
			t.Fatalf("PATCH reveals the secret: %s", r.body)
		}
		// Stored: HA1 values for the configured realm, never the secret.
		var realm, ha1MD5, ha1SHA string
		if err := e.db.QueryRowContext(ctx, `SELECT realm, ha1_md5, ha1_sha256 FROM devices WHERE id = $1`, id).Scan(&realm, &ha1MD5, &ha1SHA); err != nil {
			t.Fatal(err)
		}
		in := []byte("101-desk:" + testDomain + ":" + secret)
		m, s := md5.Sum(in), sha256.Sum256(in) //nolint:gosec // digest HA1
		if realm != testDomain || ha1MD5 != hex.EncodeToString(m[:]) || ha1SHA != hex.EncodeToString(s[:]) {
			t.Fatalf("stored realm %q HA1 %s / %s do not match the secret", realm, ha1MD5, ha1SHA)
		}
		var n int
		if err := e.db.QueryRowContext(ctx, `
			SELECT (SELECT count(*) FROM devices d WHERE position($1 in row_to_json(d)::text) > 0)
			     + (SELECT count(*) FROM audit_events a WHERE position($1 in row_to_json(a)::text) > 0)`, secret).Scan(&n); err != nil || n != 0 {
			t.Fatalf("secret plaintext stored in %d rows (%v)", n, err)
		}
	}
	checkHidden(secret)

	rotated := c.must(http.StatusOK, "POST", "/api/v1/devices/"+id+"/rotate-secret", nil).json(t)
	newSecret, _ := rotated["secret"].(string)
	if newSecret == "" || newSecret == secret || fmt.Sprint(rotated["id"]) != id || rotated["sipDomain"] != testDomain {
		t.Fatalf("rotate = %v, want the device with a new secret", rotated)
	}
	checkHidden(newSecret)
	c.must(http.StatusNotFound, "POST", "/api/v1/devices/999999/rotate-secret", nil)
}

func TestValidation(t *testing.T) {
	e := newEnv(t, noLive{})
	c := e.login()
	ext := c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "200", "name": "Sales"}).json(t)
	extPath := fmt.Sprintf("/api/v1/extensions/%v", ext["id"])
	dev := c.must(http.StatusCreated, "POST", "/api/v1/devices", map[string]any{"extensionId": ext["id"], "sipUsername": "200.a", "enabled": false}).json(t)
	if dev["enabled"] != false {
		t.Fatalf("enabled=false ignored: %v", dev)
	}
	devPath := fmt.Sprintf("/api/v1/devices/%v", dev["id"])

	bad := []struct {
		method, path string
		body         any
		want         int
	}{
		{"POST", "/api/v1/extensions", map[string]string{"number": "1", "name": "x"}, 400},
		{"POST", "/api/v1/extensions", map[string]string{"number": "12345678901", "name": "x"}, 400},
		{"POST", "/api/v1/extensions", map[string]string{"number": "12a", "name": "x"}, 400},
		{"POST", "/api/v1/extensions", map[string]string{"number": "300", "name": "  "}, 400},
		{"POST", "/api/v1/extensions", map[string]string{"number": "300", "name": strings.Repeat("n", 101)}, 400},
		{"POST", "/api/v1/extensions", map[string]any{"number": "300", "name": "x", "extra": 1}, 400},
		{"POST", "/api/v1/extensions", `{"number":"300","name":"x"} {}`, 400},
		{"POST", "/api/v1/extensions", `{"number":"300",`, 400},
		{"POST", "/api/v1/extensions", `{"number":300,"name":"x"}`, 400},
		{"POST", "/api/v1/extensions", `{"number":"300","name":"` + strings.Repeat("x", 70<<10) + `"}`, 400},
		{"PATCH", extPath, map[string]any{}, 400},
		{"PATCH", extPath, map[string]any{"number": "x"}, 400},
		{"PATCH", "/api/v1/extensions/999999", map[string]any{"name": "x"}, 404},
		{"PATCH", "/api/v1/extensions/abc", map[string]any{"name": "x"}, 404},
		{"DELETE", "/api/v1/extensions/999999", nil, 404},
		{"GET", "/api/v1/extensions/999999", nil, 404},
		{"POST", "/api/v1/devices", map[string]any{"extensionId": ext["id"], "sipUsername": "has space"}, 400},
		{"POST", "/api/v1/devices", map[string]any{"extensionId": ext["id"], "sipUsername": strings.Repeat("u", 65)}, 400},
		{"POST", "/api/v1/devices", map[string]any{"extensionId": ext["id"], "sipUsername": ""}, 400},
		{"POST", "/api/v1/devices", map[string]any{"sipUsername": "nobody"}, 400},
		{"POST", "/api/v1/devices", map[string]any{"extensionId": 999999, "sipUsername": "orphan"}, 404},
		{"POST", "/api/v1/devices", map[string]any{"extensionId": ext["id"], "sipUsername": "x", "secret": "mine"}, 400},
		{"PATCH", devPath, map[string]any{}, 400},
		{"PATCH", devPath, map[string]any{"extensionId": 999999}, 404},
		{"PATCH", devPath, map[string]any{"sipUsername": "rename"}, 400},
		{"GET", "/api/v1/devices/999999", nil, 404},
		{"DELETE", "/api/v1/devices/999999", nil, 404},
		{"GET", "/api/v1/cdrs?limit=0", nil, 400},
		{"GET", "/api/v1/cdrs?limit=201", nil, 400},
		{"GET", "/api/v1/cdrs?before=x", nil, 400},
	}
	for _, b := range bad {
		r := c.do(b.method, b.path, b.body)
		if r.code != b.want {
			t.Errorf("%s %s %v = %d %s, want %d", b.method, b.path, b.body, r.code, r.body, b.want)
			continue
		}
		code := r.json(t)["error"].(map[string]any)["code"]
		if want := map[int]string{400: "bad_request", 404: "not_found"}[b.want]; code != want {
			t.Errorf("%s %s: error code %v, want %s", b.method, b.path, code, want)
		}
	}

	// Valid updates go through, and deleting the extension cascades.
	if got := c.must(http.StatusOK, "PATCH", extPath, map[string]any{"name": " Sales team "}).json(t); got["name"] != "Sales team" || got["number"] != "200" {
		t.Fatalf("patched extension = %v", got)
	}
	if got := c.must(http.StatusOK, "PATCH", devPath, map[string]any{"enabled": true}).json(t); got["enabled"] != true {
		t.Fatalf("patched device = %v", got)
	}
	c.must(http.StatusNoContent, "DELETE", extPath, nil)
	c.must(http.StatusNotFound, "GET", devPath, nil)
}

func TestCDRPaging(t *testing.T) {
	e := newEnv(t, noLive{})
	c := e.login()
	if got := string(c.must(http.StatusOK, "GET", "/api/v1/cdrs", nil).body); got != "{\"items\":[],\"next\":\"\"}\n" {
		t.Fatalf("empty cdrs = %s", got)
	}
	start := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	for i := range 5 {
		var answer any
		if i%2 == 0 {
			answer = start.Add(time.Duration(i)*time.Minute + 3*time.Second)
		}
		if _, err := e.db.Exec(`INSERT INTO cdrs (correlation_id, sip_call_id, source, destination, start_time, answer_time,
			end_time, duration_ms, billable_ms, sip_node, final_status, termination_side)
			VALUES ($1, $2, '101', '102', $3, $4, $5, 60000, 57000, 'sip-1', 200, 'caller')`,
			fmt.Sprint("c", i), fmt.Sprint("call-", i), start.Add(time.Duration(i)*time.Minute), answer,
			start.Add(time.Duration(i)*time.Minute+time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	next := ""
	for page := 0; ; page++ {
		path := "/api/v1/cdrs?limit=2"
		if next != "" {
			path += "&before=" + next
		}
		m := c.must(http.StatusOK, "GET", path, nil).json(t)
		for _, it := range m["items"].([]any) {
			cdr := it.(map[string]any)
			seen = append(seen, cdr["correlationId"].(string))
			if _, ok := cdr["ringTime"]; ok {
				t.Fatalf("null ringTime not omitted: %v", cdr)
			}
			if _, ok := cdr["answerTime"]; ok != (cdr["correlationId"] != "c1" && cdr["correlationId"] != "c3") {
				t.Fatalf("answerTime presence wrong: %v", cdr)
			}
		}
		next = m["next"].(string)
		if next == "" || page > 5 {
			break
		}
	}
	if got := strings.Join(seen, ","); got != "c4,c3,c2,c1,c0" {
		t.Fatalf("paged CDRs = %s, want newest first without gaps or repeats", got)
	}
}

func TestLiveViews(t *testing.T) {
	addr := os.Getenv("HELLO_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("HELLO_TEST_VALKEY_ADDR not set")
	}
	// Database 3, so the livestate package's FLUSHDB on database 0 cannot
	// race this test.
	vc, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, ForceSingleClient: true, SelectDB: 3})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vc.Close)
	ctx := context.Background()
	if err := vc.Do(ctx, vc.B().Flushdb().Build()).Error(); err != nil {
		t.Fatal(err)
	}
	live := livestate.New(vc)
	e := newEnv(t, live)
	c := e.login()

	if got := string(c.must(http.StatusOK, "GET", "/api/v1/registrations", nil).body); got != "{\"items\":[]}\n" {
		t.Fatalf("empty registrations = %s", got)
	}
	b := livestate.Binding{AOR: "sip:101-desk@" + testDomain, Extension: "101", Device: "101-desk", ContactURI: "sip:101-desk@10.0.0.9:5060",
		Source: "10.0.0.9:5060", Transport: "udp", ReceivedNode: "sip-1", Expires: time.Now().Add(time.Hour), UpdatedAt: time.Now()}
	if err := live.PutBinding(ctx, b); err != nil {
		t.Fatal(err)
	}
	call := livestate.Call{ID: "corr-1", SIPCallID: "abc@10.0.0.9", From: "101", To: "102", State: "ringing", Node: "sip-1", Media: "direct", StartedAt: time.Now()}
	if err := live.PutCall(ctx, call, time.Minute); err != nil {
		t.Fatal(err)
	}
	var regs struct{ Items []livestate.Binding }
	if err := json.Unmarshal(c.must(http.StatusOK, "GET", "/api/v1/registrations", nil).body, &regs); err != nil || len(regs.Items) != 1 || regs.Items[0].ContactURI != b.ContactURI {
		t.Fatalf("registrations = %+v (%v)", regs, err)
	}
	var calls struct{ Items []livestate.Call }
	if err := json.Unmarshal(c.must(http.StatusOK, "GET", "/api/v1/calls", nil).body, &calls); err != nil || len(calls.Items) != 1 || calls.Items[0].ID != "corr-1" {
		t.Fatalf("calls = %+v (%v)", calls, err)
	}
}
