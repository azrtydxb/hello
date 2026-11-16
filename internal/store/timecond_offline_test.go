package store

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/momo"
)

// scriptDriver records every statement like fakeDriver and answers a query
// with the rows of the first reply whose prefix the statement starts with
// (none for no match), scanning each value into its destination's type.
type scriptDriver struct {
	fakeDriver
	replies []scriptReply
}

type scriptReply struct {
	prefix string
	rows   [][]any
}

func (d *scriptDriver) Query(_ context.Context, sql string, args []any) (momo.Rows, error) {
	d.sent = append(d.sent, sentStmt{sql, args})
	for _, r := range d.replies {
		if strings.HasPrefix(sql, r.prefix) {
			return &scriptRows{rows: r.rows, i: -1}, nil
		}
	}
	return &scriptRows{i: -1}, nil
}

type scriptRows struct {
	rows [][]any
	i    int
}

func (r *scriptRows) Next() bool {
	if r.i+1 >= len(r.rows) {
		return false
	}
	r.i++
	return true
}

func (r *scriptRows) Scan(dest ...any) error {
	for i, d := range dest {
		dst := reflect.ValueOf(d).Elem()
		dst.Set(reflect.ValueOf(r.rows[r.i][i]).Convert(dst.Type()))
	}
	return nil
}

func (r *scriptRows) Err() error                 { return nil }
func (r *scriptRows) Close() error               { return nil }
func (r *scriptRows) Columns() ([]string, error) { return nil, nil }

// TestTimeConditionToggleStatements pins what the time-condition statements
// hello-sip runs during a call (the *28 day/night feature code,
// ApplyTimeConditionToggle) send to PostgreSQL now that they bind by name:
// each bind is used once, so the SQL is the $n statement it was, with the
// binds numbered in the order they appear and every parameter typed by the
// column it is compared with or assigned to, as before.
func TestTimeConditionToggleStatements(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	d := &scriptDriver{fakeDriver: fakeDriver{affected: 1}, replies: []scriptReply{
		{`SELECT s.id`, [][]any{{int64(3), "office", "Europe/Brussels", at, at}}},
		{`SELECT c.id`, [][]any{{int64(5), "holidays", "", at, at}}},
	}}
	tx := &dbTx{dbConn: dbConn{momo.New(d)}}
	if _, err := listTimeConditions(ctx, tx, ` WHERE t.toggle_number = @toggle_number`, momo.Named("toggle_number", "1")); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTimeConditionSet(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := setOverride(ctx, tx, 7, "sip:1001", "auto"); err != nil {
		t.Fatal(err)
	}
	if _, err := listTimeConditions(ctx, tx, ` WHERE t.id = @id`, momo.Named("id", int64(7))); err != nil {
		t.Fatal(err)
	}
	tc := `SELECT ` + tcCols + tcFrom
	want := []sentStmt{
		{tc + ` WHERE t.toggle_number = $1 ORDER BY t.id`, []any{"1"}},
		{`SELECT s.id, s.name, s.time_zone, s.created_at, s.updated_at FROM schedules s  ORDER BY s.id`, nil},
		{`SELECT schedule_id, weekday, start_time, end_time FROM schedule_ranges
		WHERE schedule_id = ANY($1) ORDER BY schedule_id, weekday, start_time`, []any{[]int64{3}}},
		{`SELECT sc.schedule_id, c.id, c.name FROM schedule_calendars sc
		JOIN calendars c ON c.id = sc.calendar_id WHERE sc.schedule_id = ANY($1) ORDER BY sc.schedule_id, c.name`, []any{[]int64{3}}},
		{`SELECT c.id, c.name, c.description, c.created_at, c.updated_at FROM calendars c  ORDER BY c.id`, nil},
		{`SELECT e.calendar_id, ` + calEntryCols + ` FROM calendar_entries e
		WHERE e.calendar_id = ANY($1) ORDER BY e.calendar_id, e.yearly, to_char(e.start_date, 'MM-DD'), e.start_date, e.id`, []any{[]int64{5}}},
		{`SELECT sc.calendar_id, s.name FROM schedule_calendars sc
		JOIN schedules s ON s.id = sc.schedule_id WHERE sc.calendar_id = ANY($1) ORDER BY sc.calendar_id, s.name`, []any{[]int64{5}}},
		{tc + ` ORDER BY t.id`, nil},
		// The old statement numbered id first ($1); the parameters are the
		// same four, each typed by its column. Automatic mode clears
		// override_at with a NULL, as before.
		{`UPDATE time_conditions SET override = $1, override_at = $2, override_by = $3, updated_at = now()
		WHERE id = $4`, []any{"auto", nil, "sip:1001", int64(7)}},
		{tc + ` WHERE t.id = $1 ORDER BY t.id`, []any{int64(7)}},
	}
	if got, w := fmt.Sprintf("%#v", d.sent), fmt.Sprintf("%#v", want); got != w {
		t.Errorf("sent\n%s\nwant\n%s", got, w)
	}
}
