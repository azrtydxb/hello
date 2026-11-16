package sso

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/dialect"

	"github.com/azrtydxb/hello/internal/secret"
)

// scriptDriver is a momo driver with no database behind it, for
// TestPGStatementsBuild (the same as internal/directory's and
// internal/scim's). momo renders every statement (the @name binds, the
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
	box, err := secret.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, now := context.Background(), time.Now()
	sec := "s"
	id := Identity{UserID: 1, Provider: ProviderEntra, Subject: "sub", Tenant: "t", Email: "e@x"}
	cases := []struct {
		name string
		call func(p *PG) error
		want error
	}{
		{name: "Audit", call: func(p *PG) error { return p.Audit(ctx, "a", "b", "c", "d") }},
		{name: "ListProviders", call: func(p *PG) error { _, err := p.ListProviders(ctx); return err }},
		{name: "GetProvider", call: func(p *PG) error { _, err := p.GetProvider(ctx, ProviderEntra); return err }},
		{name: "SaveProvider", call: func(p *PG) error {
			_, err := p.SaveProvider(ctx, "admin", Provider{Provider: ProviderEntra, Domains: []string{"x"}}, &sec)
			return err
		}},
		{name: "ProviderSecret", call: func(p *PG) error { _, err := p.ProviderSecret(ctx, ProviderEntra); return err }, want: ErrNotFound},
		{name: "SSORequired", call: func(p *PG) error { _, err := p.SSORequired(ctx); return err }},
		{name: "SetSSORequired/on", call: func(p *PG) error { return p.SetSSORequired(ctx, "admin", true) }},
		{name: "SetSSORequired/off", call: func(p *PG) error { return p.SetSSORequired(ctx, "admin", false) }},
		{name: "BreakGlassUsers", call: func(p *PG) error { _, err := p.BreakGlassUsers(ctx); return err }},
		{name: "GetUser", call: func(p *PG) error { _, err := p.GetUser(ctx, 1); return err }},
		{name: "IdentityUser", call: func(p *PG) error { _, err := p.IdentityUser(ctx, ProviderEntra, "sub"); return err }},
		{name: "SyncedUser", call: func(p *PG) error { _, err := p.SyncedUser(ctx, "entra", "x"); return err }},
		{name: "SyncedUsersByEmail", call: func(p *PG) error { _, err := p.SyncedUsersByEmail(ctx, "e@x"); return err }},
		{name: "Link", call: func(p *PG) error { return p.Link(ctx, "sso:entra", id) }},
		{name: "TouchIdentity", call: func(p *PG) error { return p.TouchIdentity(ctx, ProviderEntra, "sub", now) }},
		{name: "ListIdentities", call: func(p *PG) error { _, err := p.ListIdentities(ctx, 1); return err }},
		{name: "Unlink", call: func(p *PG) error { return p.Unlink(ctx, "admin", 1, ProviderEntra) }},
		{name: "CreateUser", call: func(p *PG) error { _, err := p.CreateUser(ctx, "sso:entra", "u", "e@x", "U", id); return err }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &scriptDriver{}
			p := &PG{DB: momo.New(d), Box: box}
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
