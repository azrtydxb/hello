package oauth

import (
	"context"
	"crypto/rand"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
)

// The management operations behind /api/v1 (spec S-11, S-12): connected
// apps (grants) and service accounts. The API enforces the route's scope
// and role; these enforce the rules inside (whose grants, which scopes).

// Grants lists a's grants; an admin sees every user's.
func (s *Server) Grants(ctx context.Context, a auth.Actor) ([]Grant, error) {
	uid := a.UserID
	if a.Role.AtLeast(auth.RoleAdmin) && a.Scopes.Has(auth.ScopeAdmin) {
		uid = 0
	}
	if uid == 0 && !a.Role.AtLeast(auth.RoleAdmin) {
		return []Grant{}, nil
	}
	return s.o.Store.Grants(ctx, uid)
}

// RevokeGrant revokes grant id and its tokens: the caller's own, or any
// user's for an admin. Another user's grant is ErrNotFound otherwise.
func (s *Server) RevokeGrant(ctx context.Context, a auth.Actor, id int64) error {
	uid := a.UserID
	if a.Role.AtLeast(auth.RoleAdmin) && a.Scopes.Has(auth.ScopeAdmin) {
		uid = 0
	}
	if uid == 0 && !a.Role.AtLeast(auth.RoleAdmin) {
		return ErrNotFound
	}
	if err := s.o.Store.RevokeGrant(ctx, a.String(), id, uid); err != nil {
		return err
	}
	s.o.Log.Info("oauth grant revoked", "grant_id", id, "actor", a.String())
	return nil
}

// NewServiceAccount is a service account to create.
type NewServiceAccount struct {
	Name        string
	Description string
	Role        auth.Role
	Scopes      auth.Scopes
	Enabled     bool
}

// checkAccountScopes holds a service account to the scopes its role may
// hold (spec S-11, S-23).
func checkAccountScopes(role auth.Role, scopes auth.Scopes) error {
	if _, err := auth.ParseRole(string(role)); err != nil {
		return inputErr("role must be viewer, operator or admin")
	}
	if len(scopes) == 0 {
		return inputErr("a service account needs at least one scope")
	}
	g := auth.GrantableScopes(role)
	for _, sc := range scopes {
		if sc == auth.ScopeSession {
			return inputErr("scope session cannot be held by a service account")
		}
		if !g.Has(sc) {
			return inputErr("role " + string(role) + " cannot hold scope " + string(sc))
		}
	}
	return nil
}

// ServiceAccounts lists the service accounts.
func (s *Server) ServiceAccounts(ctx context.Context) ([]Client, error) {
	return s.o.Store.ServiceAccounts(ctx)
}

// ServiceAccount returns one service account.
func (s *Server) ServiceAccount(ctx context.Context, id string) (Client, error) {
	return s.o.Store.ServiceAccount(ctx, id)
}

// CreateServiceAccount creates a service account (no secret yet).
func (s *Server) CreateServiceAccount(ctx context.Context, a auth.Actor, in NewServiceAccount) (Client, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 {
		return Client{}, inputErr("name is required (at most 100 characters)")
	}
	if err := checkAccountScopes(in.Role, in.Scopes); err != nil {
		return Client{}, err
	}
	c := Client{
		ID: "hello_sa_" + strings.ToLower(rand.Text()[:16]), Kind: ClientService, Name: in.Name,
		Description: in.Description, Role: in.Role, Scopes: in.Scopes, Enabled: in.Enabled,
	}
	if a.UserID != 0 {
		uid := a.UserID
		c.CreatedBy = &uid
	}
	return s.o.Store.CreateServiceAccount(ctx, a.String(), c)
}

// UpdateServiceAccount changes a service account; a new role or scopes
// must still fit together.
func (s *Server) UpdateServiceAccount(ctx context.Context, a auth.Actor, id string, ch ServiceAccountChange) (Client, error) {
	if ch.Role != nil || ch.Scopes != nil {
		cur, err := s.o.Store.ServiceAccount(ctx, id)
		if err != nil {
			return Client{}, err
		}
		role, scopes := cur.Role, cur.Scopes
		if ch.Role != nil {
			role = *ch.Role
		}
		if ch.Scopes != nil {
			scopes = *ch.Scopes
		}
		if err := checkAccountScopes(role, scopes); err != nil {
			return Client{}, err
		}
	}
	return s.o.Store.UpdateServiceAccount(ctx, a.String(), id, ch)
}

// DeleteServiceAccount deletes a service account, its secrets and tokens.
func (s *Server) DeleteServiceAccount(ctx context.Context, a auth.Actor, id string) error {
	return s.o.Store.DeleteServiceAccount(ctx, a.String(), id)
}

// AddSecret creates a client secret for service account id and returns it
// in plaintext, the only time it is ever available.
func (s *Server) AddSecret(ctx context.Context, a auth.Actor, id string, expires *time.Time) (string, Secret, error) {
	if expires != nil && !expires.After(s.now()) {
		return "", Secret{}, inputErr("expiresAt must be in the future")
	}
	if _, err := s.o.Store.ServiceAccount(ctx, id); err != nil {
		return "", Secret{}, err
	}
	plain, hash := auth.NewPrefixed(auth.PrefixClientSecret)
	sc, err := s.o.Store.AddClientSecret(ctx, a.String(), id, hash, expires)
	if err != nil {
		return "", Secret{}, err
	}
	return plain, sc, nil
}

// RevokeSecret revokes a service account's secret and its live tokens.
func (s *Server) RevokeSecret(ctx context.Context, a auth.Actor, id string, secretID int64) error {
	return s.o.Store.RevokeClientSecret(ctx, a.String(), id, secretID)
}

// Prune deletes OAuth rows expired or revoked more than retention ago and
// stale dynamically registered clients.
func (s *Server) Prune(ctx context.Context, retention time.Duration) (int64, error) {
	return s.o.Store.PruneOAuth(ctx, s.now().Add(-retention))
}
