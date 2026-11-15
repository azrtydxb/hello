package sso_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/directory"
	"github.com/azrtydxb/hello/internal/sso"
	"github.com/azrtydxb/hello/internal/sso/ssotest"
)

const (
	tenant    = "11111111-2222-3333-4444-555555555555"
	entraApp  = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	googleApp = "12345-abc.apps.googleusercontent.com"
	secret    = "idp-client-secret"
	oidAnn    = "9f8e7d6c-5b4a-3928-1706-f5e4d3c2b1a0"
)

type fakeDir struct {
	calls int
	err   error
}

func (d *fakeDir) BindLogin(context.Context, int64, string, string) error {
	d.calls++
	return d.err
}

type rig struct {
	t   *testing.T
	idp *ssotest.IdP
	m   *sso.Mem
	s   *sso.Service
	dir *fakeDir
	ctx context.Context
}

func newRig(t *testing.T, provider string) *rig {
	t.Helper()
	clientID := googleApp
	if provider == sso.ProviderEntra {
		clientID = entraApp
	}
	idp := ssotest.New(t, clientID, secret)
	m := sso.NewMem()
	r := &rig{t: t, idp: idp, m: m, dir: &fakeDir{}, ctx: context.Background()}
	r.s = &sso.Service{Store: m, PublicURL: "https://hello.example", States: &sso.MemStates{}, Throttle: &sso.MemThrottle{},
		Directory: r.dir, HTTP: idp.Srv.Client(),
		IssuerFor: func(p sso.Provider) string {
			if p.Provider == sso.ProviderEntra {
				return idp.EntraIssuer(p.Tenant)
			}
			return idp.GoogleIssuer()
		}}
	// A break-glass admin, a local admin and a local user, who must never be
	// matched by email (T-7).
	m.Users[1] = &sso.MemUser{User: sso.User{ID: 1, Username: "root", Role: "admin", Enabled: true, BreakGlass: true}, Email: "root@example.com"}
	m.Users[2] = &sso.MemUser{User: sso.User{ID: 2, Username: "boss", Role: "admin", Enabled: true}, Email: "boss@example.com"}
	m.Users[3] = &sso.MemUser{User: sso.User{ID: 3, Username: "local", Role: "user", Enabled: true}, Email: "local@example.com"}
	// Synced users: Ann by Google (id g-ann) and Entra (oid), Lee by LDAP.
	m.Users[10] = &sso.MemUser{User: sso.User{ID: 10, Username: "ann", Role: "user", Enabled: true, ConnectorID: 7, ConnectorKind: provider,
		ConnectorTenant: tenant, ExternalID: map[string]string{sso.ProviderGoogle: "g-ann", sso.ProviderEntra: oidAnn}[provider]}}
	m.Users[11] = &sso.MemUser{User: sso.User{ID: 11, Username: "lee", Role: "user", Enabled: true, ConnectorID: 8, ConnectorKind: "ldap", ExternalID: "guid-lee"},
		Email: "lee@example.com"}
	m.Users[12] = &sso.MemUser{User: sso.User{ID: 12, Username: "chief", Role: "admin", Enabled: true, ConnectorID: 8, ConnectorKind: "ldap", ExternalID: "guid-chief"},
		Email: "chief@example.com"}
	in := sso.ProviderInput{Provider: provider, Enabled: ptr(true), ClientID: ptr(clientID), ClientSecret: ptr(secret)}
	if provider == sso.ProviderEntra {
		in.Tenant = ptr(tenant)
	} else {
		in.Domains = &[]string{"example.com"}
	}
	if _, err := r.s.PutSettings(r.ctx, "admin", sso.SettingsInput{Providers: []sso.ProviderInput{in}}); err != nil {
		t.Fatal(err)
	}
	return r
}

func ptr[T any](v T) *T { return &v }

// start begins a sign-in and returns the IdP URL and the binding cookie.
func (r *rig) start(provider string, userID int64) (string, string) {
	r.t.Helper()
	u, binding, err := r.s.Start(r.ctx, provider, userID, "/calls")
	if err != nil {
		r.t.Fatal(err)
	}
	return u, binding
}

