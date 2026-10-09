package auth

import (
	"context"
	"strings"
	"testing"
)

// TestScopesHas fails if the hierarchy admin > write > read breaks, if
// secrets or session are implied by anything, or if an unknown scope is
// granted.
func TestScopesHas(t *testing.T) {
	all := []Scope{ScopeRead, ScopeWrite, ScopeAdmin, ScopeSecrets, ScopeSession, "bogus"}
	cases := []struct {
		have Scopes
		want []Scope
	}{
		{nil, nil},
		{Scopes{ScopeRead}, []Scope{ScopeRead}},
		{Scopes{ScopeWrite}, []Scope{ScopeRead, ScopeWrite}},
		{Scopes{ScopeAdmin}, []Scope{ScopeRead, ScopeWrite, ScopeAdmin}},
		{Scopes{ScopeSecrets}, []Scope{ScopeSecrets}},
		{Scopes{ScopeSession}, []Scope{ScopeSession}},
		{AllScopes, []Scope{ScopeRead, ScopeWrite, ScopeAdmin, ScopeSecrets}},
		{Scopes{ScopeRead, ScopeSecrets}, []Scope{ScopeRead, ScopeSecrets}},
	}
	for _, c := range cases {
		for _, s := range all {
			want := false
			for _, w := range c.want {
				want = want || w == s
			}
			if got := c.have.Has(s); got != want {
				t.Errorf("%v.Has(%s) = %v, want %v", c.have, s, got, want)
			}
		}
	}
}

// TestParseScopes fails if a known list does not round-trip, duplicates
// survive, or an unknown scope parses.
func TestParseScopes(t *testing.T) {
	ss, err := ParseScopes(" read  write read secrets ")
	if err != nil || ss.String() != "read write secrets" {
		t.Fatalf("ParseScopes = %q, %v", ss, err)
	}
	if ss, err := ParseScopes(""); err != nil || len(ss) != 0 {
		t.Fatalf("empty = %v, %v", ss, err)
	}
	if _, err := ParseScopes("read root"); err == nil {
		t.Fatal("unknown scope accepted")
	}
}

// TestRoles fails if the order viewer < operator < admin breaks, an unknown
// role passes AtLeast or ParseRole, or GrantableScopes exceeds a role.
func TestRoles(t *testing.T) {
	order := []Role{RoleViewer, RoleOperator, RoleAdmin}
	for i, r := range order {
		for j, m := range order {
			if got := r.AtLeast(m); got != (i >= j) {
				t.Errorf("%s.AtLeast(%s) = %v", r, m, got)
			}
		}
		if Role("root").AtLeast(r) || Role("").AtLeast(r) {
			t.Errorf("unknown role at least %s", r)
		}
		if p, err := ParseRole(string(r)); err != nil || p != r {
			t.Errorf("ParseRole(%s) = %s, %v", r, p, err)
		}
	}
	if _, err := ParseRole("root"); err == nil {
		t.Fatal("unknown role parsed")
	}
	for r, want := range map[Role]string{RoleViewer: "read", RoleOperator: "read write", RoleAdmin: "read write admin secrets", "root": ""} {
		if got := GrantableScopes(r).String(); got != want {
			t.Errorf("GrantableScopes(%s) = %q, want %q", r, got, want)
		}
	}
	// The admin list is a copy: changing it must not widen AllScopes' users.
	g := GrantableScopes(RoleAdmin)
	g[0] = ScopeSession
	if AllScopes[0] != ScopeRead {
		t.Fatal("GrantableScopes(admin) aliases AllScopes")
	}
}

// TestPrefixedAndReplay fails if a prefixed credential lacks its prefix,
// its hash is not of the whole credential, or the replay marker leaks.
func TestPrefixedAndReplay(t *testing.T) {
	for _, p := range []string{PrefixPersonal, PrefixAccess, PrefixRefresh, PrefixClientSecret} {
		plain, hash := NewPrefixed(p)
		if !strings.HasPrefix(plain, p) || len(plain) != len(p)+43 || string(hash) != string(HashToken(plain)) {
			t.Errorf("NewPrefixed(%s) = %q", p, plain)
		}
	}
	if _, ok := ReplayFrom(context.Background()); ok {
		t.Fatal("replay marker on a plain context")
	}
	if r, ok := ReplayFrom(WithReplay(context.Background(), Replay{ClientID: "c"})); !ok || r.ClientID != "c" {
		t.Fatalf("ReplayFrom = %+v, %v", r, ok)
	}
	if got := (Actor{Kind: KindService, ServiceName: "ci", TokenID: 3}).String(); got != "service:ci" {
		t.Fatalf("service actor = %q", got)
	}
}
