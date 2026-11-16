package slack

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/dialect"

	"github.com/azrtydxb/hello/internal/events"
)

// pgCalls runs every PG method once. Finish runs twice, for the success
// branch and for the branch that disables the destination.
func pgCalls(p *PG) map[string]func() error {
	ctx, now := context.Background(), time.Now()
	d := Destination{ID: 1, Name: "n", Mode: ModeWebhook, SecretEnc: []byte("s"), Events: EventTypes, Extensions: []string{"200"}, Enabled: true}
	e := events.Entry{Event: events.Event{ID: "evt_1", Type: events.CallMissed}}
	return map[string]func() error{
		"NextID":  func() error { _, err := p.NextID(ctx); return err },
		"Create":  func() error { _, err := p.Create(ctx, "a", d); return err },
		"Get":     func() error { _, err := p.Get(ctx, 1); return err },
		"List":    func() error { _, err := p.List(ctx); return err },
		"Update":  func() error { _, err := p.Update(ctx, "a", d, true); return err },
		"Delete":  func() error { return p.Delete(ctx, "a", 1) },
		"Enqueue": func() error { return p.Enqueue(ctx, nil, e) },
		"Claim":   func() error { _, err := p.Claim(ctx, now, 10, time.Minute); return err },
		"FinishSuccess": func() error {
			return p.Finish(ctx, Finish{PostID: 1, Now: now, Outcome: OutcomeSuccess})
		},
		"FinishDisable": func() error {
			return p.Finish(ctx, Finish{PostID: 1, Now: now, Outcome: OutcomeDisable, Error: "x", Reason: ReasonAuth})
		},
		"ListPosts": func() error {
			_, err := p.ListPosts(ctx, PostQuery{DestinationID: 1, Status: StatusDead, Before: 9, Limit: 10})
			return err
		},
		"Retry": func() error { _, err := p.Retry(ctx, "a", 1, now); return err },
		"Purge": func() error { _, err := p.Purge(ctx, now, now); return err },
	}
}

// TestPGStatementsBuild runs every PG method on momo's offline handle, which
// renders a statement and then refuses to run it. A statement that does not
// build (an @name without its argument, an argument no @name uses, a
// malformed literal) is momo.ErrValidation; one that built reaches the
// handle and gets momo.ErrUnsupported. The methods that open a transaction
// stop at BEGIN here; TestPGStatementsBuildInTx reaches their statements.
func TestPGStatementsBuild(t *testing.T) {
	p := &PG{DB: momo.NewOffline(dialect.Postgres)}
	for name, call := range pgCalls(p) {
		t.Run(name, func(t *testing.T) {
			err := call()
			if errors.Is(err, momo.ErrValidation) || !errors.Is(err, momo.ErrUnsupported) {
				t.Fatalf("err = %v; want the offline handle's momo.ErrUnsupported (the statement must build)", err)
			}
		})
	}
}

// TestPGStatementsBuildInTx runs every PG method on a stub driver that
// answers each query with no rows or with one row (whose Scan leaves the
// destinations zero, except bools, which it sets so that the disable branch
// runs) and each write with one affected row. Between the two answers every
// statement of the store reaches the driver, including the ones inside
// transactions, so a statement that does not build fails here without a
// database. The test also fails if a statement reaches the driver with an
// unrendered @name or no $1 for its arguments.
func TestPGStatementsBuildInTx(t *testing.T) {
	seen := map[string]bool{}
	for _, rows := range []int{0, 1} {
		drv := &stubDriver{rows: rows}
		p := &PG{DB: momo.New(drv)}
		for name, call := range pgCalls(p) {
			err := call()
			if errors.Is(err, momo.ErrValidation) {
				t.Errorf("%s with %d rows: %v (the statement must build)", name, rows, err)
			}
			if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("%s with %d rows: unexpected error %v", name, rows, err)
			}
		}
		for _, s := range drv.stmts {
			if strings.Contains(s.sql, "@") {
				t.Errorf("unrendered bind in %q", s.sql)
			}
			if len(s.args) > 0 && !strings.Contains(s.sql, "$1") {
				t.Errorf("%d arguments but no $1 in %q", len(s.args), s.sql)
			}
			seen[s.sql] = true
		}
	}
	// The distinct statements of pg.go: audit, NextID, the destination
	// insert, read by id, list, update and delete, Enqueue's read and
	// insert, Claim's update and read, Finish's four, ListPosts, Retry's
	// update and existence check, and Purge.
	const want = 19
	if len(seen) != want {
		got := make([]string, 0, len(seen))
		for s := range seen {
			got = append(got, s)
		}
		slices.Sort(got)
		t.Fatalf("%d distinct statements reached the driver, want %d:\n%s", len(seen), want, strings.Join(got, "\n---\n"))
	}
}

type stubStmt struct {
	sql  string
	args []any
}

// stubDriver is a momo.Driver with no database: it records each statement,
// answers a query with rows rows and a write with one affected row.
type stubDriver struct {
	rows  int
	stmts []stubStmt
}

func (d *stubDriver) Dialect() *dialect.Dialect { return dialect.For(dialect.Postgres) }

func (d *stubDriver) Query(_ context.Context, query string, args []any) (momo.Rows, error) {
	d.stmts = append(d.stmts, stubStmt{query, args})
	return &stubRows{left: d.rows}, nil
}

func (d *stubDriver) Exec(_ context.Context, query string, args []any) (int64, error) {
	d.stmts = append(d.stmts, stubStmt{query, args})
	return 1, nil
}

func (d *stubDriver) Begin(context.Context, momo.TxOptions) (momo.DriverTx, error) {
	return stubTx{d}, nil
}

type stubTx struct{ d *stubDriver }

func (t stubTx) Query(ctx context.Context, query string, args []any) (momo.Rows, error) {
	return t.d.Query(ctx, query, args)
}

func (t stubTx) Exec(ctx context.Context, query string, args []any) (int64, error) {
	return t.d.Exec(ctx, query, args)
}

func (stubTx) Commit(context.Context) error   { return nil }
func (stubTx) Rollback(context.Context) error { return nil }

type stubRows struct{ left int }

func (r *stubRows) Next() bool {
	if r.left == 0 {
		return false
	}
	r.left--
	return true
}

func (r *stubRows) Scan(dest ...any) error {
	for _, v := range dest {
		if b, ok := v.(*bool); ok {
			*b = true
		}
	}
	return nil
}

func (r *stubRows) Err() error   { return nil }
func (r *stubRows) Close() error { return nil }
