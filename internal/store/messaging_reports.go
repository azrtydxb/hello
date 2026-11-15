package store

// Inbox reports (spec messaging-channels phase 8): volumes, first response
// and resolution times, and service-level breaches, in total and by
// channel, queue, agent or day. They are counts and durations only, never
// content or numbers. They read the conversations still kept: a
// conversation past its retention is no longer counted.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Report groupings.
const (
	ReportByNone    = ""
	ReportByChannel = "channel"
	ReportByQueue   = "queue"
	ReportByAgent   = "agent"
	ReportByDay     = "day"
)

// InboxSLA are the service-level targets in seconds after a conversation
// started; 0 turns one off.
type InboxSLA struct {
	FirstResponseSeconds int `json:"firstResponseSeconds"`
	ResolutionSeconds    int `json:"resolutionSeconds"`
}

// Valid reports whether the targets are inside their ranges (a week and 30
// days).
func (t InboxSLA) Valid() bool {
	return t.FirstResponseSeconds >= 0 && t.FirstResponseSeconds <= 604800 && t.ResolutionSeconds >= 0 && t.ResolutionSeconds <= 2592000
}

// GetInboxSLA returns the service-level targets.
func (s *Store) GetInboxSLA(ctx context.Context) (InboxSLA, error) {
	var t InboxSLA
	err := s.db.QueryRowContext(ctx, `SELECT sla_first_response_seconds, sla_resolution_seconds FROM messaging_settings`).
		Scan(&t.FirstResponseSeconds, &t.ResolutionSeconds)
	return t, err
}

