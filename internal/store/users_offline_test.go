package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/momo"
)

// TestUsersDynamicStatements runs the users and auth domain's statements that
// are assembled at run time, which TestStatementsBuild leaves to the DB
// tier, on the fake driver: each must build with its @name binds, and reach
// PostgreSQL with $n placeholders only.
func TestUsersDynamicStatements(t *testing.T) {
	ctx := context.Background()
	newStore := func(d *fakeDriver) *Store {
		return &Store{db: &dbPool{dbConn: dbConn{momo.New(d)}}}
	}
	check := func(t *testing.T, d *fakeDriver, want int) {
		t.Helper()
		if len(d.sent) != want {
			t.Fatalf("sent %d statements, want %d: %v", len(d.sent), want, d.sent)
		}
		for _, st := range d.sent {
			if strings.Contains(st.sql, "@") {
				t.Errorf("statement reached the driver with an @name: %s", st.sql)
			}
		}
	}

	t.Run("self-service reads", func(t *testing.T) {
		d := &fakeDriver{}
		s := newStore(d)
		if es, err := s.UserExtensions(ctx, 1); err != nil || len(es) != 0 {
			t.Errorf("UserExtensions = %v, %v", es, err)
		}
		if _, err := s.UserExtension(ctx, 1, 2); !errors.Is(err, ErrNotFound) {
			t.Errorf("UserExtension = %v, want ErrNotFound", err)
		}
		if _, err := s.UserVoicemailMessage(ctx, 1, 2); !errors.Is(err, ErrNotFound) {
			t.Errorf("UserVoicemailMessage = %v, want ErrNotFound", err)
		}
		if cs, next, err := s.UserCDRs(ctx, 1, 0, 10); err != nil || len(cs) != 0 || next != "" {
			t.Errorf("UserCDRs = %v, %q, %v", cs, next, err)
		}
		check(t, d, 4)
		// UserCDRs: momo binds each use, in order: @before twice (cast
		// each time), @user_id, then the limit, one row more than asked.
		if got := fmt.Sprint(d.sent[3].args); got != "[0 0 1 11]" {
			t.Errorf("UserCDRs args = %s, want [0 0 1 11]", got)
		}
	})

	t.Run("prune oauth", func(t *testing.T) {
		d := &fakeDriver{affected: 2}
		cutoff := time.Unix(1700000000, 0)
		n, err := newStore(d).PruneOAuth(ctx, cutoff)
		if err != nil || n != 10 {
			t.Fatalf("PruneOAuth = %d, %v; want 10", n, err)
		}
		check(t, d, 5)
		// Every use of @cutoff is its own bind of the same value.
		for _, st := range d.sent {
			if len(st.args) != strings.Count(st.sql, "$") {
				t.Errorf("%s: %d args for its binds", st.sql, len(st.args))
			}
			for _, a := range st.args {
				if a != cutoff {
					t.Errorf("%s: args %v, want only the cutoff", st.sql, st.args)
				}
			}
		}
	})

	t.Run("revoke grants", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			where string
			args  []any
		}{
			{"by id", `id = @id`, []any{momo.Named("id", int64(7))}},
			{"by id and user", `id = @id AND (@user_id::bigint = 0 OR user_id = @user_id::bigint)`,
				[]any{momo.Named("id", int64(7)), momo.Named("user_id", int64(3))}},
			{"reused code", `user_id = @user_id AND client_id = @client_id AND created_at >= @created_at`,
				[]any{momo.Named("user_id", int64(3)), momo.Named("client_id", "c"), momo.Named("created_at", time.Unix(0, 0))}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				d := &fakeDriver{rows: [][]any{{int64(7)}}}
				tx := &dbTx{dbConn: dbConn{momo.New(d)}}
				ids, err := revokeGrants(ctx, tx, tc.where, tc.args...)
				if err != nil || fmt.Sprint(ids) != "[7]" {
					t.Fatalf("revokeGrants = %v, %v; want [7]", ids, err)
				}
				check(t, d, 2)
				if got := fmt.Sprint(d.sent[1].args); got != "[[7]]" {
					t.Errorf("token revocation args = %s, want [[7]]", got)
				}
			})
		}
	})
}
