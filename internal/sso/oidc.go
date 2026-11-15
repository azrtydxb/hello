package sso

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/azrtydxb/hello/internal/safehttp"
)

// discoveryTTL is how long a provider's discovery document is reused. The key
// set inside refreshes itself when a token names an unknown kid.
const discoveryTTL = time.Hour

// discovery caches go-oidc providers by issuer.
type discovery struct {
	mu sync.Mutex
	m  map[string]discovered
}

type discovered struct {
	p  *oidc.Provider
	at time.Time
}

func (d *discovery) forget(issuer string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.m, issuer)
}

func (s *Service) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	// Discovery and keys come from the pinned public issuer only (T-3, T-6).
	return safehttp.NewClient(safehttp.Options{Timeout: 10 * time.Second})
}

func (s *Service) issuer(p Provider) string {
	if s.IssuerFor != nil {
		return s.IssuerFor(p)
	}
	return p.Issuer()
}

// provider returns the discovered OIDC provider of p. go-oidc refuses a
// discovery document whose issuer differs from the pinned one.
func (s *Service) provider(ctx context.Context, p Provider) (*oidc.Provider, error) {
	iss := s.issuer(p)
	s.disc.mu.Lock()
	if d, ok := s.disc.m[iss]; ok && s.now().Sub(d.at) < discoveryTTL {
		s.disc.mu.Unlock()
		return d.p, nil
	}
	s.disc.mu.Unlock()
	// The provider keeps this context for its later key fetches, so it is
	// not the request's; the client's own timeout bounds each fetch.
	op, err := oidc.NewProvider(oidc.ClientContext(context.WithoutCancel(ctx), s.client()), iss)
	if err != nil {
		return nil, err
	}
	s.disc.mu.Lock()
	if s.disc.m == nil {
		s.disc.m = map[string]discovered{}
	}
	s.disc.m[iss] = discovered{op, s.now()}
	s.disc.mu.Unlock()
	return op, nil
}

func random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func bindingHash(v string) string {
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:])
}

// enabledProvider loads p and its secret, refusing one that is off.
func (s *Service) enabledProvider(ctx context.Context, name string) (Provider, string, error) {
	if !s.available() {
		return Provider{}, "", ErrUnavailable
	}
	if !ValidProvider(name) {
		return Provider{}, "", ErrNotFound
	}
	p, err := s.Store.GetProvider(ctx, name)
	if errors.Is(err, ErrNotFound) {
		return p, "", ErrProviderOff
	}
	if err != nil {
		return p, "", err
	}
	if !p.Enabled || p.ClientID == "" || !p.HasClientSecret {
		return p, "", ErrProviderOff
	}
	secret, err := s.Store.ProviderSecret(ctx, name)
	if err != nil {
		return p, "", err
	}
	return p, secret, nil
}

func (s *Service) oauthConfig(op *oidc.Provider, p Provider, secret string) *oauth2.Config {
	return &oauth2.Config{ClientID: p.ClientID, ClientSecret: secret, Endpoint: op.Endpoint(), RedirectURL: s.RedirectURI(p.Provider),
		Scopes: []string{oidc.ScopeOpenID, "email", "profile"}}
}

// Start begins a sign-in (userID 0) or the linking of an identity to the
// signed-in user userID. It returns the IdP URL to send the browser to and
// the value of the hello_oidc binding cookie.
func (s *Service) Start(ctx context.Context, name string, userID int64, next string) (string, string, error) {
	p, secret, err := s.enabledProvider(ctx, name)
	if err != nil {
		return "", "", err
	}
	op, err := s.provider(ctx, p)
	if err != nil {
		s.log().Warn("sso: discovery failed", "provider", name, "err", err)
		return "", "", ErrUnavailable
	}
	state, nonce, binding := random(32), random(32), random(32)
	verifier := oauth2.GenerateVerifier()
	st := State{Kind: KindLogin, Provider: name, Nonce: nonce, Verifier: verifier, Binding: bindingHash(binding), Next: safeNext(next)}
	if userID > 0 {
		st.Kind, st.UserID, st.Next = KindLink, userID, ""
	}
	if err := s.States.Put(ctx, state, st, StateTTL); err != nil {
		return "", "", ErrUnavailable
	}
	opts := []oauth2.AuthCodeOption{oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)}
	if p.Provider == ProviderGoogle && len(p.Domains) == 1 {
		// A hint only: the hd claim is what is checked.
		opts = append(opts, oauth2.SetAuthURLParam("hd", p.Domains[0]))
	}
	if st.Kind == KindLink {
		opts = append(opts, oauth2.SetAuthURLParam("prompt", "select_account"))
	}
	return s.oauthConfig(op, p, secret).AuthCodeURL(state, opts...), binding, nil
}

