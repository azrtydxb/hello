package oauth

import (
	"context"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
)

// TestVoiceRuntimeScopeAccounts fails if a service account cannot hold
// voice-runtime alone at any role, if it can hold it with another scope, or
// if a consent-style request for it is accepted by the grantable check.
func TestVoiceRuntimeScopeAccounts(t *testing.T) {
	for _, r := range []auth.Role{auth.RoleViewer, auth.RoleOperator, auth.RoleAdmin} {
		if err := checkAccountScopes(r, auth.Scopes{auth.ScopeVoiceRuntime}); err != nil {
			t.Errorf("role %s alone: %v", r, err)
		}
		for _, other := range []auth.Scope{auth.ScopeRead, auth.ScopeAdmin} {
			if err := checkAccountScopes(r, auth.Scopes{auth.ScopeVoiceRuntime, other}); err == nil {
				t.Errorf("role %s: voice-runtime with %s accepted", r, other)
			}
			if err := checkAccountScopes(r, auth.Scopes{other, auth.ScopeVoiceRuntime}); err == nil {
				t.Errorf("role %s: %s with voice-runtime accepted", r, other)
			}
		}
	}
	if err := checkAccountScopes(auth.RoleAdmin, auth.Scopes{auth.ScopeSession}); err == nil {
		t.Error("session accepted")
	}
	// Consent bounds a grant by the approver's GrantableScopes: none has it.
	for _, r := range []auth.Role{auth.RoleViewer, auth.RoleOperator, auth.RoleAdmin} {
		if auth.GrantableScopes(r).Has(auth.ScopeVoiceRuntime) {
			t.Errorf("consent by %s could grant voice-runtime", r)
		}
	}
	if ss, err := ParseRequestScopes("voice-runtime"); err != nil || !ss.Has(auth.ScopeVoiceRuntime) {
		t.Errorf("ParseRequestScopes = %v, %v", ss, err)
	}
}

// TestVoiceRuntimeScopeConsent fails if an authorization request for
// voice-runtime can be approved by any role, an admin session included.
func TestVoiceRuntimeScopeConsent(t *testing.T) {
	e := newHarness(t, nil)
	id := e.requestID(map[string]string{"scope": "read voice-runtime"})
	for _, r := range []auth.Role{auth.RoleViewer, auth.RoleOperator, auth.RoleAdmin} {
		a := alice
		a.Role = r
		if _, err := e.srv.Approve(context.Background(), a, id, auth.Scopes{auth.ScopeVoiceRuntime}); err == nil {
			t.Errorf("a %s granted voice-runtime through consent", r)
		}
	}
}
