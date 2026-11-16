package store

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/dialect"

	"github.com/azrtydxb/hello/internal/fax"
)

// TestFaxStatementsOffline builds the fax statements TestStatementsBuild
// cannot see: ListFaxes' WHERE, assembled from the filter, and the two in-use
// reads DeleteFaxDestination runs from a table. Each condition carries its own
// @name and the matching Named, so every filter combination builds.
func TestFaxStatementsOffline(t *testing.T) {
	ctx := context.Background()
	s := &Store{db: &dbPool{dbConn: dbConn{momo.NewOffline(dialect.Postgres)}}}
	for _, fl := range []fax.ListFilter{
		{},
		{Direction: "inbound"},
		{Status: "failed"},
		{Before: 10},
		{Owners: []string{}},
		{Direction: "outbound", Status: "queued", Before: 10, Owners: []string{"100"}, Limit: 5},
	} {
		if _, err := s.ListFaxes(ctx, fl); !errors.Is(err, momo.ErrUnsupported) {
			t.Errorf("ListFaxes(%+v) = %v, want ErrUnsupported", fl, err)
		}
	}
	for _, q := range []string{faxDestRoutes, faxDestMenus} {
		if _, err := s.db.QueryContext(ctx, q, momo.Named("name", "f")); !errors.Is(err, momo.ErrUnsupported) {
			t.Errorf("%q = %v, want ErrUnsupported", q, err)
		}
	}
}

// TestListFaxesSends pins what ListFaxes sends with every filter set: the
// conditions in their order and the values in theirs, as before the move to
// @name.
func TestListFaxesSends(t *testing.T) {
	d := &fakeDriver{}
	s := &Store{db: &dbPool{dbConn: dbConn{momo.New(d)}}}
	out, err := s.ListFaxes(context.Background(), fax.ListFilter{Direction: "outbound", Status: "queued", Before: 10, Owners: []string{"100"}, Limit: 600})
	if err != nil || out == nil || len(out) != 0 {
		t.Fatalf("ListFaxes = %v, %v; want an empty list", out, err)
	}
	want := sentStmt{
		`SELECT ` + faxCols + faxFrom + ` WHERE f.direction = $1 AND f.status = $2 AND f.id < $3 AND f.owner_extension = ANY($4::text[]) ORDER BY f.id DESC LIMIT $5`,
		[]any{"outbound", "queued", int64(10), []string{"100"}, 100},
	}
	if len(d.sent) != 1 || fmt.Sprintf("%#v", d.sent[0]) != fmt.Sprintf("%#v", want) {
		t.Errorf("sent %#v\nwant %#v", d.sent, want)
	}
}
