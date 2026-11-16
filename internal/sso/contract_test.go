package sso

// The Store contract: the same assertions against Mem (always) and PG (with
// HELLO_TEST_DATABASE_URL, on a migrated throwaway database). The service
// and handler tests run on Mem, so a PG change that Mem does not mirror (or
// the reverse) would pass them and still change what production does. The
// contract pins the behaviour both share: the sealed client secret and its
// keep-on-resave rule, the SSO-required policy and its break-glass check,
// the synced-user lookups, identity linking (ErrLinked on either side
// taken) and just-in-time users.
//
// Known differences the contract leaves out on purpose (Mem is a test double,
// not a second implementation): Mem's SaveProvider audits "update" where PG
// audits "update-secret" for a new secret, and Mem's SetSSORequired audits
// "sso-required:<bool>" where PG writes "sso-required" or "sso-optional";
// Mem compares usernames case-insensitively in CreateUser, PG's unique
// constraint is case-sensitive; Mem's BreakGlassUsers and
// SyncedUsersByEmail are in map order, PG's by username and id.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// contractUser is a user to set up in the store under test.
type contractUser struct {
	username, role, email string
	enabled, breakGlass   bool
	// kind, tenant and externalID make a synced user of a new connector of
	// that kind.
	kind, tenant, externalID string
}

// contractRig is one Store under test and how to add a Hello user to it.
type contractRig struct {
	store   Store
	addUser func(t *testing.T, u contractUser) int64
}

func memContractRig(*testing.T) contractRig {
	m := NewMem()
	var next, conn int64
	return contractRig{store: m, addUser: func(_ *testing.T, u contractUser) int64 {
		next++
		mu := &MemUser{User: User{ID: next, Username: u.username, Role: u.role, Enabled: u.enabled, BreakGlass: u.breakGlass}, Email: u.email}
		if u.kind != "" {
			conn++
			mu.ConnectorID, mu.ConnectorKind, mu.ConnectorTenant, mu.ExternalID = conn, u.kind, u.tenant, u.externalID
		}
		m.Users[next] = mu
		return next
	}}
}

func pgContractRig(t *testing.T) contractRig {
	db, p := scratchPG(t)
	return contractRig{store: p, addUser: func(t *testing.T, u contractUser) int64 {
		t.Helper()
		ctx := context.Background()
		var conn *int64
		if u.kind != "" {
			var id int64
			if err := db.QueryRowContext(ctx, `INSERT INTO directory_connectors (kind, name, config) VALUES ($1, $2, jsonb_build_object('tenantId', $3::text)) RETURNING id`,
				u.kind, u.kind+"-"+u.username, u.tenant).Scan(&id); err != nil {
				t.Fatal(err)
			}
			conn = &id
		}
		var ext *string
		if u.externalID != "" {
			ext = &u.externalID
		}
		var id int64
		if err := db.QueryRowContext(ctx, `INSERT INTO users (username, password_hash, role, email, enabled, break_glass, source_connector_id, external_id)
			VALUES ($1, 'x', $2, $3, $4, $5, $6, $7) RETURNING id`, u.username, u.role, u.email, u.enabled, u.breakGlass, conn, ext).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}}
}

func TestStoreContractMem(t *testing.T) { runStoreContract(t, memContractRig) }

func TestStoreContractPG(t *testing.T) { runStoreContract(t, pgContractRig) }

func runStoreContract(t *testing.T, newRig func(*testing.T) contractRig) {
	for _, c := range []struct {
		name string
		run  func(t *testing.T, r contractRig)
	}{
		{"Providers", contractProviders},
		{"Policy", contractPolicy},
		{"Users", contractUsers},
		{"Identities", contractIdentities},
	} {
		t.Run(c.name, func(t *testing.T) { c.run(t, newRig(t)) })
	}
}

func wantErr(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: err = %v, want %v", what, err, want)
	}
}

