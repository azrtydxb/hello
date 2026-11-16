package directory

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
// TestPGStatementsBuild. momo renders every statement (the @name binds, the
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

// offlineProvisioner is a Provisioner for the build test; the scripted
// transaction has no *sql.Tx behind it, so Provision is never reached.
type offlineProvisioner struct{}

func (offlineProvisioner) Taken(context.Context, *sql.Tx, string, string) (map[string]bool, error) {
	return map[string]bool{}, nil
}

func (offlineProvisioner) Provision(context.Context, *sql.Tx, string, ProvisionRequest) (ProvisionResult, error) {
	return ProvisionResult{}, nil
}

// TestPGStatementsBuild runs every PG method, and every operation of Apply,
// on the scripted driver above. It fails if a statement does not build (an
// @name without its argument, an argument no @name uses, a malformed
// literal), which momo reports as momo.ErrValidation before anything reaches
// the driver. It needs no database, so it runs on every pull request; the
// DB-backed tests (pg_test.go, dirstore) then prove what the statements do.
func TestPGStatementsBuild(t *testing.T) {
	box, err := secret.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, now := context.Background(), time.Now()
	str := func(s string) *string { return &s }
	pw := "pw"
	conn := Connector{ID: 1, Kind: KindLDAP, Name: "AD", Enabled: true, Config: []byte(`{}`), AutoProvision: AutoProvision{Enabled: true}}
	uid := int64(7)
	full := Op{UserID: 7, Username: str("u"), Role: str("user"), Email: str("e@x"), Display: str("D"), ExternalID: "x",
		SetExtension: true, Extension: 9, Override: true, RingAdd: []int64{1}, RingRemove: []int64{2}, Managed: []string{"email"}, SetManaged: true, Devices: true}
	withKind := func(o Op, k string) Op { o.Kind = k; return o }
	disabledScan := func(q string, dest []any) {
		if strings.Contains(q, "break_glass FROM users") {
			*dest[1].(*bool) = true // enabled, so disable goes on
		}
	}
	noRun := func(q string) int {
		if strings.Contains(q, "kind = 'scim' AND status = 'succeeded' AND created_at") {
			return 0
		}
		return 1
	}
	cases := []struct {
		name string
		rows func(string) int
		scan func(string, []any)
		call func(p *PG) error
		want error
	}{
		{name: "ListConnectors", call: func(p *PG) error { _, err := p.ListConnectors(ctx); return err }},
		{name: "GetConnector", call: func(p *PG) error { _, err := p.GetConnector(ctx, 1); return err }},
		{name: "SaveConnector/create", call: func(p *PG) error {
			c := conn
			c.ID = 0
			_, err := p.SaveConnector(ctx, "admin", c, &pw, "fp")
			return err
		}},
		{name: "SaveConnector/update", call: func(p *PG) error { _, err := p.SaveConnector(ctx, "admin", conn, &pw, "fp"); return err }},
		{name: "DeleteConnector", call: func(p *PG) error { return p.DeleteConnector(ctx, "admin", 1, true) }},
		{name: "Secret", call: func(p *PG) error { _, err := p.Secret(ctx, 1); return err }, want: ErrNotFound},
		{name: "RecordOutcome", call: func(p *PG) error { _, err := p.RecordOutcome(ctx, 1, false, "c", "d", now); return err }},
		{name: "SaveSyncState", call: func(p *PG) error {
			return p.SaveSyncState(ctx, 1, SyncState{Mark: &SyncMark{Method: "usn"}, Fingerprint: "f", LastFullAt: &now})
		}},
		{name: "Audit", call: func(p *PG) error { return p.Audit(ctx, "a", "b", "c", "d") }},
		{name: "CreateRun", call: func(p *PG) error { _, err := p.CreateRun(ctx, Run{ConnectorID: 1, Kind: "apply"}); return err }},
		{name: "GetRun", call: func(p *PG) error { _, err := p.GetRun(ctx, 1); return err }},
		{name: "ListRuns", call: func(p *PG) error { _, err := p.ListRuns(ctx, 1, 10); return err }},
		{name: "UpdateRun", call: func(p *PG) error { return p.UpdateRun(ctx, Run{ID: 1, FinishedAt: &now}) }},
		{name: "ClaimQueued", call: func(p *PG) error { _, err := p.ClaimQueued(ctx, 1, "r"); return err }},
		{name: "CloseOrphans", call: func(p *PG) error { _, err := p.CloseOrphans(ctx, 1); return err }},
		{name: "LastScheduled", call: func(p *PG) error { _, _, err := p.LastScheduled(ctx, 1); return err }},
		{name: "HasApplied", call: func(p *PG) error { _, err := p.HasApplied(ctx, 1); return err }},
		{name: "ReplaceChanges", call: func(p *PG) error {
			return p.ReplaceChanges(ctx, 1, []Change{{Action: ActionCreate, UserID: &uid, Username: "u"}})
		}},
		{name: "ListChanges", call: func(p *PG) error { _, err := p.ListChanges(ctx, 1, ChangeFilter{Action: ActionCreate}); return err }},
		{name: "LoadState", call: func(p *PG) error { _, err := p.LoadState(ctx, 1); return err }},
		{name: "PruneChanges", call: func(p *PG) error { _, err := p.PruneChanges(ctx, now); return err }},
		{name: "TryLock", call: func(p *PG) error { _, _, err := p.TryLock(ctx, "hello:dirsync:1"); return err }},
		{name: "Apply/create", call: func(p *PG) error { _, err := p.Apply(ctx, "admin", conn, withKind(full, ActionCreate)); return err }},
		{name: "Apply/adopt", call: func(p *PG) error { _, err := p.Apply(ctx, "admin", conn, withKind(full, ActionAdopt)); return err }},
		{name: "Apply/disable", scan: disabledScan, call: func(p *PG) error {
			_, err := p.Apply(ctx, "admin", conn, withKind(full, ActionDisable))
			return err
		}},
		{name: "Apply/enable", call: func(p *PG) error { _, err := p.Apply(ctx, "admin", conn, withKind(full, ActionEnable)); return err }},
		{name: "Apply/absent", call: func(p *PG) error { _, err := p.Apply(ctx, "admin", conn, withKind(full, "absent")); return err }},
		{name: "Apply/touch", call: func(p *PG) error { _, err := p.Apply(ctx, "admin", conn, withKind(full, "touch")); return err }},
		{name: "Apply/managed", call: func(p *PG) error { _, err := p.Apply(ctx, "admin", conn, withKind(full, "managed")); return err }},
		{name: "setProvisioned", call: func(p *PG) error {
			return p.DB.Tx(ctx, momo.TxOptions{}, func(tx *momo.Tx) error { return setProvisioned(ctx, tx, 1, "2001") })
		}},
		{name: "RecordPush/existing", call: func(p *PG) error {
			return p.RecordPush(ctx, 1, []Change{{Action: ActionDisable, UserID: &uid}}, now)
		}},
		{name: "RecordPush/new", rows: noRun, call: func(p *PG) error {
			return p.RecordPush(ctx, 1, []Change{{Action: ActionDisable, UserID: &uid}}, now)
		}},
		{name: "PushDisables", call: func(p *PG) error { _, err := p.PushDisables(ctx, 1, now); return err }},
		{name: "SetPaused/pause", call: func(p *PG) error { return p.SetPaused(ctx, 1, true, now) }},
		{name: "SetPaused/resume", call: func(p *PG) error { return p.SetPaused(ctx, 1, false, now) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &scriptDriver{rows: c.rows, scan: c.scan}
			p := &PG{DB: momo.New(d), Box: box, Provisioner: offlineProvisioner{}}
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
