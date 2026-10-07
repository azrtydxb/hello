package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/oauth"
)

// TestTokenLifecycle (store half) fails if a legacy token loses a scope, a
// personal token is not resolved with its scopes, an expired one is
// admitted, the first user is not an admin, an OAuth access token is not
// resolved with its client, audience and the user's role, or an OAuth
// change's audit row lacks via.
func TestTokenLifecycle(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	uid, err := s.CreateUser(ctx, "test", "alice", "x")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := s.CreateUser(ctx, "test", "bob", "x")
	if err != nil {
		t.Fatal(err)
	}
	var role, bobRole string
	_ = s.db.QueryRowContext(ctx, `SELECT role FROM users WHERE id = $1`, uid).Scan(&role)
	_ = s.db.QueryRowContext(ctx, `SELECT role FROM users WHERE id = $1`, bob).Scan(&bobRole)
	if role != "admin" || bobRole != "viewer" {
		t.Fatalf("roles = %s, %s; want the first user admin, the next viewer", role, bobRole)
	}

	legacy, lh := auth.NewToken()
	if _, err := s.CreateToken(ctx, "test", uid, NewToken{Name: "old"}, lh); err != nil {
		t.Fatal(err)
	}
	a, err := s.TokenActor(ctx, auth.HashToken(legacy))
	if err != nil || a.Kind != auth.KindLegacyToken || !a.Scopes.Has(auth.ScopeSecrets) || a.Role != auth.RoleAdmin || a.TokenID == 0 {
		t.Fatalf("legacy actor = %+v, %v", a, err)
	}
	pat, ph := auth.NewPrefixed(auth.PrefixPersonal)
	past := time.Now().Add(time.Hour)
	tok, err := s.CreateToken(ctx, "test", uid, NewToken{Name: "pat", Scopes: auth.Scopes{auth.ScopeRead}, ExpiresAt: &past}, ph)
	if err != nil {
		t.Fatal(err)
	}
	a, err = s.TokenActor(ctx, auth.HashToken(pat))
	if err != nil || a.Kind != auth.KindPersonalToken || a.Scopes.Has(auth.ScopeWrite) || !a.Scopes.Has(auth.ScopeRead) {
		t.Fatalf("personal actor = %+v, %v", a, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET expires_at = now() - interval '1 second' WHERE id = $1`, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TokenActor(ctx, auth.HashToken(pat)); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("expired personal token = %v", err)
	}

	// An OAuth grant through the store: request, consent, code, tokens.
	octx := auth.WithActor(ctx, auth.Actor{ClientID: "hello_dcr_a"})
	if err := s.RegisterClient(octx, "oauth:register", oauth.Client{ID: "hello_dcr_a", Name: "A", RedirectURIs: []string{"http://127.0.0.1/cb"}}); err != nil {
		t.Fatal(err)
	}
	at, refresh := grantFlow(t, s, octx, uid, "r1", "code-1")
	a, err = s.TokenActor(ctx, auth.HashToken(at))
	if err != nil || a.Kind != auth.KindOAuth || a.ClientID != "hello_dcr_a" || a.UserID != uid || a.Role != auth.RoleAdmin ||
		len(a.Audience) != 1 || a.Audience[0] != "https://h/mcp" || !a.Scopes.Has(auth.ScopeWrite) || a.Scopes.Has(auth.ScopeAdmin) {
		t.Fatalf("oauth actor = %+v, %v", a, err)
	}
	var via string
	if err := s.db.QueryRowContext(ctx, `SELECT coalesce(via, '') FROM audit_events WHERE action = 'issue' AND resource = 'oauth_grant'`).Scan(&via); err != nil || via != "hello_dcr_a" {
		t.Fatalf("issue audit via = %q, %v", via, err)
	}
	// A second use of the code revokes the grant.
	err = s.RedeemCode(octx, "oauth", auth.HashToken("code-1"), func(oauth.AuthRequest) (oauth.Grant, []oauth.Token, error) {
		t.Fatal("issue called for a spent code")
		return oauth.Grant{}, nil, nil
	})
	if !errors.Is(err, oauth.ErrReused) {
		t.Fatalf("reused code = %v", err)
	}
	if _, err := s.TokenActor(ctx, auth.HashToken(at)); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("token after code reuse = %v", err)
	}
	if err := s.Refresh(octx, "oauth", auth.HashToken(refresh), time.Hour, nil); !errors.Is(err, oauth.ErrInvalidGrant) {
		t.Fatalf("refresh of a revoked grant = %v", err)
	}

	// Refresh rotation and reuse.
	at, refresh = grantFlow(t, s, octx, uid, "r2", "code-2")
	next, _ := auth.NewPrefixed(auth.PrefixRefresh)
	err = s.Refresh(octx, "oauth", auth.HashToken(refresh), time.Hour, func(old oauth.Token, g oauth.Grant) ([]oauth.Token, error) {
		old.Hash, old.ExpiresAt = auth.HashToken(next), time.Now().Add(time.Hour)
		return []oauth.Token{old}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Refresh(octx, "oauth", auth.HashToken(refresh), time.Hour, nil); !errors.Is(err, oauth.ErrReused) {
		t.Fatalf("spent refresh token = %v", err)
	}
	if _, err := s.TokenActor(ctx, auth.HashToken(at)); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatal("refresh reuse left the grant's access token working")
	}
	if gs, err := s.Grants(ctx, uid); err != nil || len(gs) != 0 {
		t.Fatalf("live grants after revocations = %+v, %v", gs, err)
	}
	at, _ = grantFlow(t, s, octx, uid, "r3", "code-3")
	gs, err := s.Grants(ctx, 0)
	if err != nil || len(gs) != 1 {
		t.Fatalf("grants = %+v, %v", gs, err)
	}
	if err := s.RevokeGrant(ctx, "user:bob", gs[0].ID, bob); !errors.Is(err, oauth.ErrNotFound) {
		t.Fatalf("bob revoking alice's grant = %v", err)
	}
	if err := s.RevokeGrant(ctx, "user:alice", gs[0].ID, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TokenActor(ctx, auth.HashToken(at)); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatal("a revoked grant's token works")
	}
}

// grantFlow approves a request for uid and redeems its code, returning
// the access and refresh tokens (read write, bound to the MCP resource).
func grantFlow(t *testing.T, s *Store, ctx context.Context, uid int64, id, code string) (string, string) {
	t.Helper()
	if err := s.CreateRequest(ctx, oauth.AuthRequest{ID: id, ClientID: "hello_dcr_a", RedirectURI: "http://127.0.0.1/cb",
		CodeChallenge: "c", Scopes: auth.Scopes{auth.ScopeRead, auth.ScopeWrite}, Resources: []string{"https://h/api/v1", "https://h/mcp"},
		ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Request(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.ApproveRequest(ctx, "user:alice", id, uid, auth.Scopes{auth.ScopeRead, auth.ScopeWrite}, auth.HashToken(code), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Request(ctx, id); !errors.Is(err, oauth.ErrNotFound) {
		t.Fatalf("an approved request is still pending: %v", err)
	}
	at, ah := auth.NewPrefixed(auth.PrefixAccess)
	rt, rh := auth.NewPrefixed(auth.PrefixRefresh)
	err := s.RedeemCode(ctx, "oauth", auth.HashToken(code), func(q oauth.AuthRequest) (oauth.Grant, []oauth.Token, error) {
		if q.UserID != uid || len(q.Scopes) != 2 {
			t.Fatalf("redeemed request = %+v", q)
		}
		base := oauth.Token{ClientID: q.ClientID, UserID: uid, Scopes: q.Scopes, Resources: []string{"https://h/mcp"}, ExpiresAt: time.Now().Add(time.Hour)}
		a, r := base, base
		a.Hash, a.Kind = ah, oauth.TokenAccess
		r.Hash, r.Kind, r.Resources = rh, oauth.TokenRefresh, q.Resources
		return oauth.Grant{UserID: uid, ClientID: q.ClientID, Scopes: q.Scopes, Resources: q.Resources}, []oauth.Token{a, r}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return at, rt
}

// TestServiceAccountStore fails if a third live secret is stored, client
// credentials work with a wrong, revoked or disabled account's secret, or
// a service token does not resolve to the account's role and name.
func TestServiceAccountStore(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	c, err := s.CreateServiceAccount(ctx, "user:alice", oauth.Client{ID: "hello_sa_x", Kind: oauth.ClientService, Name: "ci",
		Role: auth.RoleOperator, Scopes: auth.Scopes{auth.ScopeRead, auth.ScopeWrite}, Enabled: true})
	if err != nil || c.Role != auth.RoleOperator || len(c.Scopes) != 2 {
		t.Fatalf("create = %+v, %v", c, err)
	}
	if _, err := s.CreateServiceAccount(ctx, "user:alice", oauth.Client{ID: "hello_sa_y", Kind: oauth.ClientService, Name: "ci",
		Role: auth.RoleViewer, Scopes: auth.Scopes{auth.ScopeRead}, Enabled: true}); !errors.Is(err, oauth.ErrConflict) {
		t.Fatalf("duplicate name = %v", err)
	}
	s1, h1 := auth.NewPrefixed(auth.PrefixClientSecret)
	_, h2 := auth.NewPrefixed(auth.PrefixClientSecret)
	_, h3 := auth.NewPrefixed(auth.PrefixClientSecret)
	sec1, err := s.AddClientSecret(ctx, "user:alice", c.ID, h1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddClientSecret(ctx, "user:alice", c.ID, h2, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddClientSecret(ctx, "user:alice", c.ID, h3, nil); !errors.Is(err, oauth.ErrTooManySecrets) {
		t.Fatalf("third secret = %v", err)
	}
	issue := func(secretHash []byte) (string, error) {
		at, ah := auth.NewPrefixed(auth.PrefixAccess)
		return at, s.ClientCredentials(ctx, c.ID, secretHash, func(cl oauth.Client) (oauth.Token, error) {
			return oauth.Token{Hash: ah, Kind: oauth.TokenAccess, ClientID: cl.ID, Scopes: cl.Scopes,
				Resources: []string{"https://h/api/v1"}, ExpiresAt: time.Now().Add(time.Hour)}, nil
		})
	}
	if _, err := issue(auth.HashToken("hello_cs_wrong")); !errors.Is(err, oauth.ErrInvalidGrant) {
		t.Fatalf("wrong secret = %v", err)
	}
	at, err := issue(auth.HashToken(s1))
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.TokenActor(ctx, auth.HashToken(at))
	if err != nil || a.Kind != auth.KindService || a.ServiceName != "ci" || a.Role != auth.RoleOperator || a.String() != "service:ci" {
		t.Fatalf("service actor = %+v, %v", a, err)
	}
	if err := s.RevokeClientSecret(ctx, "user:alice", c.ID, sec1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TokenActor(ctx, auth.HashToken(at)); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatal("a revoked secret's token works")
	}
	if _, err := issue(auth.HashToken(s1)); !errors.Is(err, oauth.ErrInvalidGrant) {
		t.Fatalf("revoked secret = %v", err)
	}
	off := false
	if _, err := s.UpdateServiceAccount(ctx, "user:alice", c.ID, oauth.ServiceAccountChange{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := issue(h2); !errors.Is(err, oauth.ErrInvalidGrant) {
		t.Fatalf("disabled account = %v", err)
	}
	if err := s.DeleteServiceAccount(ctx, "user:alice", c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ServiceAccount(ctx, c.ID); !errors.Is(err, oauth.ErrNotFound) {
		t.Fatalf("deleted account = %v", err)
	}
}
