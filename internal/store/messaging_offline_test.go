package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/dialect"
)

// seqDriver is fakeDriver with one answer per query, in order: the i-th
// query gets answers[i] (no rows past the end). It records every statement
// as PostgreSQL receives it.
type seqDriver struct {
	fakeDriver
	answers [][][]any
	queries int
}

func (d *seqDriver) Query(_ context.Context, sql string, args []any) (momo.Rows, error) {
	d.sent = append(d.sent, sentStmt{sql, args})
	var rows [][]any
	if d.queries < len(d.answers) {
		rows = d.answers[d.queries]
	}
	d.queries++
	return &fakeRows{rows: rows, i: -1}, nil
}

// TestMessagingStatementsOffline builds the messaging statements
// TestStatementsBuild cannot see, because they are assembled at run time or
// picked from a switch: the listings' WHERE for every filter, routeName's
// reads, the reads that go through queryStrings and ConversationCalls' CDR read. Each fragment carries its own @name
// and its Named goes in only with it, so every combination builds.
func TestMessagingStatementsOffline(t *testing.T) {
	ctx := context.Background()
	s := &Store{db: &dbPool{dbConn: dbConn{momo.NewOffline(dialect.Postgres)}}}
	for mask := range 1 << 6 {
		f, before := messageFilterOf(mask)
		if _, err := s.ListMessages(ctx, f, before, 50); !errors.Is(err, momo.ErrUnsupported) {
			t.Errorf("ListMessages(%+v, %d) = %v, want ErrUnsupported", f, before, err)
		}
		if _, err := s.ListMessageConversations(ctx, f, 50); !errors.Is(err, momo.ErrUnsupported) {
			t.Errorf("ListMessageConversations(%+v) = %v, want ErrUnsupported", f, err)
		}
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{routeNameQueue, []any{momo.Named("id", int64(1))}},
		{routeNameUser, []any{momo.Named("id", int64(1))}},
		{routeNameExtension, []any{momo.Named("id", int64(1))}},
		{routeNameRingGroup, []any{momo.Named("id", int64(1))}},
		{ringGroupRecipients, []any{momo.Named("group_id", int64(1))}},
		{pagingGroupRecipients, []any{momo.Named("group_id", int64(1))}},
		{broadcastRecipients, nil},
		{erasePeerProviders, []any{momo.Named("peer_key", "+32475123456")}},
	} {
		if _, err := s.db.QueryContext(ctx, q.sql, q.args...); !errors.Is(err, momo.ErrUnsupported) {
			t.Errorf("%q = %v, want ErrUnsupported", q.sql, err)
		}
	}
}

// messageFilterOf is the filter whose fields are set where mask has a bit:
// Extension, Extensions, Conversation, Status, Query and the before cursor.
func messageFilterOf(mask int) (MessageFilter, int64) {
	var f MessageFilter
	var before int64
	if mask&1 != 0 {
		f.Extension = "101"
	}
	if mask&2 != 0 {
		f.Extensions = []string{"102", "103"}
	}
	if mask&4 != 0 {
		f.Conversation = "101>102"
	}
	if mask&8 != 0 {
		f.Status = "failed"
	}
	if mask&16 != 0 {
		f.Query = "Hi"
	}
	if mask&32 != 0 {
		before = 900
	}
	return f, before
}

