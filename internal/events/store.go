package events

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/azrtydxb/momo"
)

// insert stores e once; it reports whether the row is new (an event already
// stored, delivered twice by the stream, is not).
func insert(ctx context.Context, q momo.Querier, e Entry) (bool, error) {
	c, err := ParseCursor(e.Cursor)
	if err != nil {
		return false, err
	}
	b, err := json.Marshal(e.Event)
	if err != nil {
		return false, err
	}
	n, err := momo.Exec(ctx, q, `
		INSERT INTO integration_events (id, cursor_ms, cursor_seq, type, created_at, correlation_id, extension_number, payload)
		VALUES (@id, @cursor_ms, @cursor_seq, @type, @created_at, @correlation_id, @extension_number, @payload::jsonb)
		ON CONFLICT (id) DO NOTHING`,
		momo.Named("id", e.ID), momo.Named("cursor_ms", c.MS), momo.Named("cursor_seq", c.Seq), momo.Named("type", e.Type),
		momo.Named("created_at", e.CreatedAt), momo.Named("correlation_id", e.Data.CallID), momo.Named("extension_number", e.Data.Extension),
		momo.Named("payload", string(b)))
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
	arg := func(name string, v any) string {
		args = append(args, momo.Named(name, v))
		return "@" + name
	}
	if !q.After.Zero() {
		where = append(where, "(cursor_ms, cursor_seq) > ("+arg("after_ms", q.After.MS)+", "+arg("after_seq", q.After.Seq)+")")
	}
	if !q.Before.Zero() {
		where = append(where, "(cursor_ms, cursor_seq) < ("+arg("before_ms", q.Before.MS)+", "+arg("before_seq", q.Before.Seq)+")")
	}
	if q.Extension != "" {
		where = append(where, "extension_number = "+arg("extension", q.Extension))
	}
	if q.Allowed != nil {
		where = append(where, "extension_number = ANY("+arg("allowed", pgArray(q.Allowed))+"::text[])")
	}
	if len(q.Hide) > 0 {
		like := make([]string, len(q.Hide))
		for i, p := range q.Hide {
			like[i] = p + "%"
		}
		where = append(where, "NOT (type LIKE ANY("+arg("hide", pgArray(like))+"::text[]))")
	}
	if len(q.Types) > 0 {
		exact, prefixes := q.split()
		var ors []string
		if len(exact) > 0 {
			ors = append(ors, "type = ANY("+arg("types", pgArray(exact))+"::text[])")
		}
		if len(prefixes) > 0 {
			like := make([]string, len(prefixes))
			for i, p := range prefixes {
				like[i] = p + "%"
			}
			ors = append(ors, "type LIKE ANY("+arg("type_prefixes", pgArray(like))+"::text[])")
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
	sqlText += " ORDER BY cursor_ms" + order + ", cursor_seq" + order + " LIMIT " + arg("limit", q.Limit)
	rows, err := momo.Query[momo.Row3[int64, int64, string]](ctx, s.DB, sqlText, args...)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, r := range rows {
		e := Entry{Cursor: Cursor{MS: r.C1, Seq: r.C2}.String()}
		if err := json.Unmarshal([]byte(r.C3), &e.Event); err != nil {
			continue // a row this version cannot read is skipped, not fatal
		}
		out = append(out, e)
	}
	return out, nil
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
