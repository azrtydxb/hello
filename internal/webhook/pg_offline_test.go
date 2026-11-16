package webhook

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/dialect"

	"github.com/azrtydxb/hello/internal/events"
)

// stubDriver is a momo driver with no database behind it. It records every
// statement momo hands it (rendered SQL and bound arguments) and answers
// each with one row, or none when empty is set, whose booleans are true and
// whose integers are 1, so the store's methods run down their longest path:
// Claim reaches its lease and its read, Finish disables the endpoint.
// Momo builds a statement before it reaches the driver, so a statement that
// does not build (an @name without its argument, an argument no @name uses)
// fails here as momo.ErrValidation. Column names are not checked: the rows
// name no columns, so momo scans by position; the PostgreSQL rig
// (forEachStore) proves the columns.
type stubDriver struct {
	mu    sync.Mutex
	empty bool
	stmts []stubStmt
}

type stubStmt struct {
	sql  string
	args []any
}

func (d *stubDriver) Dialect() *dialect.Dialect { return dialect.For(dialect.Postgres) }

func (d *stubDriver) record(sql string, args []any) {
	d.mu.Lock()
	d.stmts = append(d.stmts, stubStmt{sql, slices.Clone(args)})
	d.mu.Unlock()
}

func (d *stubDriver) Query(_ context.Context, sql string, args []any) (momo.Rows, error) {
	d.record(sql, args)
	return &stubRows{left: !d.empty}, nil
}

func (d *stubDriver) Exec(_ context.Context, sql string, args []any) (int64, error) {
	d.record(sql, args)
	if d.empty {
		return 0, nil
	}
	return 1, nil
}

func (d *stubDriver) Begin(context.Context, momo.TxOptions) (momo.DriverTx, error) {
	return stubTx{d}, nil
}

func (d *stubDriver) taken() []stubStmt {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.stmts)
}

type stubTx struct{ d *stubDriver }

func (t stubTx) Query(ctx context.Context, sql string, args []any) (momo.Rows, error) {
	return t.d.Query(ctx, sql, args)
}

func (t stubTx) Exec(ctx context.Context, sql string, args []any) (int64, error) {
	return t.d.Exec(ctx, sql, args)
}
func (stubTx) Commit(context.Context) error   { return nil }
func (stubTx) Rollback(context.Context) error { return nil }

type stubRows struct{ left bool }

func (r *stubRows) Next() bool {
	ok := r.left
	r.left = false
	return ok
}

func (*stubRows) Scan(dest ...any) error {
	for _, d := range dest {
		switch p := d.(type) {
		case *bool:
			*p = true
		case *int:
			*p = 1
		case *int64:
			*p = 1
		}
	}
	return nil
}
func (*stubRows) Err() error   { return nil }
func (*stubRows) Close() error { return nil }

