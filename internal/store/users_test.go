package store

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/migrations"
	"github.com/pressly/goose/v3"
)

// TestUserRoles (store half; spec S-23) fails if migration 00008 leaves an
// existing user other than admin, the first user is not admin, a role is
// not stored as asked, the last admin can be demoted, a role change writes
// no audit row, or a session or token lookup returns a role other than the
// user's current one (the per-request read a demotion relies on).
func TestUserRoles(t *testing.T) {
	ctx := context.Background()

	t.Run("migration makes existing users admin", func(t *testing.T) {
		db := scratchDB(t)
		p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.UpTo(ctx, 7); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"old1", "old2"} {
			if _, err := db.ExecContext(ctx, `INSERT INTO users (username, password_hash) VALUES ($1, 'x')`, name); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := p.Up(ctx); err != nil {
			t.Fatal(err)
		}
		us, err := New(db).ListUsers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(us) != 2 {
			t.Fatalf("users = %v", us)
		}
		for _, u := range us {
			if u.Role != auth.RoleAdmin {
				t.Errorf("existing user %s migrated to %q, want admin", u.Username, u.Role)
			}
		}
	})

	s := scratchStore(t)
	first, err := s.CreateUser(ctx, "test", "first", "x", auth.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.CreateUser(ctx, "test", "op", "x", auth.RoleOperator)
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := s.CreateUser(ctx, "test", "viewer", "x", auth.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	roles := func() map[int64]auth.Role {
		t.Helper()
		us, err := s.ListUsers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		m := map[int64]auth.Role{}
		for _, u := range us {
			m[u.ID] = u.Role
		}
		return m
	}
	if got := roles(); got[first] != auth.RoleAdmin || got[op] != auth.RoleOperator || got[viewer] != auth.RoleViewer {
		t.Fatalf("roles after create = %v; the first user must be admin, the others as asked", got)
	}

	// Per-request read: a session and a token see the current role.
	sess, tok := auth.HashToken("session-v"), auth.HashToken("token-v")
	if err := s.CreateSession(ctx, viewer, sess, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateToken(ctx, "test", viewer, "t", tok); err != nil {
		t.Fatal(err)
	}
	lookupRoles := func() (auth.Role, auth.Role) {
		t.Helper()
		a, err := s.SessionActor(ctx, sess)
		if err != nil {
			t.Fatal(err)
		}
		b, err := s.TokenActor(ctx, tok)
		if err != nil {
			t.Fatal(err)
		}
		return a.Role, b.Role
	}
	if a, b := lookupRoles(); a != auth.RoleViewer || b != auth.RoleViewer {
		t.Fatalf("session role %q, token role %q, want viewer", a, b)
	}

	// The only admin cannot be demoted.
	if _, err := s.SetUserRole(ctx, "user:first", first, auth.RoleOperator); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demote last admin: err = %v, want ErrLastAdmin", err)
	}
	if got := roles()[first]; got != auth.RoleAdmin {
		t.Fatalf("last admin is now %q", got)
	}

	// Promote, then the first admin can step down; each change is audited.
	u, err := s.SetUserRole(ctx, "user:first", viewer, auth.RoleAdmin)
	if err != nil || u.Role != auth.RoleAdmin {
		t.Fatalf("promote: %v %v", u, err)
	}
	if a, b := lookupRoles(); a != auth.RoleAdmin || b != auth.RoleAdmin {
		t.Fatalf("after promotion: session role %q, token role %q, want admin", a, b)
	}
	if _, err := s.SetUserRole(ctx, "user:viewer", first, auth.RoleViewer); err != nil {
		t.Fatalf("demote one of two admins: %v", err)
	}
	if _, err := s.SetUserRole(ctx, "user:viewer", viewer, auth.RoleOperator); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demote the new last admin: err = %v, want ErrLastAdmin", err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FROM audit_events
		WHERE resource = 'user' AND action LIKE 'set-role:%' AND resource_id IN ($1, $2)`,
		strconv.FormatInt(viewer, 10), strconv.FormatInt(first, 10)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("%d role-change audit rows, want 2", n)
	}
	if _, err := s.SetUserRole(ctx, "test", 999999, auth.RoleViewer); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: err = %v, want ErrNotFound", err)
	}
}