func (r *rig) login(provider string, claims map[string]any) (sso.Outcome, error) {
	r.t.Helper()
	u, binding := r.start(provider, 0)
	state, code := r.idp.Authorize(r.t, u, claims)
	return r.s.Callback(r.ctx, provider, url.Values{"state": {state}, "code": {code}}, binding)
}

func googleClaims(sub, email string) map[string]any {
	return map[string]any{"sub": sub, "email": email, "email_verified": true, "hd": "example.com"}
}

func entraClaims(oid string) map[string]any {
	return map[string]any{"sub": "pairwise-sub", "oid": oid, "tid": tenant, "preferred_username": "ann@example.com"}
}

// TestGoogleSyncedUserSignsIn fails if a Google-synced user (Directory API id
// = OIDC sub) does not sign in, is not linked on the first sign-in, or is not
// found through the link on the next one (AC-10).
func TestGoogleSyncedUserSignsIn(t *testing.T) {
	r := newRig(t, sso.ProviderGoogle)
	out, err := r.login(sso.ProviderGoogle, googleClaims("g-ann", "ann@example.com"))
	if err != nil || out.UserID != 10 || !out.Linked || out.Next != "/calls" {
		t.Fatalf("first sign-in = %+v, %v", out, err)
	}
	out, err = r.login(sso.ProviderGoogle, googleClaims("g-ann", "ann@example.com"))
	if err != nil || out.UserID != 10 || out.Linked {
		t.Fatalf("second sign-in = %+v, %v", out, err)
	}
}

// TestEntraMatchesByObjectID fails if the Entra sign-in does not key the
// identity on oid (sub is pairwise), or accepts another tenant.
func TestEntraMatchesByObjectID(t *testing.T) {
	r := newRig(t, sso.ProviderEntra)
	out, err := r.login(sso.ProviderEntra, entraClaims(oidAnn))
	if err != nil || out.UserID != 10 {
		t.Fatalf("sign-in = %+v, %v", out, err)
	}
	ids, _ := r.m.ListIdentities(r.ctx, 10)
	if len(ids) != 1 || ids[0].Subject != oidAnn || ids[0].Tenant != tenant {
		t.Fatalf("identities = %+v", ids)
	}
	c := entraClaims(oidAnn)
	c["tid"] = "99999999-2222-3333-4444-555555555555"
	if _, err := r.login(sso.ProviderEntra, c); !errors.Is(err, sso.ErrToken) {
		t.Fatalf("foreign tenant: err = %v, want ErrToken", err)
	}
}

// TestStateIsSingleUseAndBound fails if a callback can be replayed, or
// completed by a browser without the binding cookie (T-6).
func TestStateIsSingleUseAndBound(t *testing.T) {
	r := newRig(t, sso.ProviderGoogle)
	u, binding := r.start(sso.ProviderGoogle, 0)
	state, code := r.idp.Authorize(t, u, googleClaims("g-ann", "ann@example.com"))
	q := url.Values{"state": {state}, "code": {code}}
	if _, err := r.s.Callback(r.ctx, sso.ProviderGoogle, q, "another-browser"); !errors.Is(err, sso.ErrState) {
		t.Fatalf("foreign binding: err = %v", err)
	}
	// The failed attempt spent the state: the right browser cannot use it
	// either.
	if _, err := r.s.Callback(r.ctx, sso.ProviderGoogle, q, binding); !errors.Is(err, sso.ErrState) {
		t.Fatalf("replay: err = %v", err)
	}
	u, binding = r.start(sso.ProviderGoogle, 0)
	state, code = r.idp.Authorize(t, u, googleClaims("g-ann", "ann@example.com"))
	q = url.Values{"state": {state}, "code": {code}}
	if _, err := r.s.Callback(r.ctx, sso.ProviderEntra, q, binding); !errors.Is(err, sso.ErrState) {
		t.Fatalf("provider mix-up: err = %v", err)
	}
	if _, err := r.s.Callback(r.ctx, sso.ProviderGoogle, url.Values{"state": {"made-up"}, "code": {"x"}}, binding); !errors.Is(err, sso.ErrState) {
		t.Fatalf("unknown state: err = %v", err)
	}
}

