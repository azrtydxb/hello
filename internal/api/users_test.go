package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/oauth"
	"github.com/azrtydxb/hello/internal/store"
)

// roleLookup resolves fixed credentials to actors whose role the test can
// change between requests, as a demotion in the database would.
type roleLookup struct {
	stubStore
	actors map[string]*auth.Actor // by credential hash
}

func (l roleLookup) find(hash []byte) (auth.Actor, error) {
	if a, ok := l.actors[string(hash)]; ok {
		return *a, nil
	}
	return auth.Actor{}, auth.ErrNoCredentials
}

func (l roleLookup) SessionActor(_ context.Context, h []byte) (auth.Actor, error) { return l.find(h) }
func (l roleLookup) TokenActor(_ context.Context, h []byte) (auth.Actor, error)   { return l.find(h) }
func (roleLookup) ListUsers(context.Context) ([]store.User, error)                { return []store.User{}, nil }

// credential is one way of presenting an actor.
type credential struct {
	kind   auth.Kind
	secret string
}

func (c credential) apply(r *http.Request) {
	if c.kind == auth.KindSession {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: c.secret})
		return
	}
	r.Header.Set("Authorization", "Bearer "+c.secret)
}

// serveRecover serves one request and reports the response, or ok=false if
// the handler panicked past the auth layers (the stub store has no data).
func serveRecover(h http.Handler, r *http.Request) (rec *httptest.ResponseRecorder, ok bool) {
	rec = httptest.NewRecorder()
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	h.ServeHTTP(rec, r)
	return rec, true
}

// refusedForRole reports whether a request was refused with 403
// forbidden_role.
func refusedForRole(h http.Handler, method, path string, c credential) bool {
	r := httptest.NewRequest(method, path, nil)
	c.apply(r)
	rec, ok := serveRecover(h, r)
	if !ok || rec.Code != http.StatusForbidden {
		return false
	}
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body.Error.Code == "forbidden_role"
}

