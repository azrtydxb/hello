package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/dialect"
)

// oneRowDriver answers every query with one row that scans as zero values and
// every exec with one row affected, recording what PostgreSQL would receive,
// so a store method runs its statements without a database. A statement
// that does not build (an @name with no Named argument, a Named no @name
// uses) fails in momo and never reaches it.
type oneRowDriver struct{ sent []string }

func (d *oneRowDriver) Dialect() *dialect.Dialect { return dialect.For(dialect.Postgres) }

func (d *oneRowDriver) Query(_ context.Context, sql string, _ []any) (momo.Rows, error) {
	d.sent = append(d.sent, sql)
	return &zeroRows{}, nil
}

func (d *oneRowDriver) Exec(_ context.Context, sql string, _ []any) (int64, error) {
	d.sent = append(d.sent, sql)
	return 1, nil
}

func (d *oneRowDriver) Begin(context.Context, momo.TxOptions) (momo.DriverTx, error) {
	return nil, errors.New("oneRowDriver: no transactions")
}

// sentWith reports whether a statement containing part was sent.
func (d *oneRowDriver) sentWith(part string) bool {
	for _, s := range d.sent {
		if strings.Contains(s, part) {
			return true
		}
	}
	return false
}

// noConnector is a database/sql connector that never connects.
type noConnector struct{}

func (noConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New("noConnector: no database")
}
func (noConnector) Driver() driver.Driver { return nil }

type zeroRows struct{ done bool }

func (r *zeroRows) Next() bool {
	if r.done {
		return false
	}
	r.done = true
	return true
}

func (r *zeroRows) Scan(...any) error          { return nil }
func (r *zeroRows) Err() error                 { return nil }
func (r *zeroRows) Close() error               { return nil }
func (r *zeroRows) Columns() ([]string, error) { return nil, nil }

// TestInboxRuntimeStatementsBuild runs the inbox's statements that are
// assembled at run time, which TestStatementsBuild cannot see, on a stub
// driver: every fragment's @name has its Named argument and no argument is
// left unused, for every filter and grouping.
func TestInboxRuntimeStatementsBuild(t *testing.T) {
	ctx := context.Background()
	newStore := func() (*Store, *oneRowDriver) {
		d := &oneRowDriver{}
		// The raw pool begins no transaction, so a method stops at its
		// first s.tx with the statements before it sent.
		return &Store{db: newPool(sql.OpenDB(noConnector{}), momo.New(d))}, d
	}
	notValidation := func(t *testing.T, what string, err error) {
		t.Helper()
		if errors.Is(err, momo.ErrValidation) {
			t.Errorf("%s: a statement does not build: %v", what, err)
		}
	}

	t.Run("ListConversations", func(t *testing.T) {
		after := ConvCursor{At: time.Now(), ID: 9}
		for _, f := range []ConvFilter{
			{},
			{View: "mine", Status: "any"},
			{View: "unassigned", Status: "open", ChannelID: 1, QueueID: 2, Q: "50%", After: &after},
		} {
			for _, all := range []bool{false, true} {
				s, d := newStore()
				_, _, err := s.ListConversations(ctx, InboxAccess{UserID: 1, All: all}, f, 10)
				notValidation(t, "ListConversations", err)
				if !d.sentWith("ORDER BY c.last_message_at DESC, c.id DESC LIMIT") {
					t.Errorf("ListConversations(%+v): the list statement was not sent: %v", f, d.sent)
				}
			}
		}
	})

	t.Run("GetInboxReport", func(t *testing.T) {
		for _, by := range []string{ReportByNone, ReportByChannel, ReportByQueue, ReportByAgent, ReportByDay} {
			s, d := newStore()
			_, err := s.GetInboxReport(ctx, InboxReportFilter{From: time.Now().Add(-time.Hour), To: time.Now(), GroupBy: by})
			notValidation(t, "GetInboxReport "+by, err)
			if err != nil {
				t.Errorf("GetInboxReport(%q) = %v", by, err)
			}
			if !d.sentWith("FROM conv GROUP BY k") || !d.sentWith("FROM conversation_messages m JOIN conversations c") {
				t.Errorf("GetInboxReport(%q): the report statements were not sent: %v", by, d.sent)
			}
		}
	})

	t.Run("labelReportRows", func(t *testing.T) {
		for _, by := range []string{ReportByChannel, ReportByQueue, ReportByAgent} {
			s, _ := newStore()
			rows := []InboxReportRow{{Key: "1"}}
			notValidation(t, "labelReportRows "+by, s.labelReportRows(ctx, by, rows))
		}
	})

	t.Run("ExportPeer", func(t *testing.T) {
		for _, notes := range []bool{false, true} {
			s, d := newStore()
			_, err := s.ExportPeer(ctx, "user:a", "+3212345678", notes)
			notValidation(t, "ExportPeer", err)
			if !d.sentWith("JOIN chat_visitors v ON v.id = mm.uploader_visitor_id") || !d.sentWith("FROM ai_message_runs r") {
				t.Errorf("ExportPeer(notes %v) stopped at %v before its media and AI statements: %v", notes, err, d.sent)
			}
		}
	})

	t.Run("queueRedactions", func(t *testing.T) {
		d := &oneRowDriver{}
		tx := &dbTx{dbConn: dbConn{momo.New(d)}}
		// A fragment binding by name, as this file's callers write it.
		err := queueRedactions(ctx, tx, "b", `SELECT channel_id, @provider::text, provider_message_id FROM conversation_messages WHERE id = @id`,
			momo.Named("id", 1), momo.Named("provider", "telnyx"))
		notValidation(t, "queueRedactions with @name", err)
		// ErasePeer's fragment (messaging.go), binding @peer_key.
		err = queueRedactions(ctx, tx, "b", `SELECT m.channel_id, ch.provider, m.provider_message_id FROM conversation_messages m
			JOIN conversations c ON c.id = m.conversation_id JOIN msg_channels ch ON ch.id = m.channel_id WHERE c.peer_key = @peer_key`,
			momo.Named("peer_key", "+32"))
		notValidation(t, "queueRedactions with @peer_key", err)
		if len(d.sent) != 2 {
			t.Errorf("queueRedactions sent %d statements, want 2", len(d.sent))
		}
	})

	t.Run("nameBooks", func(t *testing.T) {
		d := &oneRowDriver{}
		_, err := nameBooks(ctx, dbConn{momo.New(d)}, 1)
		notValidation(t, "nameBooks", err)
		if len(d.sent) != 4 {
			t.Errorf("nameBooks sent %d statements, want 4", len(d.sent))
		}
	})
}
