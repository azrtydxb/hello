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

// namedFiles are the files whose statements bind by name (@name with
// momo.Named) only. TestStatementsBuild fails on a constant statement with a
// native $n in one of them, so a converted file stays converted. A domain
// pull request that converts a file adds it here.
var namedFiles = map[string]bool{
	"batch.go":        true,
	"config.go":       true,
	"dialspace.go":    true,
	"dirprovision.go": true,
	"door.go":         true,
	"door_camera.go":  true,
	"door_open.go":    true,
	"door_webhook.go": true,
	"flow_canvas.go":  true,
	"scep.go":         true,
	"store.go":        true,
	"wifi.go":         true,
}

// TestStatementsBuild renders every statement the store's code passes as a
// constant to the seam (ExecContext, QueryContext, QueryRowContext) or to
// momo's raw functions (momo.Exec, momo.Query, momo.Scalars, momo.RawRows)
// through momo's raw path on the offline handle, with the call site's
// arguments: a momo.Named("x", ...) argument as momo.Named("x", nil), any
// other as a nil positional value. A statement that momo cannot build (an
// @name with no Named argument, a Named argument no @name uses, a $n beside
// an @name, a ? beside the $n binds, an unterminated literal, a $n with no
// argument, an argument no $n uses) is momo.ErrValidation and fails here; one
// that builds reaches the handle and gets momo.ErrUnsupported. It reads the
// package's source, needs no database and runs on every pull request.
// Statements assembled at run time are counted and left to the DB-backed
// tests.
func TestStatementsBuild(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, e := range entries {
		// dbq.go is the seam itself: its statements are its callers'.
		if n := e.Name(); strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") && n != "dbq.go" {
			f, err := parser.ParseFile(fset, n, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, f)
		}
	}
	for name := range namedFiles {
		if _, err := os.Stat(name); err != nil {
			t.Errorf("namedFiles lists %s: %v", name, err)
		}
	}
	consts := stringConsts(files)
	db := momo.NewOffline(dialect.Postgres)
	ctx := context.Background()
	var checked, named, dynamic int
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			i, ok := sqlArgIndex(call)
			if !ok || len(call.Args) <= i {
				return true
			}
			query, ok := constString(call.Args[i], consts)
			if !ok {
				dynamic++
				return true
			}
			args, ok := callArgs(call.Args[i+1:], call.Ellipsis.IsValid(), query, consts)
			if !ok {
				dynamic++
				return true
			}
			pos := fset.Position(call.Pos())
			checked++
			if !dollarRe.MatchString(query) && len(args) > 0 {
				named++
			}
			if namedFiles[pos.Filename] && dollarRe.MatchString(query) {
				t.Errorf("%s: statement binds with $n in a file converted to @name", pos)
			}
			_, err := momo.Exec(ctx, db, query, args...)
			if !errors.Is(err, momo.ErrUnsupported) {
				t.Errorf("%s: statement does not build on momo: %v", pos, err)
			}
			return true
		})
	}
	t.Logf("%d constant statements built (%d with @name binds), %d assembled at run time", checked, named, dynamic)
	// A floor, so a change to how the store writes its calls cannot leave
	// this test checking nothing.
	if checked < 900 {
		t.Errorf("only %d constant statements found; the sweep no longer sees the store's calls", checked)
	}
}

// sqlArgIndex reports whether call passes a statement to the seam or to one
// of momo's raw functions, and the index of the statement's argument.
func sqlArgIndex(call *ast.CallExpr) (int, bool) {
	fun := call.Fun
	switch f := fun.(type) {
	case *ast.IndexExpr: // momo.Query[T]
		fun = f.X
	case *ast.IndexListExpr:
		fun = f.X
	}
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return 0, false
	}
	switch sel.Sel.Name {
	case "ExecContext", "QueryContext", "QueryRowContext":
		return 1, true
	}
	for _, name := range []string{"Exec", "Query", "Scalars", "RawRows"} {
		if isMomo(sel, name) {
			return 2, true
		}
	}
	return 0, false
}

// isMomo reports whether e is momo.<name>.
func isMomo(e ast.Expr, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == "momo"
}

