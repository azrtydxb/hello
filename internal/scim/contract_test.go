package scim

// The Store contract: the same assertions against Mem (always) and PG (with
// HELLO_TEST_DATABASE_URL, on a migrated throwaway database). The handler
// and service tests run on Mem, so a PG change that Mem does not mirror (or
// the reverse) would pass them and still change what production does. The
// contract pins the behaviour both share: the token rules (scim connectors
// only, at most MaxActiveTokens, revocation, lookup by hash), the
// resource rules (ErrConflict for a taken name, externalId or id,
// ErrNotFound for a moved version or an unknown member, soft-deleted users,
// hard-deleted groups), membership, counts and the per-connector lock.
//
// Known differences the contract leaves out on purpose (Mem is a test double,
// not a second implementation): Mem's Lock waits without a bound where PG
// gives up with ErrBusy after lockWait; Mem keeps its audits in a slice
// instead of audit_events; when several deleted users match, Mem's
// FindDeleted prefers the oldest externalId match, PG the most recently
// updated one.

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	hellodb "github.com/azrtydxb/hello/internal/db"
)

// contractRig is one Store under test and how to add a directory connector
// of a kind to it.
type contractRig struct {
	store        Store
	addConnector func(t *testing.T, kind, name string) int64
}

func memContractRig(*testing.T) contractRig {
	m := NewMem()
	var next int64
	return contractRig{store: m, addConnector: func(_ *testing.T, kind, _ string) int64 {
		next++
		m.Kinds[next] = kind
		return next
	}}
}

