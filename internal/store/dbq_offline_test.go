package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/dialect"
	"github.com/azrtydxb/momo/driver/pgxdriver"
	"github.com/azrtydxb/momo/momolog"
	"github.com/jackc/pgx/v5/pgconn"

	hellodb "github.com/azrtydxb/hello/internal/db"
)

// TestStatementsBuild renders every statement the store's code passes to
// ExecContext, QueryContext or QueryRowContext as a constant through momo's
// raw path on the offline handle, with as many arguments as the call site
// gives. A statement that momo cannot build (a ? beside the $n binds, an
// unterminated literal, a $n with no argument, an argument no $n uses) is
// momo.ErrValidation and fails here; one that builds reaches the handle and
// gets momo.ErrUnsupported. It reads the package's source, needs no
// database and runs on every pull request. Statements assembled at run time
// are counted and left to the DB-backed tests.
func TestStatementsBuild(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, e := range entries {
		if n := e.Name(); strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			f, err := parser.ParseFile(fset, n, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, f)
		}
	}
	consts := stringConsts(files)
	db := momo.NewOffline(dialect.Postgres)
	ctx := context.Background()
	var checked, dynamic int
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "ExecContext", "QueryContext", "QueryRowContext":
			default:
				return true
			}
			query, ok := constString(call.Args[1], consts)
			if !ok {
				dynamic++
				return true
			}
			nargs := len(call.Args) - 2
			if call.Ellipsis.IsValid() {
				nargs = maxDollar(query)
			}
			checked++
			_, err := momo.Exec(ctx, db, query, make([]any, nargs)...)
			if !errors.Is(err, momo.ErrUnsupported) {
				t.Errorf("%s: statement does not build on momo: %v", fset.Position(call.Pos()), err)
			}
			return true
		})
	}
	t.Logf("%d constant statements built, %d assembled at run time", checked, dynamic)
	// A floor, so a change to how the store writes its calls cannot leave
	// this test checking nothing.
	if checked < 900 {
		t.Errorf("only %d constant statements found; the sweep no longer sees the store's calls", checked)
	}
}

// stringConsts is every string constant declared in files, at package level or
// in a function, by name. A name declared twice with different values is
// left out.
func stringConsts(files []*ast.File) map[string]string {
	exprs := map[string][]ast.Expr{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			gd, ok := n.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				return true
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if i < len(vs.Values) {
						exprs[name.Name] = append(exprs[name.Name], vs.Values[i])
					}
				}
			}
			return true
		})
	}
	out := map[string]string{}
	// Constants refer to each other; resolve until nothing changes.
	for changed := true; changed; {
		changed = false
		for name, es := range exprs {
			if _, done := out[name]; done {
				continue
			}
			var val string
			same := true
			for i, e := range es {
				v, ok := constString(e, out)
				if !ok || (i > 0 && v != val) {
					same = false
					break
				}
				val = v
			}
			if same {
				out[name] = val
				changed = true
			}
		}
	}
	return out
}

// constString evaluates e when it is a string literal, a known constant or a
// concatenation of those.
func constString(e ast.Expr, consts map[string]string) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		return s, err == nil
	case *ast.Ident:
		s, ok := consts[e.Name]
		return s, ok
	case *ast.ParenExpr:
		return constString(e.X, consts)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		x, ok := constString(e.X, consts)
		if !ok {
			return "", false
		}
		y, ok := constString(e.Y, consts)
		return x + y, ok
	}
	return "", false
}

var dollarRe = regexp.MustCompile(`\$([0-9]+)`)

// maxDollar is the highest $n in query, the argument count of a call that
// passes its arguments as a slice.
func maxDollar(query string) int {
	n := 0
	for _, m := range dollarRe.FindAllStringSubmatch(query, -1) {
		if v, _ := strconv.Atoi(m[1]); v > n {
			n = v
		}
	}
	return n
}

// TestSeamOffline checks the seam's error paths on the offline handle: a
// statement that builds reaches the driver (ErrUnsupported), one that does
// not is ErrValidation, and QueryRowContext defers either to Scan.
func TestSeamOffline(t *testing.T) {
	c := dbConn{momo.NewOffline(dialect.Postgres)}
	ctx := context.Background()
	if _, err := c.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = $1`, []byte("x")); !errors.Is(err, momo.ErrUnsupported) {
		t.Errorf("ExecContext = %v, want ErrUnsupported", err)
	}
	if _, err := c.QueryContext(ctx, `SELECT id FROM users WHERE username = $1`, "a"); !errors.Is(err, momo.ErrUnsupported) {
		t.Errorf("QueryContext = %v, want ErrUnsupported", err)
	}
	row := c.QueryRowContext(ctx, `SELECT id FROM users WHERE id = $1`, 1)
	var id int64
	if err := row.Scan(&id); !errors.Is(err, momo.ErrUnsupported) || !errors.Is(row.Err(), momo.ErrUnsupported) {
		t.Errorf("QueryRowContext().Scan = %v, Err = %v, want ErrUnsupported", err, row.Err())
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`SELECT id FROM users WHERE id = $2`, []any{1}},             // $n past the arguments
		{`SELECT id FROM users WHERE id = $1`, []any{1, 2}},          // an argument no $n uses
		{`SELECT id FROM users WHERE id = $1 AND x ? 'k'`, []any{1}}, // ? beside $n
		{`SELECT 'unterminated`, nil},
	} {
		if _, err := c.ExecContext(ctx, q.sql, q.args...); !errors.Is(err, momo.ErrValidation) {
			t.Errorf("ExecContext(%q) = %v, want ErrValidation", q.sql, err)
		}
	}
}