// TestIDTokenChecksFailClosed fails if any of the ID token checks lets a bad
// token through (AC-10).
func TestIDTokenChecksFailClosed(t *testing.T) {
	r := newRig(t, sso.ProviderGoogle)
	bend := func(k string, v any) map[string]any {
		c := googleClaims("g-ann", "ann@example.com")
		c[k] = v
		return c
	}
	for name, claims := range map[string]map[string]any{
		"wrong issuer":       bend("iss", "https://accounts.evil.example"),
		"wrong audience":     bend("aud", "someone-else.apps.googleusercontent.com"),
		"wrong nonce":        bend("nonce", "replayed-nonce"),
		"no nonce":           bend("nonce", nil),
		"expired":            bend("exp", time.Now().Add(-time.Hour).Unix()),
		"foreign azp":        bend("azp", "other.apps.googleusercontent.com"),
		"other domain":       bend("hd", "evil.example"),
		"no domain":          bend("hd", nil),
		"unverified email":   bend("email_verified", false),
		"no email_verified":  bend("email_verified", nil),
		"no subject (sub)":   bend("sub", nil),
		"other domain email": bend("email", "ann@evil.example"),
	} {
		if name == "other domain email" {
			// A verified hd with an outside email maps to nobody.
			if _, err := r.login(sso.ProviderGoogle, map[string]any{"sub": "g-unknown", "email": "ann@evil.example", "email_verified": true, "hd": "example.com"}); !errors.Is(err, sso.ErrNoUser) {
				t.Errorf("%s: err = %v, want ErrNoUser", name, err)
			}
			continue
		}
		if _, err := r.login(sso.ProviderGoogle, claims); !errors.Is(err, sso.ErrToken) {
			t.Errorf("%s: err = %v, want ErrToken", name, err)
		}
	}
	u, binding := r.start(sso.ProviderGoogle, 0)
	state, code := r.idp.ForgeCode(u, googleClaims("g-ann", "ann@example.com"))
	if _, err := r.s.Callback(r.ctx, sso.ProviderGoogle, url.Values{"state": {state}, "code": {code}}, binding); !errors.Is(err, sso.ErrToken) {
		t.Errorf("forged signature: err = %v, want ErrToken", err)
	}
	u, binding = r.start(sso.ProviderGoogle, 0)
	state, _ = r.idp.Authorize(t, u, googleClaims("g-ann", "ann@example.com"))
	if _, err := r.s.Callback(r.ctx, sso.ProviderGoogle, url.Values{"state": {state}, "code": {"stolen"}}, binding); !errors.Is(err, sso.ErrDenied) {
		t.Errorf("unknown code: err = %v, want ErrDenied", err)
	}
	u, binding = r.start(sso.ProviderGoogle, 0)
	state, _ = r.idp.Authorize(t, u, nil)
	if _, err := r.s.Callback(r.ctx, sso.ProviderGoogle, url.Values{"state": {state}, "error": {"access_denied"}}, binding); !errors.Is(err, sso.ErrDenied) {
		t.Errorf("declined: err = %v, want ErrDenied", err)
	}
	if ids, _ := r.m.ListIdentities(r.ctx, 10); len(ids) != 0 {
		t.Fatalf("a refused sign-in linked %+v", ids)
	}
}

// TestNoTakeoverByEmail fails if a local user or a local or synced admin is
// signed in by an ID token that only shares their email (T-7, AC-10), or if
// a synced non-admin with the email is not.
func TestNoTakeoverByEmail(t *testing.T) {
	r := newRig(t, sso.ProviderGoogle)
	for _, email := range []string{"boss@example.com", "root@example.com", "local@example.com", "chief@example.com"} {
		if out, err := r.login(sso.ProviderGoogle, googleClaims("g-"+email, email)); !errors.Is(err, sso.ErrNoUser) {
			t.Errorf("%s: %+v, %v; want ErrNoUser", email, out, err)
		}
	}
	out, err := r.login(sso.ProviderGoogle, googleClaims("g-lee", "LEE@example.com"))
	if err != nil || out.UserID != 11 || !out.Linked {
		t.Fatalf("LDAP-synced user by email = %+v, %v", out, err)
	}
}

