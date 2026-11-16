package scim

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/dialect"
)

// scriptDriver is a momo driver with no database behind it, for
// TestPGStatementsBuild (a copy of internal/directory's). momo renders every statement (the @name binds, the
// argument count) before it reaches the driver, so a statement that does not
// build fails with momo.ErrValidation and never gets here. A statement that
// built is recorded and answered without a server: an Exec reports one row,
// a query returns rows (one unless rows says otherwise) whose Scan fills
// every destination with a plausible value. That lets each method run past
// its first statement, including the ones inside a transaction, which
// momo.NewOffline would stop at BEGIN.
type scriptDriver struct {
	mu   sync.Mutex
	stmt []string
	// rows is the number of rows a query returns; nil means one.
	rows func(sql string) int
	// scan adjusts the destinations after the default fill.
	scan func(sql string, dest []any)
}

func (d *scriptDriver) record(q string) {
	d.mu.Lock()
	d.stmt = append(d.stmt, q)
	d.mu.Unlock()
}

func (d *scriptDriver) Dialect() *dialect.Dialect { return dialect.For(dialect.Postgres) }

func (d *scriptDriver) Query(_ context.Context, q string, _ []any) (momo.Rows, error) {
	d.record(q)
	n := 1
	if d.rows != nil {
		n = d.rows(q)
	}
	return &scriptRows{d: d, sql: q, left: n}, nil
}

func (d *scriptDriver) Exec(_ context.Context, q string, _ []any) (int64, error) {
	d.record(q)
	return 1, nil
}

func (d *scriptDriver) Begin(context.Context, momo.TxOptions) (momo.DriverTx, error) {
	return scriptTx{d}, nil
}

func (d *scriptDriver) Pin(context.Context) (momo.PinnedConn, error) { return scriptConn{d}, nil }

type scriptTx struct{ d *scriptDriver }

func (t scriptTx) Query(ctx context.Context, q string, args []any) (momo.Rows, error) {
	return t.d.Query(ctx, q, args)
}
func (t scriptTx) Exec(ctx context.Context, q string, args []any) (int64, error) {
	return t.d.Exec(ctx, q, args)
}
func (scriptTx) Commit(context.Context) error   { return nil }
func (scriptTx) Rollback(context.Context) error { return nil }

type scriptConn struct{ d *scriptDriver }

func (c scriptConn) Query(ctx context.Context, q string, args []any) (momo.Rows, error) {
	return c.d.Query(ctx, q, args)
}
func (c scriptConn) Exec(ctx context.Context, q string, args []any) (int64, error) {
	return c.d.Exec(ctx, q, args)
}
func (c scriptConn) Begin(ctx context.Context, o momo.TxOptions) (momo.DriverTx, error) {
	return c.d.Begin(ctx, o)
}
func (scriptConn) Release() error { return nil }

type scriptRows struct {
	d    *scriptDriver
	sql  string
	left int
}

func (r *scriptRows) Next() bool {
	if r.left == 0 {
		return false
	}
	r.left--
	return true
}

func (r *scriptRows) Scan(dest ...any) error {
	for _, d := range dest {
		switch v := d.(type) {
		case *string:
			*v = "{}" // valid for the JSON columns read as text
		case *int:
			*v = 1
		case *int64:
			*v = 1
		case *bool:
			*v = false
		case *time.Time:
			*v = time.Unix(1700000000, 0)
		case sql.Scanner:
			if err := v.Scan(nil); err != nil {
				return err
			}
		}
	}
	if r.d.scan != nil {
		r.d.scan(r.sql, dest)
	}
	return nil
}

func (*scriptRows) Err() error   { return nil }
func (*scriptRows) Close() error { return nil }