// safeNext keeps only a console path.
func safeNext(n string) string {
	if !strings.HasPrefix(n, "/") || strings.HasPrefix(n, "//") || strings.ContainsAny(n, "\\\r\n") || len(n) > 512 {
		return ""
	}
	return n
}

// Outcome is a completed callback.
type Outcome struct {
	Kind     string
	UserID   int64
	Username string
	Next     string
	// Linked is set when this sign-in created the link (first login).
	Linked bool
}

// Claims are the ID token claims Hello reads.
type Claims struct {
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified *bool  `json:"email_verified"`
	Name          string `json:"name"`
	Nonce         string `json:"nonce"`
	AZP           string `json:"azp"`
	// Google Workspace domain.
	HD string `json:"hd"`
	// Entra object id, tenant id and the sign-in name.
	OID               string `json:"oid"`
	TID               string `json:"tid"`
	PreferredUsername string `json:"preferred_username"`
}

// subject is the stable key of the identity: Google's sub, Entra's oid
// (Entra's sub is pairwise per application, oid is the user).
func (c Claims) subject(provider string) string {
	if provider == ProviderEntra {
		return strings.ToLower(c.OID)
	}
	return c.Subject
}

func (c Claims) tenant(provider string) string {
	if provider == ProviderEntra {
		return strings.ToLower(c.TID)
	}
	return strings.ToLower(c.HD)
}

func (c Claims) email(provider string) string {
	if c.Email != "" {
		return c.Email
	}
	if provider == ProviderEntra {
		return c.PreferredUsername
	}
	return ""
}

// Callback completes a sign-in, a link or nothing. state is single use: it is
// taken first, so a replay fails whatever else is wrong. The binding cookie
// must be the one Start set in this browser. Every check fails closed.
func (s *Service) Callback(ctx context.Context, name string, q url.Values, binding string) (Outcome, error) {
	if !s.available() {
		return Outcome{}, ErrUnavailable
	}
	stateParam := q.Get("state")
	if stateParam == "" || len(stateParam) > 256 {
		return Outcome{}, ErrState
	}
	st, err := s.States.Take(ctx, stateParam)
	if err != nil {
		return Outcome{}, err
	}
	if st.Provider != name || (st.Kind != KindLogin && st.Kind != KindLink) || binding == "" ||
		subtle.ConstantTimeCompare([]byte(bindingHash(binding)), []byte(st.Binding)) != 1 {
		return Outcome{}, ErrState
	}
	if q.Get("error") != "" {
		return Outcome{}, ErrDenied
	}
	code := q.Get("code")
	if code == "" || len(code) > 4096 {
		return Outcome{}, ErrDenied
	}
	p, secret, err := s.enabledProvider(ctx, name)
	if err != nil {
		return Outcome{}, err
	}
	op, err := s.provider(ctx, p)
	if err != nil {
		s.log().Warn("sso: discovery failed", "provider", name, "err", err)
		return Outcome{}, ErrUnavailable
	}
	xctx, cancel := context.WithTimeout(oidc.ClientContext(ctx, s.client()), 15*time.Second)
	defer cancel()
	tok, err := s.oauthConfig(op, p, secret).Exchange(xctx, code, oauth2.VerifierOption(st.Verifier))
	if err != nil {
		return Outcome{}, ErrDenied
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return Outcome{}, ErrToken
	}
	c, err := s.verify(xctx, op, p, raw, st.Nonce)
	if err != nil {
		return Outcome{}, err
	}
	subject := c.subject(name)
	ident := Identity{Provider: name, Subject: subject, Tenant: c.tenant(name), Email: c.email(name)}
	if st.Kind == KindLink {
		ident.UserID = st.UserID
		u, err := s.Store.GetUser(ctx, st.UserID)
		if err != nil || !u.Enabled {
			return Outcome{}, ErrUserDisabled
		}
		if err := s.Store.Link(ctx, "user:"+u.Username, ident); err != nil {
			return Outcome{}, err
		}
		return Outcome{Kind: KindLink, UserID: u.ID, Username: u.Username, Linked: true}, nil
	}
	u, linked, err := s.resolve(ctx, p, c, ident)
	if err != nil {
		return Outcome{}, err
	}
	if !u.Enabled {
		return Outcome{}, ErrUserDisabled
	}
	if err := s.Store.TouchIdentity(ctx, name, subject, s.now()); err != nil {
		s.log().Warn("sso: touch identity", "err", err)
	}
	return Outcome{Kind: KindLogin, UserID: u.ID, Username: u.Username, Next: st.Next, Linked: linked}, nil
}

