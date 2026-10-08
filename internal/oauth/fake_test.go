package oauth

import (
	"bytes"
	"context"
	"encoding/hex"
	"slices"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
)

// fakeStore is an in-memory Store with the semantics *store.Store has over
// PostgreSQL (internal/store tests the SQL itself).
type fakeStore struct {
	mu       sync.Mutex
	clients  map[string]Client
	requests map[string]*fakeRequest
	grants   map[int64]*Grant
	tokens   map[string]*fakeToken
	secrets  map[int64]*fakeSecret
	audit    []string
	seq      int64
}

type fakeRequest struct {
	AuthRequest
	codeHash    []byte
	codeExpires time.Time
	used        bool
}

type fakeToken struct {
	Token
	spent, revoked bool
}

type fakeSecret struct {
	Secret
	client string
	hash   []byte
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		clients: map[string]Client{}, requests: map[string]*fakeRequest{}, grants: map[int64]*Grant{},
		tokens: map[string]*fakeToken{}, secrets: map[int64]*fakeSecret{},
	}
}

func (f *fakeStore) record(ctx context.Context, actor, action, resource string) {
	via := ""
	if a, ok := auth.ActorFrom(ctx); ok {
		via = a.ClientID
	}
	f.audit = append(f.audit, actor+" "+action+" "+resource+" via="+via)
}

func (f *fakeStore) Client(_ context.Context, id string) (Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.clients[id]
	if !ok {
		return Client{}, ErrNotFound
	}
	return c, nil
}

func (f *fakeStore) SaveCIMDClient(_ context.Context, c Client) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clients[c.ID] = c
	return nil
}

func (f *fakeStore) RegisterClient(ctx context.Context, actor string, c Client) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c.CreatedAt = time.Now()
	f.clients[c.ID] = c
	f.record(ctx, actor, "register", "oauth_client")
	return nil
}

func (f *fakeStore) CreateRequest(_ context.Context, r AuthRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r.CreatedAt = time.Now()
	f.requests[r.ID] = &fakeRequest{AuthRequest: r}
	return nil
}

func (f *fakeStore) Request(_ context.Context, id string) (AuthRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.requests[id]
	if !ok || r.UserID != 0 || !r.ExpiresAt.After(time.Now()) {
		return AuthRequest{}, ErrNotFound
	}
	return r.AuthRequest, nil
}

func (f *fakeStore) ApproveRequest(ctx context.Context, actor, id string, userID int64, scopes auth.Scopes, codeHash []byte, codeExpires time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.requests[id]
	if !ok || r.UserID != 0 || !r.ExpiresAt.After(time.Now()) {
		return ErrNotFound
	}
	r.UserID, r.Scopes, r.codeHash, r.codeExpires = userID, scopes, codeHash, codeExpires
	f.record(ctx, actor, "approve", "oauth_request")
	return nil
}

func (f *fakeStore) DenyRequest(ctx context.Context, actor, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.requests[id]; !ok || r.UserID != 0 {
		return ErrNotFound
	}
	delete(f.requests, id)
	f.record(ctx, actor, "deny", "oauth_request")
	return nil
}

func (f *fakeStore) revokeGrantLocked(id int64) {
	g := f.grants[id]
	if g == nil || g.RevokedAt != nil {
		return
	}
	now := time.Now()
	g.RevokedAt = &now
	for _, t := range f.tokens {
		if t.GrantID == id {
			t.revoked = true
		}
	}
}

func (f *fakeStore) RedeemCode(ctx context.Context, actor string, codeHash []byte, issue func(AuthRequest) (Grant, []Token, error)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var r *fakeRequest
	for _, q := range f.requests {
		if q.codeHash != nil && bytes.Equal(q.codeHash, codeHash) {
			r = q
		}
	}
	if r == nil {
		return ErrInvalidGrant
	}
	if r.used {
		for id, g := range f.grants {
			if g.UserID == r.UserID && g.ClientID == r.ClientID && !g.CreatedAt.Before(r.CreatedAt) {
				f.revokeGrantLocked(id)
			}
		}
		f.record(ctx, "system", "revoke_reused_code", "oauth_grant")
		return ErrReused
	}
	if !r.codeExpires.After(time.Now()) {
		return ErrInvalidGrant
	}
	g, toks, err := issue(r.AuthRequest)
	if err != nil {
		return err
	}
	r.used = true
	f.seq++
	g.ID, g.CreatedAt = f.seq, time.Now()
	f.grants[g.ID] = &g
	for _, t := range toks {
		t.GrantID = g.ID
		f.tokens[hex.EncodeToString(t.Hash)] = &fakeToken{Token: t}
	}
	f.record(ctx, actor, "issue", "oauth_grant")
	return nil
}