// TestPGStatementsBuild runs every PG method on the scripted driver above.
// It fails if a statement does not build (an @name without its argument, an
// argument no @name uses, a malformed literal), which momo reports as
// momo.ErrValidation before anything reaches the driver. It needs no
// database, so it runs on every pull request; TestPGStore and the contract
// test then prove what the statements do.
func TestPGStatementsBuild(t *testing.T) {
	ctx, now := context.Background(), time.Now()
	scimKind := func(q string, dest []any) {
		if strings.Contains(q, "SELECT kind FROM directory_connectors") {
			*dest[0].(*string) = "scim"
		}
	}
	user := Resource{ID: "u", ConnectorID: 1, Type: TypeUser, Name: "alice", ExternalID: "e", Data: map[string]any{"active": true}}
	updated := user
	updated.Version = 3
	cases := []struct {
		name string
		scan func(string, []any)
		call func(p *PG) error
		want error
	}{
		{name: "Lock", call: func(p *PG) error {
			unlock, err := p.Lock(ctx, 1)
			if err == nil {
				unlock()
			}
			return err
		}},
		{name: "LookupToken", call: func(p *PG) error { _, err := p.LookupToken(ctx, []byte("h")); return err }},
		{name: "TouchToken", call: func(p *PG) error { return p.TouchToken(ctx, 1, now) }},
		{name: "CreateToken", scan: scimKind, call: func(p *PG) error { _, err := p.CreateToken(ctx, "admin", 1, []byte("h"), "hint"); return err }},
		{name: "CreateToken/wrong kind", call: func(p *PG) error { _, err := p.CreateToken(ctx, "admin", 1, []byte("h"), "hint"); return err }, want: ErrWrongKind},
		{name: "ListTokens", call: func(p *PG) error { _, err := p.ListTokens(ctx, 1); return err }},
		{name: "RevokeToken", call: func(p *PG) error { _, err := p.RevokeToken(ctx, "admin", 1); return err }},
		{name: "GetResource", call: func(p *PG) error { _, err := p.GetResource(ctx, 1, TypeUser, "u", true); return err }},
		{name: "FindResources", call: func(p *PG) error { _, err := p.FindResources(ctx, 1, TypeUser, nil); return err }},
		{name: "FindResources/id", call: func(p *PG) error { _, err := p.FindResources(ctx, 1, TypeUser, &Hint{"id", "u"}); return err }},
		{name: "FindResources/name", call: func(p *PG) error { _, err := p.FindResources(ctx, 1, TypeUser, &Hint{"name", "a"}); return err }},
		{name: "FindResources/external_id", call: func(p *PG) error {
			_, err := p.FindResources(ctx, 1, TypeUser, &Hint{"external_id", "e"})
			return err
		}},
		{name: "FindDeleted", call: func(p *PG) error { _, err := p.FindDeleted(ctx, 1, TypeUser, "alice", "e"); return err }},
		{name: "SaveResource/insert", call: func(p *PG) error { _, err := p.SaveResource(ctx, user, []string{"m"}); return err }},
		{name: "SaveResource/update", call: func(p *PG) error { _, err := p.SaveResource(ctx, updated, []string{"m"}); return err }},
		{name: "MarkDeleted/user", call: func(p *PG) error { return p.MarkDeleted(ctx, 1, TypeUser, "u") }},
		{name: "MarkDeleted/group", call: func(p *PG) error { return p.MarkDeleted(ctx, 1, TypeGroup, "g") }},
		{name: "Members", call: func(p *PG) error { _, err := p.Members(ctx, []string{"g"}); return err }},
		{name: "GroupsOf", call: func(p *PG) error { _, err := p.GroupsOf(ctx, []string{"u"}); return err }},
		{name: "Existing", call: func(p *PG) error { _, err := p.Existing(ctx, 1, []string{"u"}); return err }},
		{name: "Counts", call: func(p *PG) error { _, err := p.Counts(ctx, 1); return err }},
		{name: "LastUsed", call: func(p *PG) error { _, err := p.LastUsed(ctx, 1); return err }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &scriptDriver{scan: c.scan}
			p := &PG{DB: momo.New(d)}
			err := c.call(p)
			if errors.Is(err, momo.ErrValidation) || !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v (every statement must build)", err, c.want)
			}
			if len(d.stmt) == 0 {
				t.Fatal("no statement reached the driver")
			}
			for _, q := range d.stmt {
				if strings.Contains(q, "@") {
					t.Errorf("statement still has an @ bind: %s", q)
				}
			}
		})
	}
}
