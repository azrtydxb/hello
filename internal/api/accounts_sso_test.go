package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/directory"
	"github.com/azrtydxb/hello/internal/mailer"
	"github.com/azrtydxb/hello/internal/sso"
	"github.com/azrtydxb/hello/internal/sso/ssotest"
)

const ssoGoogleApp = "12345-abc.apps.googleusercontent.com"

// bindDir is the directory engine's LDAP bind login for the SSO service:
// "ldap-pw" is the password of every synced user.
type bindDir struct{ calls int }

func (d *bindDir) BindLogin(_ context.Context, _ int64, _, password string) error {
	d.calls++
	if password != "ldap-pw" {
		return directory.ErrBindRefused
	}
	return nil
}

type ssoEnv struct {
	f   *acctFake
	h   http.Handler
	idp *ssotest.IdP
	m   *sso.Mem
	dir *bindDir
	eng *directory.Engine
	tok *httptest.Server
}

// newSSOEnv is the account fake with single sign-on: Google enabled against
// a fake IdP, alice synced by a Google connector (id g-alice), bob by an AD
// connector with LDAP bind login, and a Google OAuth-admin directory
// connector (id 1) whose consent redeems at a fake token endpoint.
func newSSOEnv(t *testing.T) *ssoEnv {
	t.Helper()
	f, _, _, _ := newAcctFake(t)
	f.synced = map[int64]syncedUser{uAlice: {connector: 7, external: "g-alice"}, uBob: {connector: 8, external: "guid-bob"}}
	f.breakGlass = map[int64]bool{uCarol: true}
	f.actors[hex.EncodeToString(auth.HashToken("pat"))] = auth.Actor{UserID: uDave, Username: "dave", Role: auth.RoleViewer,
		Kind: auth.KindPersonalToken, Scopes: auth.Scopes{auth.ScopeRead, auth.ScopeWrite}}
	idp := ssotest.New(t, ssoGoogleApp, "google-secret")
	m := sso.NewMem()
	m.Users[uAlice] = &sso.MemUser{User: sso.User{ID: uAlice, Username: "alice", Role: "user", Enabled: true, ConnectorID: 7, ConnectorKind: "google", ExternalID: "g-alice"}}
	m.Users[uCarol] = &sso.MemUser{User: sso.User{ID: uCarol, Username: "carol", Role: "admin", Enabled: true, BreakGlass: true}, Email: "carol@example.com"}
	m.Users[uDave] = &sso.MemUser{User: sso.User{ID: uDave, Username: "dave", Role: "viewer", Enabled: true}, Email: "dave@example.com"}
	dir := &bindDir{}
	svc := &sso.Service{Store: m, PublicURL: "https://hello.example", States: &sso.MemStates{}, Throttle: &sso.MemThrottle{},
		Directory: dir, HTTP: idp.Srv.Client(), IssuerFor: func(sso.Provider) string { return idp.GoogleIssuer() }}
	ctx := context.Background()
	if _, err := svc.PutSettings(ctx, "admin", sso.SettingsInput{Providers: []sso.ProviderInput{{Provider: "google", Enabled: ptrTo(true),
		ClientID: ptrTo(ssoGoogleApp), ClientSecret: ptrTo("google-secret"), Domains: &[]string{"example.com"}}}}); err != nil {
		t.Fatal(err)
	}
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		if r.PostForm.Get("code") != "consent-ok" || r.PostForm.Get("code_verifier") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"access_token":"x","refresh_token":"rt","scope":"`+directory.ScopeGoogleUsers+`"}`)
	}))
	t.Cleanup(tok.Close)
	eng := &directory.Engine{Store: directory.NewMem(), HTTP: tok.Client(),
		Drivers: map[string]directory.Driver{directory.KindGoogle: directory.Google{TokenURL: tok.URL}, directory.KindLDAP: directory.LDAP{}}}
	cs := "client-secret"
	if _, err := eng.Create(ctx, "admin", directory.ConnectorInput{Kind: directory.KindGoogle, Name: ptrTo("GW"), Secret: &cs,
		Config: json.RawMessage(`{"authMode":"oauth_admin","clientId":"9-x.apps.googleusercontent.com"}`)}); err != nil {
		t.Fatal(err)
	}
	h := Handler(Config{Store: f, SSO: svc, Directory: eng, Log: slog.New(slog.DiscardHandler), SessionTTL: time.Hour,
		SIPDomain: testDomain, Clients: f, ClientAuth: testClientAuth(),
		SMTPDeliver: func(context.Context, mailer.Settings, string, []byte) error { return nil }})
	return &ssoEnv{f: f, h: h, idp: idp, m: m, dir: dir, eng: eng, tok: tok}
}

func ptrTo[T any](v T) *T { return &v }

// call sends a request as who (a session, "" for none, "pat" for dave's
// personal token) with the cookies given.
func (e *ssoEnv) call(who, method, path string, body any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, rd)
	switch who {
	case "":
	case "pat":
		r.Header.Set("Authorization", "Bearer pat")
	default:
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: who + cookieSuffix})
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func cookieOf(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// TestSSOSignInRoundTrip fails if a synced user does not get the same
// session cookie as a password login through Google, if the callback can be
// replayed, or if the binding cookie is not Lax, HttpOnly and short-lived
// (AC-10, I-27).
func TestSSOSignInRoundTrip(t *testing.T) {
	e := newSSOEnv(t)
	rec := e.call("", "GET", "/api/v1/auth/oidc/providers", nil)
	if got := wantJSON(t, rec, http.StatusOK, "providers"); len(got["items"].([]any)) != 1 {
		t.Fatalf("providers = %v", got)
	}
	rec = e.call("", "GET", "/api/v1/auth/oidc/google/start?next=/calls", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("start = %d %s", rec.Code, rec.Body)
	}
	bind := cookieOf(rec, oidcCookie)
	if bind == nil || !bind.HttpOnly || bind.SameSite != http.SameSiteLaxMode || bind.MaxAge != 600 || bind.Path != "/api/v1/" {
		t.Fatalf("binding cookie = %+v", bind)
	}
	state, code := e.idp.Authorize(t, rec.Header().Get("Location"), map[string]any{"sub": "g-alice", "email": "alice@example.com", "email_verified": true, "hd": "example.com"})
	cb := "/api/v1/auth/oidc/google/callback?" + url.Values{"state": {state}, "code": {code}}.Encode()
	rec = e.call("", "GET", cb, nil, bind)
	sess := cookieOf(rec, auth.SessionCookie)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/calls" || sess == nil || sess.SameSite != http.SameSiteStrictMode || !sess.HttpOnly {
		t.Fatalf("callback = %d %q, session = %+v", rec.Code, rec.Header().Get("Location"), sess)
	}
	if c := cookieOf(rec, oidcCookie); c == nil || c.MaxAge >= 0 {
		t.Fatalf("binding cookie not cleared: %+v", c)
	}
	rec = e.call("", "GET", cb, nil, bind)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login?sso_error=retry") || cookieOf(rec, auth.SessionCookie) != nil {
		t.Fatalf("replay = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	found := false
	for _, a := range e.f.audit {
		found = found || a == "user:alice login-sso:google user "+strconv.Itoa(uAlice)
	}
	if !found {
		t.Fatalf("no login audit in %v", e.f.audit)
	}
}

// TestSSONoTakeoverOfLocalAdmin fails if an ID token with a local admin's
// email signs in as that admin (T-7, AC-10).
func TestSSONoTakeoverOfLocalAdmin(t *testing.T) {
	e := newSSOEnv(t)
	rec := e.call("", "GET", "/api/v1/auth/oidc/google/start", nil)
	state, code := e.idp.Authorize(t, rec.Header().Get("Location"), map[string]any{"sub": "g-attacker", "email": "carol@example.com", "email_verified": true, "hd": "example.com"})
	rec = e.call("", "GET", "/api/v1/auth/oidc/google/callback?"+url.Values{"state": {state}, "code": {code}}.Encode(), nil, cookieOf(rec, oidcCookie))
	if rec.Header().Get("Location") != "/login?sso_error=failed" || cookieOf(rec, auth.SessionCookie) != nil {
		t.Fatalf("callback = %q", rec.Header().Get("Location"))
	}
	if rec := e.call("", "GET", "/api/v1/auth/oidc/entra/start", nil); rec.Header().Get("Location") != "/login?sso_error=unavailable" {
		t.Fatalf("disabled provider start = %q", rec.Header().Get("Location"))
	}
}

// TestSyncedUserPasswordForm fails if a synced user signs in with a local
// password, an AD-synced user cannot sign in by an LDAP bind, or a synced
// user can set a password (I-29, AC-10b).
func TestSyncedUserPasswordForm(t *testing.T) {
	e := newSSOEnv(t)
	login := func(user, pw string) *httptest.ResponseRecorder {
		return e.call("", "POST", "/api/v1/auth/login", map[string]string{"username": user, "password": pw})
	}
	// alice's local hash is still the strong password, and it must not work.
	if rec := login("alice", strongPassword); rec.Code != http.StatusUnauthorized {
		t.Fatalf("synced user with the local password = %d", rec.Code)
	}
	if rec := login("bob", "wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bob with a wrong password = %d", rec.Code)
	}
	if rec := login("bob", "ldap-pw"); rec.Code != http.StatusNoContent || cookieOf(rec, auth.SessionCookie) == nil {
		t.Fatalf("bob by LDAP bind = %d %s", rec.Code, rec.Body)
	}
	rec := e.call("alice", "POST", "/api/v1/auth/password", map[string]string{"currentPassword": strongPassword, "newPassword": "a-new-strong-password-1"})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "managed_by_directory") {
		t.Fatalf("synced user changing the password = %d %s", rec.Code, rec.Body)
	}
}

// TestSSORequiredClosesThePasswordForm fails if, with sso_required on, a
// non-break-glass user uses the password form or a break-glass admin cannot.
func TestSSORequiredClosesThePasswordForm(t *testing.T) {
	e := newSSOEnv(t)
	e.f.ssoReq = true
	if rec := e.call("", "POST", "/api/v1/auth/login", map[string]string{"username": "dave", "password": strongPassword}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("dave = %d", rec.Code)
	}
	if rec := e.call("", "POST", "/api/v1/auth/login", map[string]string{"username": "carol", "password": strongPassword}); rec.Code != http.StatusNoContent {
		t.Fatalf("break-glass carol = %d %s", rec.Code, rec.Body)
	}
}

// TestSSOSettingsRoutes fails if the settings are not admin only, a bad input
// is not a field error, or sso_required can be turned on without a
// break-glass admin.
func TestSSOSettingsRoutes(t *testing.T) {
	e := newSSOEnv(t)
	got := wantJSON(t, e.call("carol", "GET", "/api/v1/auth/sso", nil), http.StatusOK, "get settings")
	if got["redirectUris"].(map[string]any)["google"] != "https://hello.example/api/v1/auth/oidc/google/callback" {
		t.Fatalf("settings = %v", got)
	}
	if rec := e.call("dave", "GET", "/api/v1/auth/sso", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer get = %d", rec.Code)
	}
	if rec := e.call("dave", "PUT", "/api/v1/auth/sso", map[string]any{"ssoRequired": false}); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer put = %d", rec.Code)
	}
	if rec := e.call("carol", "PUT", "/api/v1/auth/sso", map[string]any{"providers": []map[string]any{{"provider": "entra", "tenant": "common"}}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("common tenant = %d %s", rec.Code, rec.Body)
	}
	e.m.Users[uCarol].BreakGlass = false
	if rec := e.call("carol", "PUT", "/api/v1/auth/sso", map[string]any{"ssoRequired": true}); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no_break_glass") {
		t.Fatalf("no break-glass = %d %s", rec.Code, rec.Body)
	}
	e.m.Users[uCarol].BreakGlass = true
	got = wantJSON(t, e.call("carol", "PUT", "/api/v1/auth/sso", map[string]any{"ssoRequired": true}), http.StatusOK, "require sso")
	if got["ssoRequired"] != true {
		t.Fatalf("settings = %v", got)
	}
	got = wantJSON(t, e.call("carol", "POST", "/api/v1/auth/sso/google/test", nil), http.StatusOK, "test provider")
	if got["ok"] != true {
		t.Fatalf("test = %v", got)
	}
	if rec := e.call("carol", "POST", "/api/v1/auth/sso/okta/test", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown provider test = %d", rec.Code)
	}
	if rec := e.call("dave", "POST", "/api/v1/auth/sso/google/test", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer test = %d", rec.Code)
	}
}

// TestIdentityRoutes fails if a user cannot link, list and unlink their own
// identity from a browser session, a token can, or an admin cannot see and
// remove a user's identities.
func TestIdentityRoutes(t *testing.T) {
	e := newSSOEnv(t)
	rec := e.call("dave", "POST", "/api/v1/me/identities/google/link", nil)
	got := wantJSON(t, rec, http.StatusOK, "link")
	// The link response set the binding cookie in dave's browser.
	bind := cookieOf(rec, oidcCookie)
	state, code := e.idp.Authorize(t, got["url"].(string), map[string]any{"sub": "g-dave", "email": "dave@example.com", "email_verified": true, "hd": "example.com"})
	rec = e.call("", "GET", "/api/v1/auth/oidc/google/callback?"+url.Values{"state": {state}, "code": {code}}.Encode(), nil, bind)
	if rec.Header().Get("Location") != "/me/profile?sso_linked=google" {
		t.Fatalf("link callback = %q", rec.Header().Get("Location"))
	}
	got = wantJSON(t, e.call("dave", "GET", "/api/v1/me/identities", nil), http.StatusOK, "my identities")
	if items := got["items"].([]any); len(items) != 1 || items[0].(map[string]any)["subject"] != "g-dave" {
		t.Fatalf("identities = %v", got)
	}
	if rec := e.call("pat", "GET", "/api/v1/me/identities", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("token list = %d", rec.Code)
	}
	if rec := e.call("pat", "POST", "/api/v1/me/identities/google/link", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("token link = %d", rec.Code)
	}
	if rec := e.call("pat", "DELETE", "/api/v1/me/identities/google", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("token unlink = %d", rec.Code)
	}
	if rec := e.call("dave", "POST", "/api/v1/me/identities/entra/link", nil); rec.Code != http.StatusConflict {
		t.Fatalf("link at a disabled provider = %d", rec.Code)
	}
	if rec := e.call("dave", "POST", "/api/v1/me/identities/okta/link", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("link at an unknown provider = %d", rec.Code)
	}
	dave := "/api/v1/users/" + strconv.Itoa(uDave) + "/identities"
	got = wantJSON(t, e.call("carol", "GET", dave, nil), http.StatusOK, "admin list")
	if len(got["items"].([]any)) != 1 {
		t.Fatalf("admin list = %v", got)
	}
	if rec := e.call("dave", "GET", dave, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer admin list = %d", rec.Code)
	}
	if rec := e.call("dave", "DELETE", dave+"/google", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer admin unlink = %d", rec.Code)
	}
	if rec := e.call("dave", "DELETE", "/api/v1/me/identities/google", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("unlink = %d", rec.Code)
	}
	if rec := e.call("dave", "DELETE", "/api/v1/me/identities/google", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("second unlink = %d", rec.Code)
	}
	if rec := e.call("carol", "DELETE", dave+"/google", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("admin unlink of nothing = %d", rec.Code)
	}
	_ = e.m.Link(context.Background(), "t", sso.Identity{UserID: uDave, Provider: "google", Subject: "g-dave"})
	if rec := e.call("carol", "DELETE", dave+"/google", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("admin unlink = %d", rec.Code)
	}
}

// TestDirectoryConsentRoutes fails if the Google consent is not secrets and
// session only, does not store the refresh token on a good callback, or
// accepts a callback without its binding.
func TestDirectoryConsentRoutes(t *testing.T) {
	e := newSSOEnv(t)
	rec := e.call("carol", "POST", "/api/v1/directory/connectors/1/consent", nil)
	got := wantJSON(t, rec, http.StatusOK, "consent")
	bind := cookieOf(rec, oidcCookie)
	pu, _ := url.Parse(got["url"].(string))
	state := pu.Query().Get("state")
	if pu.Query().Get("access_type") != "offline" || bind == nil {
		t.Fatalf("consent url = %v, cookie = %v", got, bind)
	}
	if rec := e.call("dave", "POST", "/api/v1/directory/connectors/1/consent", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer consent = %d", rec.Code)
	}
	if rec := e.call("carol", "POST", "/api/v1/directory/connectors/99/consent", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown connector = %d", rec.Code)
	}
	// A connector of a kind without consent is a field error.
	sa := "x"
	if _, err := e.eng.Create(context.Background(), "admin", directory.ConnectorInput{Kind: directory.KindLDAP, Name: ptrTo("AD"), Secret: &sa,
		Config: json.RawMessage(`{"url":"ldaps://dc.example","bindDn":"cn=svc","baseDn":"dc=example"}`)}); err != nil {
		t.Fatal(err)
	}
	if rec := e.call("carol", "POST", "/api/v1/directory/connectors/2/consent", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("ldap consent = %d %s", rec.Code, rec.Body)
	}
	cb := "/api/v1/directory/oauth/callback?" + url.Values{"state": {state}, "code": {"consent-ok"}}.Encode()
	if rec := e.call("", "GET", cb, nil); !strings.HasPrefix(rec.Header().Get("Location"), "/integrations/directory?consent=failed") {
		t.Fatalf("callback without the binding = %q", rec.Header().Get("Location"))
	}
	// The failed attempt spent the state; start again.
	rec = e.call("carol", "POST", "/api/v1/directory/connectors/1/consent", nil)
	got = wantJSON(t, rec, http.StatusOK, "consent again")
	pu, _ = url.Parse(got["url"].(string))
	cb = "/api/v1/directory/oauth/callback?" + url.Values{"state": {pu.Query().Get("state")}, "code": {"consent-ok"}}.Encode()
	if rec := e.call("", "GET", cb, nil, cookieOf(rec, oidcCookie)); rec.Header().Get("Location") != "/integrations/directory?consent=ok&connector=1" {
		t.Fatalf("callback = %q", rec.Header().Get("Location"))
	}
	if s, _ := e.eng.Store.Secret(context.Background(), 1); !strings.Contains(s, `"refreshToken":"rt"`) {
		t.Fatal("the refresh token was not sealed")
	}
}
