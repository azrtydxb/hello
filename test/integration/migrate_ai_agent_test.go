package integration

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/azrtydxb/hello/migrations"
	"github.com/pressly/goose/v3"
)

// TestMigrateAIAgentRollback fails if 00009_ai_agent does not apply, roll
// back to 00008 without leftovers and apply again, or if the schema accepts
// a status, severity, outcome or message role outside the contract, a
// second live finding for one candidate, or loses a CDR's call quality.
func TestMigrateAIAgentRollback(t *testing.T) {
	dsn := os.Getenv("HELLO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HELLO_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", scratchDatabase(t, ctx, dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 8); err != nil {
		t.Fatal(err)
	}
	if _, err := migrate.Up(ctx, db); err != nil {
		t.Fatal(err)
	}

	tables := []string{"ai_usage", "ai_tasks", "ai_agent_runs", "ai_agent_requests", "ai_samples",
		"ai_findings", "ai_proposals", "ai_sessions", "ai_messages"}
	columns := []string{"rtp_packets", "rtp_lost", "rtp_jitter_ms"}
	present := func() (n int) {
		t.Helper()
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name IN ('`+strings.Join(tables, "','")+`')`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		var k int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns
			WHERE table_name = 'cdrs' AND column_name IN ('`+strings.Join(columns, "','")+`')`).Scan(&k); err != nil {
			t.Fatal(err)
		}
		return n + k
	}
	want := len(tables) + len(columns)
	if n := present(); n != want {
		t.Fatalf("after up: %d of %d AI agent objects present", n, want)
	}

	exec := func(q string, args ...any) error {
		_, err := db.ExecContext(ctx, q, args...)
		return err
	}
	refused := func(what, q string, args ...any) {
		t.Helper()
		if err := exec(q, args...); err == nil {
			t.Fatalf("%s was accepted", what)
		}
	}
	accepted := func(what, q string, args ...any) {
		t.Helper()
		if err := exec(q, args...); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	var uid int64
	if err := db.QueryRowContext(ctx, `INSERT INTO users (username, password_hash) VALUES ('u', 'x') RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	const (
		sid = "00000000-0000-0000-0000-000000000001"
		tid = "00000000-0000-0000-0000-000000000002"
	)
	accepted("a session", `INSERT INTO ai_sessions (id, owner) VALUES ($1, $2)`, sid, uid)
	task := `INSERT INTO ai_tasks (id, kind, requested_by, replica, status) VALUES ($1, 'message', $2, 'r', $3)`
	refused("an unknown task status", task, tid, uid, "done")
	accepted("a task", task, tid, uid, "running")
	run := `INSERT INTO ai_agent_runs (agent, replica, outcome) VALUES ('aiops', 'r', $1)`
	refused("an unknown run outcome", run, "success")
	accepted("a run", run, "skipped_locked")
	finding := `INSERT INTO ai_findings (id, candidate_id, type, subject, severity, status, title) VALUES ($1, 'trunk_down:t', 'trunk_down', 't', $2, $3, 'x')`
	refused("an unknown severity", finding, "00000000-0000-0000-0000-0000000000f1", "fatal", "open")
	refused("an unknown finding status", finding, "00000000-0000-0000-0000-0000000000f1", "info", "closed")
	accepted("a finding", finding, "00000000-0000-0000-0000-0000000000f1", "warning", "open")
	refused("a second live finding for the candidate", finding, "00000000-0000-0000-0000-0000000000f2", "info", "acknowledged")
	accepted("a resolved finding beside the live one", finding, "00000000-0000-0000-0000-0000000000f3", "info", "resolved")
	prop := `INSERT INTO ai_proposals (id, source, fingerprint, title, actions, config_revision, status) VALUES ($1, 'assistant', 'f', 't', '[]', 1, $2)`
	refused("an unknown proposal status", prop, "00000000-0000-0000-0000-0000000000a1", "pending")
	accepted("a proposal", prop, "00000000-0000-0000-0000-0000000000a1", "superseded")
	msg := `INSERT INTO ai_messages (session_id, role, content) VALUES ($1, $2, 'hi')`
	refused("an unknown message role", msg, sid, "system")
	accepted("a message", msg, sid, "user")

	// Deleting the owner removes their sessions, tasks and messages.
	accepted("deleting the owner", `DELETE FROM users WHERE id = $1`, uid)
	var left int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM ai_sessions) + (SELECT count(*) FROM ai_tasks) + (SELECT count(*) FROM ai_messages)`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("after deleting the owner: %d rows left, %v", left, err)
	}

	if _, err := p.DownTo(ctx, 8); err != nil {
		t.Fatalf("roll back to 00008: %v", err)
	}
	if n := present(); n != 0 {
		t.Fatalf("after rollback: %d AI agent objects left", n)
	}
	if n, err := migrate.Up(ctx, db); err != nil || n != 2 { // 00009 and 00010
		t.Fatalf("re-apply = %d, %v; want 2, nil", n, err)
	}
	if n := present(); n != want {
		t.Fatalf("after re-apply: %d of %d AI agent objects present", n, want)
	}
}