// TestRoleEnforcement (spec S-23) fails if, for any route, a role below the
// route's minimum is admitted or the minimum (or above) is refused, with a
// session, a personal token, an OAuth token or a service account (faked
// over every route, then real ones minted through the API); if an
// actor with no role is admitted anywhere; if a demotion does not apply on
// the next request; or if a route's role differs from its x-hello-role.
func TestRoleEnforcement(t *testing.T) {
	kinds := []credential{
		{auth.KindSession, "session"},
		{auth.KindPersonalToken, auth.PrefixPersonal + "x"},
		{auth.KindOAuth, auth.PrefixAccess + "x"},
		{auth.KindService, auth.PrefixAccess + "svc"},
	}
	roles := []auth.Role{auth.RoleViewer, auth.RoleOperator, auth.RoleAdmin, ""}
	l := roleLookup{actors: map[string]*auth.Actor{}}
	as, err := oauth.New(oauth.Options{PublicURL: "https://hello.test"})
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(Config{Store: l, AI: as})

	for _, k := range kinds {
		a := &auth.Actor{UserID: 7, Username: "u", Kind: k.kind, Scopes: append(slices.Clone(auth.AllScopes), auth.ScopeSession)}
		if k.kind == auth.KindOAuth || k.kind == auth.KindService {
			a.ClientID, a.Audience = "client", []string{"https://hello.test/api/v1"}
		}
		if k.kind == auth.KindService {
			a.UserID, a.Username, a.ServiceName = 0, "", "ci"
		}
		l.actors[string(auth.HashToken(k.secret))] = a
		for _, role := range roles {
			a.Role = role
			for _, rt := range Routes() {
				if rt.Public {
					continue
				}
				refused := refusedForRole(h, rt.Method, concrete(rt.Pattern), k)
				if want := !role.AtLeast(rt.Role); refused != want {
					t.Errorf("%s %s as %s %q: refused for role = %v, want %v", rt.Method, rt.Pattern, k.kind, role, refused, want)
				}
			}
		}
	}

	t.Run("demotion applies on the next request", func(t *testing.T) {
		for _, k := range kinds {
			a := l.actors[string(auth.HashToken(k.secret))]
			a.Role = auth.RoleAdmin
			if refusedForRole(h, "GET", "/api/v1/users", k) {
				t.Fatalf("%s: admin refused", k.kind)
			}
			a.Role = auth.RoleOperator
			if !refusedForRole(h, "GET", "/api/v1/users", k) {
				t.Errorf("%s: demoted to operator, still admitted to an admin route", k.kind)
			}
		}
	})

	t.Run("real credentials", func(t *testing.T) {
		e := newAIEnv(t)
		hash, err := auth.HashPassword(testPassword)
		if err != nil {
			t.Fatal(err)
		}
		uid, err := e.st.CreateUser(context.Background(), "test", "dave", hash, auth.RoleAdmin)
		if err != nil {
			t.Fatal(err)
		}
		alice, sess := e.login(), e.client()
		sess.must(http.StatusNoContent, "POST", "/api/v1/auth/login", map[string]string{"username": "dave", "password": testPassword})
		pat, oat := e.client(), e.client()
		pat.bearer = sess.must(http.StatusCreated, "POST", "/api/v1/tokens", map[string]any{"name": "ci"}).json(t)["token"].(string)
		oat.bearer = e.oauthToken(sess, "read write admin")["access_token"].(string)
		sa := alice.must(http.StatusCreated, "POST", "/api/v1/service-accounts",
			map[string]any{"name": "ci", "role": "operator", "scopes": []string{"read", "write"}}).json(t)
		secret := alice.must(http.StatusCreated, "POST", "/api/v1/service-accounts/"+sa["id"].(string)+"/secrets", nil).json(t)["secret"].(string)
		tok := e.oauthPost("/oauth/token", url.Values{"grant_type": {"client_credentials"}}, sa["id"].(string), secret)
		if tok.code != http.StatusOK {
			t.Fatalf("client credentials = %d %s", tok.code, tok.body)
		}
		svc := e.client()
		svc.bearer = tok.json(t)["access_token"].(string)

		creds := map[string]*client{"session": sess, "personal token": pat, "oauth token": oat}
		for _, c := range creds {
			c.must(http.StatusOK, "GET", "/api/v1/users", nil)
		}
		forbiddenRole := func(name string, c *client) {
			t.Helper()
			r := c.do("GET", "/api/v1/users", nil)
			if r.code != http.StatusForbidden || !strings.Contains(string(r.body), "forbidden_role") {
				t.Errorf("%s: GET /api/v1/users = %d %s, want 403 forbidden_role", name, r.code, r.body)
			}
		}
		forbiddenRole("operator service account", svc)
		alice.must(http.StatusOK, "PATCH", "/api/v1/users/"+strconv.FormatInt(uid, 10), map[string]string{"role": "operator"})
		for name, c := range creds {
			forbiddenRole("demoted "+name, c)
		}
	})

	t.Run("route roles match x-hello-role", func(t *testing.T) {
		var doc struct {
			Paths map[string]map[string]struct {
				Role string `json:"x-hello-role"`
			} `json:"paths"`
		}
		if err := json.Unmarshal(OpenAPI(), &doc); err != nil {
			t.Fatal(err)
		}
		for _, rt := range Routes() {
			op, ok := doc.Paths[rt.Pattern][strings.ToLower(rt.Method)]
			if !ok {
				continue // TestVersionAndOpenAPI owns missing operations
			}
			if auth.Role(op.Role) != rt.Role {
				t.Errorf("%s %s: route role %q, x-hello-role %q", rt.Method, rt.Pattern, rt.Role, op.Role)
			}
		}
	})
}

