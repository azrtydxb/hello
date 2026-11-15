package directory

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // G505: the test checks the x5t thumbprint Entra defines as SHA-1
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func init() {
	// Retry-After waits are counted, not slept, in tests.
	sleep = func(context.Context, time.Duration) error { return nil }
}

// verifyJWS checks a compact RS256 JWS against pub and returns its header and
// claims; ok is false for a bad signature. Fakes call it from their handler
// goroutine, so it does not fail the test itself.
func verifyJWS(s string, pub *rsa.PublicKey) (h, c map[string]any, ok bool) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return nil, nil, false
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		return nil, nil, false
	}
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	_ = json.Unmarshal(hb, &h)
	_ = json.Unmarshal(cb, &c)
	return h, c, true
}

func keyPEM(k *rsa.PrivateKey) string {
	b, _ := x509.MarshalPKCS8PrivateKey(k)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b}))
}

// fakeGoogle is the token endpoint and the Directory API.
type fakeGoogle struct {
	t         *testing.T
	srv       *httptest.Server
	key       *rsa.PrivateKey
	users     int
	mu        sync.Mutex
	scopes    []string
	subs      []string
	grants    []string
	throttled bool
	// groupMembers is group id -> user ids.
	groupMembers map[string][]string
}

func newFakeGoogle(t *testing.T, users int) *fakeGoogle {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	f := &fakeGoogle{t: t, key: key, users: users, groupMembers: map[string][]string{"g-sales": {"u3", "u7"}}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGoogle) driver() Google {
	return Google{TokenURL: f.srv.URL + "/token", AuthURL: "https://accounts.google.example/auth", APIBase: f.srv.URL}
}

func (f *fakeGoogle) keyFile() string {
	b, _ := json.Marshal(map[string]string{"type": "service_account", "client_email": "hello-sync@proj.iam.gserviceaccount.com",
		"private_key": keyPEM(f.key), "private_key_id": "kid-1", "token_uri": "https://evil.example/token"})
	return string(b)
}

func (f *fakeGoogle) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/token":
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		f.grants = append(f.grants, r.PostForm.Get("grant_type"))
		switch r.PostForm.Get("grant_type") {
		case "urn:ietf:params:oauth:grant-type:jwt-bearer":
			_, c, ok := verifyJWS(r.PostForm.Get("assertion"), &f.key.PublicKey)
			if !ok {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.scopes = append(f.scopes, strings.Fields(c["scope"].(string))...)
			sub, _ := c["sub"].(string)
			f.subs = append(f.subs, sub)
			if c["aud"] != googleTokenURL || c["iss"] != "hello-sync@proj.iam.gserviceaccount.com" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		case "refresh_token":
			if r.PostForm.Get("refresh_token") != "rt-1" || r.PostForm.Get("client_secret") != "cs-1" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
		case "authorization_code":
			if r.PostForm.Get("code_verifier") == "" || r.PostForm.Get("client_secret") != "cs-1" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			scope := strings.Join(GoogleScopes, " ")
			if r.PostForm.Get("code") == "greedy" {
				scope += " https://www.googleapis.com/auth/admin.directory.user"
			}
			_, _ = fmt.Fprintf(w, `{"access_token":"at","refresh_token":"rt-new","scope":%q}`, scope)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"google-at","expires_in":3600}`))
	case r.Header.Get("Authorization") != "Bearer google-at":
		w.WriteHeader(http.StatusUnauthorized)
	case r.URL.Path == "/admin/directory/v1/users":
		q := r.URL.Query()
		if q.Get("customer") != "my_customer" || q.Get("viewType") != "admin_view" || q.Get("projection") != "full" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		if !f.throttled && q.Get("pageToken") == "2" {
			f.throttled = true
			f.mu.Unlock()
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		f.mu.Unlock()
		max, _ := strconv.Atoi(q.Get("maxResults"))
		page, _ := strconv.Atoi(q.Get("pageToken"))
		var us []map[string]any
		for i := page * max; i < min((page+1)*max, f.users); i++ {
			us = append(us, map[string]any{"id": "u" + strconv.Itoa(i), "primaryEmail": fmt.Sprintf("user%d@example.com", i),
				"name": map[string]any{"fullName": fmt.Sprintf("User %d", i)}, "suspended": i == 5, "archived": i == 6,
				"phones": []any{map[string]any{"type": "work", "value": fmt.Sprintf("+1 555 01%02d", i%100)}, map[string]any{"type": "mobile", "value": "+1 555 9999"}}})
		}
		out := map[string]any{"users": us}
		if (page+1)*max < f.users {
			out["nextPageToken"] = strconv.Itoa(page + 1)
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.URL.Path == "/admin/directory/v1/groups":
		_, _ = w.Write([]byte(`{"groups":[{"id":"g-sales","name":"Sales"}]}`))
	case strings.HasPrefix(r.URL.Path, "/admin/directory/v1/groups/"):
		rest := strings.TrimPrefix(r.URL.Path, "/admin/directory/v1/groups/")
		id, members, _ := strings.Cut(rest, "/")
		ms, ok := f.groupMembers[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if members == "" {
			_, _ = fmt.Fprintf(w, `{"id":%q,"name":"Sales"}`, id)
			return
		}
		if r.URL.Query().Get("includeDerivedMembership") != "true" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var out []map[string]string
		for _, m := range ms {
			out = append(out, map[string]string{"id": m, "type": "USER"})
		}
		out = append(out, map[string]string{"id": "g-nested", "type": "GROUP"})
		_ = json.NewEncoder(w).Encode(map[string]any{"members": out})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func googleCfgRaw(t *testing.T, s string) json.RawMessage {
	t.Helper()
	out, err := Google{}.Validate(json.RawMessage(s), ValidateOptions{})
	if err != nil {
		t.Fatalf("validate %s: %v", s, err)
	}
	return out
}

// TestGooglePagesAndScopes fails if the Google connector does not page through
// 2,500 users, honour Retry-After, map suspended and archived users inactive,
// resolve group members, or asks for any scope outside the read-only
// allow-list (AC-8).
func TestGooglePagesAndScopes(t *testing.T) {
	f := newFakeGoogle(t, 2500)
	cfg := googleCfgRaw(t, `{}`)
	res, err := f.driver().Fetch(context.Background(), cfg, f.keyFile(), FetchOptions{Limit: 5000, NeedGroups: []string{"g-sales", "g-gone"}, HTTP: f.srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Users) != 2500 || !f.throttled || res.Incremental || res.Mark != nil {
		t.Fatalf("users = %d, throttled = %v, incremental = %v", len(res.Users), f.throttled, res.Incremental)
	}
	m := (Google{}).DefaultMappings()
	byID := map[string]SourceUser{}
	for _, u := range res.Users {
		byID[u.ExternalID] = u
	}
	if a := m.Apply(byID["u5"]); a.Active {
		t.Error("a suspended user is active")
	}
	if a := m.Apply(byID["u6"]); a.Active {
		t.Error("an archived user is active")
	}
	a := m.Apply(byID["u7"])
	if !a.Active || a.Username != "user7@example.com" || a.DisplayName != "User 7" || a.Extension != "15550107" {
		t.Errorf("mapped u7 = %+v", a)
	}
	if g := byID["u7"].Groups; len(g) != 1 || g[0] != "g-sales" || len(res.Groups) != 1 {
		t.Errorf("groups = %v, %v", g, res.Groups)
	}
	for _, s := range f.scopes {
		if !googleScopeAllowed(s) {
			t.Errorf("asked for scope %q outside the allow-list", s)
		}
	}
	if len(f.subs) != 1 || f.subs[0] != "" {
		t.Errorf("a service account with an admin role must not impersonate: subs = %q", f.subs)
	}
	if _, err := f.driver().Fetch(context.Background(), cfg, f.keyFile(), FetchOptions{Limit: 1000, HTTP: f.srv.Client()}); err == nil {
		t.Fatal("2500 users under a cap of 1000 did not fail")
	} else if de := classify(err); de.Code != CodeUserCap {
		t.Fatalf("cap: %v", err)
	}
}

// TestGoogleDelegationAndOAuth fails if delegation does not impersonate the
// configured admin with only the user scope, or the OAuth mode does not use
// the sealed refresh token.
func TestGoogleDelegationAndOAuth(t *testing.T) {
	f := newFakeGoogle(t, 3)
	cfg := googleCfgRaw(t, `{"authMode":"delegation","subject":"admin@example.com"}`)
	if _, err := f.driver().Fetch(context.Background(), cfg, f.keyFile(), FetchOptions{Limit: 10, HTTP: f.srv.Client()}); err != nil {
		t.Fatal(err)
	}
	if len(f.subs) != 1 || f.subs[0] != "admin@example.com" || len(f.scopes) != 1 || f.scopes[0] != ScopeGoogleUsers {
		t.Fatalf("delegation: subs = %q, scopes = %q", f.subs, f.scopes)
	}
	oc := googleCfgRaw(t, `{"authMode":"oauth_admin","clientId":"1-abc.apps.googleusercontent.com"}`)
	if _, err := f.driver().Fetch(context.Background(), oc, `{"clientSecret":"cs-1"}`, FetchOptions{Limit: 10, HTTP: f.srv.Client()}); classify(err).Code != CodeConsentRequired {
		t.Fatalf("no consent yet: %v", err)
	}
	res, err := f.driver().Fetch(context.Background(), oc, `{"clientSecret":"cs-1","refreshToken":"rt-1"}`, FetchOptions{Limit: 10, HTTP: f.srv.Client()})
	if err != nil || len(res.Users) != 3 {
		t.Fatalf("oauth admin: %v", err)
	}
	if _, err := f.driver().Fetch(context.Background(), oc, `{"clientSecret":"wrong","refreshToken":"rt-1"}`, FetchOptions{Limit: 10, HTTP: f.srv.Client()}); classify(err).Code != CodeAuthFailed {
		t.Fatalf("revoked grant: %v", err)
	}
}

// TestGoogleConsent fails if the consent URL lacks PKCE, offline access or
// the read-only scopes, or a grant with a write scope is stored.
func TestGoogleConsent(t *testing.T) {
	f := newFakeGoogle(t, 1)
	oc := googleCfgRaw(t, `{"authMode":"oauth_admin","clientId":"1-abc.apps.googleusercontent.com"}`)
	u, err := f.driver().ConsentURL(oc, "https://hello.example/api/v1/directory/oauth/callback", "st", "verifier-123")
	if err != nil {
		t.Fatal(err)
	}
	q, _ := url.Parse(u)
	qq := q.Query()
	if qq.Get("access_type") != "offline" || qq.Get("code_challenge_method") != "S256" || qq.Get("code_challenge") == "" ||
		qq.Get("scope") != strings.Join(GoogleScopes, " ") || qq.Get("state") != "st" {
		t.Fatalf("consent URL = %s", u)
	}
	next, err := f.driver().Redeem(context.Background(), oc, "cs-1", "good", "verifier-123", "https://hello.example/cb", FetchOptions{HTTP: f.srv.Client()})
	if err != nil || !strings.Contains(next, `"refreshToken":"rt-new"`) || !strings.Contains(next, `"clientSecret":"cs-1"`) {
		t.Fatalf("redeem = %q, %v", next, err)
	}
	if _, err := f.driver().Redeem(context.Background(), oc, "cs-1", "greedy", "v", "https://hello.example/cb", FetchOptions{HTTP: f.srv.Client()}); err == nil {
		t.Fatal("a grant with a write scope was accepted")
	}
	if _, err := f.driver().ConsentURL(googleCfgRaw(t, `{}`), "https://x", "s", "v"); err == nil {
		t.Fatal("consent for a service-account connector")
	}
}

// TestGoogleValidation fails if a bad config or credential is accepted.
func TestGoogleValidation(t *testing.T) {
	for name, cfg := range map[string]string{
		"delegation without subject": `{"authMode":"delegation"}`,
		"subject without delegation": `{"subject":"a@b.example"}`,
		"oauth without client":       `{"authMode":"oauth_admin"}`,
		"bad customer":               `{"customer":"x y"}`,
		"unknown field":              `{"bogus":1}`,
		"bad mode":                   `{"authMode":"password"}`,
	} {
		if _, err := (Google{}).Validate(json.RawMessage(cfg), ValidateOptions{}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	f := newFakeGoogle(t, 0)
	if err := (Google{}).CheckSecret(googleCfgRaw(t, `{}`), `{"type":"authorized_user"}`); err == nil {
		t.Error("a user credential was accepted as a service account key")
	}
	if err := (Google{}).CheckSecret(googleCfgRaw(t, `{}`), f.keyFile()); err != nil {
		t.Errorf("key file: %v", err)
	}
	if GoogleClientEmail(f.keyFile()) != "hello-sync@proj.iam.gserviceaccount.com" {
		t.Error("client email")
	}
}

// fakeGraph is the Entra token endpoint and Microsoft Graph.
type fakeGraph struct {
	t         *testing.T
	srv       *httptest.Server
	users     int
	cert      *x509.Certificate
	certPEM   string
	key       *rsa.PrivateKey
	mu        sync.Mutex
	tokenReqs []url.Values
	throttled bool
	// deltaPages answers a delta link: changed user ids, removed ids, gone.
	changed []string
	removed []string
	gone    bool
	foreign bool
	members map[string][]string
}

func newFakeGraph(t *testing.T, users int) *fakeGraph {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "hello-sync"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	cert, _ := x509.ParseCertificate(der)
	f := &fakeGraph{t: t, users: users, cert: cert, key: key, members: map[string][]string{gsales: {uid(3), uid(4)}},
		certPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

const (
	entraTenant = "11111111-2222-3333-4444-555555555555"
	entraClient = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	gsales      = "cccccccc-0000-0000-0000-000000000001"
)

func uid(i int) string { return fmt.Sprintf("00000000-0000-0000-0000-%012d", i) }

func (f *fakeGraph) driver() Entra { return Entra{LoginBase: f.srv.URL, GraphBase: f.srv.URL} }

func (f *fakeGraph) user(i int) map[string]any {
	return map[string]any{"id": uid(i), "userPrincipalName": fmt.Sprintf("user%d@contoso.example", i), "mail": fmt.Sprintf("user%d@contoso.example", i),
		"displayName": fmt.Sprintf("User %d", i), "accountEnabled": i != 5, "businessPhones": []any{fmt.Sprintf("2%03d", i%1000)},
		"userType": map[bool]string{true: "Guest", false: "Member"}[i == 6]}
}

func (f *fakeGraph) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/"+entraTenant+"/oauth2/v2.0/token" {
		_ = r.ParseForm()
		f.mu.Lock()
		f.tokenReqs = append(f.tokenReqs, r.PostForm)
		f.mu.Unlock()
		ok := r.PostForm.Get("client_secret") == "graph-secret"
		if a := r.PostForm.Get("client_assertion"); a != "" {
			h, c, valid := verifyJWS(a, &f.key.PublicKey)
			s1 := sha1.Sum(f.cert.Raw) //nolint:gosec // G401: x5t is SHA-1 by definition
			ok = valid && h["x5t"] == base64.RawURLEncoding.EncodeToString(s1[:]) && c["aud"] == f.srv.URL+r.URL.Path &&
				c["iss"] == entraClient && c["sub"] == entraClient && r.PostForm.Get("client_assertion_type") == "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
		}
		if !ok || r.PostForm.Get("grant_type") != "client_credentials" || r.PostForm.Get("scope") != EntraScope {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"graph-at","expires_in":3600}`))
		return
	}
	if r.Header.Get("Authorization") != "Bearer graph-at" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	switch {
	case r.URL.Path == "/v1.0/users/delta" && q.Get("$deltatoken") == "latest":
		_, _ = fmt.Fprintf(w, `{"value":[],"@odata.deltaLink":%q}`, f.srv.URL+"/v1.0/users/delta?$deltatoken=T1")
	case r.URL.Path == "/v1.0/users/delta":
		if f.gone {
			w.WriteHeader(http.StatusGone)
			return
		}
		var vals []map[string]any
		for _, id := range f.changed {
			vals = append(vals, map[string]any{"id": id, "displayName": "only the changed property"})
		}
		for _, id := range f.removed {
			vals = append(vals, map[string]any{"id": id, "@removed": map[string]any{"reason": "deleted"}})
		}
		out := map[string]any{"value": vals, "@odata.deltaLink": f.srv.URL + "/v1.0/users/delta?$deltatoken=T2"}
		if f.foreign {
			out = map[string]any{"value": vals, "@odata.nextLink": "https://evil.example/v1.0/users/delta?x=1"}
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.URL.Path == "/v1.0/users" && q.Get("$filter") != "":
		var vals []map[string]any
		for i := 0; i < f.users; i++ {
			if strings.Contains(q.Get("$filter"), "'"+uid(i)+"'") {
				vals = append(vals, f.user(i))
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"value": vals})
	case r.URL.Path == "/v1.0/users":
		if !strings.Contains(q.Get("$select"), "accountEnabled") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		if !f.throttled && q.Get("skip") == "999" {
			f.throttled = true
			f.mu.Unlock()
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		f.mu.Unlock()
		top, _ := strconv.Atoi(q.Get("$top"))
		skip, _ := strconv.Atoi(q.Get("skip"))
		var vals []map[string]any
		for i := skip; i < min(skip+top, f.users); i++ {
			vals = append(vals, f.user(i))
		}
		out := map[string]any{"value": vals}
		if skip+top < f.users {
			nq := url.Values{"$select": {q.Get("$select")}, "$top": {q.Get("$top")}, "skip": {strconv.Itoa(skip + top)}}
			out["@odata.nextLink"] = f.srv.URL + "/v1.0/users?" + nq.Encode()
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.URL.Path == "/v1.0/groups/"+gsales:
		_, _ = w.Write([]byte(`{"id":"` + gsales + `","displayName":"Sales"}`))
	case r.URL.Path == "/v1.0/groups/"+gsales+"/transitiveMembers/microsoft.graph.user":
		var vals []map[string]any
		for _, id := range f.members[gsales] {
			vals = append(vals, map[string]any{"id": id})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"value": vals})
	case r.URL.Path == "/v1.0/groups":
		_, _ = w.Write([]byte(`{"value":[{"id":"` + gsales + `","displayName":"Sales"}]}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func entraCfgRaw(t *testing.T, s string) json.RawMessage {
	t.Helper()
	out, err := Entra{}.Validate(json.RawMessage(s), ValidateOptions{})
	if err != nil {
		t.Fatalf("validate %s: %v", s, err)
	}
	return out
}

// TestEntraFullReadAndScope fails if the Entra connector does not page
// 2,500 users through nextLink, honour Retry-After, leave guests out, take a
// delta mark, or asks for anything but the .default scope (AC-8).
func TestEntraFullReadAndScope(t *testing.T) {
	f := newFakeGraph(t, 2500)
	cfg := entraCfgRaw(t, `{"tenantId":"`+entraTenant+`","clientId":"`+entraClient+`"}`)
	res, err := f.driver().Fetch(context.Background(), cfg, "graph-secret", FetchOptions{Limit: 5000, NeedGroups: []string{gsales}, HTTP: f.srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Users) != 2499 || !f.throttled || res.Incremental {
		t.Fatalf("users = %d (want 2499: one guest), throttled = %v", len(res.Users), f.throttled)
	}
	if res.Mark == nil || res.Mark.Method != markDelta || !strings.HasSuffix(res.Mark.Delta, "$deltatoken=T1") || res.Mark.Groups == "" {
		t.Fatalf("mark = %+v", res.Mark)
	}
	for _, q := range f.tokenReqs {
		if q.Get("scope") != EntraScope {
			t.Errorf("token request scope = %q", q.Get("scope"))
		}
	}
	m := (Entra{}).DefaultMappings()
	by := map[string]SourceUser{}
	for _, u := range res.Users {
		by[u.ExternalID] = u
	}
	if a := m.Apply(by[uid(5)]); a.Active {
		t.Error("a disabled account is active")
	}
	if a := m.Apply(by[uid(3)]); a.Username != "user3@contoso.example" || a.Extension != "2003" || !a.Active {
		t.Errorf("mapped = %+v", a)
	}
	if g := by[uid(3)].Groups; len(g) != 1 || g[0] != gsales {
		t.Errorf("groups = %v", g)
	}
}

// TestEntraDelta fails if an incremental run does not read the changed users
// whole, or does not fall back to a full read when the link expired, a user
// was removed, a group changed, or Graph pointed paging at another host.
func TestEntraDelta(t *testing.T) {
	f := newFakeGraph(t, 30)
	cfg := entraCfgRaw(t, `{"tenantId":"`+entraTenant+`","clientId":"`+entraClient+`"}`)
	full, err := f.driver().Fetch(context.Background(), cfg, "graph-secret", FetchOptions{Limit: 100, NeedGroups: []string{gsales}, HTTP: f.srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	f.changed = []string{uid(3), uid(9)}
	res, err := f.driver().Fetch(context.Background(), cfg, "graph-secret", FetchOptions{Limit: 100, NeedGroups: []string{gsales}, Since: full.Mark, HTTP: f.srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Incremental || len(res.Users) != 2 || res.Users[0].Attrs["userprincipalname"][0] != "user3@contoso.example" ||
		!strings.HasSuffix(res.Mark.Delta, "T2") || len(res.Users[0].Groups) != 1 {
		t.Fatalf("incremental = %+v", res)
	}
	for name, setup := range map[string]func(){
		FullDeltaExpired:  func() { f.gone = true },
		FullDeltaRemoved:  func() { f.removed = []string{uid(4)} },
		FullGroupsChanged: func() { f.members[gsales] = []string{uid(3)} },
	} {
		f.gone, f.removed, f.members[gsales] = false, nil, []string{uid(3), uid(4)}
		setup()
		res, err := f.driver().Fetch(context.Background(), cfg, "graph-secret", FetchOptions{Limit: 100, NeedGroups: []string{gsales}, Since: full.Mark, HTTP: f.srv.Client()})
		if err != nil || res.Incremental || res.FullReason != name || len(res.Users) != 29 {
			t.Errorf("%s: incremental = %v, reason = %q, users = %d, err = %v", name, res != nil && res.Incremental, res.FullReason, len(res.Users), err)
		}
	}
	f.gone, f.removed, f.foreign = false, nil, true
	if _, err := f.driver().Fetch(context.Background(), cfg, "graph-secret", FetchOptions{Limit: 100, Since: full.Mark, HTTP: f.srv.Client()}); err == nil ||
		!strings.Contains(err.Error(), "another host") {
		t.Fatalf("foreign paging link: %v", err)
	}
	forged := *full.Mark
	forged.Delta = "https://evil.example/v1.0/users/delta?$deltatoken=x"
	f.foreign = false
	res, err = f.driver().Fetch(context.Background(), cfg, "graph-secret", FetchOptions{Limit: 100, Since: &forged, HTTP: f.srv.Client()})
	if err != nil || res.Incremental {
		t.Fatalf("a stored link to another host must force a full read: %v", err)
	}
}

// TestEntraCertificate fails if the certificate credential is not a client
// assertion signed with the key and naming the certificate, or a key that
// does not belong to the certificate is accepted.
func TestEntraCertificate(t *testing.T) {
	f := newFakeGraph(t, 3)
	b, _ := json.Marshal(map[string]string{"tenantId": entraTenant, "clientId": entraClient, "authMethod": "certificate", "certificatePem": f.certPEM})
	cfg := entraCfgRaw(t, string(b))
	if err := (Entra{}).CheckSecret(cfg, keyPEM(f.key)); err != nil {
		t.Fatalf("matching key: %v", err)
	}
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	if err := (Entra{}).CheckSecret(cfg, keyPEM(other)); err == nil {
		t.Fatal("a key of another certificate was accepted")
	}
	res, err := f.driver().Test(context.Background(), cfg, keyPEM(f.key), FetchOptions{HTTP: f.srv.Client()})
	if err != nil || !res.OK || res.MatchedUsers != 3 || len(res.Groups) != 1 {
		t.Fatalf("test = %+v, %v", res, err)
	}
	if f.tokenReqs[0].Get("client_secret") != "" {
		t.Fatal("certificate auth sent a client secret")
	}
	if _, err := f.driver().Fetch(context.Background(), cfg, keyPEM(other), FetchOptions{Limit: 10, HTTP: f.srv.Client()}); classify(err).Code != CodeAuthFailed {
		t.Fatalf("wrong key: %v", err)
	}
}

// TestEntraValidation fails if a multi-tenant authority or a bad id is
// accepted.
func TestEntraValidation(t *testing.T) {
	for name, cfg := range map[string]string{
		"common":         `{"tenantId":"common","clientId":"` + entraClient + `"}`,
		"organizations":  `{"tenantId":"organizations","clientId":"` + entraClient + `"}`,
		"bad client":     `{"tenantId":"` + entraTenant + `","clientId":"app"}`,
		"cert without":   `{"tenantId":"` + entraTenant + `","clientId":"` + entraClient + `","authMethod":"certificate"}`,
		"cert on secret": `{"tenantId":"` + entraTenant + `","clientId":"` + entraClient + `","certificatePem":"x"}`,
	} {
		if _, err := (Entra{}).Validate(json.RawMessage(cfg), ValidateOptions{}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := (Entra{}).Validate(json.RawMessage(`{"tenantId":"contoso.onmicrosoft.com","clientId":"`+entraClient+`"}`), ValidateOptions{}); err != nil {
		t.Errorf("a verified domain as tenant: %v", err)
	}
}

// TestCloudConnectorLifecycle fails if a Google connector does not default to
// OIDC sign-in, cannot be enabled before its provider is, names its
// credential field, or checks the credential on save.
func TestCloudConnectorLifecycle(t *testing.T) {
	m := NewMem()
	providerOn := false
	e := &Engine{Store: m, Drivers: map[string]Driver{KindGoogle: Google{}}, LoginCheck: func(_ context.Context, p string) error {
		if p != KindGoogle || !providerOn {
			return errors.New("off")
		}
		return nil
	}}
	ctx := context.Background()
	bad := "not a key file"
	_, err := e.Create(ctx, "admin", ConnectorInput{Kind: KindGoogle, Name: strPtr("GW"), Secret: &bad})
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Fields[0].Path != "credential" {
		t.Fatalf("bad credential: %v", err)
	}
	f := newFakeGoogle(t, 0)
	key := f.keyFile()
	c, err := e.Create(ctx, "admin", ConnectorInput{Kind: KindGoogle, Name: strPtr("GW"), Secret: &key})
	if err != nil || c.Options.LoginMethod != LoginOIDC || c.Options.OIDCProvider != KindGoogle {
		t.Fatalf("create = %+v, %v", c.Options, err)
	}
	_, err = e.Update(ctx, "admin", c.ID, ConnectorInput{Enabled: boolPtr(true)})
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeNoLoginMethod {
		t.Fatalf("enable before the provider: %v", err)
	}
	providerOn = true
	if _, err := e.Update(ctx, "admin", c.ID, ConnectorInput{Enabled: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Update(ctx, "admin", c.ID, ConnectorInput{Options: &Options{LoginMethod: LoginLDAPBind}}); !errors.As(err, &ve) {
		t.Fatalf("ldap_bind on a google connector: %v", err)
	}
	if _, err := e.Update(ctx, "admin", c.ID, ConnectorInput{Options: &Options{}}); !errors.As(err, &de) || de.Code != CodeNoLoginMethod {
		t.Fatalf("enabled connector without a login method: %v", err)
	}
}

// TestGoogleSyncAutoProvisions fails if a Google Workspace sync with range
// auto-provisioning on (P12) does not give a user without an extension a
// number from the connector's range, does not link one whose phone names an
// existing extension, provisions an adopted local user only once it is
// adopted, or touches a break-glass admin with a matching email.
func TestGoogleSyncAutoProvisions(t *testing.T) {
	f := newFakeGoogle(t, 4) // u0 to u3, work phones +1 555 0100 to 0103
	m := NewMem()
	e := &Engine{Store: m, Drivers: map[string]Driver{KindGoogle: f.driver()}, HTTP: f.srv.Client(), Replica: "r1",
		LoginCheck: func(context.Context, string) error { return nil }}
	ctx := context.Background()
	linked := m.AddExt("15550101", false)
	legacy := m.AddUser(LocalUser{Username: "legacy", Email: "user2@example.com", Enabled: true})
	root := m.AddUser(LocalUser{Username: "root", Email: "user3@example.com", Role: "admin", Enabled: true, BreakGlass: true})
	key := f.keyFile()
	ap := AutoProvision{Enabled: true, RangeStart: "300", RangeEnd: "309", Device: true, Voicemail: true}
	c, err := e.Create(ctx, "admin", ConnectorInput{Kind: KindGoogle, Name: strPtr("GW"), Secret: &key, Enabled: boolPtr(true), AutoProvision: &ap})
	if err != nil {
		t.Fatal(err)
	}
	pre, err := e.RequestRun(ctx, "admin", c.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	e.TickOnce(ctx)
	if pre, _ = m.GetRun(ctx, pre.ID); pre.Status != StatusPreview {
		t.Fatalf("preview = %+v", pre)
	}
	cs, _ := m.ListChanges(ctx, pre.ID, ChangeFilter{})
	byExt := map[string]Change{}
	for _, ch := range cs {
		byExt[ch.ExternalID] = ch
	}
	inRange := func(n string) bool { return len(n) == 3 && n >= "300" && n <= "309" }
	if u0 := byExt["u0"]; u0.Action != ActionCreate || u0.Reason != ReasonAutoProvisioned || !inRange(u0.FieldDiffs["extension"].After) {
		t.Fatalf("u0 = %+v", u0)
	}
	if u1 := byExt["u1"]; u1.Action != ActionCreate || u1.Reason == ReasonAutoProvisioned || u1.FieldDiffs["extension"].After != "15550101" {
		t.Fatalf("u1 = %+v", u1)
	}
	if u2 := byExt["u2"]; u2.Action != ActionAdopt {
		t.Fatalf("u2 = %+v", u2)
	}
	if u3 := byExt["u3"]; u3.Action != ActionSkip || u3.Reason != ReasonProtected {
		t.Fatalf("u3 = %+v", u3)
	}
	if len(m.Provisioned) != 0 {
		t.Fatal("a preview provisioned")
	}
	if _, err := e.Apply(ctx, "admin", pre.ID, Decisions{Adopt: []string{"u2"}}); err != nil {
		t.Fatal(err)
	}
	var u0 *MemUser
	for _, u := range m.Users {
		if u.ExternalID == "u0" {
			u0 = u
		}
	}
	if u0 == nil || u0.ConnectorID != c.ID || !inRange(u0.ProvisionedNumber) || m.Provisioned[u0.ProvisionedNumber].Connector != c.ID {
		t.Fatalf("u0 = %+v, provisioned = %v", u0, m.Provisioned)
	}
	for _, u := range m.Users {
		if u.ExternalID == "u1" && (u.OwnedExtension != linked || u.ProvisionedNumber != "") {
			t.Fatalf("u1 = %+v", u)
		}
	}
	lg := m.Users[legacy]
	if lg.ConnectorID != c.ID || lg.ExternalID != "u2" || !inRange(lg.ProvisionedNumber) || lg.ProvisionedNumber == u0.ProvisionedNumber {
		t.Fatalf("adopted = %+v", lg)
	}
	if r := m.Users[root]; r.ConnectorID != 0 || r.ProvisionedNumber != "" || r.OwnedExtension != 0 {
		t.Fatalf("break-glass admin touched: %+v", r)
	}
	if len(m.Provisioned) != 2 {
		t.Fatalf("provisioned = %v", m.Provisioned)
	}
	// A second run allocates nothing more and never renumbers.
	before := lg.ProvisionedNumber
	run, err := e.RequestRun(ctx, "admin", c.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	e.TickOnce(ctx)
	if run, _ = m.GetRun(ctx, run.ID); run.Status != StatusSucceeded || len(m.Provisioned) != 2 || m.Users[legacy].ProvisionedNumber != before {
		t.Fatalf("re-run = %+v, provisioned = %v", run, m.Provisioned)
	}
}