// The listings' statements as PostgreSQL receives them, around the WHERE.
const (
	wantListHead = "\n\t\tSELECT id, batch, sender, sender_kind, sender_name, actor, recipient, recipient_kind, scope, scope_ref, trunk, content_type,\n\t\t       body_text, body_len, status, detail, attempts, created_at, expires_at, delivered_at, n, nd, np, nf\n\t\tFROM (\n\t\t\tSELECT DISTINCT ON (m.batch) m.id, m.batch, m.sender, m.sender_kind, m.sender_name, m.actor, m.recipient,\n\t\t\t       m.recipient_kind, m.scope, m.scope_ref, COALESCE(t.name, '') AS trunk, m.content_type, m.body_text,\n\t\t\t       COALESCE(length(m.body_raw), octet_length(m.body_text)) AS body_len, m.status, m.detail, m.attempts,\n\t\t\t       m.created_at, m.expires_at, m.delivered_at,\n\t\t\t       count(*) OVER w AS n,\n\t\t\t       count(*) FILTER (WHERE m.status = 'delivered') OVER w AS nd,\n\t\t\t       count(*) FILTER (WHERE m.status IN ('pending', 'sending')) OVER w AS np,\n\t\t\t       count(*) FILTER (WHERE m.status = 'failed') OVER w AS nf\n\t\t\tFROM messages m LEFT JOIN trunks t ON t.id = m.trunk_id"
	wantListTail = "\n\t\t\tWINDOW w AS (PARTITION BY m.batch)\n\t\t\tORDER BY m.batch, m.id) q\n\t\tORDER BY id DESC LIMIT $%d"
	wantConvHead = "\n\t\tWITH keyed AS (\n\t\t\tSELECT m.id, m.batch, m.scope, m.scope_ref, ("
	wantConvMid  = ") AS k FROM messages m"
	wantConvTail = ")\n\t\tSELECT c.k, c.scope, c.scope_ref, c.n, c.last_id, m.created_at, m.sender, m.recipient, m.status,\n\t\t       left(coalesce(m.body_text, ''), 120)\n\t\tFROM (SELECT k, (array_agg(scope))[1] AS scope, (array_agg(scope_ref))[1] AS scope_ref,\n\t\t             count(DISTINCT batch) AS n, max(id) AS last_id FROM keyed GROUP BY k) c\n\t\tJOIN messages m ON m.id = c.last_id\n\t\tORDER BY c.last_id DESC LIMIT $%d"
	wantConvKey  = "CASE m.scope\n\tWHEN 'direct' THEN least(m.sender, m.recipient) || '>' || greatest(m.sender, m.recipient)\n\tWHEN 'broadcast' THEN 'broadcast'\n\tELSE m.scope || ':' || m.scope_ref END"
)

