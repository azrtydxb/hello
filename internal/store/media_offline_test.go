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

// TestMediaRuntimeStatements runs the voicemail, ring group, music on hold
// and recording lookup statements that are assembled at run time (a WHERE
// fragment, a SET column, the lookup filter), which TestStatementsBuild
// counts but cannot build, on a recording driver: each must build on momo
// (every @name has its Named and every Named its @name) and reach the driver.
func TestMediaRuntimeStatements(t *testing.T) {
	ctx := context.Background()
	store := func(d *fakeDriver) *Store { return &Store{db: &dbPool{dbConn: dbConn{momo.New(d)}}} }
	tx := func(d *fakeDriver) *dbTx { return &dbTx{dbConn: dbConn{momo.New(d)}} }
	full := LookupFilter{
		Number: "+32 11 111 222", Extension: "100", CallID: "c1", Recorded: true,
		From: time.Unix(1, 0), To: time.Unix(2, 0), Restrict: true, Allowed: []string{"100", "101"},
	}

	for _, tc := range []struct {
		name string
		rows [][]any
		run  func(*fakeDriver) error
		sent int   // statements the driver receives
		want error // the run's error, nil for none
	}{
		{name: "ListVoicemailMessages", sent: 1, run: func(d *fakeDriver) error {
			_, err := store(d).ListVoicemailMessages(ctx, 1, false)
			return err
		}},
		{name: "ListVoicemailMessages unheard", sent: 1, run: func(d *fakeDriver) error {
			_, err := store(d).ListVoicemailMessages(ctx, 1, true)
			return err
		}},
		{name: "SetVoicemailGreeting", rows: [][]any{{"old"}}, sent: 2, run: func(d *fakeDriver) error {
			prev, err := store(d).SetVoicemailGreeting(ctx, 1, "greeting", "new")
			if err == nil && prev != "old" {
				return fmt.Errorf("prev = %q", prev)
			}
			return err
		}},
		{name: "SetVoicemailGreeting name", rows: [][]any{{"old"}}, sent: 2, run: func(d *fakeDriver) error {
			_, err := store(d).SetVoicemailGreeting(ctx, 1, "name", "")
			return err
		}},
		{name: "ListRingGroups", sent: 1, run: func(d *fakeDriver) error {
			_, err := store(d).ListRingGroups(ctx)
			return err
		}},
		{name: "GetRingGroup", sent: 1, want: ErrNotFound, run: func(d *fakeDriver) error {
			_, err := store(d).GetRingGroup(ctx, 1)
			return err
		}},
		{name: "getGroupTx", sent: 1, want: ErrNotFound, run: func(d *fakeDriver) error {
			_, err := getGroupTx(ctx, tx(d), 1)
			return err
		}},
		{name: "ListVoicemailGroups", sent: 1, run: func(d *fakeDriver) error {
			_, err := store(d).ListVoicemailGroups(ctx)
			return err
		}},
		{name: "GetVoicemailGroup", sent: 1, want: ErrNotFound, run: func(d *fakeDriver) error {
			_, err := store(d).GetVoicemailGroup(ctx, 1)
			return err
		}},
		{name: "refuseIfVoicemailGroupUsed", sent: 4, run: func(d *fakeDriver) error {
			return refuseIfVoicemailGroupUsed(ctx, tx(d), "voicemail group 800", "800")
		}},
		{name: "ListMOHClasses", sent: 1, run: func(d *fakeDriver) error {
			_, err := store(d).ListMOHClasses(ctx)
			return err
		}},
		{name: "GetMOHClass", sent: 1, want: ErrNotFound, run: func(d *fakeDriver) error {
			_, err := store(d).GetMOHClass(ctx, 1)
			return err
		}},
		{name: "LookupCalls", sent: 1, run: func(d *fakeDriver) error {
			_, _, err := store(d).LookupCalls(ctx, LookupFilter{}, 0, 10)
			return err
		}},
		{name: "LookupCalls every filter", sent: 1, run: func(d *fakeDriver) error {
			_, _, err := store(d).LookupCalls(ctx, full, 5, 10)
			return err
		}},
		{name: "LookupRecordings", sent: 1, run: func(d *fakeDriver) error {
			_, _, err := store(d).LookupRecordings(ctx, LookupFilter{}, 0, 10)
			return err
		}},
		{name: "LookupRecordings every filter", sent: 1, run: func(d *fakeDriver) error {
			_, _, err := store(d).LookupRecordings(ctx, full, 5, 10)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &fakeDriver{rows: tc.rows, affected: 1}
			if err := tc.run(d); !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if len(d.sent) != tc.sent {
				t.Fatalf("sent %d statements, want %d: %v", len(d.sent), tc.sent, d.sent)
			}
			for _, s := range d.sent {
				if strings.Contains(s.sql, "@") {
					t.Errorf("statement reached the driver with an @name: %s", s.sql)
				}
			}
		})
	}
}

// TestLookupWhereBinds pins the names lookupWhere binds: one Named per
// condition it adds, none for a condition it leaves out.
func TestLookupWhereBinds(t *testing.T) {
	where, args := lookupWhere(LookupFilter{}, nil)
	if where != "TRUE" || len(args) != 0 {
		t.Errorf("empty filter = %q, %v", where, args)
	}
	where, args = lookupWhere(LookupFilter{Extension: "100", Recorded: true}, nil)
	if want := "(c.source = @extension::text OR c.destination = @extension::text OR c.original_destination = @extension::text) AND r.id IS NOT NULL"; where != want {
		t.Errorf("where = %q, want %q", where, want)
	}
	if len(args) != 1 {
		t.Errorf("args = %v, want the extension only", args)
	}
}

// TestMailboxNotify pins the mailbox NOTIFY hello-sip listens for: the
// statement, the channel and the box id as text.
func TestMailboxNotify(t *testing.T) {
	d := &fakeDriver{}
	if err := notifyMailbox(context.Background(), &dbTx{dbConn: dbConn{momo.New(d)}}, 17); err != nil {
		t.Fatal(err)
	}
	want := []sentStmt{{`SELECT pg_notify($1, $2::text)`, []any{MailboxChannel, "17"}}}
	if fmt.Sprintf("%#v", d.sent) != fmt.Sprintf("%#v", want) {
		t.Errorf("sent %v, want %v", d.sent, want)
	}
}