// must returns v, or panics with err (which fails the test with its stack):
// Go cannot pass t beside a call's two results.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func contractProviders(t *testing.T, r contractRig) {
	ctx, s := context.Background(), r.store
	_, err := s.GetProvider(ctx, ProviderEntra)
	wantErr(t, "GetProvider unknown", err, ErrNotFound)
	_, err = s.ProviderSecret(ctx, ProviderEntra)
	wantErr(t, "ProviderSecret unknown", err, ErrNotFound)
	if list := must(s.ListProviders(ctx)); len(list) != 0 {
		t.Fatalf("providers %+v", list)
	}
	sec := "client-secret"
	pr := must(s.SaveProvider(ctx, "admin", Provider{Provider: ProviderEntra, Enabled: true, ClientID: "c1", Tenant: "t1",
		Domains: []string{"corp.example"}, JIT: true}, &sec))
	if !pr.HasClientSecret || pr.ClientID != "c1" || pr.Tenant != "t1" || !pr.JIT || len(pr.Domains) != 1 || pr.Domains[0] != "corp.example" {
		t.Fatalf("saved %+v", pr)
	}
	if got := must(s.ProviderSecret(ctx, ProviderEntra)); got != sec {
		t.Fatalf("secret = %q", got)
	}
	pr = must(s.SaveProvider(ctx, "admin", Provider{Provider: ProviderEntra, ClientID: "c2", Domains: []string{}}, nil))
	if !pr.HasClientSecret || pr.ClientID != "c2" || pr.Enabled || len(pr.Domains) != 0 {
		t.Fatalf("resaved %+v", pr)
	}
	if got := must(s.ProviderSecret(ctx, ProviderEntra)); got != sec {
		t.Fatalf("secret after a resave = %q", got)
	}
	g := must(s.SaveProvider(ctx, "admin", Provider{Provider: ProviderGoogle, ClientID: "g", Domains: []string{}}, nil))
	if g.HasClientSecret {
		t.Fatalf("google %+v", g)
	}
	_, err = s.ProviderSecret(ctx, ProviderGoogle)
	wantErr(t, "ProviderSecret without a secret", err, ErrNotFound)
	if list := must(s.ListProviders(ctx)); len(list) != 2 || list[0].Provider != ProviderEntra || list[1].Provider != ProviderGoogle {
		t.Fatalf("providers %+v", list)
	}
}

func contractPolicy(t *testing.T, r contractRig) {
	ctx, s := context.Background(), r.store
	if on := must(s.SSORequired(ctx)); on {
		t.Fatal("sso_required on by default")
	}
	wantErr(t, "no break-glass admin", s.SetSSORequired(ctx, "admin", true), ErrNoBreakGlass)
	r.addUser(t, contractUser{username: "off", role: "admin", enabled: false, breakGlass: true})
	r.addUser(t, contractUser{username: "user", role: "user", enabled: true, breakGlass: true})
	wantErr(t, "only a disabled or non-admin break-glass user", s.SetSSORequired(ctx, "admin", true), ErrNoBreakGlass)
	if on := must(s.SSORequired(ctx)); on {
		t.Fatal("sso_required stored after a refusal")
	}
	r.addUser(t, contractUser{username: "root", role: "admin", enabled: true, breakGlass: true})
	if err := s.SetSSORequired(ctx, "admin", true); err != nil {
		t.Fatal(err)
	}
	if on := must(s.SSORequired(ctx)); !on {
		t.Fatal("sso_required not stored")
	}
	if bg := must(s.BreakGlassUsers(ctx)); len(bg) != 3 {
		t.Fatalf("break-glass users %+v", bg)
	}
	if err := s.SetSSORequired(ctx, "admin", false); err != nil {
		t.Fatal(err)
	}
	if on := must(s.SSORequired(ctx)); on {
		t.Fatal("sso_required still on")
	}
}

func contractUsers(t *testing.T, r contractRig) {
	ctx, s := context.Background(), r.store
	ann := r.addUser(t, contractUser{username: "ann", role: "user", email: "ann@x.example", enabled: true, kind: "entra", tenant: "tid-1", externalID: "OID-ANN"})
	lee := r.addUser(t, contractUser{username: "lee", role: "user", email: "Lee@x.example", enabled: true, kind: "ldap", externalID: "guid-lee"})
	root := r.addUser(t, contractUser{username: "root", role: "admin", email: "root@x.example", enabled: true, breakGlass: true})
	_, err := s.GetUser(ctx, 999999)
	wantErr(t, "GetUser unknown", err, ErrNotFound)
	u := must(s.GetUser(ctx, ann))
	if u.Username != "ann" || u.Role != "user" || !u.Enabled || u.ConnectorID == 0 || u.ConnectorKind != "entra" || u.ExternalID != "OID-ANN" || u.ConnectorTenant != "tid-1" {
		t.Fatalf("ann %+v", u)
	}
	if u := must(s.GetUser(ctx, root)); u.ConnectorID != 0 || u.ConnectorKind != "" || !u.BreakGlass || u.Role != "admin" {
		t.Fatalf("root %+v", u)
	}
	if u := must(s.SyncedUser(ctx, "entra", "oid-ann")); u.ID != ann || u.ConnectorTenant != "tid-1" {
		t.Fatalf("synced %+v", u)
	}
	_, err = s.SyncedUser(ctx, "google", "oid-ann")
	wantErr(t, "SyncedUser of another kind", err, ErrNotFound)
	if us := must(s.SyncedUsersByEmail(ctx, "LEE@x.example")); len(us) != 1 || us[0].ID != lee {
		t.Fatalf("by email %+v", us)
	}
	if us := must(s.SyncedUsersByEmail(ctx, "root@x.example")); len(us) != 0 {
		t.Fatalf("a local user matched by email %+v", us)
	}
}

