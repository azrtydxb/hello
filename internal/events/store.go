package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// insert stores e once; it reports whether the row is new (an event already
// stored, delivered twice by the stream, is not).
func insert(ctx context.Context, tx *sql.Tx, e Entry) (bool, error) {
	c, err := ParseCursor(e.Cursor)
	if err != nil {
		return false, err
	}
	b, err := json.Marshal(e.Event)
	if err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO integration_events (id, cursor_ms, cursor_seq, type, created_at, correlation_id, extension_number, payload)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb)
		ON CONFLICT (id) DO NOTHING`,
		e.ID, c.MS, c.Seq, e.Type, e.CreatedAt, e.Data.CallID, e.Data.Extension, string(b))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// Query selects stored events.
type Query struct {
	Filter
	// After returns events with a later cursor (ascending); Before returns
	// events with an earlier one. Either may be zero.
	After, Before Cursor
	// Desc returns the newest first.
	Desc  bool
	Limit int
}

// MaxLimit bounds one page.
const MaxLimit = 1000

// History returns stored events matching q, in cursor order (descending with
// q.Desc).
func (s *Service) History(ctx context.Context, q Query) ([]Entry, error) {
	if q.Limit <= 0 || q.Limit > MaxLimit {
		q.Limit = 100
	}
	var (
		where []string
		args  []any
	)
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if !q.After.Zero() {
		where = append(where, fmt.Sprintf("(cursor_ms, cursor_seq) > (%s, %s)", arg(q.After.MS), arg(q.After.Seq)))
	}
	if !q.Before.Zero() {
		where = append(where, fmt.Sprintf("(cursor_ms, cursor_seq) < (%s, %s)", arg(q.Before.MS), arg(q.Before.Seq)))
	}
	if q.Extension != "" {
		where = append(where, "extension_number = "+arg(q.Extension))
	}
	if q.Allowed != nil {
		where = append(where, "extension_number = ANY("+arg(pgArray(q.Allowed))+"::text[])")
	}
	if len(q.Types) > 0 {
		exact, prefixes := q.split()
		var ors []string
		if len(exact) > 0 {
			ors = append(ors, "type = ANY("+arg(pgArray(exact))+"::text[])")
		}
		if len(prefixes) > 0 {
			like := make([]string, len(prefixes))
			for i, p := range prefixes {
				like[i] = p + "%"
			}
			ors = append(ors, "type LIKE ANY("+arg(pgArray(like))+"::text[])")
		}
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}
	sqlText := "SELECT cursor_ms, cursor_seq, payload::text FROM integration_events"
	if len(where) > 0 {
		sqlText += " WHERE " + strings.Join(where, " AND ")
	}
	order := ""
	if q.Desc {
		order = " DESC"
	}
	//nolint:gosec // G202: only fixed fragments and $n placeholders are joined; every value is bound.
	sqlText += " ORDER BY cursor_ms" + order + ", cursor_seq" + order + " LIMIT " + arg(q.Limit)
	rows, err := s.DB.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Entry
	for rows.Next() {
		var (
			c       Cursor
			payload string
		)
		if err := rows.Scan(&c.MS, &c.Seq, &payload); err != nil {
			return nil, err
		}
		e := Entry{Cursor: c.String()}
		if err := json.Unmarshal([]byte(payload), &e.Event); err != nil {
			continue // a row this version cannot read is skipped, not fatal
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// pgArray is a PostgreSQL text[] literal.
func pgArray(v []string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, s := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}
