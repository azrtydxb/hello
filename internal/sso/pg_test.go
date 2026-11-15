package sso

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/azrtydxb/hello/internal/secret"
)

// scratchPG is a migrated scratch database; it skips without
// HELLO_TEST_DATABASE_URL.
func scratchPG(t *testing.T) (*sql.DB, *PG) {
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
	name := fmt.Sprintf("hello_sso_%d", time.Now().UnixNano())
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
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, err := secret.New(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	return db, &PG{DB: db, Box: box}
}

// TestPGStore fails if a provider's secret is not sealed and readable only
// through ProviderSecret, sso_required can be turned on without a
// break-glass admin, an identity can be linked twice, or the synced-user
// lookups miss or mix up their connector kinds.
func TestPGStore(t *testing.T) {
	db, p := scratchPG(t)
	ctx := context.Background()
	s := "very-secret"
	pr, err := p.SaveProvider(ctx, "admin", Provider{Provider: ProviderEntra, Enabled: true, ClientID: "c", Tenant: "t", Domains: []string{}}, &s)
	if err != nil || !pr.HasClientSecret || pr.Tenant != "t" {
		t.Fatalf("save = %+v, %v", pr, err)
	}
	var raw []byte
	_ = db.QueryRowContext(ctx, `SELECT client_secret_enc FROM sso_providers`).Scan(&raw)
	if len(raw) == 0 || string(raw) == s {
		t.Fatal("the client secret is not sealed")
	}
	if got, err := p.ProviderSecret(ctx, ProviderEntra); err != nil || got != s {
		t.Fatalf("secret = %q, %v", got, err)
	}
	// A save without a secret keeps it.
	if pr, err = p.SaveProvider(ctx, "admin", Provider{Provider: ProviderEntra, ClientID: "c2", Domains: []string{}}, nil); err != nil || !pr.HasClientSecret || pr.ClientID != "c2" {
		t.Fatalf("resave = %+v, %v", pr, err)
	}
	if _, err := p.GetProvider(ctx, ProviderGoogle); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing provider: %v", err)
	}
	if err := p.SetSSORequired(ctx, "admin", true); !errors.Is(err, ErrNoBreakGlass) {
		t.Fatalf("no break-glass: %v", err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO users (id, username, password_hash, role, break_glass) VALUES (1, 'root', 'x', 'admin', TRUE)`)
	mustExec(`INSERT INTO directory_connectors (id, kind, name, config) VALUES (5, 'entra', 'E', '{"tenantId":"tid-1"}'), (6, 'ldap', 'AD', '{}')`)
	mustExec(`INSERT INTO users (id, username, password_hash, role, email, source_connector_id, external_id) VALUES
		(2, 'ann', '!', 'user', 'ann@x.example', 5, 'OID-ANN'), (3, 'lee', '!', 'user', 'Lee@x.example', 6, 'guid-lee')`)
	mustExec(`SELECT setval('users_id_seq', 100)`)
	if err := p.SetSSORequired(ctx, "admin", true); err != nil {
		t.Fatal(err)
	}
	if on, _ := p.SSORequired(ctx); !on {
		t.Fatal("sso_required not stored")
	}
	if bg, _ := p.BreakGlassUsers(ctx); len(bg) != 1 || bg[0].Username != "root" {
		t.Fatalf("break-glass = %+v", bg)
	}
	u, err := p.SyncedUser(ctx, "entra", "oid-ann")
	if err != nil || u.ID != 2 || u.ConnectorTenant != "tid-1" {
		t.Fatalf("synced = %+v, %v", u, err)
	}
	if _, err := p.SyncedUser(ctx, "google", "oid-ann"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("kind mix-up: %v", err)
	}
	if us, _ := p.SyncedUsersByEmail(ctx, "LEE@x.example"); len(us) != 1 || us[0].ID != 3 {
		t.Fatalf("by email = %+v", us)
	}
	if us, _ := p.SyncedUsersByEmail(ctx, "root@x.example"); len(us) != 0 {
		t.Fatalf("a local user matched by email: %+v", us)
	}
	id := Identity{UserID: 2, Provider: ProviderEntra, Subject: "oid-ann", Tenant: "tid-1", Email: "ann@x.example"}
	if err := p.Link(ctx, "sso:entra", id); err != nil {
		t.Fatal(err)
	}
	if err := p.Link(ctx, "sso:entra", Identity{UserID: 3, Provider: ProviderEntra, Subject: "oid-ann"}); !errors.Is(err, ErrLinked) {
		t.Fatalf("second link of a subject: %v", err)
	}
	if u, err := p.IdentityUser(ctx, ProviderEntra, "oid-ann"); err != nil || u.ID != 2 {
		t.Fatalf("identity user = %+v, %v", u, err)
	}
	if err := p.TouchIdentity(ctx, ProviderEntra, "oid-ann", time.Now()); err != nil {
		t.Fatal(err)
	}
	if ids, _ := p.ListIdentities(ctx, 2); len(ids) != 1 || ids[0].LastLoginAt == nil {
		t.Fatalf("identities = %+v", ids)
	}
	jit, err := p.CreateUser(ctx, "sso:google", "new@x.example", "new@x.example", "New", Identity{Provider: ProviderGoogle, Subject: "g-new"})
	if err != nil || jit.Role != "user" {
		t.Fatalf("jit = %+v, %v", jit, err)
	}
	var hash string
	_ = db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = $1`, jit.ID).Scan(&hash)
	if hash != "!" {
		t.Fatalf("jit password = %q, want the no-match marker", hash)
	}
	if err := p.Unlink(ctx, "admin", 2, ProviderEntra); err != nil {
		t.Fatal(err)
	}
	if err := p.Unlink(ctx, "admin", 2, ProviderEntra); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second unlink: %v", err)
	}
	// The migration added the new kinds to the live CHECK list.
	mustExec(`INSERT INTO directory_connectors (kind, name) VALUES ('google', 'G')`)
	if _, err := db.ExecContext(ctx, `INSERT INTO directory_connectors (kind, name) VALUES ('okta', 'O')`); err == nil {
		t.Fatal("an unknown connector kind was accepted")
	}
}
