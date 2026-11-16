package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/azrtydxb/momo"
)

// TestRoutingRuntimeStatements runs the routing and time-condition domain's
// statements that are assembled at run time, which TestStatementsBuild
// leaves to the DB tier (a WHERE fragment, a statement chosen from a table,
// a caller's SQL passed through routeNames or stringsOf), on a recording
// driver: each must build, so a misspelled or missing @name fails here on
// the pull request tier, and reach PostgreSQL with $n binds only.
func TestRoutingRuntimeStatements(t *testing.T) {
	ctx := context.Background()
	d := &scriptDriver{fakeDriver: fakeDriver{affected: 1}, replies: []scriptReply{
		{`SELECT count(*)`, [][]any{{int64(1)}}},
		{`SELECT number FROM extensions`, [][]any{{"1001"}}},
	}}
	tx := &dbTx{dbConn: dbConn{momo.New(d)}}
	s := &Store{db: &dbPool{dbConn: tx.dbConn}}
	d0 := &scriptDriver{replies: []scriptReply{{`SELECT count(*)`, [][]any{{int64(0)}}}}}
	s0 := &Store{db: &dbPool{dbConn: dbConn{momo.New(d0)}}}
	runs := map[string]func() error{
		"ListBlocklist": func() error {
			_, _, _, err := s.ListBlocklist(ctx, BlocklistFilter{Q: "+32", Action: "block", Scope: "global"})
			return err
		},
		// No extension would change, so neither reaches its transaction,
		// which raw database/sql begins.
		"SetCalendarDND on":  func() error { _, err := s0.SetCalendarDND(ctx, 1, true); return err },
		"SetCalendarDND off": func() error { _, err := s0.SetCalendarDND(ctx, 1, false); return err },
		"tcReferences":       func() error { _, err := tcReferences(ctx, tx, 7, "night"); return err },
		"extensionRoutes":    func() error { _, _, err := extensionRoutes(ctx, tx, 7); return err },
		"getTrunk":           func() error { _, err := getTrunk(ctx, tx, 7, "FOR UPDATE"); return notFound(err) },
		"getOutbound":        func() error { _, err := getOutbound(ctx, tx, 7, "FOR UPDATE"); return notFound(err) },
		"getInbound":         func() error { _, err := getInbound(ctx, tx, 7, "FOR UPDATE"); return notFound(err) },
		"deleteByID": func() error {
			_, err := deleteByID(ctx, `DELETE FROM outbound_routes WHERE id = @id`, 7)(tx)
			return err
		},
		"listSchedules": func() error {
			_, err := listSchedules(ctx, tx, ` WHERE s.id = @id`, momo.Named("id", int64(7)))
			return err
		},
		"listCalendars": func() error {
			_, err := listCalendars(ctx, tx, ` WHERE c.id = @id`, momo.Named("id", int64(7)))
			return err
		},
	}
	for kind, rt := range routeTables {
		runs["setPosition "+kind] = func() error {
			_, err := tx.ExecContext(ctx, rt.setPosition, momo.Named("position", 1), momo.Named("id", int64(7)))
			return err
		}
	}
	for name, run := range runs {
		if err := run(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, st := range append(d.sent, d0.sent...) {
		if strings.Contains(st.sql, "@") {
			t.Errorf("statement reached PostgreSQL with an @name: %s", st.sql)
		}
	}
}

func notFound(err error) error {
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}
