package integration

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/migrate"
)

// TestAIAccessSchema fails if 00008_ai_access's objects are missing from a
// new install or from a database taken over from goose 00007 (where its data
// step must make the existing users admin), or if the schema accepts a role,
// token kind, client kind or OAuth token kind outside the contract, or an
// OAuth token without scopes or resources.
func TestAIAccessSchema(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		db := openScratch(t)
		schemaBuilds[0].build(t, db)
		aiAccessChecks(t, db, false)
	})
	t.Run("taken over from 00007", func(t *testing.T) {
		db := openScratch(t)
		ctx := context.Background()
		gooseUpTo(t, db, 7)
		if _, err := db.ExecContext(ctx, `INSERT INTO users (username, password_hash) VALUES ('old', 'x')`); err != nil {
			t.Fatal(err)
		}
		n, err := migrate.Up(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		if want := legacyAfter(t, 7) + momoAfterTakeover(t); n != want {
			t.Fatalf("takeover from 00007 ran %d migrations, want %d", n, want)
		}
		assertTakenOver(t, ctx, db)
		aiAccessChecks(t, db, true)
	})
}

func aiAccessChecks(t *testing.T, db *sql.DB, takenOver bool) {
	ctx := context.Background()
	tables := []string{"oauth_clients", "oauth_client_secrets", "oauth_requests", "oauth_grants", "oauth_tokens"}
	columns := [][2]string{{"users", "role"}, {"api_tokens", "scopes"}, {"api_tokens", "expires_at"},
		{"api_tokens", "revoked_at"}, {"api_tokens", "kind"}, {"audit_events", "via"}}
	want := len(tables) + len(columns)
	present := func() (n int) {
		t.Helper()
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name IN ('`+strings.Join(tables, "','")+`')`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		for _, c := range columns {
			var k int
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns
				WHERE table_name = $1 AND column_name = $2`, c[0], c[1]).Scan(&k); err != nil {
				t.Fatal(err)
			}
			n += k
		}
		return n
	}
	if n := present(); n != want {
		t.Fatalf("after up: %d of %d AI access objects present", n, want)
	}

	var role string
	if takenOver {
		if err := db.QueryRowContext(ctx, `SELECT role FROM users WHERE username = 'old'`).Scan(&role); err != nil || role != "admin" {
			t.Fatalf("existing user's role = %q, %v; want admin", role, err)
		}
	}
	var uid int64
	if err := db.QueryRowContext(ctx, `INSERT INTO users (username, password_hash) VALUES ('new', 'x') RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT role FROM users WHERE id = $1`, uid).Scan(&role); err != nil || role != "viewer" {
		t.Fatalf("new user's role = %q, %v; want viewer", role, err)
	}

	// Constraints the parallel streams rely on.
	refused := func(what, q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err == nil {
			t.Fatalf("%s was accepted", what)
		}
	}
	accepted := func(what, q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	refused("an unknown role", `UPDATE users SET role = 'root' WHERE id = $1`, uid)
	refused("an unknown token kind", `INSERT INTO api_tokens (user_id, name, token_hash, kind) VALUES ($1, 't', '\x01', 'oauth')`, uid)
	accepted("a personal token", `INSERT INTO api_tokens (user_id, name, token_hash, kind, scopes) VALUES ($1, 't', '\x02', 'personal', '{read}')`, uid)
	refused("an unknown client kind", `INSERT INTO oauth_clients (client_id, kind, name) VALUES ('c0', 'public', 'x')`)
	refused("a service account without a role", `INSERT INTO oauth_clients (client_id, kind, name, scopes) VALUES ('hello_sa_0', 'service', 'x', '{read}')`)
	accepted("a CIMD client", `INSERT INTO oauth_clients (client_id, kind, name) VALUES ('https://c.example/m.json', 'cimd', 'x')`)
	token := `INSERT INTO oauth_tokens (hash, kind, client_id, user_id, scopes, resources, expires_at)
		VALUES ($1, $2, 'https://c.example/m.json', $3, $4, $5, now() + interval '1 hour')`
	refused("an unknown OAuth token kind", token, []byte("h1"), "id", uid, "{read}", "{}")
	refused("an OAuth token without scopes", token, []byte("h2"), "access", uid, nil, "{}")
	refused("an OAuth token without resources", token, []byte("h3"), "access", uid, "{read}", nil)
	accepted("an access token", token, []byte("h4"), "access", uid, "{read}", "{https://h.example/mcp}")
}