// TestDisabledUserIsRefused fails if a disabled user signs in.
func TestDisabledUserIsRefused(t *testing.T) {
	r := newRig(t, sso.ProviderGoogle)
	r.m.Users[10].Enabled = false
	if _, err := r.login(sso.ProviderGoogle, googleClaims("g-ann", "ann@example.com")); !errors.Is(err, sso.ErrUserDisabled) {
		t.Fatalf("err = %v, want ErrUserDisabled", err)
	}
}

// TestJustInTime fails if an unknown identity creates a user while jit is
// off, or does not while it is on.
func TestJustInTime(t *testing.T) {
	r := newRig(t, sso.ProviderGoogle)
	if _, err := r.login(sso.ProviderGoogle, googleClaims("g-new", "new@example.com")); !errors.Is(err, sso.ErrNoUser) {
		t.Fatalf("jit off: err = %v", err)
	}
	if _, err := r.s.PutSettings(r.ctx, "admin", sso.SettingsInput{Providers: []sso.ProviderInput{{Provider: sso.ProviderGoogle, JIT: ptr(true)}}}); err != nil {
		t.Fatal(err)
	}
	out, err := r.login(sso.ProviderGoogle, googleClaims("g-new", "new@example.com"))
	if err != nil || out.Username != "new@example.com" || !out.Linked {
		t.Fatalf("jit on = %+v, %v", out, err)
	}
	u, _ := r.m.GetUser(r.ctx, out.UserID)
	if u.Role != "user" {
		t.Fatalf("jit user role = %q, want user", u.Role)
	}
}

// TestLinkingAnIdentity fails if a signed-in user cannot link an identity, or
// if one identity can be linked to two users.
func TestLinkingAnIdentity(t *testing.T) {
	r := newRig(t, sso.ProviderGoogle)
	u, binding := r.start(sso.ProviderGoogle, 2)
	state, code := r.idp.Authorize(t, u, googleClaims("g-boss", "boss@example.com"))
	out, err := r.s.Callback(r.ctx, sso.ProviderGoogle, url.Values{"state": {state}, "code": {code}}, binding)
	if err != nil || out.Kind != sso.KindLink || out.UserID != 2 {
		t.Fatalf("link = %+v, %v", out, err)
	}
	// Now the admin signs in through Google: the link, never the email.
	out, err = r.login(sso.ProviderGoogle, googleClaims("g-boss", "boss@example.com"))
	if err != nil || out.UserID != 2 {
		t.Fatalf("linked admin sign-in = %+v, %v", out, err)
	}
	u, binding = r.start(sso.ProviderGoogle, 3)
	state, code = r.idp.Authorize(t, u, googleClaims("g-boss", "boss@example.com"))
	if _, err := r.s.Callback(r.ctx, sso.ProviderGoogle, url.Values{"state": {state}, "code": {code}}, binding); !errors.Is(err, sso.ErrLinked) {
		t.Fatalf("second link of the same identity: err = %v", err)
	}
	if err := r.s.Unlink(r.ctx, "admin", 2, sso.ProviderGoogle); err != nil {
		t.Fatal(err)
	}
	if _, err := r.login(sso.ProviderGoogle, googleClaims("g-boss", "boss@example.com")); !errors.Is(err, sso.ErrNoUser) {
		t.Fatalf("after unlink: err = %v", err)
	}
}

