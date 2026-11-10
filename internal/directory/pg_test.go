package directory

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/azrtydxb/hello/internal/store"
)

// pgRig is a migrated scratch database with the engine on the PostgreSQL
// store; it skips without HELLO_TEST_DATABASE_URL.
type pgRig struct {
	t   *testing.T
	db  *sql.DB
	pg  *PG
	d   *fakeDriver
	e   *Engine
	c   Connector
	ctx context.Context
}

func newPGRig(t *testing.T) *pgRig {
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
	name := fmt.Sprintf("hello_directory_%d", time.Now().UnixNano())
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
	r := &pgRig{t: t, db: db, ctx: ctx, pg: &PG{DB: db, Box: box}, d: &fakeDriver{res: &Result{}}}
	r.e = &Engine{Store: r.pg, Drivers: map[string]Driver{KindLDAP: r.d}, Replica: "pg1"}
	pw := "bind-password-123"
	r.c, err = r.e.Create(ctx, "admin", ConnectorInput{Kind: KindLDAP, Name: strPtr("AD"), Secret: &pw, Enabled: boolPtr(true),
		Config: json.RawMessage(`{"url":"ldaps://dc"}`)})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *pgRig) exec(q string, args ...any) {
	r.t.Helper()
	if _, err := r.db.ExecContext(r.ctx, q, args...); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
}

func (r *pgRig) scalar(q string, args ...any) string {
	r.t.Helper()
	var s sql.NullString
	if err := r.db.QueryRowContext(r.ctx, q, args...).Scan(&s); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
	return s.String
}

func (r *pgRig) apply() Run {
	r.t.Helper()
	run, err := r.e.RequestRun(r.ctx, "admin", r.c.ID, false)
	if err != nil {
		r.t.Fatal(err)
	}
	r.e.TickOnce(r.ctx)
	run, err = r.pg.GetRun(r.ctx, run.ID)
	if err != nil {
		r.t.Fatal(err)
	}
	return run
}

func TestPGSecretIsSealedAndBoundToTheConnector(t *testing.T) {
	r := newPGRig(t)
	got, err := r.pg.Secret(r.ctx, r.c.ID)
	if err != nil || got != "bind-password-123" {
		t.Fatalf("secret = %q, %v", got, err)
	}
	var enc []byte
	if err := r.db.QueryRowContext(r.ctx, `SELECT credential_enc FROM directory_connectors WHERE id = $1`, r.c.ID).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(enc), "bind-password") || len(enc) == 0 {
		t.Fatal("the credential is not sealed")
	}
	// The AAD binds it to its connector: moved to another row it does not open.
	pw := "x"
	other, err := r.e.Create(r.ctx, "admin", ConnectorInput{Kind: KindLDAP, Name: strPtr("Two"), Secret: &pw})
	if err != nil {
		t.Fatal(err)
	}
	r.exec(`UPDATE directory_connectors SET credential_enc = $2 WHERE id = $1`, other.ID, enc)
	if _, err := r.pg.Secret(r.ctx, other.ID); err == nil {
		t.Fatal("a credential opened under another connector's AAD")
	}
	// Nothing returned by the API shape carries it.
	c, _ := r.pg.GetConnector(r.ctx, r.c.ID)
	b, _ := json.Marshal(c)
	if strings.Contains(string(b), "bind-password") || !c.HasCredential {
		t.Fatalf("connector json = %s", b)
	}
	// A duplicate name is a field error, not a 500.
	_, err = r.e.Create(r.ctx, "admin", ConnectorInput{Kind: KindLDAP, Name: strPtr("ad")})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("duplicate name: %v", err)
	}
	if n := r.scalar(`SELECT count(*) FROM audit_events WHERE resource = 'directory_connector' AND action = 'set-credential'`); n != "2" {
		t.Fatalf("credential audits = %s", n)
	}
}