func (f *fakeStore) Refresh(ctx context.Context, actor string, hash []byte, maxAge time.Duration, issue func(Token, Grant) ([]Token, error)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.tokens[hex.EncodeToString(hash)]
	if t == nil || t.Kind != TokenRefresh {
		return ErrInvalidGrant
	}
	if t.spent {
		f.revokeGrantLocked(t.GrantID)
		f.record(ctx, "system", "revoke_reused_refresh", "oauth_grant")
		return ErrReused
	}
	g := f.grants[t.GrantID]
	if t.revoked || !t.ExpiresAt.After(time.Now()) || g == nil || g.RevokedAt != nil || time.Since(g.CreatedAt) > maxAge {
		return ErrInvalidGrant
	}
	toks, err := issue(t.Token, *g)
	if err != nil {
		return err
	}
	t.spent = true
	for _, n := range toks {
		f.tokens[hex.EncodeToString(n.Hash)] = &fakeToken{Token: n}
	}
	f.record(ctx, actor, "refresh", "oauth_grant")
	return nil
}

func (f *fakeStore) ClientCredentials(ctx context.Context, clientID string, secretHash []byte, issue func(Client) (Token, error)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.clients[clientID]
	if !ok || c.Kind != ClientService || !c.Enabled {
		return ErrInvalidGrant
	}
	var sec *fakeSecret
	for _, s := range f.secrets {
		if s.client == clientID && bytes.Equal(s.hash, secretHash) && s.RevokedAt == nil &&
			(s.ExpiresAt == nil || s.ExpiresAt.After(time.Now())) {
			sec = s
		}
	}
	if sec == nil {
		return ErrInvalidGrant
	}
	t, err := issue(c)
	if err != nil {
		return err
	}
	f.tokens[hex.EncodeToString(t.Hash)] = &fakeToken{Token: t}
	f.record(ctx, "service:"+c.Name, "issue", "oauth_token")
	return nil
}

func (f *fakeStore) RevokeToken(ctx context.Context, actor string, hash []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.tokens[hex.EncodeToString(hash)]
	if t == nil {
		return nil
	}
	t.revoked = true
	if t.Kind == TokenRefresh {
		f.revokeGrantLocked(t.GrantID)
	}
	f.record(ctx, actor, "revoke", "oauth_token")
	return nil
}

func (f *fakeStore) Grants(_ context.Context, userID int64) ([]Grant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []Grant{}
	for _, g := range f.grants {
		if g.RevokedAt == nil && (userID == 0 || g.UserID == userID) {
			out = append(out, *g)
		}
	}
	slices.SortFunc(out, func(a, b Grant) int { return int(a.ID - b.ID) })
	return out, nil
}

func (f *fakeStore) RevokeGrant(ctx context.Context, actor string, id, userID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := f.grants[id]
	if g == nil || g.RevokedAt != nil || (userID != 0 && g.UserID != userID) {
		return ErrNotFound
	}
	f.revokeGrantLocked(id)
	f.record(ctx, actor, "revoke", "oauth_grant")
	return nil
}

func (f *fakeStore) ServiceAccounts(context.Context) ([]Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []Client{}
	for _, c := range f.clients {
		if c.Kind == ClientService {
			out = append(out, f.withSecrets(c))
		}
	}
	return out, nil
}

func (f *fakeStore) withSecrets(c Client) Client {
	c.Secrets = []Secret{}
	for _, s := range f.secrets {
		if s.client == c.ID && s.RevokedAt == nil {
			c.Secrets = append(c.Secrets, s.Secret)
		}
	}
	return c
}

func (f *fakeStore) ServiceAccount(_ context.Context, id string) (Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.clients[id]
	if !ok || c.Kind != ClientService {
		return Client{}, ErrNotFound
	}
	return f.withSecrets(c), nil
}