// verify checks the ID token (I-26): signature from the issuer's keys, iss,
// aud, exp (go-oidc), then the nonce, azp when present, and the tenant: tid
// for Entra; hd and a verified email for Google.
func (s *Service) verify(ctx context.Context, op *oidc.Provider, p Provider, raw, nonce string) (Claims, error) {
	var c Claims
	cfg := &oidc.Config{ClientID: p.ClientID, SupportedSigningAlgs: []string{oidc.RS256}}
	if s.Now != nil {
		cfg.Now = s.Now
	}
	idt, err := op.Verifier(cfg).Verify(ctx, raw)
	if err != nil {
		return c, ErrToken
	}
	if err := idt.Claims(&c); err != nil {
		return c, ErrToken
	}
	if nonce == "" || subtle.ConstantTimeCompare([]byte(c.Nonce), []byte(nonce)) != 1 {
		return c, ErrToken
	}
	if c.AZP != "" && c.AZP != p.ClientID {
		return c, ErrToken
	}
	switch p.Provider {
	case ProviderEntra:
		if !strings.EqualFold(c.TID, p.Tenant) || !guidRe.MatchString(c.OID) {
			return c, ErrToken
		}
	case ProviderGoogle:
		if c.Subject == "" || c.EmailVerified == nil || !*c.EmailVerified || !inDomains(c.HD, p.Domains) {
			return c, ErrToken
		}
	}
	return c, nil
}

func inDomains(hd string, ds []string) bool {
	for _, d := range ds {
		if hd != "" && strings.EqualFold(hd, d) {
			return true
		}
	}
	return false
}

// resolve maps a verified identity to a user (I-28): an existing link; else,
// on the first sign-in, the user a connector of this provider's kind synced
// under the same stable id (Google id = sub, Entra objectId = oid); else, for
// Google, the one synced user with the verified Workspace email, never an
// admin (T-7); else a just-in-time user when the provider allows it. Local
// users are never matched: they link themselves while signed in.
func (s *Service) resolve(ctx context.Context, p Provider, c Claims, ident Identity) (User, bool, error) {
	u, err := s.Store.IdentityUser(ctx, p.Provider, ident.Subject)
	if err == nil {
		return u, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return u, false, err
	}
	actor := "sso:" + p.Provider
	u, err = s.Store.SyncedUser(ctx, p.Provider, ident.Subject)
	switch {
	case err == nil:
		if p.Provider == ProviderEntra && guidRe.MatchString(u.ConnectorTenant) && !strings.EqualFold(u.ConnectorTenant, c.TID) {
			return User{}, false, ErrNoUser
		}
	case errors.Is(err, ErrNotFound):
		u, err = s.byEmail(ctx, p, c)
		if errors.Is(err, ErrNoUser) && p.JIT {
			return s.jit(ctx, p, c, ident)
		}
		if err != nil {
			return User{}, false, err
		}
	default:
		return User{}, false, err
	}
	ident.UserID = u.ID
	if err := s.Store.Link(ctx, actor, ident); err != nil {
		return User{}, false, err
	}
	return u, true, nil
}