// TestSettingsValidation fails if a multi-tenant Entra authority, a Google
// provider without a domain, or sso_required without a break-glass admin is
// accepted.
func TestSettingsValidation(t *testing.T) {
	r := newRig(t, sso.ProviderEntra)
	for name, in := range map[string]sso.ProviderInput{
		"common tenant":       {Provider: sso.ProviderEntra, Tenant: ptr("common")},
		"organizations":       {Provider: sso.ProviderEntra, Tenant: ptr("organizations")},
		"tenant not a guid":   {Provider: sso.ProviderEntra, Tenant: ptr("contoso.com")},
		"google no domain":    {Provider: sso.ProviderGoogle, Enabled: ptr(true), ClientID: ptr(googleApp), ClientSecret: ptr("s")},
		"google bad client":   {Provider: sso.ProviderGoogle, ClientID: ptr("not-a-client")},
		"secret with a space": {Provider: sso.ProviderEntra, ClientSecret: ptr("a b")},
		"unknown provider":    {Provider: "okta"},
	} {
		_, err := r.s.PutSettings(r.ctx, "admin", sso.SettingsInput{Providers: []sso.ProviderInput{in}})
		if !errors.Is(err, sso.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	r.m.Users[1].BreakGlass = false
	if _, err := r.s.PutSettings(r.ctx, "admin", sso.SettingsInput{SSORequired: ptr(true)}); !errors.Is(err, sso.ErrNoBreakGlass) {
		t.Fatalf("sso_required without break-glass: err = %v", err)
	}
	r.m.Users[1].BreakGlass = true
	st, err := r.s.PutSettings(r.ctx, "admin", sso.SettingsInput{SSORequired: ptr(true)})
	if err != nil || !st.SSORequired || len(st.BreakGlass) != 1 || st.RedirectURIs["entra"] != "https://hello.example/api/v1/auth/oidc/entra/callback" {
		t.Fatalf("settings = %+v, %v", st, err)
	}
	for _, p := range st.Providers {
		if p.Provider == sso.ProviderEntra && (!p.HasClientSecret || p.Tenant != tenant) {
			t.Fatalf("entra = %+v", p)
		}
	}
}

// TestDisabledProviderCannotStart fails if a sign-in starts through a
// provider that is off, or without HELLO_PUBLIC_URL.
func TestDisabledProviderCannotStart(t *testing.T) {
	r := newRig(t, sso.ProviderGoogle)
	if _, _, err := r.s.Start(r.ctx, sso.ProviderEntra, 0, ""); !errors.Is(err, sso.ErrProviderOff) {
		t.Fatalf("unconfigured provider: err = %v", err)
	}
	if _, err := r.s.PutSettings(r.ctx, "admin", sso.SettingsInput{Providers: []sso.ProviderInput{{Provider: sso.ProviderGoogle, Enabled: ptr(false)}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.s.Start(r.ctx, sso.ProviderGoogle, 0, ""); !errors.Is(err, sso.ErrProviderOff) {
		t.Fatalf("disabled provider: err = %v", err)
	}
	if ps, _ := r.s.PublicProviders(r.ctx); len(ps) != 0 {
		t.Fatalf("public providers = %v", ps)
	}
	r.s.PublicURL = ""
	if _, _, err := r.s.Start(r.ctx, sso.ProviderGoogle, 0, ""); !errors.Is(err, sso.ErrUnavailable) {
		t.Fatalf("no public URL: err = %v", err)
	}
}

// TestStartCarriesPKCEAndHint fails if the authorization request lacks PKCE,
// nonce or state, or keeps an unsafe return path.
func TestStartCarriesPKCEAndHint(t *testing.T) {
	r := newRig(t, sso.ProviderGoogle)
	u, binding, err := r.s.Start(r.ctx, sso.ProviderGoogle, 0, "//evil.example/x")
	if err != nil || binding == "" {
		t.Fatal(err)
	}
	q, _ := url.Parse(u)
	if q.Query().Get("hd") != "example.com" || q.Query().Get("redirect_uri") != "https://hello.example/api/v1/auth/oidc/google/callback" {
		t.Fatalf("auth URL = %s", u)
	}
	state, code := r.idp.Authorize(t, u, googleClaims("g-ann", "ann@example.com"))
	out, err := r.s.Callback(r.ctx, sso.ProviderGoogle, url.Values{"state": {state}, "code": {code}}, binding)
	if err != nil || out.Next != "" {
		t.Fatalf("next = %q, %v; an off-site return path must be dropped", out.Next, err)
	}
}

// TestProviderTest fails if the provider check does not read discovery and
// keys from the issuer.
func TestProviderTest(t *testing.T) {
	r := newRig(t, sso.ProviderEntra)
	res, err := r.s.TestProvider(r.ctx, "admin", sso.ProviderEntra)
	if err != nil || !res.OK || res.Keys != 1 || !strings.HasSuffix(res.Issuer, "/"+tenant+"/v2.0") {
		t.Fatalf("test = %+v, %v", res, err)
	}
	res, err = r.s.TestProvider(r.ctx, "admin", sso.ProviderGoogle)
	if err != nil || !res.OK {
		t.Fatalf("google test = %+v, %v", res, err)
	}
	if _, err := r.s.TestProvider(r.ctx, "admin", "okta"); !errors.Is(err, sso.ErrNotFound) {
		t.Fatalf("unknown provider: err = %v", err)
	}
}

// TestBindLoginThrottle fails if the LDAP bind login does not refuse an empty
// password without a bind, or keeps binding after 5 failures for a user or
// an address (I-30).
func TestBindLoginThrottle(t *testing.T) {
	r := newRig(t, sso.ProviderGoogle)
	if err := r.s.BindLogin(r.ctx, 8, "guid-lee", 11, "", "10.0.0.1"); !errors.Is(err, directory.ErrBindRefused) || r.dir.calls != 0 {
		t.Fatalf("empty password: err = %v, binds = %d", err, r.dir.calls)
	}
	r.dir.err = directory.ErrBindRefused
	for i := 0; i < sso.BindFailLimit; i++ {
		if err := r.s.BindLogin(r.ctx, 8, "guid-lee", 11, "wrong", "10.0.0.1"); !errors.Is(err, directory.ErrBindRefused) {
			t.Fatalf("attempt %d: err = %v", i, err)
		}
	}
	r.dir.err = nil
	if err := r.s.BindLogin(r.ctx, 8, "guid-lee", 11, "right", "10.0.0.2"); !errors.Is(err, sso.ErrThrottled) || r.dir.calls != sso.BindFailLimit {
		t.Fatalf("user over the limit: err = %v, binds = %d", err, r.dir.calls)
	}
	if err := r.s.BindLogin(r.ctx, 8, "guid-x", 12, "right", "10.0.0.1"); !errors.Is(err, sso.ErrThrottled) {
		t.Fatalf("address over the limit: err = %v", err)
	}
	if err := r.s.BindLogin(r.ctx, 8, "guid-x", 12, "right", "10.0.0.3"); err != nil {
		t.Fatalf("fresh user and address: err = %v", err)
	}
}

type fakeConsents struct {
	gotCode, gotVerifier string
}

func (f *fakeConsents) ConsentURL(_ context.Context, id int64, redirect, state, verifier string) (string, error) {
	return "https://accounts.example/auth?state=" + url.QueryEscape(state) + "&redirect_uri=" + url.QueryEscape(redirect), nil
}

func (f *fakeConsents) CompleteConsent(_ context.Context, actor string, id int64, code, verifier, redirect string) error {
	f.gotCode, f.gotVerifier = code, verifier
	if actor != "user:carol" || id != 4 || redirect != "https://hello.example/api/v1/directory/oauth/callback" {
		return errors.New("unexpected consent completion")
	}
	return nil
}

// TestConsentRoundTrip fails if a directory consent is not single use and
// bound to the administrator's browser.
func TestConsentRoundTrip(t *testing.T) {
	r := newRig(t, sso.ProviderGoogle)
	c := &fakeConsents{}
	u, binding, err := r.s.StartConsent(r.ctx, c, "user:carol", 4)
	if err != nil {
		t.Fatal(err)
	}
	pu, _ := url.Parse(u)
	state := pu.Query().Get("state")
	q := url.Values{"state": {state}, "code": {"consent-code"}}
	id, err := r.s.FinishConsent(r.ctx, c, q, binding)
	if err != nil || id != 4 || c.gotCode != "consent-code" || c.gotVerifier == "" {
		t.Fatalf("finish = %d, %v, %+v", id, err, c)
	}
	if _, err := r.s.FinishConsent(r.ctx, c, q, binding); !errors.Is(err, sso.ErrState) {
		t.Fatalf("replay: err = %v", err)
	}
}
