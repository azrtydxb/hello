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
	}
	secrets := map[string]bool{
		"POST /api/v1/devices/{id}/rotate-secret":        true,
		"POST /api/v1/phones/{id}/rotate-token":          true,
		"POST /api/v1/phones/{id}/rearm":                 true,
		"POST /api/v1/phones/{id}/admin-password/reveal": true,
		"POST /api/v1/phones/{id}/admin-password/rotate": true,
	}
	// Self-service routes (spec P-19) write with the write scope but ask
	// only the viewer role: any user with a linked extension edits their own
	// contacts. A session holds every scope; a viewer's token can only hold
	// read, so a viewer changes contacts from the console.
	selfService := func(r RouteInfo) bool {
		return r.Scope == auth.ScopeWrite && r.Role == auth.RoleViewer && (strings.HasPrefix(r.Pattern, "/api/v1/me/contacts") || strings.HasPrefix(r.Pattern, "/api/v1/me/messages"))
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