// byEmail is the Google email match: exactly one synced, non-admin user.
func (s *Service) byEmail(ctx context.Context, p Provider, c Claims) (User, error) {
	if p.Provider != ProviderGoogle || c.Email == "" {
		return User{}, ErrNoUser
	}
	at := strings.LastIndex(c.Email, "@")
	if at < 0 || !strings.EqualFold(c.Email[at+1:], c.HD) && !inDomains(c.Email[at+1:], p.Domains) {
		return User{}, ErrNoUser
	}
	us, err := s.Store.SyncedUsersByEmail(ctx, c.Email)
	if err != nil {
		return User{}, err
	}
	var match []User
	for _, u := range us {
		if u.ConnectorID != 0 && !u.BreakGlass && u.Role != "admin" {
			match = append(match, u)
		}
	}
	if len(match) != 1 {
		return User{}, ErrNoUser
	}
	return match[0], nil
}

// jit creates a user for a verified identity of the provider's tenant.
func (s *Service) jit(ctx context.Context, p Provider, c Claims, ident Identity) (User, bool, error) {
	email := c.email(p.Provider)
	if email == "" || (p.Provider == ProviderGoogle && (c.EmailVerified == nil || !*c.EmailVerified)) {
		return User{}, false, ErrNoUser
	}
	name := strings.TrimSpace(c.Name)
	if len(name) > 100 {
		name = name[:100]
	}
	u, err := s.Store.CreateUser(ctx, "sso:"+p.Provider, strings.ToLower(email), email, name, ident)
	if err != nil {
		return User{}, false, err
	}
	return u, true, nil
}

// TestResult is a provider check: discovery from the pinned issuer and its
// signing keys. It carries no secret.
type TestResult struct {
	OK          bool   `json:"ok"`
	Issuer      string `json:"issuer"`
	Keys        int    `json:"keys"`
	ErrorDetail string `json:"errorDetail"`
}

// TestProvider fetches provider name's discovery document from its pinned
// issuer and counts its signing keys, audited. A provider need not be
// enabled to be tested.
func (s *Service) TestProvider(ctx context.Context, actor, name string) (TestResult, error) {
	if !ValidProvider(name) {
		return TestResult{}, ErrNotFound
	}
	p, err := s.Store.GetProvider(ctx, name)
	if errors.Is(err, ErrNotFound) {
		p, err = Provider{Provider: name}, nil
	}
	if err != nil {
		return TestResult{}, err
	}
	if err := s.Store.Audit(ctx, actor, "test", "sso_provider", name); err != nil {
		return TestResult{}, err
	}
	res := TestResult{Issuer: s.issuer(p)}
	if p.Provider == ProviderEntra && !guidRe.MatchString(p.Tenant) {
		res.ErrorDetail = "set the tenant id first"
		return res, nil
	}
	s.disc.forget(res.Issuer)
	op, err := s.provider(ctx, p)
	if err != nil {
		res.ErrorDetail = "the discovery document could not be read from the pinned issuer"
		return res, nil
	}
	var meta struct {
		JWKS string `json:"jwks_uri"`
	}
	if err := op.Claims(&meta); err != nil || meta.JWKS == "" {
		res.ErrorDetail = "the discovery document names no key set"
		return res, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, meta.JWKS, nil)
	if err != nil {
		res.ErrorDetail = "the key set URL is not valid"
		return res, nil
	}
	resp, err := s.client().Do(req)
	if err != nil {
		res.ErrorDetail = "the key set could not be fetched"
		return res, nil
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := safehttp.ReadBody(resp.Body, 1<<20)
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err != nil || resp.StatusCode != http.StatusOK || json.Unmarshal(body, &set) != nil || len(set.Keys) == 0 {
		res.ErrorDetail = "the key set is empty or unreadable"
		return res, nil
	}
	res.OK, res.Keys = true, len(set.Keys)
	return res, nil
}