func contractIdentities(t *testing.T, r contractRig) {
	ctx, s := context.Background(), r.store
	ann := r.addUser(t, contractUser{username: "ann", role: "user", enabled: true})
	lee := r.addUser(t, contractUser{username: "lee", role: "user", enabled: true})
	_, err := s.IdentityUser(ctx, ProviderEntra, "oid-ann")
	wantErr(t, "IdentityUser unlinked", err, ErrNotFound)
	if err := s.Link(ctx, "sso:entra", Identity{UserID: ann, Provider: ProviderEntra, Subject: "oid-ann", Tenant: "t", Email: "ann@x.example"}); err != nil {
		t.Fatal(err)
	}
	wantErr(t, "a linked subject", s.Link(ctx, "sso:entra", Identity{UserID: lee, Provider: ProviderEntra, Subject: "oid-ann"}), ErrLinked)
	wantErr(t, "a user linked to the provider", s.Link(ctx, "sso:entra", Identity{UserID: ann, Provider: ProviderEntra, Subject: "oid-other"}), ErrLinked)
	if err := s.Link(ctx, "sso:google", Identity{UserID: ann, Provider: ProviderGoogle, Subject: "g-ann"}); err != nil {
		t.Fatal(err)
	}
	if u := must(s.IdentityUser(ctx, ProviderEntra, "oid-ann")); u.ID != ann {
		t.Fatalf("identity user %+v", u)
	}
	at := time.Now().UTC().Truncate(time.Second)
	if err := s.TouchIdentity(ctx, ProviderEntra, "oid-ann", at); err != nil {
		t.Fatal(err)
	}
	ids := must(s.ListIdentities(ctx, ann))
	if len(ids) != 2 || ids[0].Provider != ProviderEntra || ids[1].Provider != ProviderGoogle || ids[0].Subject != "oid-ann" ||
		ids[0].Email != "ann@x.example" || ids[0].LastLoginAt == nil || !ids[0].LastLoginAt.Equal(at) {
		t.Fatalf("identities %+v", ids)
	}
	if err := s.Unlink(ctx, "admin", ann, ProviderEntra); err != nil {
		t.Fatal(err)
	}
	wantErr(t, "second unlink", s.Unlink(ctx, "admin", ann, ProviderEntra), ErrNotFound)
	_, err = s.IdentityUser(ctx, ProviderEntra, "oid-ann")
	wantErr(t, "IdentityUser after unlink", err, ErrNotFound)

	jit := must(s.CreateUser(ctx, "sso:google", "new@x.example", "new@x.example", "New", Identity{Provider: ProviderGoogle, Subject: "g-new"}))
	if jit.ID == 0 || jit.Username != "new@x.example" || jit.Role != "user" || !jit.Enabled || jit.ConnectorID != 0 {
		t.Fatalf("jit %+v", jit)
	}
	if u := must(s.IdentityUser(ctx, ProviderGoogle, "g-new")); u.ID != jit.ID {
		t.Fatalf("jit identity %+v", u)
	}
	_, err = s.CreateUser(ctx, "sso:google", "new@x.example", "", "", Identity{Provider: ProviderGoogle, Subject: "g-other"})
	wantErr(t, "a taken username", err, ErrLinked)
	_, err = s.CreateUser(ctx, "sso:google", "second@x.example", "", "", Identity{Provider: ProviderGoogle, Subject: "g-new"})
	wantErr(t, "a linked subject", err, ErrLinked)
	// The refused user was rolled back with its identity: the name is free.
	if u := must(s.CreateUser(ctx, "sso:google", "second@x.example", "", "", Identity{Provider: ProviderGoogle, Subject: "g-second"})); u.ID == 0 {
		t.Fatalf("second %+v", u)
	}
}