func pgContractRig(t *testing.T) contractRig {
	db := pgDB(t)
	m := hellodb.New(db, slog.New(slog.DiscardHandler))
	return contractRig{store: &PG{DB: m}, addConnector: func(t *testing.T, kind, name string) int64 {
		t.Helper()
		var id int64
		if err := db.QueryRowContext(context.Background(),
			`INSERT INTO directory_connectors (kind, name) VALUES ($1, $2) RETURNING id`, kind, name).Scan(&id); err != nil {
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
		{"Tokens", contractTokens},
		{"Resources", contractResources},
		{"Lock", contractLock},
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

func contractTokens(t *testing.T, r contractRig) {
	ctx, s := context.Background(), r.store
	scim := r.addConnector(t, "scim", "Okta")
	ldap := r.addConnector(t, "ldap", "AD")
	_, err := s.CreateToken(ctx, "admin", 999999, []byte("h0"), "x")
	wantErr(t, "CreateToken unknown connector", err, ErrNotFound)
	_, err = s.CreateToken(ctx, "admin", ldap, []byte("h0"), "x")
	wantErr(t, "CreateToken ldap connector", err, ErrWrongKind)
	if got := must(s.LastUsed(ctx, scim)); got != nil {
		t.Fatalf("LastUsed with no token = %v", got)
	}
	t1 := must(s.CreateToken(ctx, "admin", scim, []byte("h1"), "aaaa"))
	t2 := must(s.CreateToken(ctx, "admin", scim, []byte("h2"), "bbbb"))
	if t1.ConnectorID != scim || t1.Hint != "aaaa" || t1.CreatedBy != "admin" || t1.RevokedAt != nil || t2.ID <= t1.ID {
		t.Fatalf("tokens %+v %+v", t1, t2)
	}
	_, err = s.CreateToken(ctx, "admin", scim, []byte("h3"), "cccc")
	wantErr(t, "third active token", err, ErrTooMany)
	if got := must(s.LookupToken(ctx, []byte("h1"))); got.ID != t1.ID {
		t.Fatalf("lookup = %+v", got)
	}
	_, err = s.LookupToken(ctx, []byte("nope"))
	wantErr(t, "LookupToken unknown", err, ErrNotFound)
	if list := must(s.ListTokens(ctx, scim)); len(list) != 2 || list[0].ID != t2.ID || list[1].ID != t1.ID {
		t.Fatalf("list %+v", list)
	}
	at := time.Now().UTC().Truncate(time.Second)
	if err := s.TouchToken(ctx, t1.ID, at); err != nil {
		t.Fatal(err)
	}
	if got := must(s.LastUsed(ctx, scim)); got == nil || !got.Equal(at) {
		t.Fatalf("LastUsed = %v, want %v", got, at)
	}
	rev := must(s.RevokeToken(ctx, "admin", t1.ID))
	if rev.RevokedAt == nil || rev.ID != t1.ID {
		t.Fatalf("revoked %+v", rev)
	}
	again := must(s.RevokeToken(ctx, "admin", t1.ID))
	if again.RevokedAt == nil || !again.RevokedAt.Equal(*rev.RevokedAt) {
		t.Fatalf("revoked again %v, first %v", again.RevokedAt, rev.RevokedAt)
	}
	_, err = s.LookupToken(ctx, []byte("h1"))
	wantErr(t, "LookupToken revoked", err, ErrNotFound)
	_, err = s.RevokeToken(ctx, "admin", 999999)
	wantErr(t, "RevokeToken unknown", err, ErrNotFound)
	if t3 := must(s.CreateToken(ctx, "admin", scim, []byte("h3"), "cccc")); t3.ID == 0 || t3.RevokedAt != nil {
		t.Fatalf("token after a revoke %+v", t3)
	}
}

func contractResources(t *testing.T, r contractRig) {
	ctx, s := context.Background(), r.store
	c := r.addConnector(t, "scim", "Okta")
	save := func(x Resource, members []string) (Resource, error) {
		x.ConnectorID = c
		return s.SaveResource(ctx, x, members)
	}
	u1 := must(save(Resource{ID: "u-1", Type: TypeUser, Name: "alice", ExternalID: "e1", Data: map[string]any{"active": true}}, nil))
	if u1.Version != 1 || u1.Deleted || u1.Name != "alice" || u1.ExternalID != "e1" {
		t.Fatalf("u1 %+v", u1)
	}
	u2 := must(save(Resource{ID: "u-2", Type: TypeUser, Name: "bob", ExternalID: "e2", Data: map[string]any{"active": false}}, nil))
	for what, x := range map[string]Resource{
		"taken name":       {ID: "u-3", Type: TypeUser, Name: "ALICE"},
		"taken externalId": {ID: "u-3", Type: TypeUser, Name: "carol", ExternalID: "e1"},
		"taken id":         {ID: "u-1", Type: TypeUser, Name: "dave"},
	} {
		_, err := save(x, nil)
		wantErr(t, what, err, ErrConflict)
	}
	_, err := save(Resource{ID: "u-1", Type: TypeUser, Name: "alice", Version: 7}, nil)
	wantErr(t, "moved version", err, ErrNotFound)
	u1 = must(save(Resource{ID: "u-1", Type: TypeUser, Name: "alice", ExternalID: "e1", Version: 1, Data: map[string]any{"title": "x"}}, nil))
	if u1.Version != 2 || u1.Data["title"] != "x" {
		t.Fatalf("updated u1 %+v", u1)
	}
	got := must(s.GetResource(ctx, c, TypeUser, "u-1", false))
	if got.Version != 2 || got.Name != "alice" {
		t.Fatalf("get %+v", got)
	}
	_, err = s.GetResource(ctx, c, TypeGroup, "u-1", false)
	wantErr(t, "GetResource wrong type", err, ErrNotFound)
	for _, h := range []*Hint{{"name", "ALICE"}, {"external_id", "e1"}, {"id", "u-1"}} {
		if found := must(s.FindResources(ctx, c, TypeUser, h)); len(found) != 1 || found[0].ID != "u-1" {
			t.Fatalf("find %+v: %+v", h, found)
		}
	}
	if all := must(s.FindResources(ctx, c, TypeUser, nil)); len(all) != 2 || all[0].ID != "u-1" || all[1].ID != "u-2" {
		t.Fatalf("all users %+v", all)
	}

	_, err = save(Resource{ID: "g-x", Type: TypeGroup, Name: "broken"}, []string{"u-1", "nobody"})
	wantErr(t, "unknown member", err, ErrNotFound)
	g := must(save(Resource{ID: "g-1", Type: TypeGroup, Name: "Sales", ExternalID: "ge1"}, []string{"u-1", u2.ID}))
	if g.Version != 1 {
		t.Fatalf("group %+v", g)
	}
	members := must(s.Members(ctx, []string{"g-1"}))
	if ms := members["g-1"]; len(ms) != 2 || ms[0].ID != "u-1" || ms[0].Name != "alice" || ms[1].ID != "u-2" {
		t.Fatalf("members %+v", members)
	}
	groups := must(s.GroupsOf(ctx, []string{"u-1"}))
	if gs := groups["u-1"]; len(gs) != 1 || gs[0].ID != "g-1" || gs[0].ExternalID != "ge1" || gs[0].Name != "Sales" {
		t.Fatalf("groups %+v", groups)
	}
	if ex := must(s.Existing(ctx, c, []string{"u-1", "g-1", "nobody"})); len(ex) != 1 || !ex["u-1"] {
		t.Fatalf("existing %+v", ex)
	}
	if n := must(s.Counts(ctx, c)); n != (Counts{Users: 2, ActiveUsers: 1, Groups: 1}) {
		t.Fatalf("counts %+v", n)
	}

	if err := s.MarkDeleted(ctx, c, TypeUser, "u-2"); err != nil {
		t.Fatal(err)
	}
	wantErr(t, "MarkDeleted twice", s.MarkDeleted(ctx, c, TypeUser, "u-2"), ErrNotFound)
	_, err = s.GetResource(ctx, c, TypeUser, "u-2", false)
	wantErr(t, "GetResource deleted", err, ErrNotFound)
	if del := must(s.GetResource(ctx, c, TypeUser, "u-2", true)); !del.Deleted || del.Version != 2 {
		t.Fatalf("deleted %+v", del)
	}
	if ms := must(s.Members(ctx, []string{"g-1"}))["g-1"]; len(ms) != 1 || ms[0].ID != "u-1" {
		t.Fatalf("members after a delete %+v", ms)
	}
	if n := must(s.Counts(ctx, c)); n != (Counts{Users: 1, ActiveUsers: 1, DeletedUsers: 1, Groups: 1}) {
		t.Fatalf("counts after a delete %+v", n)
	}
	if d := must(s.FindDeleted(ctx, c, TypeUser, "nobody", "e2")); d == nil || d.ID != "u-2" {
		t.Fatalf("find deleted by externalId %+v", d)
	}
	if d := must(s.FindDeleted(ctx, c, TypeUser, "BOB", "")); d == nil || d.ID != "u-2" {
		t.Fatalf("find deleted by name %+v", d)
	}
	if d := must(s.FindDeleted(ctx, c, TypeUser, "alice", "e1")); d != nil {
		t.Fatalf("find deleted matched a live user %+v", d)
	}
	// A deleted user's name and externalId are free again.
	must(save(Resource{ID: "u-4", Type: TypeUser, Name: "bob", ExternalID: "e2"}, nil))

	if err := s.MarkDeleted(ctx, c, TypeGroup, "g-1"); err != nil {
		t.Fatal(err)
	}
	_, err = s.GetResource(ctx, c, TypeGroup, "g-1", true)
	wantErr(t, "GetResource of a deleted group", err, ErrNotFound)
	wantErr(t, "MarkDeleted unknown", s.MarkDeleted(ctx, c, TypeUser, "nobody"), ErrNotFound)
}

func contractLock(t *testing.T, r contractRig) {
	ctx := context.Background()
	c := r.addConnector(t, "scim", "Okta")
	for range 2 {
		unlock, err := r.store.Lock(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		unlock()
	}
}