// TestPGStatementsBuild runs every PG method on the stub driver. It needs no
// database, so a statement that does not build fails on every pull request;
// the DB-backed rig then proves what the statements do.
func TestPGStatementsBuild(t *testing.T) {
	ctx, now := context.Background(), time.Now()
	calls := map[string]func(p *PG) error{
		"NextEndpointID": func(p *PG) error { _, err := p.NextEndpointID(ctx); return err },
		"CreateEndpoint": func(p *PG) error {
			_, _, err := p.CreateEndpoint(ctx, "u", Endpoint{ID: 1, URL: "https://x", Events: []string{"call.*"}, IncludePeer: true})
			return err
		},
		"GetEndpoint":    func(p *PG) error { _, err := p.GetEndpoint(ctx, 1); return err },
		"ListEndpoints":  func(p *PG) error { _, err := p.ListEndpoints(ctx); return err },
		"UpdateEndpoint": func(p *PG) error { _, err := p.UpdateEndpoint(ctx, "u", Endpoint{ID: 1}, true); return err },
		"SetSecrets":     func(p *PG) error { _, err := p.SetSecrets(ctx, "u", 1, []byte("a"), nil, nil); return err },
		"DeleteEndpoint": func(p *PG) error { return p.DeleteEndpoint(ctx, "u", 1) },
		"Enqueue": func(p *PG) error {
			return p.Enqueue(ctx, nil, events.Entry{Event: events.New("call.started", events.Data{}, "", now)})
		},
		"Claim": func(p *PG) error { _, err := p.Claim(ctx, now, 10, 2, time.Minute); return err },
		"FinishSuccess": func(p *PG) error {
			_, err := p.Finish(ctx, Finish{DeliveryID: 1, Now: now, Outcome: OutcomeSuccess, Status: 200})
			return err
		},
		"FinishDisable": func(p *PG) error {
			res, err := p.Finish(ctx, Finish{DeliveryID: 1, Now: now, Outcome: OutcomeRetry, MaxFailures: 1})
			if err == nil && !res.Disabled {
				return errors.New("the stub's row should have disabled the endpoint")
			}
			return err
		},
		"ListDeliveries": func(p *PG) error { _, err := p.ListDeliveries(ctx, DeliveryQuery{}); return err },
		"ListDeliveriesAll": func(p *PG) error {
			_, err := p.ListDeliveries(ctx, DeliveryQuery{EndpointID: 1, Status: StatusDead, Before: 9, Limit: 5})
			return err
		},
		"GetDelivery":   func(p *PG) error { _, err := p.GetDelivery(ctx, 1); return err },
		"Redeliver":     func(p *PG) error { _, err := p.Redeliver(ctx, "u", 1, now); return err },
		"RedeliverDead": func(p *PG) error { _, err := p.RedeliverDead(ctx, "u", 1, now); return err },
		"Purge":         func(p *PG) error { _, err := p.Purge(ctx, now, now, 3); return err },
		"Counts":        func(p *PG) error { _, err := p.Counts(ctx); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			d := &stubDriver{}
			if err := call(&PG{DB: momo.New(d)}); err != nil {
				t.Fatalf("err = %v", err)
			}
			if len(d.taken()) == 0 {
				t.Fatal("no statement reached the driver")
			}
		})
	}
	// The empty answers: a missing row is ErrNotFound, a delivery that is
	// gone is not "in flight".
	empty := map[string]func(p *PG) error{
		"GetEndpoint":    func(p *PG) error { _, err := p.GetEndpoint(ctx, 1); return err },
		"UpdateEndpoint": func(p *PG) error { _, err := p.UpdateEndpoint(ctx, "u", Endpoint{ID: 1}, false); return err },
		"SetSecrets":     func(p *PG) error { _, err := p.SetSecrets(ctx, "u", 1, nil, nil, nil); return err },
		"DeleteEndpoint": func(p *PG) error { return p.DeleteEndpoint(ctx, "u", 1) },
		"Finish":         func(p *PG) error { _, err := p.Finish(ctx, Finish{DeliveryID: 1, Now: now}); return err },
		"GetDelivery":    func(p *PG) error { _, err := p.GetDelivery(ctx, 1); return err },
		"Redeliver":      func(p *PG) error { _, err := p.Redeliver(ctx, "u", 1, now); return err },
	}
	for name, call := range empty {
		t.Run("empty/"+name, func(t *testing.T) {
			if err := call(&PG{DB: momo.New(&stubDriver{empty: true})}); !errors.Is(err, ErrNotFound) {
				t.Fatalf("err = %v, want ErrNotFound", err)
			}
		})
	}
	t.Run("empty/CreateEndpointInserts", func(t *testing.T) {
		d := &stubDriver{empty: true}
		// The read-back finds nothing on the stub: ErrNotFound after the insert.
		if _, _, err := (&PG{DB: momo.New(d)}).CreateEndpoint(ctx, "u", Endpoint{ID: 1}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		if !slices.ContainsFunc(d.taken(), func(s stubStmt) bool { return strings.Contains(s.sql, "INSERT INTO webhook_endpoints") }) {
			t.Fatal("no insert reached the driver")
		}
	})
}

// TestPGLockKeys pins the advisory lock statements and keys. Replicas of the
// version before momo take the same locks with raw SQL, so during a rolling
// update both must contend on identical keys: the claim lock is the int64
// "hellowhd", the create lock hashtext of its text key.
func TestPGLockKeys(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name    string
		call    func(p *PG) error
		sql     string
		wantArg any
	}{
		{"Claim", func(p *PG) error { _, err := p.Claim(ctx, time.Now(), 1, 1, time.Minute); return err },
			"SELECT pg_advisory_xact_lock($1)", int64(0x68656c6c6f776864)},
		{"CreateEndpoint", func(p *PG) error { _, _, err := p.CreateEndpoint(ctx, "u", Endpoint{ID: 1}); return err },
			"SELECT pg_advisory_xact_lock(hashtext($1))", "hello:webhooks:create"},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := &stubDriver{}
			if err := c.call(&PG{DB: momo.New(d)}); err != nil {
				t.Fatal(err)
			}
			stmts := d.taken()
			if len(stmts) == 0 || stmts[0].sql != c.sql || len(stmts[0].args) != 1 || stmts[0].args[0] != c.wantArg {
				t.Fatalf("first statement = %+v; want %q with %#v", stmts, c.sql, c.wantArg)
			}
		})
	}
}