// callArgs is the argument list a call site gives its statement, with nil
// values: momo.Named("x", nil) for a momo.Named argument, nil for any other.
// A call that passes its arguments as a slice (args...) gets one argument per
// $n, or one Named per distinct @name, of the statement. It reports false
// when a Named argument's name is not a constant.
func callArgs(exprs []ast.Expr, spread bool, query string, consts map[string]string) ([]any, bool) {
	if spread {
		if n := maxDollar(query); n > 0 {
			return make([]any, n), true
		}
		var args []any
		seen := map[string]bool{}
		for _, m := range namedRe.FindAllStringSubmatch(query, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				args = append(args, momo.Named(m[1], nil))
			}
		}
		return args, true
	}
	args := make([]any, 0, len(exprs))
	for _, e := range exprs {
		call, ok := e.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			args = append(args, nil)
			continue
		}
		if !isMomo(call.Fun, "Named") {
			args = append(args, nil)
			continue
		}
		name, ok := constString(call.Args[0], consts)
		if !ok {
			return nil, false
		}
		args = append(args, momo.Named(name, nil))
	}
	return args, true
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

// namedRe finds the @name binds of a statement (not PostgreSQL's <@
// operator), for a call that passes its arguments as a slice.
var namedRe = regexp.MustCompile(`(?:^|[^<@\w])@([A-Za-z_][A-Za-z0-9_]*)`)

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
	if _, err := c.ExecContext(ctx, `UPDATE users SET name = @name WHERE id = @id OR (@id::bigint IS NULL AND name = @name)`,
		momo.Named("id", 1), momo.Named("name", "a")); !errors.Is(err, momo.ErrUnsupported) {
		t.Errorf("ExecContext with reused @name binds = %v, want ErrUnsupported", err)
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
		{`SELECT id FROM users WHERE id = @id`, []any{momo.Named("di", 1)}},                        // a misspelled name
		{`SELECT id FROM users WHERE id = @id`, []any{momo.Named("id", 1), momo.Named("x", 2)}},    // a Named no @name uses
		{`SELECT id FROM users WHERE id = $1 AND name = @name`, []any{1, momo.Named("name", "a")}}, // $n beside @name
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
	sent     []sentStmt // every statement, as PostgreSQL receives it
	rows     [][]any
	rowsErr  error
	closeErr error
	affected int64
}

func (d *fakeDriver) Dialect() *dialect.Dialect { return dialect.For(dialect.Postgres) }

// sentStmt is a statement the fake driver received, with its arguments.
type sentStmt struct {
	sql  string
	args []any
}

func (d *fakeDriver) Query(_ context.Context, sql string, args []any) (momo.Rows, error) {
	d.sent = append(d.sent, sentStmt{sql, args})
	return &fakeRows{rows: d.rows, err: d.rowsErr, closeErr: d.closeErr, i: -1}, nil
}

func (d *fakeDriver) Exec(_ context.Context, sql string, args []any) (int64, error) {
	d.sent = append(d.sent, sentStmt{sql, args})
	return d.affected, nil
}

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

// TestOne pins one: the first row, sql.ErrNoRows for none (so mapErr makes
// it ErrNotFound), and a read error as is.
func TestOne(t *testing.T) {
	boom := errors.New("boom")
	if v, err := one([]int64{7, 8}, nil); v != 7 || err != nil {
		t.Errorf("one(two rows) = %d, %v; want the first", v, err)
	}
	if _, err := one([]int64{}, nil); !errors.Is(err, sql.ErrNoRows) || !errors.Is(mapErr(err), ErrNotFound) {
		t.Errorf("one(no rows) = %v, want sql.ErrNoRows", err)
	}
	if _, err := one([]int64{7}, boom); !errors.Is(err, boom) {
		t.Errorf("one(error) = %v, want the error", err)
	}
	d := &fakeDriver{rows: [][]any{{int64(42)}}}
	if rev, err := (&Store{db: &dbPool{dbConn: dbConn{momo.New(d)}}}).ConfigRevision(context.Background()); rev != 42 || err != nil {
		t.Errorf("ConfigRevision = %d, %v; want 42", rev, err)
	}
}

// TestConfigLockAndNotify pins what the configuration lock and the revision
// NOTIFY send to PostgreSQL: the statements and the lock key every replica,
// old or new, takes.
func TestConfigLockAndNotify(t *testing.T) {
	ctx := context.Background()
	d := &fakeDriver{}
	c := dbConn{momo.New(d)}
	if err := lockConfig(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := notifyConfig(ctx, c, 17); err != nil {
		t.Fatal(err)
	}
	want := []sentStmt{
		{`SELECT pg_advisory_xact_lock($1)`, []any{int64(0x68656c6c6f636667)}},
		{`SELECT pg_notify($1, $2::text)`, []any{"hello_config", "17"}},
	}
	if fmt.Sprintf("%#v", d.sent) != fmt.Sprintf("%#v", want) {
		t.Errorf("sent %v, want %v", d.sent, want)
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
