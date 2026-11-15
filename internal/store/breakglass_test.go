package store

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
)

// TestBreakGlassGuards (AC-10c, on PostgreSQL) fails if a non-admin, a
// disabled admin or a directory-synced admin can be flagged break-glass, or
// if the last enabled break-glass admin can be disabled, demoted, deleted or
// unflagged, also under two concurrent requests.
func TestBreakGlassGuards(t *testing.T) {
	ctx := context.Background()
	s := scratchStore(t)
	a1, err := s.CreateUser(ctx, "test", "root1", "x", auth.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := s.CreateUser(ctx, "test", "root2", "x", auth.RoleAdmin)
	viewer, _ := s.CreateUser(ctx, "test", "viewer", "x", auth.RoleViewer)
	yes, no := true, false
	if _, err := s.UpdateAccount(ctx, "test", viewer, AccountChange{BreakGlass: &yes}); !errors.Is(err, ErrBreakGlassLocalAdmin) {
		t.Fatalf("viewer flagged: %v", err)
	}
	// A synced admin cannot be break-glass: it has no local password.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO directory_connectors (kind, name) VALUES ('entra', 'E')`); err != nil {
		t.Fatal(err)
	}
	synced, _ := s.CreateUser(ctx, "test", "synced", "x", auth.RoleAdmin)
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET source_connector_id = (SELECT id FROM directory_connectors), external_id = 'x' WHERE id = $1`, synced); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateAccount(ctx, "test", synced, AccountChange{BreakGlass: &yes}); !errors.Is(err, ErrBreakGlassLocalAdmin) {
		t.Fatalf("synced admin flagged: %v", err)
	}
	if _, err := s.UpdateAccount(ctx, "test", synced, AccountChange{PasswordHash: strPtrStore("h")}); !errors.Is(err, ErrManagedByDirectory) {
		t.Fatalf("synced admin password: %v", err)
	}
	if _, err := s.CreateUser(ctx, "cli", "synced", "x", auth.RoleViewer); !errors.Is(err, ErrManagedByDirectory) {
		t.Fatalf("CLI user add over a synced user: %v", err)
	}
	u, err := s.UpdateAccount(ctx, "test", a1, AccountChange{BreakGlass: &yes})
	if err != nil || !u.BreakGlass {
		t.Fatalf("flag root1 = %+v, %v", u, err)
	}
	l, _ := s.UserForLogin(ctx, "root1")
	if !l.BreakGlass || l.ConnectorID != 0 {
		t.Fatalf("login of root1 = %+v", l)
	}
	if l, _ := s.UserForLogin(ctx, "synced"); l.ConnectorID == 0 || l.ExternalID != "x" {
		t.Fatalf("login of synced = %+v", l)
	}
	viewerRole := auth.RoleViewer
	for name, c := range map[string]AccountChange{
		"disable": {Enabled: &no},
		"demote":  {Role: &viewerRole},
		"unflag":  {BreakGlass: &no},
	} {
		if _, err := s.UpdateAccount(ctx, "test", a1, c); !errors.Is(err, ErrLastBreakGlass) {
			t.Errorf("%s the last break-glass admin: %v", name, err)
		}
	}
	if err := s.DeleteUser(ctx, "test", a1); !errors.Is(err, ErrLastBreakGlass) {
		t.Fatalf("delete the last break-glass admin: %v", err)
	}
	// With a second break-glass admin, two concurrent removals: exactly one
	// may win.
	if _, err := s.UpdateAccount(ctx, "test", a2, AccountChange{BreakGlass: &yes}); err != nil {
		t.Fatal(err)
	}
	var (
		wg   sync.WaitGroup
		errs = make([]error, 2)
	)
	for i, id := range []int64{a1, a2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.UpdateAccount(ctx, "test", id, AccountChange{Enabled: &no})
		}()
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, ErrLastBreakGlass):
			t.Fatalf("concurrent disable: %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("concurrent disables that succeeded = %d, want 1 (%v)", won, errs)
	}
	var left int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE role = 'admin' AND enabled AND break_glass`).Scan(&left); err != nil || left != 1 {
		t.Fatalf("enabled break-glass admins = %d, %v", left, err)
	}
	if on, err := s.SSORequired(ctx); err != nil || on {
		t.Fatalf("sso_required = %v, %v", on, err)
	}
}

func strPtrStore(s string) *string { return &s }