// wantMessageWhere is the WHERE PostgreSQL receives for f and the cursor,
// with its values in bind order; each use of a value is its own bind,
// numbered from 1.
func wantMessageWhere(f MessageFilter, before int64) (string, []any) {
	var (
		conds []string
		args  []any
	)
	add := func(cond string, v any) {
		n := strings.Count(cond, "$%d")
		nums := make([]any, n)
		for i := range nums {
			args = append(args, v)
			nums[i] = len(args)
		}
		conds = append(conds, fmt.Sprintf(cond, nums...))
	}
	if f.Extension != "" {
		add("(m.sender = $%d OR m.recipient = $%d)", f.Extension)
	}
	if len(f.Extensions) > 0 {
		add("(m.sender = ANY($%d) OR m.recipient = ANY($%d))", f.Extensions)
	}
	if f.Conversation != "" {
		add("("+wantConvKey+") = $%d", f.Conversation)
	}
	if f.Status != "" {
		add("m.status = $%d", f.Status)
	}
	if f.Query != "" {
		add("(position(lower($%d::text) in lower(coalesce(m.body_text, ''))) > 0 OR position(lower($%d::text) in lower(m.sender)) > 0\n\t\t\tOR position(lower($%d::text) in lower(m.recipient)) > 0 OR position(lower($%d::text) in lower(m.scope_ref)) > 0)", f.Query)
	}
	if before > 0 {
		add("m.id < $%d", before)
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// TestMessageListingsSend pins what ListMessages and ListMessageConversations
// send for every combination of the filter's fields and the cursor: the
// conditions in their order, the binds numbered in the order they appear and
// the values in theirs. ListMessageConversations ignores the conversation
// filter and takes no cursor.
func TestMessageListingsSend(t *testing.T) {
	ctx := context.Background()
	for mask := range 1 << 6 {
		f, before := messageFilterOf(mask)
		t.Run(fmt.Sprintf("%06b", mask), func(t *testing.T) {
			d := &fakeDriver{}
			s := &Store{db: &dbPool{dbConn: dbConn{momo.New(d)}}}
			out, err := s.ListMessages(ctx, f, before, 50)
			if err != nil || out == nil || len(out) != 0 {
				t.Fatalf("ListMessages = %v, %v; want an empty list", out, err)
			}
			where, args := wantMessageWhere(f, before)
			want := sentStmt{wantListHead + where + fmt.Sprintf(wantListTail, len(args)+1), append(args, 50)}
			if len(d.sent) != 1 || fmt.Sprintf("%#v", d.sent[0]) != fmt.Sprintf("%#v", want) {
				t.Errorf("ListMessages sent %#v\nwant %#v", d.sent, want)
			}

			d.sent = nil
			convs, err := s.ListMessageConversations(ctx, f, 20)
			if err != nil || convs == nil || len(convs) != 0 {
				t.Fatalf("ListMessageConversations = %v, %v; want an empty list", convs, err)
			}
			cf := f
			cf.Conversation = ""
			where, args = wantMessageWhere(cf, 0)
			want = sentStmt{wantConvHead + wantConvKey + wantConvMid + where + fmt.Sprintf(wantConvTail, len(args)+1), append(args, 20)}
			if len(d.sent) != 1 || fmt.Sprintf("%#v", d.sent[0]) != fmt.Sprintf("%#v", want) {
				t.Errorf("ListMessageConversations sent %#v\nwant %#v", d.sent, want)
			}
		})
	}
}

// wantCDRDigits is the digits of CDR column col, as PostgreSQL receives it.
func wantCDRDigits(col string) string {
	return "regexp_replace(split_part(regexp_replace(" + col + ", '^(sips?|tel):', '', 'i'), '@', 1), '[^0-9]', '', 'g')"
}

// TestConversationCallsSends pins ConversationCalls' CDR read: a long number
// matches by its last nine digits, a short one whole, each column with its
// own bind of the digits, and an agent's read adds the extension condition.
func TestConversationCallsSends(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, peer, digits string
		long, all          bool
	}{
		{"long, supervisor", "+32475123456", "475123456", true, true},
		{"long, agent", "+32475123456", "475123456", true, false},
		{"short, supervisor", "+3212345", "3212345", false, true},
		{"short, agent", "+3212345", "3212345", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &seqDriver{answers: [][][]any{{{tc.peer, "phone"}}}}
			s := &Store{db: &dbPool{dbConn: dbConn{momo.New(d)}}}
			out, err := s.ConversationCalls(ctx, InboxAccess{UserID: 7, All: tc.all}, 3, 10)
			if err != nil || out == nil || len(out) != 0 {
				t.Fatalf("ConversationCalls = %v, %v; want an empty list", out, err)
			}
			// The visibility read binds @id and visibleTo's @all and
			// @user_id, one bind per use in order.
			if len(d.sent) > 0 {
				wantVis := []any{int64(3), tc.all, int64(7), int64(7), int64(7), int64(7)}
				if v := d.sent[0]; strings.Contains(v.sql, "@") || !strings.Contains(v.sql, "c.id = $1 AND ($2::boolean") ||
					fmt.Sprintf("%#v", v.args) != fmt.Sprintf("%#v", wantVis) {
					t.Errorf("visibility read sent %#v\nwant args %#v", v, wantVis)
				}
			}
			var (
				conds []string
				args  []any
			)
			for _, col := range []string{"c.source", "c.destination", "c.original_destination", "c.rewritten_destination"} {
				args = append(args, tc.digits)
				n := len(args)
				if tc.long {
					conds = append(conds, fmt.Sprintf("(length(%s) >= 9 AND right(%s, 9) = $%d::text)", wantCDRDigits(col), wantCDRDigits(col), n))
				} else {
					conds = append(conds, fmt.Sprintf("%s = $%d::text", wantCDRDigits(col), n))
				}
			}
			where := "(" + strings.Join(conds, " OR ") + ")"
			if !tc.all {
				args = append(args, int64(7))
				where += fmt.Sprintf(" AND EXISTS (\n\t\t\tSELECT 1 FROM user_extensions ue JOIN extensions e ON e.id = ue.extension_id\n\t\t\tWHERE ue.user_id = $%d AND (c.source = e.number OR c.destination = e.number OR c.original_destination = e.number))", len(args))
			}
			want := sentStmt{cdrQuery + "FROM cdrs c WHERE " + where + fmt.Sprintf(" ORDER BY c.id DESC LIMIT $%d", len(args)+1), append(args, 10)}
			if len(d.sent) != 2 || fmt.Sprintf("%#v", d.sent[1]) != fmt.Sprintf("%#v", want) {
				t.Errorf("sent %#v\nwant %#v", d.sent, want)
			}
		})
	}
}
