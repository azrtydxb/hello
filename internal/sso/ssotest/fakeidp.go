// Package ssotest is a fake OIDC identity provider for tests: discovery, a
// key set, an authorization step the test drives, and a token endpoint that
// checks the PKCE verifier and the client credentials. It signs ID tokens
// whose claims a test can bend, to prove every check fails closed.
package ssotest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// IdP is a fake identity provider serving two issuers: Google's shape at the
// root and Entra's at /<tenant>/v2.0.
type IdP struct {
	Srv          *httptest.Server
	ClientID     string
	ClientSecret string
	Key          *rsa.PrivateKey
	KID          string
	// Exchanges counts token requests.
	Exchanges int

	mu    sync.Mutex
	codes map[string]grant
}

type grant struct {
	claims    map[string]any
	raw       string
	challenge string
	redirect  string
	issuer    string
}

// New starts a fake IdP for clientID and secret.
func New(t *testing.T, clientID, secret string) *IdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &IdP{ClientID: clientID, ClientSecret: secret, Key: key, KID: "k1", codes: map[string]grant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", f.serve)
	f.Srv = httptest.NewServer(mux)
	t.Cleanup(f.Srv.Close)
	return f
}

// GoogleIssuer and EntraIssuer are the issuers the fake serves.
func (f *IdP) GoogleIssuer() string             { return f.Srv.URL }
func (f *IdP) EntraIssuer(tenant string) string { return f.Srv.URL + "/" + tenant + "/v2.0" }

func (f *IdP) serve(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case strings.HasSuffix(p, "/.well-known/openid-configuration"):
		iss := f.Srv.URL + strings.TrimSuffix(p, "/.well-known/openid-configuration")
		writeJSON(w, map[string]any{
			"issuer": iss, "authorization_endpoint": iss + "/authorize", "token_endpoint": f.Srv.URL + "/token",
			"jwks_uri": f.Srv.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
		})
	case p == "/jwks":
		writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &f.Key.PublicKey, KeyID: f.KID, Algorithm: "RS256", Use: "sig"}}})
	case p == "/token":
		f.token(w, r)
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// Authorize is the person signing in at the IdP: it reads the authorization
// URL Hello built and returns the code the IdP would redirect back with,
// for an ID token with claims (iss, aud, nonce, iat and exp are filled in
// unless claims sets them; a nil value removes one).
func (f *IdP) Authorize(t *testing.T, authURL string, claims map[string]any) (state, code string) {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("client_id") != f.ClientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" ||
		q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("state") == "" || !strings.Contains(q.Get("scope"), "openid") {
		t.Fatalf("authorization request is missing a required parameter: %v", q)
	}
	iss := f.Srv.URL + strings.TrimSuffix(u.Path, "/authorize")
	c := map[string]any{"iss": iss, "aud": f.ClientID, "nonce": q.Get("nonce"),
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
	for k, v := range claims {
		if v == nil {
			delete(c, k)
			continue
		}
		c[k] = v
	}
	code = randomString()
	f.mu.Lock()
	f.codes[code] = grant{claims: c, challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), issuer: iss}
	f.mu.Unlock()
	return q.Get("state"), code
}

func (f *IdP) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	f.Exchanges++
	g, ok := f.codes[r.PostForm.Get("code")]
	delete(f.codes, r.PostForm.Get("code"))
	f.mu.Unlock()
	id, secret, basic := r.BasicAuth()
	if !basic {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	id, _ = url.QueryUnescape(id)
	secret, _ = url.QueryUnescape(secret)
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !ok || id != f.ClientID || secret != f.ClientSecret || r.PostForm.Get("grant_type") != "authorization_code" ||
		base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge || r.PostForm.Get("redirect_uri") != g.redirect {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	idt := g.raw
	if idt == "" {
		idt = f.Sign(g.claims, f.Key, f.KID)
	}
	writeJSON(w, map[string]any{"access_token": "at-" + randomString(), "token_type": "Bearer", "expires_in": 3600, "id_token": idt})
}

// Sign makes a compact RS256 JWS of claims with key and kid.
func (f *IdP) Sign(claims map[string]any, key *rsa.PrivateKey, kid string) string {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))
	if err != nil {
		panic(err)
	}
	b, _ := json.Marshal(claims)
	obj, err := signer.Sign(b)
	if err != nil {
		panic(err)
	}
	s, _ := obj.CompactSerialize()
	return s
}

// ForgeCode registers a code whose ID token is signed by another key under
// the IdP's kid: the signature check must refuse it.
func (f *IdP) ForgeCode(authURL string, claims map[string]any) (state, code string) {
	u, _ := url.Parse(authURL)
	q := u.Query()
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	iss := f.Srv.URL + strings.TrimSuffix(u.Path, "/authorize")
	c := map[string]any{"iss": iss, "aud": f.ClientID, "nonce": q.Get("nonce"), "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
	for k, v := range claims {
		c[k] = v
	}
	code = randomString()
	f.mu.Lock()
	f.codes[code] = grant{raw: f.Sign(c, other, f.KID), challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), issuer: iss}
	f.mu.Unlock()
	return q.Get("state"), code
}

func randomString() string {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	return n.Text(36)
}