// TestUserRoles (API half; spec S-23) fails if a non-admin can list users
// or change a role, an admin cannot, auth/me omits the caller's role, the
// last admin can be demoted, a role change writes no audit row or does not
// apply to an open session's next request, a bad role or unknown user is
// accepted, or GrantableScopes gives a viewer more than read or an operator
// admin or secrets (which bounds what consent and service accounts can
// hold).
func TestUserRoles(t *testing.T) {
	for role, want := range map[auth.Role]auth.Scopes{
		auth.RoleViewer:   {auth.ScopeRead},
		auth.RoleOperator: {auth.ScopeRead, auth.ScopeWrite},
		auth.RoleAdmin:    auth.AllScopes,
	} {
		if got := auth.GrantableScopes(role); !slices.Equal(got, want) {
			t.Errorf("GrantableScopes(%s) = %v, want %v", role, got, want)
		}
	}
	if auth.GrantableScopes("").Has(auth.ScopeRead) {
		t.Error("an unknown role may grant read")
	}

	e := newEnv(t, nil)
	ctx := context.Background()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for name, role := range map[string]auth.Role{"bob": auth.RoleViewer, "carol": auth.RoleOperator} {
		if ids[name], err = e.st.CreateUser(ctx, "test", name, hash, role); err != nil {
			t.Fatal(err)
		}
	}
	as := func(name string) *client {
		c := e.client()
		c.must(http.StatusNoContent, "POST", "/api/v1/auth/login", map[string]string{"username": name, "password": testPassword})
		return c
	}
	alice, bob, carol := e.login(), as("bob"), as("carol")

	for name, c := range map[string]*client{"bob": bob, "carol": carol} {
		if r := c.do("GET", "/api/v1/users", nil); r.code != http.StatusForbidden {
			t.Errorf("%s lists users: %d %s", name, r.code, r.body)
		}
		if r := c.do("PATCH", "/api/v1/users/"+strconv.FormatInt(ids[name], 10), map[string]string{"role": "admin"}); r.code != http.StatusForbidden {
			t.Errorf("%s promotes themself: %d %s", name, r.code, r.body)
		}
	}
	if got := bob.must(http.StatusOK, "GET", "/api/v1/auth/me", nil).json(t)["role"]; got != "viewer" {
		t.Errorf("bob's me role = %v", got)
	}

	var users struct {
		Items []store.User `json:"items"`
	}
	if err := json.Unmarshal(alice.must(http.StatusOK, "GET", "/api/v1/users", nil).body, &users); err != nil {
		t.Fatal(err)
	}
	got := map[string]auth.Role{}
	for _, u := range users.Items {
		got[u.Username] = u.Role
		if u.Username == testUser {
			ids[testUser] = u.ID
		}
	}
	if got[testUser] != auth.RoleAdmin || got["bob"] != auth.RoleViewer || got["carol"] != auth.RoleOperator {
		t.Fatalf("listed roles = %v", got)
	}

	aliceURL := "/api/v1/users/" + strconv.FormatInt(ids[testUser], 10)
	if r := alice.must(http.StatusConflict, "PATCH", aliceURL, map[string]string{"role": "viewer"}); r.json(t)["error"].(map[string]any)["code"] != "last_admin" {
		t.Errorf("demote last admin: %s", r.body)
	}
	alice.must(http.StatusBadRequest, "PATCH", "/api/v1/users/"+strconv.FormatInt(ids["bob"], 10), map[string]string{"role": "root"})
	alice.must(http.StatusNotFound, "PATCH", "/api/v1/users/999999", map[string]string{"role": "viewer"})

	// Bob's open session is checked against his new role on the next request.
	bob.must(http.StatusForbidden, "POST", "/api/v1/extensions", map[string]string{"number": "150", "name": "x"})
	r := alice.must(http.StatusOK, "PATCH", "/api/v1/users/"+strconv.FormatInt(ids["bob"], 10), map[string]string{"role": "operator"})
	if r.json(t)["role"] != "operator" {
		t.Errorf("PATCH answered %s", r.body)
	}
	if got := bob.must(http.StatusOK, "GET", "/api/v1/auth/me", nil).json(t)["role"]; got != "operator" {
		t.Errorf("bob's role after promotion = %v", got)
	}
	bob.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "150", "name": "x"})
	bob.must(http.StatusForbidden, "GET", "/api/v1/users", nil)

	var n int
	if err := e.db.QueryRowContext(ctx, `
		SELECT count(*) FROM audit_events
		WHERE actor = $1 AND resource = 'user' AND resource_id = $2 AND action = 'set-role:operator'`,
		"user:"+testUser, strconv.FormatInt(ids["bob"], 10)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d audit rows for the role change, want 1", n)
	}
}