// fakeDriver answers every query with rows and every exec with affected; it
// stands in for PostgreSQL so the seam's database/sql semantics can be
// checked without one.
type fakeDriver struct {
	rows     [][]any
	rowsErr  error
	closeErr error
	affected int64
}

func (d *fakeDriver) Dialect() *dialect.Dialect { return dialect.For(dialect.Postgres) }

func (d *fakeDriver) Query(context.Context, string, []any) (momo.Rows, error) {
	return &fakeRows{rows: d.rows, err: d.rowsErr, closeErr: d.closeErr, i: -1}, nil
}

func (d *fakeDriver) Exec(context.Context, string, []any) (int64, error) { return d.affected, nil }

func (d *fakeDriver) Begin(context.Context, momo.TxOptions) (momo.DriverTx, error) {
	return nil, errors.New("fake: no transactions")
}

type fakeRows struct {
	rows     [][]any
	err      error
	closeErr error
	i        int
	closed   bool
}

func (r *fakeRows) Next() bool {
	if r.closed || r.i+1 >= len(r.rows) {
		return false
	}
	r.i++
	return true
}

func (r *fakeRows) Scan(dest ...any) error {
	for i, d := range dest {
		switch p := d.(type) {
		case *int64:
			*p = r.rows[r.i][i].(int64)
		case *string:
			*p = r.rows[r.i][i].(string)
		default:
			return fmt.Errorf("fake: cannot scan into %T", d)
		}
	}
	return nil
}

func (r *fakeRows) Err() error { return r.err }

func (r *fakeRows) Close() error {
	r.closed = true
	return r.closeErr
}

func (r *fakeRows) Columns() ([]string, error) { return nil, nil }

// TestSeamSemantics checks that the seam keeps database/sql's conventions the
// store's code relies on: QueryRowContext with no row is sql.ErrNoRows, the
// first row is scanned, an iteration or close error is reported, and
// ExecContext reports the rows affected.
func TestSeamSemantics(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	conn := func(d *fakeDriver) dbConn { return dbConn{momo.New(d)} }

	var id int64
	if err := conn(&fakeDriver{}).QueryRowContext(ctx, `SELECT 1`).Scan(&id); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("no row: Scan = %v, want sql.ErrNoRows", err)
	}
	if err := conn(&fakeDriver{rowsErr: boom}).QueryRowContext(ctx, `SELECT 1`).Scan(&id); !errors.Is(err, boom) {
		t.Errorf("no row after an error: Scan = %v, want the error", err)
	}
	if err := conn(&fakeDriver{rows: [][]any{{int64(7)}, {int64(8)}}}).QueryRowContext(ctx, `SELECT 1`).Scan(&id); err != nil || id != 7 {
		t.Errorf("two rows: Scan = %v, id %d; want the first row", err, id)
	}
	if err := conn(&fakeDriver{rows: [][]any{{int64(7)}}, closeErr: boom}).QueryRowContext(ctx, `SELECT 1`).Scan(&id); !errors.Is(err, boom) {
		t.Errorf("close error: Scan = %v, want the error", err)
	}

	rows, err := conn(&fakeDriver{rows: [][]any{{int64(1), "a"}, {int64(2), "b"}}}).QueryContext(ctx, `SELECT id, name FROM t`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var (
			n    int64
			name string
		)
		if err := rows.Scan(&n, &name); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%d %s", n, name))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil || rows.Close() != nil {
		t.Errorf("Close = %v; want nil, twice", err)
	}
	if strings.Join(got, ",") != "1 a,2 b" {
		t.Errorf("rows = %v", got)
	}

	res, err := conn(&fakeDriver{affected: 3}).ExecContext(ctx, `DELETE FROM t WHERE id = $1`, 1)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := res.RowsAffected(); n != 3 || err != nil {
		t.Errorf("RowsAffected = %d, %v; want 3", n, err)
	}
	if err := requireRow(execResult(0)); !errors.Is(err, ErrNotFound) {
		t.Errorf("requireRow(0) = %v, want ErrNotFound", err)
	}
}