func (f *fakeStore) CreateServiceAccount(ctx context.Context, actor string, c Client) (Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range f.clients {
		if o.Kind == ClientService && o.Name == c.Name {
			return Client{}, ErrConflict
		}
	}
	c.CreatedAt = time.Now()
	f.clients[c.ID] = c
	f.record(ctx, actor, "create", "service_account")
	return f.withSecrets(c), nil
}

func (f *fakeStore) UpdateServiceAccount(ctx context.Context, actor, id string, ch ServiceAccountChange) (Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.clients[id]
	if !ok || c.Kind != ClientService {
		return Client{}, ErrNotFound
	}
	if ch.Description != nil {
		c.Description = *ch.Description
	}
	if ch.Role != nil {
		c.Role = *ch.Role
	}
	if ch.Scopes != nil {
		c.Scopes = *ch.Scopes
	}
	if ch.Enabled != nil {
		c.Enabled = *ch.Enabled
	}
	if ch.Enabled != nil || ch.Scopes != nil || ch.Role != nil {
		for _, t := range f.tokens {
			if t.ClientID == id {
				t.revoked = true
			}
		}
	}
	f.clients[id] = c
	f.record(ctx, actor, "update", "service_account")
	return f.withSecrets(c), nil
}

func (f *fakeStore) DeleteServiceAccount(ctx context.Context, actor, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.clients[id]; !ok || c.Kind != ClientService {
		return ErrNotFound
	}
	delete(f.clients, id)
	for k, t := range f.tokens {
		if t.ClientID == id {
			delete(f.tokens, k)
		}
	}
	f.record(ctx, actor, "delete", "service_account")
	return nil
}

func (f *fakeStore) AddClientSecret(ctx context.Context, actor, clientID string, hash []byte, expires *time.Time) (Secret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.secrets {
		if s.client == clientID && s.RevokedAt == nil && (s.ExpiresAt == nil || s.ExpiresAt.After(time.Now())) {
			n++
		}
	}
	if n >= 2 {
		return Secret{}, ErrTooManySecrets
	}
	f.seq++
	s := &fakeSecret{Secret: Secret{ID: f.seq, CreatedAt: time.Now(), ExpiresAt: expires}, client: clientID, hash: hash}
	f.secrets[s.ID] = s
	f.record(ctx, actor, "create", "client_secret")
	return s.Secret, nil
}

func (f *fakeStore) RevokeClientSecret(ctx context.Context, actor, clientID string, secretID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.secrets[secretID]
	if s == nil || s.client != clientID || s.RevokedAt != nil {
		return ErrNotFound
	}
	now := time.Now()
	s.RevokedAt = &now
	for _, t := range f.tokens {
		if t.ClientID == clientID {
			t.revoked = true
		}
	}
	f.record(ctx, actor, "revoke", "client_secret")
	return nil
}

func (f *fakeStore) PruneOAuth(context.Context, time.Time) (int64, error) { return 0, nil }

// TokenActor is the access-token half of store.TokenActor, for checking
// what an issued token admits.
func (f *fakeStore) TokenActor(_ context.Context, hash []byte) (auth.Actor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.tokens[hex.EncodeToString(hash)]
	if t == nil || t.Kind != TokenAccess || t.revoked || !t.ExpiresAt.After(time.Now()) {
		return auth.Actor{}, auth.ErrNoCredentials
	}
	if g := f.grants[t.GrantID]; t.GrantID != 0 && (g == nil || g.RevokedAt != nil) {
		return auth.Actor{}, auth.ErrNoCredentials
	}
	c := f.clients[t.ClientID]
	a := auth.Actor{UserID: t.UserID, Kind: auth.KindOAuth, Scopes: t.Scopes, ClientID: t.ClientID, Audience: t.Resources, Role: auth.RoleAdmin}
	if c.Kind == ClientService {
		if !c.Enabled {
			return auth.Actor{}, auth.ErrNoCredentials
		}
		a.Kind, a.ServiceName, a.Role = auth.KindService, c.Name, c.Role
	}
	return a, nil
}

func (f *fakeStore) SessionActor(context.Context, []byte) (auth.Actor, error) {
	return auth.Actor{}, auth.ErrNoCredentials
}

// UserActor is the lookup of the in-product agent, unused by OAuth.
func (*fakeStore) UserActor(context.Context, int64) (auth.Actor, error) {
	return auth.Actor{}, auth.ErrNoCredentials
}
