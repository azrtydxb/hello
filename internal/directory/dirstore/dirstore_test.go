package dirstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	hellodb "github.com/azrtydxb/hello/internal/db"
	"github.com/azrtydxb/hello/internal/directory"
	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/azrtydxb/hello/internal/store"
)

// scratchDB is a migrated scratch database; it skips without
// HELLO_TEST_DATABASE_URL.
func scratchDB(t *testing.T) *sql.DB {
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
	name := fmt.Sprintf("hello_dirstore_%d", time.Now().UnixNano())
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

type rig struct {
	t   *testing.T
	ctx context.Context
	db  *sql.DB
	st  *store.Store
	drv *directory.Static
	e   *directory.Engine
	c   directory.Connector
}

func newRig(t *testing.T, ap directory.AutoProvision) *rig {
	t.Helper()
	db := scratchDB(t)
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, err := secret.New(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, ctx: context.Background(), db: db, st: store.New(db, nil), drv: &directory.Static{Res: &directory.Result{}}}
	pg := &directory.PG{DB: hellodb.New(db, slog.New(slog.DiscardHandler)), Box: box, Provisioner: &Provisioner{Store: r.st, Realm: "pbx.test"}}
	r.e = &directory.Engine{Store: pg, Drivers: map[string]directory.Driver{directory.KindLDAP: r.drv}, Replica: "t"}
	pw, name, on := "bind-password-123", "AD", true
	r.c, err = r.e.Create(r.ctx, "admin", directory.ConnectorInput{Kind: directory.KindLDAP, Name: &name, Secret: &pw, Enabled: &on,
		Config: json.RawMessage(`{"url":"ldaps://dc","ldapBindLogin":true}`), AutoProvision: &ap})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func user(id, upn, mail, name string) directory.SourceUser {
	return directory.SourceUser{ExternalID: id, Attrs: map[string][]string{"userprincipalname": {upn}, "mail": {mail},
		"displayname": {name}, "useraccountcontrol": {"512"}}}
}

// sync runs a preview and applies it.
func (r *rig) sync(users ...directory.SourceUser) map[string]directory.Change {
	r.t.Helper()
	r.drv.Res = &directory.Result{Users: users}
	pre, err := r.e.RequestRun(r.ctx, "admin", r.c.ID, true)
	if err != nil {
		r.t.Fatal(err)
	}
	r.e.TickOnce(r.ctx)
	run, err := r.e.Apply(r.ctx, "admin", pre.ID, directory.Decisions{})
	if err != nil {
		r.t.Fatalf("apply: %v", err)
	}
	cs, err := r.e.ListChanges(r.ctx, run.ID, directory.ChangeFilter{})
	if err != nil {
		r.t.Fatal(err)
	}
	out := map[string]directory.Change{}
	for _, c := range cs {
		out[c.ExternalID] = c
	}
	return out
}

func (r *rig) one(q string, args ...any) string {
	r.t.Helper()
	var s sql.NullString
	if err := r.db.QueryRowContext(r.ctx, q, args...).Scan(&s); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
	return s.String
}

func compile(cfg routing.Config) []routing.FieldError {
	_, errs := routing.Compile(cfg)
	return errs
}

func TestProvisionOnPostgres(t *testing.T) {
	r := newRig(t, directory.AutoProvision{Enabled: true, RangeStart: "2000", RangeEnd: "2003", Device: true, Voicemail: true})
	if _, err := r.st.CreateExtension(r.ctx, "admin", "2000", "Manual", "", compile); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(r.ctx, `INSERT INTO ring_groups (name, strategy) VALUES ('2001', 'ring-all')`); err != nil {
		t.Fatal(err)
	}
	cs := r.sync(user("a", "ann@corp", "ann@corp.org", "Ann Example"), user("b", "bob@corp", "", "Bob"))
	if a := cs["a"]; a.Action != directory.ActionCreate || a.FieldDiffs["extension"].After != "2002" || a.Reason != directory.ReasonAutoProvisioned {
		t.Fatalf("a = %+v", a)
	}
	if b := cs["b"]; b.FieldDiffs["extension"].After != "2003" {
		t.Fatalf("b = %+v", b)
	}
	// The extension, its voicemail box and device, marked and linked.
	if got := r.one(`SELECT e.name || '|' || e.voicemail_enabled || '|' || e.provisioned_by || '|' || v.email
		FROM extensions e JOIN voicemail_boxes v ON v.extension_id = e.id WHERE e.number = '2002'`); got != fmt.Sprintf("Ann Example|true|%d|ann@corp.org", r.c.ID) {
		t.Fatalf("2002 = %s", got)
	}
	if got := r.one(`SELECT d.sip_username || '|' || d.realm || '|' || (d.ha1_md5 <> '') FROM devices d JOIN extensions e ON e.id = d.extension_id WHERE e.number = '2002'`); got != "2002|pbx.test|true" {
		t.Fatalf("device = %s", got)
	}
	if got := r.one(`SELECT u.provisioned_number || '|' || ue.source_connector_id FROM users u
		JOIN user_extensions ue ON ue.user_id = u.id JOIN extensions e ON e.id = ue.extension_id
		WHERE u.username = 'ann@corp' AND e.number = '2002'`); got != fmt.Sprintf("2002|%d", r.c.ID) {
		t.Fatalf("link = %s", got)
	}
	if got := r.one(`SELECT count(*)::text FROM audit_events WHERE actor = 'directory:AD' AND action IN ('provision', 'create')
		AND resource IN ('user', 'extension', 'device')`); got != "8" {
		t.Fatalf("audit rows = %s", got)
	}
	// Full range: the next user is a conflict.
	cs = r.sync(user("a", "ann@corp", "ann@corp.org", "Ann Example"), user("b", "bob@corp", "", "Bob"), user("c", "cat@corp", "", "Cat"))
	if c := cs["c"]; c.Action != directory.ActionConflict || c.Reason != directory.ReasonRangeExhausted {
		t.Fatalf("c = %+v", c)
	}
	if _, ok := cs["a"]; ok {
		t.Fatalf("a re-run changed a: %+v", cs["a"])
	}
	// A display name and email change follow on the provisioned extension.
	r.sync(user("a", "ann@corp", "ann@new.org", "Ann Renamed"), user("b", "bob@corp", "", "Bob"))
	if got := r.one(`SELECT e.name || '|' || v.email FROM extensions e JOIN voicemail_boxes v ON v.extension_id = e.id WHERE e.number = '2002'`); got != "Ann Renamed|ann@new.org" {
		t.Fatalf("followed = %s", got)
	}
	// A deleted extension comes back with the same number.
	var ext int64
	if err := r.db.QueryRowContext(r.ctx, `SELECT id FROM extensions WHERE number = '2003'`).Scan(&ext); err != nil {
		t.Fatal(err)
	}
	if err := r.st.DeleteExtension(r.ctx, "admin", ext, compile); err != nil {
		t.Fatal(err)
	}
	cs = r.sync(user("a", "ann@corp", "ann@new.org", "Ann Renamed"), user("b", "bob@corp", "", "Bob"))
	if b := cs["b"]; b.Action != directory.ActionUpdate || b.FieldDiffs["extension"].After != "2003" {
		t.Fatalf("b = %+v", b)
	}
	u, err := r.e.RangeUsage(r.ctx, r.c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u.Size != 4 || u.Used != 4 || u.NextFree != "" || len(u.Provisioned) != 2 {
		t.Fatalf("usage = %+v", u)
	}
	// Deleting the connector keeps the extensions and unmarks them.
	if err := r.e.Delete(r.ctx, "admin", r.c.ID, true); err != nil {
		t.Fatal(err)
	}
	if got := r.one(`SELECT count(*)::text FROM extensions WHERE number IN ('2002', '2003') AND provisioned_by IS NULL`); got != "2" {
		t.Fatalf("after delete = %s", got)
	}
}

func TestProvisionerPreferredAndCodes(t *testing.T) {
	db := scratchDB(t)
	st := store.New(db, nil)
	ctx := context.Background()
	p := &Provisioner{Store: st, Realm: "pbx.test"}
	if _, err := db.ExecContext(ctx, `INSERT INTO directory_connectors (kind, name) VALUES ('ldap', 'X')`); err != nil {
		t.Fatal(err)
	}
	var cid int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM directory_connectors`).Scan(&cid); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	// A park lot's slots are taken.
	if _, err := tx.ExecContext(ctx, `INSERT INTO park_lots (name, slot_start, slot_end) VALUES ('p31', 3100, 3199)`); err != nil {
		t.Fatal(err)
	}
	taken, err := p.Taken(ctx, tx, "3000", "3200")
	if err != nil {
		t.Fatal(err)
	}
	if !taken["3100"] || !taken["3199"] || taken["3099"] || taken["3200"] {
		t.Fatalf("taken = %d numbers, 3100 %v 3099 %v", len(taken), taken["3100"], taken["3099"])
	}
	// The preferred number wins even outside the range; a taken one does not.
	res, err := p.Provision(ctx, tx, "directory:X", directory.ProvisionRequest{ConnectorID: cid, Preferred: "4500", RangeStart: "3000", RangeEnd: "3001",
		Name: "Pat", Device: false, Voicemail: false})
	if err != nil || res.Number != "4500" || res.DeviceID != 0 {
		t.Fatalf("preferred = %+v, %v", res, err)
	}
	res, err = p.Provision(ctx, tx, "directory:X", directory.ProvisionRequest{ConnectorID: cid, Preferred: "4500", RangeStart: "3000", RangeEnd: "3001", Device: true})
	if err != nil || res.Number != "3000" || res.DeviceID == 0 {
		t.Fatalf("fallback = %+v, %v", res, err)
	}
	if _, err := p.Provision(ctx, tx, "directory:X", directory.ProvisionRequest{ConnectorID: cid, RangeStart: "3000", RangeEnd: "3000"}); !errors.Is(err, directory.ErrRangeExhausted) {
		t.Fatalf("exhausted: %v", err)
	}
}

// TestProvisionAdoptBreakGlassAndRelease fails if, with range
// auto-provisioning on, an adopted local user does not lose its local
// password (I-29) and get a number from the range, a break-glass admin with a
// matching email is touched, or releasing the connector's users drops the
// provisioned number, the extension, or brings a local password back.
func TestProvisionAdoptBreakGlassAndRelease(t *testing.T) {
	r := newRig(t, directory.AutoProvision{Enabled: true, RangeStart: "500", RangeEnd: "509", Device: true, Voicemail: true})
	if _, err := r.db.ExecContext(r.ctx, `INSERT INTO users (username, password_hash, role, email) VALUES
		('legacy', 'argon-hash', 'user', 'legacy@corp.org'),
		('root', 'root-hash', 'admin', 'root@corp.org')`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(r.ctx, `UPDATE users SET break_glass = TRUE WHERE username = 'root'`); err != nil {
		t.Fatal(err)
	}
	r.drv.Res = &directory.Result{Users: []directory.SourceUser{
		user("l", "legacy@corp", "legacy@corp.org", "Legacy User"),
		user("x", "root@corp", "root@corp.org", "Root"),
	}}
	pre, err := r.e.RequestRun(r.ctx, "admin", r.c.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	r.e.TickOnce(r.ctx)
	run, err := r.e.Apply(r.ctx, "admin", pre.ID, directory.Decisions{Adopt: []string{"l"}})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := r.e.ListChanges(r.ctx, run.ID, directory.ChangeFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.ExternalID == "x" && (c.Action != directory.ActionSkip || c.Reason != directory.ReasonProtected) {
			t.Fatalf("break-glass row = %+v", c)
		}
	}
	if got := r.one(`SELECT u.password_hash || '|' || u.must_change_password || '|' || u.external_id || '|' || u.provisioned_number || '|' || e.provisioned_by
		FROM users u JOIN user_extensions ue ON ue.user_id = u.id JOIN extensions e ON e.id = ue.extension_id
		WHERE u.email = 'legacy@corp.org'`); got != fmt.Sprintf("!|false|l|500|%d", r.c.ID) {
		t.Fatalf("adopted = %s", got)
	}
	if got := r.one(`SELECT password_hash || '|' || (source_connector_id IS NULL) || '|' || coalesce(provisioned_number, '') || '|' ||
		(SELECT count(*) FROM user_extensions WHERE user_id = users.id) FROM users WHERE username = 'root'`); got != "root-hash|true||0" {
		t.Fatalf("break-glass admin = %s", got)
	}
	var id int64
	if err := r.db.QueryRowContext(r.ctx, `SELECT id FROM users WHERE email = 'legacy@corp.org'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if managed, err := r.st.UserManagedByDirectory(r.ctx, id); err != nil || !managed {
		t.Fatalf("managed = %v, %v", managed, err)
	}
	// The adopted user took the directory's username (its UPN).
	if got := r.one(`SELECT username FROM users WHERE id = $1`, id); got != "legacy@corp" {
		t.Fatalf("username = %s", got)
	}
	// Released, the user is local again: no password until an administrator
	// sets one, and the number it was given stays its own.
	if err := r.e.Delete(r.ctx, "admin", r.c.ID, true); err != nil {
		t.Fatal(err)
	}
	if got := r.one(`SELECT u.password_hash || '|' || (u.source_connector_id IS NULL) || '|' || u.provisioned_number || '|' || e.number || '|' || (e.provisioned_by IS NULL)
		FROM users u JOIN user_extensions ue ON ue.user_id = u.id JOIN extensions e ON e.id = ue.extension_id
		WHERE u.email = 'legacy@corp.org'`); got != "!|true|500|500|true" {
		t.Fatalf("released = %s", got)
	}
	if managed, err := r.st.UserManagedByDirectory(r.ctx, id); err != nil || managed {
		t.Fatalf("released managed = %v, %v", managed, err)
	}
}