func TestPGSyncLifecycle(t *testing.T) {
	r := newPGRig(t)
	r.exec(`INSERT INTO extensions (number, name) VALUES ('2001', 'Ann'), ('2002', 'Bob')`)
	r.exec(`INSERT INTO ring_groups (name, strategy) VALUES ('sales', 'ring-all')`)
	rg := r.scalar(`SELECT id FROM ring_groups`)
	r.exec(`INSERT INTO devices (extension_id, sip_username, realm, ha1_md5, ha1_sha256) SELECT id, 'ann-phone', 'r', 'a', 'b' FROM extensions WHERE number = '2001'`)
	var rgID int64
	if _, err := fmt.Sscan(rg, &rgID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.e.Update(r.ctx, "admin", r.c.ID, ConnectorInput{GroupRules: &GroupRules{
		Roles:      []RoleRule{{GroupID: "g-op", Role: "operator"}},
		RingGroups: []RingGroupRule{{GroupID: "g-sales", RingGroupID: rgID}}}}); err != nil {
		t.Fatal(err)
	}
	// A local admin so the last-admin guard has someone to keep.
	r.exec(`INSERT INTO users (username, password_hash, role) VALUES ('root', 'x', 'admin')`)

	r.d.res = &Result{Users: []SourceUser{
		su("g1", "ann@corp", "ann@corp.org", "Ann A", "2001", "g-op", "g-sales"),
		su("g2", "bob@corp", "bob@corp.org", "Bob B", "2002"),
	}}
	run := r.apply()
	if run.Status != StatusSucceeded || run.Counts["create"] != 2 {
		t.Fatalf("run = %+v", run)
	}
	if got := r.scalar(`SELECT role || '/' || auth_method || '/' || password_hash || '/' || external_id FROM users WHERE username = 'ann@corp'`); got != "operator/local/!/g1" {
		t.Fatalf("ann = %q", got)
	}
	if got := r.scalar(`SELECT count(*) FROM user_extensions ue JOIN users u ON u.id = ue.user_id WHERE u.username = 'ann@corp' AND ue.source_connector_id = $1`, r.c.ID); got != "1" {
		t.Fatalf("synced link = %s", got)
	}
	if got := r.scalar(`SELECT extension_id = (SELECT id FROM extensions WHERE number = '2001') FROM users WHERE username = 'ann@corp'`); got != "true" {
		t.Fatalf("primary extension = %s", got)
	}
	if got := r.scalar(`SELECT count(*) FROM ring_group_members WHERE source_connector_id = $1`, r.c.ID); got != "1" {
		t.Fatalf("ring rows = %s", got)
	}
	if got := r.scalar(`SELECT array_to_string(managed_fields, ',') FROM users WHERE username = 'ann@corp'`); got != "username,email,displayName,extension,role,enabled" {
		t.Fatalf("managed = %q", got)
	}
	if got := r.scalar(`SELECT count(*) FROM audit_events WHERE actor = 'directory:AD' AND action = 'create' AND resource = 'user'`); got != "2" {
		t.Fatalf("create audits = %s", got)
	}
	// The users API sees the ownership.
	users, err := store.New(r.db).ListUsers(r.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, u := range users {
		if u.Username == "ann@corp" {
			seen = u.ManagedBy != nil && u.ManagedBy.Connector == "AD" && len(u.ManagedBy.Fields) == 6 && u.AuthMethod == "local"
		}
		if u.Username == "root" && u.ManagedBy != nil {
			t.Fatal("a local user reads as managed")
		}
	}
	if !seen {
		t.Fatalf("users = %+v", users)
	}

	// A session, a token and a device exist, then Ann is inactive in the source.
	r.exec(`INSERT INTO sessions (token_hash, user_id, expires_at) SELECT '\x01', id, now() + interval '1 hour' FROM users WHERE username = 'ann@corp'`)
	r.exec(`INSERT INTO api_tokens (user_id, name, token_hash) SELECT id, 't', '\x02' FROM users WHERE username = 'ann@corp'`)
	off := su("g1", "ann@corp", "ann@corp.org", "Ann A", "2001", "g-op", "g-sales")
	off.Attrs["useraccountcontrol"] = []string{"514"}
	r.d.res.Users[0] = off
	rev := r.scalar(`SELECT config_revision FROM schema_info`)
	r.apply()
	if got := r.scalar(`SELECT enabled::text || '/' || (disabled_at IS NOT NULL)::text FROM users WHERE username = 'ann@corp'`); got != "false/true" {
		t.Fatalf("ann after disable = %q", got)
	}
	if got := r.scalar(`SELECT count(*) FROM sessions`); got != "0" {
		t.Fatalf("sessions = %s", got)
	}
	if got := r.scalar(`SELECT count(*) FROM api_tokens WHERE revoked_at IS NULL`); got != "0" {
		t.Fatalf("live tokens = %s", got)
	}
	if got := r.scalar(`SELECT enabled::text || '/' || disabled_by_directory::text FROM devices WHERE sip_username = 'ann-phone'`); got != "false/true" {
		t.Fatalf("device = %q", got)
	}
	if got := r.scalar(`SELECT count(*) FROM users`); got != "3" {
		t.Fatalf("users deleted: %s", got)
	}
	if r.scalar(`SELECT config_revision FROM schema_info`) == rev {
		t.Fatal("disabling a device did not bump the configuration revision")
	}

	// She returns: user and device come back.
	r.d.res.Users[0] = su("g1", "ann@corp", "ann@corp.org", "Ann A", "2001", "g-op", "g-sales")
	r.apply()
	if got := r.scalar(`SELECT enabled::text FROM users WHERE username = 'ann@corp'`); got != "true" {
		t.Fatalf("ann after return = %q", got)
	}
	if got := r.scalar(`SELECT enabled::text || '/' || disabled_by_directory::text FROM devices WHERE sip_username = 'ann-phone'`); got != "true/false" {
		t.Fatalf("device after return = %q", got)
	}

	// Leaving the sales group removes the synced row only.
	r.exec(`INSERT INTO ring_group_members (group_id, extension_id, position) SELECT $1, id, 5 FROM extensions WHERE number = '2002'`, rgID)
	r.d.res.Users[0] = su("g1", "ann@corp", "ann@corp.org", "Ann A", "2001", "g-op")
	r.apply()
	if got := r.scalar(`SELECT count(*) FROM ring_group_members`); got != "1" {
		t.Fatalf("ring rows = %s (the manual row must stay, the synced one go)", got)
	}
}

func TestPGPreviewDoesNotWriteAndApplyHonoursStale(t *testing.T) {
	r := newPGRig(t)
	r.d.res = &Result{Users: []SourceUser{su("g1", "ann@corp", "", "Ann", "")}}
	run, _ := r.e.RequestRun(r.ctx, "admin", r.c.ID, true)
	r.e.TickOnce(r.ctx)
	pre, _ := r.pg.GetRun(r.ctx, run.ID)
	if pre.Status != StatusPreview || r.scalar(`SELECT count(*) FROM users WHERE source_connector_id IS NOT NULL`) != "0" {
		t.Fatalf("preview = %+v", pre)
	}
	cs, err := r.pg.ListChanges(r.ctx, pre.ID, ChangeFilter{})
	if err != nil || len(cs) != 1 || cs[0].Action != ActionCreate || cs[0].FieldDiffs["displayName"].After != "Ann" {
		t.Fatalf("changes = %+v, %v", cs, err)
	}
	r.d.res.Users = append(r.d.res.Users, su("g2", "bob@corp", "", "Bob", ""))
	if _, err := r.e.Apply(r.ctx, "admin", pre.ID, Decisions{}); !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v", err)
	}
	if got := r.scalar(`SELECT status FROM directory_sync_runs WHERE id = $1`, pre.ID); got != StatusStale {
		t.Fatalf("old preview = %s", got)
	}
}

func TestPGAdoptAndRelease(t *testing.T) {
	r := newPGRig(t)
	r.exec(`INSERT INTO users (username, password_hash, role, email) VALUES ('old', 'bcrypt-hash', 'operator', 'old@corp.org')`)
	r.d.res = &Result{Users: []SourceUser{su("g1", "old@corp", "old@corp.org", "Old", "")}}
	run, _ := r.e.RequestRun(r.ctx, "admin", r.c.ID, true)
	r.e.TickOnce(r.ctx)
	if _, err := r.e.Apply(r.ctx, "admin", run.ID, Decisions{Adopt: []string{"g1"}}); err != nil {
		t.Fatal(err)
	}
	// An adopted user keeps its local password and login until its directory sign-in exists.
	if got := r.scalar(`SELECT auth_method || '/' || password_hash || '/' || external_id || '/' || role FROM users WHERE email = 'old@corp.org'`); got != "local/bcrypt-hash/g1/operator" {
		t.Fatalf("adopted = %q", got)
	}
	if err := r.e.Delete(r.ctx, "admin", r.c.ID, false); !errors.Is(err, ErrHasUsers) {
		t.Fatalf("delete = %v", err)
	}
	if err := r.e.Delete(r.ctx, "admin", r.c.ID, true); err != nil {
		t.Fatal(err)
	}
	if got := r.scalar(`SELECT coalesce(source_connector_id::text, 'none') || '/' || coalesce(array_to_string(managed_fields, ','), '') FROM users WHERE email = 'old@corp.org'`); got != "none/" {
		t.Fatalf("released = %q", got)
	}
	if got := r.scalar(`SELECT count(*) FROM directory_connectors`); got != "0" {
		t.Fatalf("connectors = %s", got)
	}
}

func TestPGLocksOrphansAndRetention(t *testing.T) {
	r := newPGRig(t)
	unlock, ok, err := r.pg.TryLock(r.ctx, lockKey(r.c.ID))
	if err != nil || !ok {
		t.Fatalf("first lock: %v %v", ok, err)
	}
	if _, ok2, _ := r.pg.TryLock(r.ctx, lockKey(r.c.ID)); ok2 {
		t.Fatal("the lock was taken twice")
	}
	unlock()
	u2, ok, _ := r.pg.TryLock(r.ctx, lockKey(r.c.ID))
	if !ok {
		t.Fatal("the lock was not released")
	}
	u2()

	run, _ := r.pg.CreateRun(r.ctx, Run{ConnectorID: r.c.ID, Kind: RunScheduled, Status: StatusRunning, Counts: map[string]int{}})
	if n, err := r.pg.CloseOrphans(r.ctx, r.c.ID); err != nil || n != 1 {
		t.Fatalf("orphans = %d, %v", n, err)
	}
	if got, _ := r.pg.GetRun(r.ctx, run.ID); got.ErrorCode != CodeReplicaLost || got.Status != StatusFailed {
		t.Fatalf("run = %+v", got)
	}
	if err := r.pg.ReplaceChanges(r.ctx, run.ID, []Change{{Action: ActionSkip, ExternalID: "x", Reason: "r"}}); err != nil {
		t.Fatal(err)
	}
	r.exec(`UPDATE directory_sync_changes SET created_at = now() - interval '91 days'`)
	if n, err := r.pg.PruneChanges(r.ctx, time.Now().Add(-ChangeRetention)); err != nil || n != 1 {
		t.Fatalf("pruned = %d, %v", n, err)
	}
	// Two queued runs are claimed one at a time, oldest first.
	a, _ := r.pg.CreateRun(r.ctx, Run{ConnectorID: r.c.ID, Kind: RunPreview, Status: StatusQueued, Counts: map[string]int{}})
	_, _ = r.pg.CreateRun(r.ctx, Run{ConnectorID: r.c.ID, Kind: RunPreview, Status: StatusQueued, Counts: map[string]int{}})
	got, err := r.pg.ClaimQueued(r.ctx, r.c.ID, "pg1")
	if err != nil || got == nil || got.ID != a.ID || got.Status != StatusRunning {
		t.Fatalf("claimed = %+v, %v", got, err)
	}
}

func TestPGLastAdminIsKept(t *testing.T) {
	r := newPGRig(t)
	if _, err := r.e.Update(r.ctx, "admin", r.c.ID, ConnectorInput{Options: &Options{AllowAdminFromDirectory: true},
		GroupRules: &GroupRules{Roles: []RoleRule{{GroupID: "adm", Role: "admin"}}}}); err != nil {
		t.Fatal(err)
	}
	r.d.res = &Result{Users: []SourceUser{su("g1", "a@corp", "", "A", "", "adm")}}
	r.apply()
	off := su("g1", "a@corp", "", "A", "", "adm")
	off.Attrs["useraccountcontrol"] = []string{"2"}
	r.d.res = &Result{Users: []SourceUser{off}}
	r.apply()
	if got := r.scalar(`SELECT enabled::text FROM users WHERE username = 'a@corp'`); got != "true" {
		t.Fatalf("the last admin was disabled: %s", got)
	}
}

func TestPGIncrementalStateAndRunColumns(t *testing.T) {
	r := newPGRig(t)
	at := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	st := SyncState{Mark: &SyncMark{Method: markUSN, USN: 4242, Invocation: "abc"}, Fingerprint: "fp1", LastFullAt: &at}
	if err := r.pg.SaveSyncState(r.ctx, r.c.ID, st); err != nil {
		t.Fatal(err)
	}
	c, err := r.pg.GetConnector(r.ctx, r.c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Sync.Mark == nil || *c.Sync.Mark != *st.Mark || c.Sync.Fingerprint != "fp1" || c.LastFullRunAt == nil || !c.LastFullRunAt.Equal(at) {
		t.Fatalf("state = %+v, last full %v", c.Sync, c.LastFullRunAt)
	}
	// Editing the connector keeps the mark (the fingerprint decides on the next run).
	if _, err := r.e.Update(r.ctx, "admin", r.c.ID, ConnectorInput{Name: strPtr("AD2")}); err != nil {
		t.Fatal(err)
	}
	if c, _ = r.pg.GetConnector(r.ctx, r.c.ID); c.Sync.Mark == nil || c.Sync.Mark.USN != 4242 {
		t.Fatalf("an edit lost the mark: %+v", c.Sync)
	}
	// Clearing it.
	if err := r.pg.SaveSyncState(r.ctx, r.c.ID, SyncState{}); err != nil {
		t.Fatal(err)
	}
	if c, _ = r.pg.GetConnector(r.ctx, r.c.ID); c.Sync.Mark != nil || c.LastFullRunAt != nil {
		t.Fatalf("state not cleared: %+v", c.Sync)
	}
	if err := r.pg.SaveSyncState(r.ctx, 987654, SyncState{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown connector: %v", err)
	}

	run, err := r.pg.CreateRun(r.ctx, Run{ConnectorID: r.c.ID, Kind: RunScheduled, Status: StatusRunning, Counts: map[string]int{}, Incremental: true})
	if err != nil || !run.Incremental {
		t.Fatalf("create: %+v %v", run, err)
	}
	run.Incremental, run.FullReason = false, FullInterval
	if err := r.pg.UpdateRun(r.ctx, run); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.pg.GetRun(r.ctx, run.ID); got.Incremental || got.FullReason != FullInterval {
		t.Fatalf("run = %+v", got)
	}
}
