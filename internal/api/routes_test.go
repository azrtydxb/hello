package api

import (
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
)

// TestRoutesTable fails if a route is listed twice, a private route lacks a
// scope or carries a role other than its scope's (read and session →
// viewer, write → operator, admin and secrets → admin), a public
// route carries either, or the secrets scope drifts from spec S-5's list.
func TestRoutesTable(t *testing.T) {
	roleOf := map[auth.Scope]auth.Role{
		auth.ScopeRead: auth.RoleViewer, auth.ScopeSession: auth.RoleViewer,
		auth.ScopeWrite: auth.RoleOperator, auth.ScopeAdmin: auth.RoleAdmin, auth.ScopeSecrets: auth.RoleAdmin,
		// The inbox: a supervisor (operator) reaches every conversation; the
		// agents' own /me/inbox routes ask the user role like the other
		// self-service routes, and the handlers refuse a viewer.
		auth.ScopeMessages: auth.RoleOperator,
		// The events scope guards the call-event routes, which every role
		// reads (a user only their own extensions' events).
		auth.ScopeEvents: auth.RoleViewer,
	}
	secrets := map[string]bool{
		"POST /api/v1/devices/{id}/rotate-secret":        true,
		"POST /api/v1/phones/{id}/rotate-token":          true,
		"POST /api/v1/phones/{id}/rearm":                 true,
		"POST /api/v1/phones/{id}/admin-password/reveal": true,
		"POST /api/v1/phones/{id}/admin-password/rotate": true,
	}
	// Self-service routes (spec P-19, user accounts) ask only the user role,
	// which ranks below viewer: the signed-in user's own profile, contacts,
	// voicemail, extensions and calls, and the auth basics. Every route a
	// role user can reach must be listed here on purpose; the handlers
	// derive the owner from the credential and resolve ids through the
	// caller's own extensions (see TestSelfServiceIsolation).
	selfService := func(r RouteInfo) bool {
		if r.Role != auth.RoleUser {
			return false
		}
		switch r.Pattern {
		case "/api/v1/auth/me", "/api/v1/auth/logout", "/api/v1/auth/password":
			return true
		}
		return strings.HasPrefix(r.Pattern, "/api/v1/me/") || strings.HasPrefix(r.Pattern, "/api/v1/events")
	}
	public := map[string]bool{"GET /api/v1/version": true, "GET /api/v1/openapi.json": true, "POST /api/v1/auth/login": true}
	seen := map[string]bool{}
	for _, r := range Routes() {
		key := r.Method + " " + r.Pattern
		if seen[key] {
			t.Errorf("%s listed twice", key)
		}
		seen[key] = true
		if r.Public != public[key] {
			t.Errorf("%s: public = %v", key, r.Public)
		}
		if r.Public {
			if r.Scope != "" || r.Role != "" {
				t.Errorf("%s: public route with scope %q role %q", key, r.Scope, r.Role)
			}
			continue
		}
		want, ok := roleOf[r.Scope]
		if !ok || (r.Role != want && !selfService(r)) {
			t.Errorf("%s: scope %q role %q, want the scope's role %q", key, r.Scope, r.Role, want)
		}
		if (r.Scope == auth.ScopeSecrets) != secrets[key] {
			t.Errorf("%s: scope %q, secrets list says %v", key, r.Scope, secrets[key])
		}
	}
	if n := len(seen); n < 120 {
		t.Fatalf("only %d routes", n)
	}
}
