package scim

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	hellodb "github.com/azrtydxb/hello/internal/db"
	"github.com/azrtydxb/hello/internal/directory"
	"github.com/azrtydxb/hello/internal/migrate"
)

// pgDB is a migrated scratch database; it skips without
// HELLO_TEST_DATABASE_URL.
func pgDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("HELLO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HELLO_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("hello_scim_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)") })
	u, _ := url.Parse(dsn)
	u.Path = "/" + name
	scfg, err := pgx.ParseConfig(u.String())
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*scfg)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := migrate.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestPGStore(t *testing.T) {
	db := pgDB(t)
	ctx := context.Background()
	p := &PG{DB: db}
	eng := &directory.Engine{Store: &directory.PG{DB: hellodb.New(db, slog.New(slog.DiscardHandler))}, Drivers: map[string]directory.Driver{directory.KindSCIM: Driver{Store: p}}, Replica: "pg"}
	on, name := true, "Okta"
	c, err := eng.Create(ctx, "admin", directory.ConnectorInput{Kind: directory.KindSCIM, Name: &name, Enabled: &on,
		Options: &directory.Options{LoginMethod: directory.LoginOIDC, OIDCProvider: directory.KindEntra}})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: p, Engine: eng, PublicURL: "https://pbx.example.com"}

	// Tokens: hashed, at most two, revocable, audited.
	t1, tok, err := svc.IssueToken(ctx, "admin", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := db.QueryRowContext(ctx, `SELECT token_hash FROM scim_tokens WHERE id = $1`, t1.ID).Scan(&stored); err != nil || strings.Contains(string(stored), tok[len(TokenPrefix):]) {
		t.Fatalf("stored hash: %v", err)
	}
	if got, err := p.LookupToken(ctx, hashToken(tok)); err != nil || got.ID != t1.ID {
		t.Fatalf("lookup = %+v, %v", got, err)
	}
	if _, _, err := svc.IssueToken(ctx, "admin", c.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.IssueToken(ctx, "admin", c.ID); !errors.Is(err, ErrTooMany) {
		t.Fatalf("third token: %v", err)
	}
	if err := p.TouchToken(ctx, t1.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if last, err := p.LastUsed(ctx, c.ID); err != nil || last == nil {
		t.Fatalf("last used = %v, %v", last, err)
	}
	if _, err := svc.RevokeToken(ctx, "admin", t1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.LookupToken(ctx, hashToken(tok)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked lookup: %v", err)
	}
	var audits int
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE action LIKE 'scim-token-%'`).Scan(&audits)
	if audits != 3 {
		t.Fatalf("token audits = %d", audits)
	}
	ldapName := "AD"
	eng.Drivers[directory.KindLDAP] = directory.LDAP{}
	ad, err := eng.Create(ctx, "admin", directory.ConnectorInput{Kind: directory.KindLDAP, Name: &ldapName,
		Config: json.RawMessage(`{"url":"ldaps://dc.corp.test","bindDn":"cn=svc","baseDn":"dc=corp"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.IssueToken(ctx, "admin", ad.ID); !errors.Is(err, ErrWrongKind) {
		t.Fatalf("token for an ldap connector: %v", err)
	}

	// Resources: insert, conflict, version, delete and revive, members.
	ids := []string{}
	for _, n := range []string{"ann", "bob"} {
		id, _ := newID()
		r, err := p.SaveResource(ctx, Resource{ID: id, ConnectorID: c.ID, Type: TypeUser, Name: n + "@corp.org", ExternalID: "x-" + n,
			Data: map[string]any{"active": true, "emails": []any{map[string]any{"value": n + "@corp.org"}}}}, nil)
		if err != nil || r.Version != 1 {
			t.Fatalf("save = %+v, %v", r, err)
		}
		ids = append(ids, id)
	}
	dup, _ := newID()
	if _, err := p.SaveResource(ctx, Resource{ID: dup, ConnectorID: c.ID, Type: TypeUser, Name: "ANN@corp.org", Data: map[string]any{}}, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate userName: %v", err)
	}
	if _, err := p.SaveResource(ctx, Resource{ID: dup, ConnectorID: c.ID, Type: TypeUser, Name: "cat", ExternalID: "x-bob", Data: map[string]any{}}, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate externalId: %v", err)
	}
	ann, _ := p.GetResource(ctx, c.ID, TypeUser, ids[0], false)
	ann.Data["title"] = "Boss"
	ann2, err := p.SaveResource(ctx, ann, nil)
	if err != nil || ann2.Version != 2 || ann2.Data["title"] != "Boss" {
		t.Fatalf("update = %+v, %v", ann2, err)
	}
	if _, err := p.SaveResource(ctx, ann, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale version: %v", err)
	}
	for f, want := range map[string]int{"name": 1, "external_id": 1, "id": 1} {
		v := map[string]string{"name": "Ann@Corp.org", "external_id": "x-ann", "id": ids[0]}[f]
		rs, err := p.FindResources(ctx, c.ID, TypeUser, &Hint{Field: f, Value: v})
		if err != nil || len(rs) != want {
			t.Fatalf("find by %s = %d, %v", f, len(rs), err)
		}
	}
	gid, _ := newID()
	if _, err := p.SaveResource(ctx, Resource{ID: gid, ConnectorID: c.ID, Type: TypeGroup, Name: "Sales", Data: map[string]any{}}, []string{ids[0], ids[1]}); err != nil {
		t.Fatal(err)
	}
	ms, _ := p.Members(ctx, []string{gid})
	gs, _ := p.GroupsOf(ctx, ids)
	if len(ms[gid]) != 2 || len(gs[ids[1]]) != 1 || gs[ids[1]][0].Name != "Sales" {
		t.Fatalf("members = %v, groups = %v", ms, gs)
	}
	ex, _ := p.Existing(ctx, c.ID, []string{ids[0], "nope", gid})
	if !ex[ids[0]] || ex["nope"] || ex[gid] {
		t.Fatalf("existing = %v", ex)
	}
	g, _ := p.GetResource(ctx, c.ID, TypeGroup, gid, false)
	if _, err := p.SaveResource(ctx, g, []string{ids[1]}); err != nil {
		t.Fatal(err)
	}
	if ms, _ = p.Members(ctx, []string{gid}); len(ms[gid]) != 1 || ms[gid][0].ID != ids[1] {
		t.Fatalf("members after replace = %v", ms)
	}
	if err := p.MarkDeleted(ctx, c.ID, TypeUser, ids[1]); err != nil {
		t.Fatal(err)
	}
	if ms, _ = p.Members(ctx, []string{gid}); len(ms[gid]) != 0 {
		t.Fatalf("a deleted user is listed as a member: %v", ms)
	}
	if _, err := p.GetResource(ctx, c.ID, TypeUser, ids[1], false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted: %v", err)
	}
	old, err := p.FindDeleted(ctx, c.ID, TypeUser, "nobody", "x-bob")
	if err != nil || old == nil || old.ID != ids[1] {
		t.Fatalf("find deleted = %+v, %v", old, err)
	}
	// A new user may take a deleted user's name; reviving is the handler's
	// choice.
	cnt, err := p.Counts(ctx, c.ID)
	if err != nil || cnt.Users != 1 || cnt.DeletedUsers != 1 || cnt.Groups != 1 || cnt.ActiveUsers != 1 {
		t.Fatalf("counts = %+v, %v", cnt, err)
	}
	if err := p.MarkDeleted(ctx, c.ID, TypeGroup, gid); err != nil {
		t.Fatal(err)
	}
	if err := p.MarkDeleted(ctx, c.ID, TypeGroup, gid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second group delete: %v", err)
	}

	// The lock is the engine's: while a SCIM write holds it, a manual run
	// does not start.
	unlock, err := p.Lock(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := (&directory.PG{DB: hellodb.New(db, slog.New(slog.DiscardHandler))}).TryLock(ctx, "hello:dirsync:"+fmt.Sprint(c.ID)); err != nil || ok {
		t.Fatalf("engine lock while SCIM holds it: %v %v", ok, err)
	}
	unlock()

	// End to end over HTTP on PostgreSQL: a create links a Hello user.
	_, tok2, err := svc.IssueToken(ctx, "admin", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "https://pbx.example.com/scim/v2/Users", strings.NewReader(`{"userName":"dan@corp.org","emails":[{"value":"dan@corp.org","primary":true}]}`))
	r.Header.Set("Authorization", "Bearer "+tok2)
	rec := httptest.NewRecorder()
	svc.ServeHTTP(rec, r)
	if rec.Code != 201 {
		t.Fatalf("create over PG = %d %s", rec.Code, rec.Body.String())
	}
	var hello string
	if err := db.QueryRowContext(ctx, `SELECT username FROM users WHERE source_connector_id = $1 AND email = 'dan@corp.org'`, c.ID).Scan(&hello); err != nil {
		t.Fatalf("hello user: %v", err)
	}
}
