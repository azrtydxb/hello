package events

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/dialect"
)

// TestStatementsBuild runs every events statement on momo's offline handle,
// which renders the statement and then refuses to run it. A statement that
// does not build (an @name without its argument, an argument no @name uses)
// is momo.ErrValidation; one that built reaches the handle and gets
// momo.ErrUnsupported. It needs no database, so it runs on every pull
// request; the DB-backed pipeline tests prove what the statements do. The
// ingest transaction stops at BEGIN offline, so its two statements (insert,
// usersOf) are called directly.
func TestStatementsBuild(t *testing.T) {
	ctx := context.Background()
	db := momo.NewOffline(dialect.Postgres)
	var logged bytes.Buffer
	s := &Service{DB: db, Log: slog.New(slog.NewTextHandler(&logged, nil))}
	after, before := Cursor{MS: 1, Seq: 2}, Cursor{MS: 3, Seq: 4}
	calls := map[string]func() error{
		"insert": func() error {
			_, err := insert(ctx, db, Entry{Cursor: "1-0", Event: ev(CallRinging, "200")})
			return err
		},
		"usersOf":      func() error { _, err := usersOf(ctx, db, []string{"200", "300"}); return err },
		"HistoryPlain": func() error { _, err := s.History(ctx, Query{}); return err },
		"HistoryEvery": func() error {
			_, err := s.History(ctx, Query{After: after, Before: before, Desc: true, Limit: 5,
				Filter: Filter{Extension: "200", Allowed: []string{"200"}, Hide: []string{"message."}, Types: []string{"call.ended", "call.*"}}})
			return err
		},
		"HistoryPrefixesOnly": func() error {
			_, err := s.History(ctx, Query{Filter: Filter{Types: []string{"call.*"}}})
			return err
		},
		"HistoryExactOnly": func() error {
			_, err := s.History(ctx, Query{Filter: Filter{Types: []string{"call.ended"}}})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			if errors.Is(err, momo.ErrValidation) || !errors.Is(err, momo.ErrUnsupported) {
				t.Fatalf("err = %v; want the offline handle's momo.ErrUnsupported (the statement must build)", err)
			}
		})
	}
	t.Run("purge", func(t *testing.T) {
		logged.Reset()
		s.purge(ctx)
		// purge logs its error instead of returning it.
		out := logged.String()
		if !strings.Contains(out, "retention purge failed") || !strings.Contains(out, "unsupported") || strings.Contains(out, "validation") {
			t.Fatalf("purge logged %q; want the offline handle's unsupported error", out)
		}
	})
}

// TestStoreNeedsSQLTx fails if a Service whose DB is not over database/sql
// runs its hooks without the *sql.Tx they need instead of refusing the batch.
func TestStoreNeedsSQLTx(t *testing.T) {
	called := false
	s := &Service{DB: momo.New(beginOnly{}), Hooks: []TxHook{func(context.Context, *sql.Tx, Entry) error { called = true; return nil }}}
	if err := s.store(context.Background(), nil, []rawEntry{{id: "1-0"}}); !errors.Is(err, errNotSQLTx) || called {
		t.Fatalf("err = %v, hook called %v; want errNotSQLTx before any hook", err, called)
	}
}

// beginOnly is a momo driver whose transactions are not database/sql ones.
type beginOnly struct{}

func (beginOnly) Dialect() *dialect.Dialect { return dialect.For(dialect.Postgres) }
func (beginOnly) Query(context.Context, string, []any) (momo.Rows, error) {
	return nil, errors.New("no statement expected")
}
func (beginOnly) Exec(context.Context, string, []any) (int64, error) {
	return 0, errors.New("no statement expected")
}
func (beginOnly) Begin(context.Context, momo.TxOptions) (momo.DriverTx, error) {
	return beginOnlyTx{}, nil
}

type beginOnlyTx struct{ beginOnly }

func (beginOnlyTx) Commit(context.Context) error   { return nil }
func (beginOnlyTx) Rollback(context.Context) error { return nil }
