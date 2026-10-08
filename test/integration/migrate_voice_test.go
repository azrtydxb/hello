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

// TestMigrateVoiceAgentsRollback fails if 00010_voice_agents does not apply,
// roll back to 00009 without leftovers and apply again, or if the schema
// accepts an unknown caller verification mode or MCP authentication, a ring
// group member that is neither an extension nor a voice agent (or both), a
// call outcome outside the contract, or a name, sip_user, extension or
// tenant-shaped column of the wrong shape.
func TestMigrateVoiceAgentsRollback(t *testing.T) {
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
	if _, err := p.UpTo(ctx, 9); err != nil {
		t.Fatal(err)
	}
	if _, err := migrate.Up(ctx, db); err != nil {
		t.Fatal(err)
	}

	tables := []string{"voice_agents", "voice_agent_versions", "voice_mcp_servers", "voice_agent_mcp",
		"voice_runtime", "voice_revision", "voice_agent_calls"}
	columns := []string{"voice_agent_id", "voice_agent_name"}
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
		t.Fatalf("after up: %d of %d voice objects present", n, want)
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
	var aid int64
	if err := db.QueryRowContext(ctx, `INSERT INTO voice_agents (name, sip_user) VALUES ('reception', '2201') RETURNING id`).Scan(&aid); err != nil {
		t.Fatal(err)
	}
	refused("an unknown caller verification", `INSERT INTO voice_agents (name, sip_user, caller_verification) VALUES ('a', '2202', 'question')`)
	refused("a sip_user with letters", `INSERT INTO voice_agents (name, sip_user) VALUES ('a', 'ab')`)
	refused("a name with a pipe", `INSERT INTO voice_agents (name, sip_user) VALUES ('a|b', '2203')`)
	refused("a second agent on one sip_user", `INSERT INTO voice_agents (name, sip_user) VALUES ('a', '2201')`)
	refused("an extension with a pipe", `INSERT INTO voice_agents (name, sip_user, extension) VALUES ('a', '2204', '2|0')`)
	accepted("an agent with an extension", `INSERT INTO voice_agents (name, sip_user, extension) VALUES ('a', '2204', '204')`)
	refused("a second agent on one extension", `INSERT INTO voice_agents (name, sip_user, extension) VALUES ('b', '2205', '204')`)

	// MCP servers: the authentication kind and last check status are enums.
	var sid int64
	if err := db.QueryRowContext(ctx, `INSERT INTO voice_mcp_servers (name, url, auth) VALUES ('crm', 'https://mcp.test', 'bearer') RETURNING id`).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	refused("an unknown MCP authentication", `INSERT INTO voice_mcp_servers (name, url, auth) VALUES ('b', 'https://mcp.test', 'basic')`)
	refused("an unknown check status", `UPDATE voice_mcp_servers SET last_check_status = 'slow' WHERE id = $1`, sid)

	// Attachments: one row per agent and server, delete-protected servers.
	accepted("an attachment", `INSERT INTO voice_agent_mcp (agent_id, server_id) VALUES ($1, $2)`, aid, sid)
	refused("a second attachment of the pair", `INSERT INTO voice_agent_mcp (agent_id, server_id) VALUES ($1, $2)`, aid, sid)

	// Ring group members: an extension or a voice agent, never both, never
	// neither; one agent per group.
	var gid int64
	if err := db.QueryRowContext(ctx, `INSERT INTO ring_groups (name, strategy) VALUES ('front', 'sequential') RETURNING id`).Scan(&gid); err != nil {
		t.Fatal(err)
	}
	var eid int64
	if err := db.QueryRowContext(ctx, `INSERT INTO extensions (number, name) VALUES ('204', 'Front') RETURNING id`).Scan(&eid); err != nil {
		t.Fatal(err)
	}
	accepted("an extension member", `INSERT INTO ring_group_members (group_id, extension_id, position) VALUES ($1, $2, 1)`, gid, eid)
	accepted("an agent member", `INSERT INTO ring_group_members (group_id, voice_agent_id, position) VALUES ($1, $2, 2)`, gid, aid)
	refused("a member that is neither", `INSERT INTO ring_group_members (group_id, position) VALUES ($1, 3)`, gid)
	refused("a member that is both", `INSERT INTO ring_group_members (group_id, extension_id, voice_agent_id, position) VALUES ($1, $2, $3, 3)`, gid, eid, aid)
	refused("a second agent member in the group", `INSERT INTO ring_group_members (group_id, voice_agent_id, position) VALUES ($1, $2, 3)`, gid, aid)

	// Ring group failure kind and inbound route destination kind.
	refused("an unknown failure kind", `UPDATE ring_groups SET failure_kind = 'agent' WHERE id = $1`, gid)
	accepted("a voice_agent failure kind", `UPDATE ring_groups SET failure_kind = 'voice_agent' WHERE id = $1`, gid)
	refused("an unknown destination kind", `INSERT INTO inbound_routes (position, name, did_kind, destination_kind, destination)
		VALUES (900, 'v', 'any', 'agent', 'reception')`)
	accepted("a voice_agent destination", `INSERT INTO inbound_routes (position, name, did_kind, destination_kind, destination)
		VALUES (900, 'v', 'any', 'voice_agent', 'reception')`)

	// The revision counter starts at zero and the runtime singleton is
	// single-row.
	var rev int64
	if err := db.QueryRowContext(ctx, `SELECT revision FROM voice_revision`).Scan(&rev); err != nil || rev != 0 {
		t.Fatalf("voice_revision = %d, %v; want 0, nil", rev, err)
	}
	accepted("the runtime singleton", `INSERT INTO voice_runtime (id) VALUES (1)`)
	refused("a second runtime row", `INSERT INTO voice_runtime (id) VALUES (2)`)
	refused("an unknown call outcome", `INSERT INTO voice_agent_calls (correlation_id, outcome) VALUES ('c1', 'lost')`)
	accepted("a call report row", `INSERT INTO voice_agent_calls (correlation_id, outcome, summary) VALUES ('c1', 'answered', 'booked a table')`)

	// Deleting a referenced agent is refused; deleting the group leaves the
	// agent; the version history follows the agent.
	accepted("a version", `INSERT INTO voice_agent_versions (agent_id, revision, persona) VALUES ($1, 1, '{}')`, aid)
	accepted("a ring group naming the agent", `INSERT INTO ring_groups (name, strategy, failure_kind) VALUES ('back', 'sequential', 'voice_agent')`)
	refused("deleting a referenced agent", `DELETE FROM voice_agents WHERE id = $1`, aid)
	accepted("deleting the group", `DELETE FROM ring_groups`)
	var left int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM voice_agents WHERE id = $1`, aid).Scan(&left); err != nil || left != 1 {
		t.Fatalf("the agent did not survive its ring group: %d, %v", left, err)
	}

	if _, err := p.DownTo(ctx, 9); err != nil {
		t.Fatalf("roll back to 00009: %v", err)
	}
	if n := present(); n != 0 {
		t.Fatalf("after rollback: %d voice objects left", n)
	}
	if n, err := migrate.Up(ctx, db); err != nil || n != 1 {
		t.Fatalf("re-apply = %d, %v; want 1, nil", n, err)
	}
	if n := present(); n != want {
		t.Fatalf("after re-apply: %d of %d voice objects present", n, want)
	}
}