// PutInboxSLA replaces the service-level targets.
func (s *Store) PutInboxSLA(ctx context.Context, actor string, t InboxSLA) (InboxSLA, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE messaging_settings SET sla_first_response_seconds = $1, sla_resolution_seconds = $2`,
			t.FirstResponseSeconds, t.ResolutionSeconds); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "update", "messaging_sla", "1")
	})
	if err != nil {
		return InboxSLA{}, err
	}
	return s.GetInboxSLA(ctx)
}

// Durations summarises a set of durations in seconds.
type Durations struct {
	Count      int     `json:"count"`
	AvgSeconds float64 `json:"avgSeconds"`
	P50Seconds float64 `json:"p50Seconds"`
	P90Seconds float64 `json:"p90Seconds"`
}

// InboxReportRow is one group of the report (or the totals).
type InboxReportRow struct {
	// Key is the group's id (channel, queue or user id; the day as
	// YYYY-MM-DD); empty for the totals and for "no queue" or "nobody".
	Key   string `json:"key"`
	Label string `json:"label"`
	// Conversations started in the period, split by who started them.
	Conversations         int `json:"conversations"`
	InboundConversations  int `json:"inboundConversations"`
	OutboundConversations int `json:"outboundConversations"`
	Resolved              int `json:"resolved"`
	// MessagesIn and MessagesOut count the customer-facing messages of the
	// period (not notes or system lines).
	MessagesIn  int `json:"messagesIn"`
	MessagesOut int `json:"messagesOut"`
	// FirstResponse is how long customers waited for the first answer to a
	// conversation they started; Resolution how long a conversation took to
	// be resolved.
	FirstResponse Durations `json:"firstResponse"`
	Resolution    Durations `json:"resolution"`
	// The breaches count conversations past a target: answered or resolved
	// late, or still waiting beyond it.
	SLAFirstResponseBreaches int `json:"slaFirstResponseBreaches"`
	SLAResolutionBreaches    int `json:"slaResolutionBreaches"`
}

// InboxReport is the report of one period.
type InboxReport struct {
	From     time.Time        `json:"from"`
	To       time.Time        `json:"to"`
	GroupBy  string           `json:"groupBy"`
	TimeZone string           `json:"timeZone"`
	SLA      InboxSLA         `json:"sla"`
	Totals   InboxReportRow   `json:"totals"`
	Rows     []InboxReportRow `json:"rows"`
}

// InboxReportFilter selects a report: the period [From, To), the grouping
// and, for days, the zone they are counted in.
type InboxReportFilter struct {
	From, To time.Time
	GroupBy  string
	Location *time.Location
}

// convKey and msgKey are the SQL of a grouping's key over the conversation
// (c) and over a message of it (m).
func reportKeys(by string) (convKey, msgKey string, err error) {
	switch by {
	case ReportByNone:
		return `''`, `''`, nil
	case ReportByChannel:
		return `c.channel_id::text`, `m.channel_id::text`, nil
	case ReportByQueue:
		return `coalesce(c.queue_id::text, '')`, `coalesce(c.queue_id::text, '')`, nil
	case ReportByAgent:
		// A conversation counts for whoever has it; a reply for whoever
		// wrote it, a customer's text for whoever has the conversation.
		return `coalesce(c.assignee_user_id::text, '')`,
			`coalesce(CASE WHEN m.direction = 'out' THEN m.sender_user_id ELSE c.assignee_user_id END::text, '')`, nil
	case ReportByDay:
		return `to_char(c.created_at AT TIME ZONE $5, 'YYYY-MM-DD')`, `to_char(m.created_at AT TIME ZONE $5, 'YYYY-MM-DD')`, nil
	}
	return "", "", fmt.Errorf("store: unknown report grouping %q", by)
}

// GetInboxReport computes the report. Spam is left out.
func (s *Store) GetInboxReport(ctx context.Context, f InboxReportFilter) (InboxReport, error) {
	if f.Location == nil {
		f.Location = time.UTC
	}
	rep := InboxReport{From: f.From.UTC(), To: f.To.UTC(), GroupBy: f.GroupBy, TimeZone: f.Location.String(), Rows: []InboxReportRow{}}
	sla, err := s.GetInboxSLA(ctx)
	if err != nil {
		return rep, err
	}
	rep.SLA = sla
	totals, err := s.reportRows(ctx, f, ReportByNone, sla)
	if err != nil {
		return rep, err
	}
	if len(totals) > 0 {
		rep.Totals = totals[0]
	}
	rep.Totals.Key, rep.Totals.Label = "", "All"
	if f.GroupBy == ReportByNone {
		return rep, nil
	}
	rows, err := s.reportRows(ctx, f, f.GroupBy, sla)
	if err != nil {
		return rep, err
	}
	if err := s.labelReportRows(ctx, f.GroupBy, rows); err != nil {
		return rep, err
	}
	rep.Rows = rows
	return rep, nil
}

func (s *Store) reportRows(ctx context.Context, f InboxReportFilter, by string, sla InboxSLA) ([]InboxReportRow, error) {
	convKey, msgKey, err := reportKeys(by)
	if err != nil {
		return nil, err
	}
	tz := f.Location.String()
	byKey := map[string]*InboxReportRow{}
	row := func(k string) *InboxReportRow {
		r, ok := byKey[k]
		if !ok {
			r = &InboxReportRow{Key: k}
			byKey[k] = r
		}
		return r
	}
	// $5 (the zone) is only read by the day grouping; it is passed always so
	// the parameter list is the same.
	//nolint:gosec // G202: the keys are fixed SQL fragments chosen above; every value is bound.
	rows, err := s.db.QueryContext(ctx, `
		WITH conv AS (
			SELECT `+convKey+` AS k, c.origin, c.status, c.created_at,
			       CASE WHEN c.origin = 'inbound' AND c.first_response_at IS NOT NULL
			            THEN extract(epoch FROM c.first_response_at - c.created_at) END AS frt,
			       CASE WHEN c.status = 'resolved' AND c.resolved_at IS NOT NULL
			            THEN extract(epoch FROM c.resolved_at - c.created_at) END AS rt
			FROM conversations c
			WHERE c.created_at >= $1 AND c.created_at < $2 AND c.status <> 'spam' AND $5::text IS NOT NULL
		)
		SELECT k, count(*), count(*) FILTER (WHERE origin = 'inbound'), count(*) FILTER (WHERE origin = 'outbound'),
		       count(*) FILTER (WHERE status = 'resolved'),
		       count(frt), coalesce(avg(frt), 0), coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY frt), 0),
		       coalesce(percentile_cont(0.9) WITHIN GROUP (ORDER BY frt), 0),
		       count(rt), coalesce(avg(rt), 0), coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY rt), 0),
		       coalesce(percentile_cont(0.9) WITHIN GROUP (ORDER BY rt), 0),
		       count(*) FILTER (WHERE origin = 'inbound' AND $3::int > 0
		           AND (frt > $3::int OR (frt IS NULL AND status <> 'resolved' AND now() - created_at > make_interval(secs => $3::int)))),
		       count(*) FILTER (WHERE $4::int > 0
		           AND (rt > $4::int OR (rt IS NULL AND status <> 'resolved' AND now() - created_at > make_interval(secs => $4::int))))
		FROM conv GROUP BY k`, f.From, f.To, sla.FirstResponseSeconds, sla.ResolutionSeconds, tz)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			k string
			r InboxReportRow
		)
		if err := rows.Scan(&k, &r.Conversations, &r.InboundConversations, &r.OutboundConversations, &r.Resolved,
			&r.FirstResponse.Count, &r.FirstResponse.AvgSeconds, &r.FirstResponse.P50Seconds, &r.FirstResponse.P90Seconds,
			&r.Resolution.Count, &r.Resolution.AvgSeconds, &r.Resolution.P50Seconds, &r.Resolution.P90Seconds,
			&r.SLAFirstResponseBreaches, &r.SLAResolutionBreaches); err != nil {
			_ = rows.Close()
			return nil, err
		}
		r.Key = k
		*row(k) = r
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	//nolint:gosec // G202: as above.
	rows, err = s.db.QueryContext(ctx, `
		SELECT `+strings.ReplaceAll(msgKey, "$5", "$3")+` AS k, count(*) FILTER (WHERE m.direction = 'in'), count(*) FILTER (WHERE m.direction = 'out')
		FROM conversation_messages m JOIN conversations c ON c.id = m.conversation_id
		WHERE m.kind = 'message' AND m.created_at >= $1 AND m.created_at < $2 AND c.status <> 'spam' AND $3::text IS NOT NULL
		GROUP BY 1`, f.From, f.To, tz)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			k       string
			in, out int
		)
		if err := rows.Scan(&k, &in, &out); err != nil {
			_ = rows.Close()
			return nil, err
		}
		r := row(k)
		r.MessagesIn, r.MessagesOut = in, out
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]InboxReportRow, 0, len(byKey))
	for _, r := range byKey {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if by == ReportByDay {
			return out[i].Key < out[j].Key
		}
		if out[i].Conversations != out[j].Conversations {
			return out[i].Conversations > out[j].Conversations
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

// labelReportRows names each group: the channel's, queue's or agent's name,
// the day itself.
func (s *Store) labelReportRows(ctx context.Context, by string, rows []InboxReportRow) error {
	var q, none string
	switch by {
	case ReportByChannel:
		q, none = `SELECT name FROM msg_channels WHERE id = $1`, "Deleted channel"
	case ReportByQueue:
		q, none = `SELECT name FROM queues WHERE id = $1`, "No queue"
	case ReportByAgent:
		q, none = `SELECT coalesce(NULLIF(display_name, ''), username) FROM users WHERE id = $1`, "Unassigned"
	default:
		for i := range rows {
			rows[i].Label = rows[i].Key
		}
		return nil
	}
	for i := range rows {
		id, err := strconv.ParseInt(rows[i].Key, 10, 64)
		if err != nil {
			rows[i].Label = none
			continue
		}
		var name string
		switch err := s.db.QueryRowContext(ctx, q, id).Scan(&name); {
		case errors.Is(err, sql.ErrNoRows):
			rows[i].Label = "#" + rows[i].Key + " (deleted)"
		case err != nil:
			return err
		default:
			rows[i].Label = name
		}
	}
	return nil
}
