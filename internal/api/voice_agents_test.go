package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/config"
	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/internal/voice"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// voiceEnv is the API on a scratch database with the voice registry wired
// to the same store and a secret box, like cmd does.
type voiceEnv struct {
	*env
	reg *voice.Registry
	box *secret.Box
}

// voiceCredential opens a server's sealed credential through the store.
func (e *voiceEnv) voiceCredential(ctx context.Context, id string) (string, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return "", err
	}
	return e.st.VoiceMCPServerCredential(ctx, n)
}

// newVoiceEnv is newEnvConfig plus the voice registry and its box; the DB
// is created here because the registry must be in the Config before the
// handler is built.
func newVoiceEnv(t *testing.T) *voiceEnv {
	t.Helper()
	dsn := os.Getenv("HELLO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HELLO_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pgcfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*pgcfg)
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("hello_api_voice_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop scratch database %s: %v", name, err)
		}
	})
	u, _ := url.Parse(dsn)
	u.Path = "/" + name
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrate.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("b", 32)))
	box, err := secret.New(key)
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(db).WithSecretBox(box)
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(ctx, "test", testUser, hash, auth.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	reg := voice.New(st, box, config.Voice{MaxAgents: 3, AllowLoopback: true}, nil)
	cfg := Config{Store: st, Live: noLive{}, SIPDomain: testDomain, SessionTTL: sessionTTL, Voice: reg}
	srv := httptest.NewServer(Handler(cfg))
	t.Cleanup(func() {
		srv.Close()
		_ = db.Close()
	})
	return &voiceEnv{env: &env{t: t, srv: srv, db: db, st: st}, reg: reg, box: box}
}

// voiceAgentBody is the JSON of a create/update agent request.
func voiceAgentBody(name string, mut ...func(map[string]any)) map[string]any {
	in := map[string]any{
		"name": name, "description": "answers the phone", "enabled": true,
		"prompt": "You are the front desk.", "greeting": "Hello, this is an automated assistant.",
		"language": "en-US", "callerVerification": "none",
	}
	for _, m := range mut {
		m(in)
	}
	return in
}

