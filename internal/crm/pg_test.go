package crm

// The PostgreSQL repository: every statement against the real schema. The
// tests skip without HELLO_TEST_DATABASE_URL and each runs on its own
// migrated throwaway database.

import (
	"context"
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

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/azrtydxb/hello/internal/store"
)

func scratchPG(t *testing.T) (*PG, *sql.DB) {
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
	name := fmt.Sprintf("hello_crm_%d", time.Now().UnixNano())
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
	box, err := secret.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	return &PG{DB: db, Box: box}, db
}

func pgConnector(t *testing.T, p *PG, name string) ConnectorRow {
	t.Helper()
	c, err := p.CreateConnector(context.Background(), ConnectorInput{Kind: "fake", Name: name, Enabled: true,
		Config: map[string]string{"clientId": "cid"}, Recordings: true, LinkTTLHours: 720, LogMissed: true}, "client-secret")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestPGConnectorsAndSealing fails if a client secret or a token is stored in
// the clear, a sealed value opens under another connector's additional data,
// a refresh that returns no refresh token loses the stored one, or a name is
// not unique.
func TestPGConnectorsAndSealing(t *testing.T) {
	p, db := scratchPG(t)
	ctx := context.Background()
	c := pgConnector(t, p, "HubSpot")
	if !c.HasSecret || c.Config["clientId"] != "cid" || c.LinkTTLHours != 720 {
		t.Fatalf("connector = %+v", c)
	}
	if _, err := p.CreateConnector(ctx, ConnectorInput{Kind: "fake", Name: "HubSpot", LinkTTLHours: 1}, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name = %v", err)
	}
	if s, err := p.ClientSecret(ctx, c.ID); err != nil || s != "client-secret" {
		t.Fatalf("secret = %q, %v", s, err)
	}
	var raw []byte
	if err := db.QueryRowContext(ctx, `SELECT client_secret_enc FROM crm_connectors WHERE id = $1`, c.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if string(raw) == "client-secret" || len(raw) < 20 {
		t.Fatalf("the client secret is stored as %q", raw)
	}
	other := pgConnector(t, p, "Other")
	if _, err := p.Box.Open(raw, secret.CRMSecretAAD(other.ID)); err == nil {
		t.Fatal("a secret sealed for one connector opens for another")
	}
	keep, err := p.UpdateConnector(ctx, c.ID, ConnectorInput{Kind: "fake", Name: "HubSpot 2", Enabled: false, LinkTTLHours: 24, Config: map[string]string{"clientId": "cid"}}, nil)
	if err != nil || keep.Name != "HubSpot 2" || keep.Enabled || !keep.HasSecret || keep.LinkTTLHours != 24 {
		t.Fatalf("update keeps the secret: %+v, %v", keep, err)
	}
	empty := ""
	cleared, err := p.UpdateConnector(ctx, c.ID, ConnectorInput{Kind: "fake", Name: "HubSpot 2", LinkTTLHours: 24}, &empty)
	if err != nil || cleared.HasSecret {
		t.Fatalf("clearing the secret: %+v, %v", cleared, err)
	}
	if _, err := p.UpdateConnector(ctx, 9999, ConnectorInput{Name: "x", LinkTTLHours: 1}, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update of a missing connector = %v", err)
	}
	if _, err := p.UpdateConnector(ctx, c.ID, ConnectorInput{Name: "Other", LinkTTLHours: 1}, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("rename onto a taken name = %v", err)
	}

	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if _, err := p.SaveOrgConnection(ctx, c.ID, Tokens{Access: "ACCESS", Refresh: "REFRESH", ExpiresAt: exp, Account: "42", Scopes: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	var a, r []byte
	if err := db.QueryRowContext(ctx, `SELECT access_enc, refresh_enc FROM crm_connections WHERE connector_id = $1`, c.ID).Scan(&a, &r); err != nil {
		t.Fatal(err)
	}
	if string(a) == "ACCESS" || string(r) == "REFRESH" {
		t.Fatal("a token is stored in the clear")
	}
	// A refresh without a new refresh token keeps the old one.
	if _, err := p.SaveOrgConnection(ctx, c.ID, Tokens{Access: "ACCESS2", ExpiresAt: exp.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	cr, tk, err := p.OrgConnection(ctx, c.ID)
	if err != nil || tk.Access != "ACCESS2" || tk.Refresh != "REFRESH" || tk.Account != "42" || len(tk.Scopes) != 2 || cr.Status != StatusConnected || !cr.ExpiresAt.Equal(exp.Add(time.Hour)) {
		t.Fatalf("connection = %+v, tokens = %+v, %v", cr, tk, err)
	}
	if err := p.SetConnectionStatus(ctx, cr.ID, StatusNeedsReauth, "refused"); err != nil {
		t.Fatal(err)
	}
	if cr, _, _ = p.OrgConnection(ctx, c.ID); cr.Status != StatusNeedsReauth || cr.LastError != "refused" {
		t.Fatalf("status = %+v", cr)
	}
	// Signing in again resets the status.
	if _, err := p.SaveOrgConnection(ctx, c.ID, Tokens{Access: "ACCESS3", Refresh: "R3"}); err != nil {
		t.Fatal(err)
	}
	cr, _, _ = p.OrgConnection(ctx, c.ID)
	if cr.Status != StatusConnected || cr.LastError != "" || !cr.ExpiresAt.IsZero() {
		t.Fatalf("after reconnect: %+v", cr)
	}
	if _, _, err := p.OrgConnection(ctx, other.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no connection = %v", err)
	}
	if err := p.DeleteOrgConnection(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := p.DeleteOrgConnection(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second disconnect = %v", err)
	}
	if err := p.DeleteConnector(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	if err := p.DeleteConnector(ctx, other.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
}

// TestPGOAuthState fails if a state can be used twice, after its expiry, or
// is stored in the clear.
func TestPGOAuthState(t *testing.T) {
	p, db := scratchPG(t)
	ctx := context.Background()
	c := pgConnector(t, p, "A")
	st, err := p.NewState(ctx, c.ID, "user:admin", time.Minute)
	if err != nil || len(st) < 40 {
		t.Fatalf("state = %q, %v", st, err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM crm_oauth_states WHERE encode(state_hash, 'escape') LIKE '%' || $1 || '%'`, st).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the state is stored in the clear (%d, %v)", n, err)
	}
	id, actor, err := p.TakeState(ctx, st)
	if err != nil || id != c.ID || actor != "user:admin" {
		t.Fatalf("take = %d %q %v", id, actor, err)
	}
	if _, _, err := p.TakeState(ctx, st); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a state was taken twice: %v", err)
	}
	old, _ := p.NewState(ctx, c.ID, "x", time.Minute)
	if _, err := db.ExecContext(ctx, `UPDATE crm_oauth_states SET expires_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.TakeState(ctx, old); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an expired state was taken: %v", err)
	}
	if err := p.PurgeStates(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM crm_oauth_states`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("purge left %d states", n)
	}
}

func connect(t *testing.T, p *PG, id int64) {
	t.Helper()
	if _, err := p.SaveOrgConnection(context.Background(), id, Tokens{Access: "a", Refresh: "r", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
}

// TestPGCallLogLifecycle walks one call through the statements the hook, the
// worker and the renewer run: a recording that arrives first, the call data
// that fills the placeholder, the claim with its lease, the settle, a later
// recording, the renewal and a dead letter retry. It fails if a duplicate
// event queues a second job, a waiting row is claimed, a claimed row is
// claimed again before its lease ends, or a recording that arrives while the
// job runs is lost.
func TestPGCallLogLifecycle(t *testing.T) {
	p, _ := scratchPG(t)
	ctx := context.Background()
	c := pgConnector(t, p, "A")
	connect(t, p, c.ID)

	active, err := p.ActiveConnectors(ctx, nil)
	if err != nil || len(active) != 1 {
		t.Fatalf("active = %v, %v", active, err)
	}
	if err := p.AttachRecording(ctx, nil, c.ID, "call-1", 42); err != nil {
		t.Fatal(err)
	}
	rows, err := p.ClaimDue(ctx, 10, time.Minute)
	if err != nil || len(rows) != 0 {
		t.Fatalf("a waiting row was claimed: %v, %v", rows, err)
	}
	job := LogJob{CallID: "call-1", Direction: "inbound", EventType: "call.completed", Payload: []byte(`{"callId":"call-1"}`), OwnerExtension: "101"}
	for range 2 {
		if err := p.UpsertCall(ctx, nil, c.ID, job); err != nil {
			t.Fatal(err)
		}
	}
	list, err := p.ListLogs(ctx, c.ID, "", 10, 0)
	if err != nil || len(list) != 1 || list[0].Status != LogPending || list[0].RecordingID != 42 || list[0].OwnerExtension != "101" {
		t.Fatalf("after upsert: %+v, %v", list, err)
	}
	rows, err = p.ClaimDue(ctx, 10, time.Minute)
	if err != nil || len(rows) != 1 || string(rows[0].Payload) != `{"callId": "call-1"}` && string(rows[0].Payload) != `{"callId":"call-1"}` {
		t.Fatalf("claim = %+v, %v", rows, err)
	}
	if again, _ := p.ClaimDue(ctx, 10, time.Minute); len(again) != 0 {
		t.Fatal("a leased job was claimed twice")
	}
	row := rows[0]
	// A recording arrives while the job runs (the worker saw recording 42).
	if err := p.AttachRecording(ctx, nil, c.ID, "call-1", 43); err != nil {
		t.Fatal(err)
	}
	exp := time.Now().Add(30 * 24 * time.Hour)
	if err := p.Finish(ctx, row.ID, LogResult{Status: LogLogged, ExternalID: "ext-1", NextAttemptAt: time.Now(), LinkExpiresAt: &exp, RecordingSeen: 42}); err != nil {
		t.Fatal(err)
	}
	got, _ := p.GetLog(ctx, row.ID)
	if got.Status != LogUpdate || got.ExternalID != "ext-1" || got.LinkExpiresAt == nil || got.RecordingID != 43 {
		t.Fatalf("a recording that arrived mid-job was lost: %+v", got)
	}
	rows, _ = p.ClaimDue(ctx, 10, time.Minute)
	if len(rows) != 1 || rows[0].ExternalID != "ext-1" {
		t.Fatalf("update claim = %+v", rows)
	}
	if err := p.Finish(ctx, row.ID, LogResult{Status: LogLogged, NextAttemptAt: time.Now(), RecordingSeen: 43}); err != nil {
		t.Fatal(err)
	}
	got, _ = p.GetLog(ctx, row.ID)
	if got.Status != LogLogged || got.ExternalID != "ext-1" || got.LinkExpiresAt == nil || got.Attempts != 0 {
		t.Fatalf("settled: %+v", got)
	}

	// Renewal: a link with days left is left alone, one about to expire is
	// queued, an old call is not.
	if n, err := p.MarkRenewals(ctx, time.Now(), RenewWindow, RenewMaxAge); err != nil || n != 0 {
		t.Fatalf("renewals = %d, %v", n, err)
	}
	if n, err := p.MarkRenewals(ctx, time.Now().Add(26*24*time.Hour), RenewWindow, RenewMaxAge); err != nil || n != 1 {
		t.Fatalf("renewals = %d, %v", n, err)
	}
	if n, err := p.MarkRenewals(ctx, time.Now().Add(400*24*time.Hour), RenewWindow, RenewMaxAge); err != nil || n != 0 {
		t.Fatalf("an old call was renewed (%d, %v)", n, err)
	}

	// A dead letter: retry restarts the series; a live job cannot be retried.
	if err := p.UpsertCall(ctx, nil, c.ID, LogJob{CallID: "call-2", Direction: "outbound", EventType: "call.completed", Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	two, _ := p.ListLogs(ctx, c.ID, LogPending, 10, 0)
	if len(two) != 1 {
		t.Fatalf("pending = %+v", two)
	}
	if err := p.RetryLog(ctx, two[0].ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("retry of a pending job = %v", err)
	}
	if err := p.RetryLog(ctx, 99999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("retry of a missing job = %v", err)
	}
	if err := p.Finish(ctx, two[0].ID, LogResult{Status: LogDead, Attempts: 8, NextAttemptAt: time.Now(), LastError: "boom"}); err != nil {
		t.Fatal(err)
	}
	dead, _ := p.ListLogs(ctx, c.ID, LogDead, 10, 0)
	if len(dead) != 1 || dead[0].LastError != "boom" {
		t.Fatalf("dead = %+v", dead)
	}
	if err := p.RetryLog(ctx, dead[0].ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := p.GetLog(ctx, dead[0].ID); got.Status != LogPending || got.Attempts != 0 || got.LastError != "" {
		t.Fatalf("retried: %+v", got)
	}

	// Skipped rows keep their payload so they can be retried.
	if err := p.Skip(ctx, nil, c.ID, LogJob{CallID: "call-3", Direction: "inbound", EventType: "call.completed", Payload: []byte(`{"x":1}`)}, "no owner"); err != nil {
		t.Fatal(err)
	}
	sk, _ := p.ListLogs(ctx, c.ID, LogSkipped, 10, 0)
	if len(sk) != 1 || sk[0].LastError != "no owner" {
		t.Fatalf("skipped = %+v", sk)
	}
	if err := p.RetryLog(ctx, sk[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := p.Skip(ctx, nil, c.ID, LogJob{CallID: "call-4", Direction: "inbound"}, "no data"); err != nil {
		t.Fatalf("a skip without a payload: %v", err)
	}

	// Paging: newest first, keyset.
	all, _ := p.ListLogs(ctx, c.ID, "", 2, 0)
	if len(all) != 2 || all[0].ID < all[1].ID {
		t.Fatalf("page = %+v", all)
	}
	rest, _ := p.ListLogs(ctx, c.ID, "", 10, all[1].ID)
	for _, r := range rest {
		if r.ID >= all[1].ID {
			t.Fatalf("keyset paging repeated %d", r.ID)
		}
	}
	stats, err := p.Stats(ctx)
	if err != nil || len(stats) == 0 {
		t.Fatalf("stats = %v, %v", stats, err)
	}
}

// TestPGClaimSkipsDisabledAndUnconnected fails if a job of a disabled
// connector, or of one whose connection needs re-authorisation, is claimed.
func TestPGClaimSkipsDisabledAndUnconnected(t *testing.T) {
	p, _ := scratchPG(t)
	ctx := context.Background()
	c := pgConnector(t, p, "A")
	if err := p.UpsertCall(ctx, nil, c.ID, LogJob{CallID: "x", Direction: "inbound", EventType: "call.completed", Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if rows, _ := p.ClaimDue(ctx, 10, time.Minute); len(rows) != 0 {
		t.Fatal("a job of a connector with no connection was claimed")
	}
	connect(t, p, c.ID)
	cr, _, _ := p.OrgConnection(ctx, c.ID)
	_ = p.SetConnectionStatus(ctx, cr.ID, StatusNeedsReauth, "x")
	if rows, _ := p.ClaimDue(ctx, 10, time.Minute); len(rows) != 0 {
		t.Fatal("a job of a connection that needs re-authorisation was claimed")
	}
	_ = p.SetConnectionStatus(ctx, cr.ID, StatusConnected, "")
	if _, err := p.UpdateConnector(ctx, c.ID, ConnectorInput{Kind: "fake", Name: "A", Enabled: false, LinkTTLHours: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if rows, _ := p.ClaimDue(ctx, 10, time.Minute); len(rows) != 0 {
		t.Fatal("a job of a disabled connector was claimed")
	}
	if act, _ := p.ActiveConnectors(ctx, nil); len(act) != 0 {
		t.Fatal("a disabled connector is active")
	}
}

// TestPGEngagementHandshake fails if the iframe's engagement replaces the
// server's own or a second claim of the same call is not refused.
func TestPGEngagementHandshake(t *testing.T) {
	p, _ := scratchPG(t)
	ctx := context.Background()
	c := pgConnector(t, p, "A")
	if ok, err := p.AttachEngagement(ctx, c.ID, "c1", "111"); err != nil || !ok {
		t.Fatalf("first = %v, %v", ok, err)
	}
	if ok, err := p.AttachEngagement(ctx, c.ID, "c1", "222"); err != nil || ok {
		t.Fatalf("second = %v, %v: the first engagement must stay", ok, err)
	}
	if ok, err := p.AttachEngagement(ctx, c.ID, "c1", "111"); err != nil || !ok {
		t.Fatalf("the same engagement again = %v, %v", ok, err)
	}
	if err := p.UpsertCall(ctx, nil, c.ID, LogJob{CallID: "c1", Direction: "outbound", EventType: "call.completed", Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	l, _ := p.ListLogs(ctx, c.ID, "", 10, 0)
	if len(l) != 1 || l[0].Status != LogPending || l[0].ExternalID != "111" {
		t.Fatalf("log = %+v", l)
	}
}

// TestPGUsersMappingsAndOwnership covers the user queries: the user of an
// extension, the mapping with manual precedence, the administrators' emails
// and the ownership check of an event.
func TestPGUsersMappingsAndOwnership(t *testing.T) {
	p, db := scratchPG(t)
	ctx := context.Background()
	st := store.New(db)
	hash, err := auth.HashPassword("a-Long-Password-1")
	if err != nil {
		t.Fatal(err)
	}
	// The first user is always an administrator, so the administrator comes first.
	bossID, err := st.CreateUser(ctx, "test", "boss", hash, auth.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	annaID, err := st.CreateUser(ctx, "test", "anna", hash, auth.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE users SET email = $2 WHERE id = $1`, annaID, "anna@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE users SET email = 'boss@example.com' WHERE id = $1`, bossID); err != nil {
		t.Fatal(err)
	}
	ext, err := st.CreateExtension(ctx, "test", "101", "Anna", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO user_extensions (user_id, extension_id) VALUES ($1, $2)`, annaID, ext.ID); err != nil {
		t.Fatal(err)
	}
	u, ok, err := p.UserForExtension(ctx, "101")
	if err != nil || !ok || u.ID != annaID || u.Email != "anna@example.com" {
		t.Fatalf("user for 101 = %+v, %v, %v", u, ok, err)
	}
	if _, ok, err := p.UserForExtension(ctx, "999"); ok || err != nil {
		t.Fatalf("user for 999 = %v, %v", ok, err)
	}
	users, err := p.Users(ctx)
	if err != nil || len(users) != 2 {
		t.Fatalf("users = %+v, %v", users, err)
	}
	for _, us := range users {
		if us.Username == "anna" && us.Extension != "101" {
			t.Fatalf("anna's extension = %q", us.Extension)
		}
	}
	if em, err := p.AdminEmails(ctx); err != nil || len(em) != 1 || em[0] != "boss@example.com" {
		t.Fatalf("admin emails = %v, %v", em, err)
	}

	c := pgConnector(t, p, "A")
	if err := p.SetMapping(ctx, c.ID, annaID, "auto-1", "email"); err != nil {
		t.Fatal(err)
	}
	if err := p.SetMapping(ctx, c.ID, annaID, "hand-2", "manual"); err != nil {
		t.Fatal(err)
	}
	if err := p.SetMapping(ctx, c.ID, annaID, "auto-3", "email"); err != nil {
		t.Fatal(err)
	}
	ms, err := p.Mappings(ctx, c.ID)
	if err != nil || len(ms) != 1 || ms[0].ExternalUserID != "hand-2" || ms[0].Source != "manual" {
		t.Fatalf("an automatic mapping replaced a manual one: %+v, %v", ms, err)
	}
	if err := p.DeleteMapping(ctx, c.ID, annaID); err != nil {
		t.Fatal(err)
	}
	if ms, _ := p.Mappings(ctx, c.ID); len(ms) != 0 {
		t.Fatalf("mappings = %+v", ms)
	}

	for i, e := range []struct{ id, call, ext string }{{"e1", "call-1", "101"}, {"e2", "call-2", "102"}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO integration_events (id, cursor_ms, cursor_seq, type, created_at, correlation_id, extension_number, payload)
			VALUES ($1, 1, $2, 'call.ended', now(), $3, $4, '{}'::jsonb)`, e.id, i, e.call, e.ext); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		call string
		exts []string
		want bool
	}{{"call-1", []string{"101"}, true}, {"call-1", []string{"102"}, false}, {"call-2", []string{"101", "102"}, true}, {"call-1", nil, false}, {"nope", []string{"101"}, false}} {
		if got, err := p.CallInvolves(ctx, tc.call, tc.exts); err != nil || got != tc.want {
			t.Errorf("CallInvolves(%s, %v) = %v, %v; want %v", tc.call, tc.exts, got, err, tc.want)
		}
	}
	if err := p.Audit(ctx, "user:anna", "create", "crm_connector", "1"); err != nil {
		t.Fatal(err)
	}
}

// TestPGCascadeOnConnectorDelete fails if deleting a connector leaves its
// connection, jobs, mappings or states behind.
func TestPGCascadeOnConnectorDelete(t *testing.T) {
	p, db := scratchPG(t)
	ctx := context.Background()
	c := pgConnector(t, p, "A")
	connect(t, p, c.ID)
	_ = p.UpsertCall(ctx, nil, c.ID, LogJob{CallID: "x", Direction: "inbound", EventType: "call.completed", Payload: []byte(`{}`)})
	_, _ = p.NewState(ctx, c.ID, "a", time.Minute)
	if err := p.DeleteConnector(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"crm_connections", "crm_call_logs", "crm_oauth_states", "crm_user_map"} {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+tbl).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s has %d rows after the connector was deleted (%v)", tbl, n, err)
		}
	}
}