// TestMapErr pins mapErr on errors as momo classifies them and as raw
// database/sql returns them.
func TestMapErr(t *testing.T) {
	pg := func(code, msg, constraint string) *pgconn.PgError {
		return &pgconn.PgError{Code: code, Message: msg, ConstraintName: constraint}
	}
	uniq := pg("23505", `duplicate key value violates unique constraint "users_username_key"`, "users_username_key")
	fkInsert := pg("23503", `insert or update on table "phones" violates foreign key constraint "phones_device_id_fkey"`, "phones_device_id_fkey")
	fkDelete := pg("23503", `update or delete on table "devices" violates foreign key constraint "phones_device_id_fkey" on table "phones"`, "phones_device_id_fkey")
	check := pg("23514", `new row for relation "phones" violates check constraint "phones_model_check"`, "phones_model_check")
	other := errors.New("boom")
	inUse := "still referenced (phones_device_id_fkey); remove the reference first"

	for _, tc := range []struct {
		name string
		in   error
		is   error  // errors.Is(got, is)
		msg  string // got.Error(), when set
		same bool   // got is in itself
	}{
		{name: "nil"},
		{name: "no rows", in: sql.ErrNoRows, is: ErrNotFound},
		{name: "wrapped no rows", in: fmt.Errorf("x: %w", sql.ErrNoRows), is: ErrNotFound},
		{name: "unique, raw", in: uniq, is: ErrConflict, msg: "store: conflict: users_username_key"},
		{name: "unique, momo", in: pgxdriver.ClassifyError(uniq), is: ErrConflict, msg: "store: conflict: users_username_key"},
		{name: "unique, momo without the pg error", in: &momo.Error{Kind: momo.ErrUniqueViolation, Constraint: "c"}, is: ErrConflict, msg: "store: conflict: c"},
		{name: "fk insert, raw", in: fkInsert, is: ErrNotFound, msg: "store: not found: phones_device_id_fkey"},
		{name: "fk insert, momo", in: pgxdriver.ClassifyError(fkInsert), is: ErrNotFound, msg: "store: not found: phones_device_id_fkey"},
		{name: "fk delete, raw", in: fkDelete, is: ErrInUse, msg: inUse},
		{name: "fk delete, momo", in: pgxdriver.ClassifyError(fkDelete), is: ErrInUse, msg: inUse},
		{name: "fk delete, momo, wrapped", in: fmt.Errorf("x: %w", pgxdriver.ClassifyError(fkDelete)), is: ErrInUse, msg: inUse},
		{name: "check, momo", in: pgxdriver.ClassifyError(check), same: true},
		{name: "check, raw", in: check, same: true},
		{name: "other", in: other, same: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mapErr(tc.in)
			switch {
			case tc.in == nil:
				if got != nil {
					t.Fatalf("mapErr(nil) = %v", got)
				}
				return
			case tc.same:
				if got != tc.in { //nolint:errorlint // identity is the point.
					t.Fatalf("mapErr = %v, want the error unchanged", got)
				}
				return
			}
			if !errors.Is(got, tc.is) {
				t.Fatalf("mapErr = %v, want %v", got, tc.is)
			}
			if tc.msg != "" && got.Error() != tc.msg {
				t.Fatalf("mapErr = %q, want %q", got.Error(), tc.msg)
			}
		})
	}
	// The check violation keeps its constraint name in the text callers
	// show (the API's prov test matches on it).
	if got := mapErr(pgxdriver.ClassifyError(check)); !strings.Contains(got.Error(), "phones_model_check") {
		t.Errorf("check violation text %q lost the constraint", got)
	}
}

// BenchmarkSeam is the seam's cost per statement over a driver that does no
// I/O: momo's statement build, hooks (a disabled debug logger, as in
// production) and the cursor, against calling the driver directly. A
// statement to PostgreSQL costs a network round trip on top of either.
func BenchmarkSeam(b *testing.B) {
	const q = `SELECT id, password_hash FROM users WHERE username = $1 AND enabled AND id > $2`
	d := &fakeDriver{rows: [][]any{{int64(1), "h"}}}
	ctx := context.Background()
	b.Run("driver", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			r, _ := d.Query(ctx, q, []any{"alice", 0})
			var (
				id int64
				h  string
			)
			r.Next()
			_ = r.Scan(&id, &h)
			_ = r.Close()
		}
	})
	b.Run("seam", func(b *testing.B) {
		c := dbConn{momo.New(d, momo.WithHook(nopHook{}))}
		b.ReportAllocs()
		for b.Loop() {
			var (
				id int64
				h  string
			)
			_ = c.QueryRowContext(ctx, q, "alice", 0).Scan(&id, &h)
		}
	})
	b.Run("seam-log", func(b *testing.B) {
		// internal/db's hook on a logger at info level, as in production:
		// debug statements are not logged.
		log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo}))
		c := dbConn{momo.New(d, momo.WithHook(momolog.New(log, momolog.Level(slog.LevelDebug), momolog.SlowOver(hellodb.SlowQuery))))}
		b.ReportAllocs()
		for b.Loop() {
			var (
				id int64
				h  string
			)
			_ = c.QueryRowContext(ctx, q, "alice", 0).Scan(&id, &h)
		}
	})
}

type nopHook struct{}

func (nopHook) BeforeQuery(ctx context.Context, _ *momo.QueryEvent) context.Context { return ctx }
func (nopHook) AfterQuery(context.Context, *momo.QueryEvent)                        {}