// TestVoiceAgentCRUD (acceptance S-1 to S-4) fails if a name outside the
// pattern, a prompt over 8000 characters, an out-of-range limit, a
// duplicate extension (agent or extension), or an eleventh version is
// accepted or kept, if sip_user can be edited, if a restore rewrites
// history instead of adding a revision, or if an audit row holds prompt
// text. It also covers the voice_revision bumps of every change.
func TestVoiceAgentCRUD(t *testing.T) {
	e := newVoiceEnv(t)
	ctx := context.Background()
	c := e.login()

	// Create: the generated sip_user, the revision and the version 1 row.
	a := c.must(http.StatusCreated, "POST", "/api/v1/voice/agents", voiceAgentBody("support")).json(t)
	id := fmt.Sprint(a["id"])
	if a["revision"] != float64(1) || a["enabled"] != true || a["prompt"] == "" {
		t.Fatalf("created agent = %v", a)
	}
	sipUser := a["sipUser"].(string)
	if len(sipUser) < 3 || len(sipUser) > 15 {
		t.Fatalf("sipUser = %q", sipUser)
	}
	var n int
	if err := e.db.QueryRowContext(ctx, `SELECT count(*) FROM voice_agent_versions WHERE agent_id = $1`, a["id"]).Scan(&n); err != nil || n != 1 {
		t.Fatalf("version rows = %d (%v)", n, err)
	}
	revision := func() int64 {
		var rev int64
		if err := e.db.QueryRowContext(ctx, `SELECT revision FROM voice_revision WHERE id = 1`).Scan(&rev); err != nil {
			t.Fatal(err)
		}
		return rev
	}
	rev0 := revision()

	// Invalid input: name, prompt, limits, extension.
	c.must(http.StatusBadRequest, "POST", "/api/v1/voice/agents", voiceAgentBody("bad name!"))
	c.must(http.StatusBadRequest, "POST", "/api/v1/voice/agents", voiceAgentBody("x2", func(m map[string]any) {
		m["prompt"] = strings.Repeat("p", 8001)
	}))
	c.must(http.StatusBadRequest, "POST", "/api/v1/voice/agents", voiceAgentBody("x3", func(m map[string]any) {
		m["maxConcurrent"] = 51
	}))
	c.must(http.StatusBadRequest, "POST", "/api/v1/voice/agents", voiceAgentBody("x4", func(m map[string]any) {
		m["extension"] = "10x01"
	}))
	c.must(http.StatusConflict, "POST", "/api/v1/voice/agents", voiceAgentBody("support"))

	// Extension namespace: another agent's extension and an extension's
	// number are both refused.
	c.must(http.StatusCreated, "POST", "/api/v1/voice/agents", voiceAgentBody("billing",
		func(m map[string]any) { m["extension"] = "2001" }))
	c.must(http.StatusConflict, "POST", "/api/v1/voice/agents", voiceAgentBody("sales",
		func(m map[string]any) { m["extension"] = "2001" }))
	c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "2002", "name": "Human"})
	c.must(http.StatusConflict, "POST", "/api/v1/voice/agents", voiceAgentBody("sales",
		func(m map[string]any) { m["extension"] = "2002" }))
	// A duplicate extension on update is refused too.
	c.must(http.StatusConflict, "PUT", "/api/v1/voice/agents/"+id, voiceAgentBody("support",
		func(m map[string]any) { m["extension"] = "2001" }))

	// The agent limit (HELLO_VOICE_MAX_AGENTS = 3 in this env): a fourth
	// agent answers 409 voice_agent_limit.
	c.must(http.StatusCreated, "POST", "/api/v1/voice/agents", voiceAgentBody("third"))
	c.must(http.StatusConflict, "POST", "/api/v1/voice/agents", voiceAgentBody("fourth"))

	// Update: the sip_user cannot be edited (the field is ignored), the
	// persona moves to a new revision and a version row.
	up := voiceAgentBody("support", func(m map[string]any) {
		m["sipUser"] = "999999"
		m["prompt"] = "You are the night desk."
	})
	got := c.must(http.StatusOK, "PUT", "/api/v1/voice/agents/"+id, up).json(t)
	if got["sipUser"] != sipUser || got["revision"] != float64(2) {
		t.Fatalf("updated agent = %v", got)
	}
	if revision() != rev0+3 { // the billing and third creates and the update
		t.Fatalf("voice revision = %d, want %d", revision(), rev0+3)
	}

	// The single-agent GET and the 400/404 answers the document promises.
	c.must(http.StatusOK, "GET", "/api/v1/voice/agents/"+id, nil)
	c.must(http.StatusBadRequest, "PUT", "/api/v1/voice/agents/"+id, voiceAgentBody("bad name!"))
	c.must(http.StatusNotFound, "PUT", "/api/v1/voice/agents/999999", voiceAgentBody("absent"))
	c.must(http.StatusNotFound, "DELETE", "/api/v1/voice/agents/999999", nil)
	c.must(http.StatusNotFound, "GET", "/api/v1/voice/agents/999999/versions", nil)

	// Versions: 10 more persona edits make 12 versions total, 10 kept.
	for i := 0; i < 10; i++ {
		c.must(http.StatusOK, "PUT", "/api/v1/voice/agents/"+id, voiceAgentBody("support", func(m map[string]any) {
			m["prompt"] = fmt.Sprintf("You are desk number %d.", i)
		}))
	}
	vs := c.must(http.StatusOK, "GET", "/api/v1/voice/agents/"+id+"/versions", nil).json(t)["items"].([]any)
	if len(vs) != 10 {
		t.Fatalf("versions kept = %d, want 10", len(vs))
	}
	oldest := vs[len(vs)-1].(map[string]any)
	if oldest["revision"] != float64(3) {
		t.Fatalf("oldest kept revision = %v, want 3", oldest["revision"])
	}

	// Restore revision 5: the persona comes back, a new revision and a new
	// version row appear, the history keeps revision 5 unchanged.
	before := c.must(http.StatusOK, "GET", "/api/v1/voice/agents/"+id+"/versions", nil).json(t)["items"].([]any)
	restored := c.must(http.StatusOK, "POST", "/api/v1/voice/agents/"+id+"/versions/5/restore", nil).json(t)
	if restored["prompt"] == nil || restored["revision"] != float64(13) {
		t.Fatalf("restored agent revision = %v", restored["revision"])
	}
	after := c.must(http.StatusOK, "GET", "/api/v1/voice/agents/"+id+"/versions", nil).json(t)["items"].([]any)
	if len(after) != 10 || len(before) != 10 {
		t.Fatalf("versions before/after restore = %d/%d", len(before), len(after))
	}
	var rev5Count int
	if err := e.db.QueryRowContext(ctx,
		`SELECT count(*) FROM voice_agent_versions WHERE agent_id = $1 AND revision = 5`, id).Scan(&rev5Count); err != nil || rev5Count != 1 {
		t.Fatalf("source version rows = %d (%v)", rev5Count, err)
	}
	if after[0].(map[string]any)["revision"] != float64(13) {
		t.Fatalf("restore did not add a version row: %v", after[0])
	}
	c.must(http.StatusNotFound, "POST", "/api/v1/voice/agents/"+id+"/versions/99/restore", nil)

	// The audit rows hold the actor and the action, never the prompt text
	// (spec S-4): the prompt of every save contained "desk", the audit
	// text must not.
	var hits int
	if err := e.db.QueryRowContext(ctx,
		`SELECT count(*) FROM audit_events WHERE resource = 'voice_agent' AND action LIKE '%desk%'`).Scan(&hits); err != nil || hits != 0 {
		// The audit table has no text column for it at all; also scan raw.
		t.Fatalf("audit text rows = %d (%v)", hits, err)
	}
	var bodyHas bool
	if err := e.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM audit_events WHERE resource = 'voice_agent' AND to_jsonb(audit_events)::text LIKE '%night desk%')`).Scan(&bodyHas); err != nil || bodyHas {
		t.Fatalf("prompt text in audit (%v)", err)
	}

	// References on delete: an inbound route and a ring group member name
	// the agent, the delete answers 409 naming them.
	if _, err := e.db.ExecContext(ctx, `
		INSERT INTO inbound_routes (position, name, did_kind, destination_kind, destination)
		VALUES (1, 'voice-did', 'any', 'voice_agent', 'third')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.ExecContext(ctx, `
		WITH g AS (
			INSERT INTO ring_groups (name, strategy) VALUES ('afterhours', 'sequential') RETURNING id
		)
		INSERT INTO ring_group_members (group_id, position, voice_agent_id)
		SELECT g.id, 1, (SELECT id FROM voice_agents WHERE name = 'third') FROM g`); err != nil {
		t.Fatal(err)
	}
	all := c.must(http.StatusOK, "GET", "/api/v1/voice/agents", nil).json(t)["items"].([]any)
	var thirdID string
	for _, it := range all {
		if it.(map[string]any)["name"] == "third" {
			thirdID = fmt.Sprint(it.(map[string]any)["id"])
		}
	}
	if r := c.do("DELETE", "/api/v1/voice/agents/"+thirdID, nil); r.code != http.StatusConflict ||
		!strings.Contains(string(r.body), "inbound route") {
		t.Fatalf("delete referenced agent = %d %s", r.code, r.body)
	}
	if _, err := e.db.ExecContext(ctx, `DELETE FROM inbound_routes WHERE name = 'voice-did'`); err != nil {
		t.Fatal(err)
	}
	if r := c.do("DELETE", "/api/v1/voice/agents/"+thirdID, nil); r.code != http.StatusConflict ||
		!strings.Contains(string(r.body), "ring group") {
		t.Fatalf("delete referenced agent = %d %s", r.code, r.body)
	}

	// A plain delete works and removes the versions with it.
	if _, err := e.db.ExecContext(ctx, `DELETE FROM ring_groups WHERE name = 'afterhours'`); err != nil {
		t.Fatal(err)
	}
	c.must(http.StatusNoContent, "DELETE", "/api/v1/voice/agents/"+thirdID, nil)
	c.must(http.StatusNotFound, "GET", "/api/v1/voice/agents/"+thirdID, nil)
}
