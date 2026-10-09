package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/routing"
)

// CDR is one call detail record written by hello-sip.
type CDR struct {
	ID                   int64      `json:"id"`
	CorrelationID        string     `json:"correlationId"`
	SIPCallID            string     `json:"sipCallId"`
	Source               string     `json:"source"`
	Destination          string     `json:"destination"`
	StartTime            time.Time  `json:"startTime"`
	RingTime             *time.Time `json:"ringTime,omitempty"`
	AnswerTime           *time.Time `json:"answerTime,omitempty"`
	EndTime              time.Time  `json:"endTime"`
	DurationMs           int64      `json:"durationMs"`
	BillableMs           int64      `json:"billableMs"`
	SIPNode              string     `json:"sipNode"`
	MediaMode            string     `json:"mediaMode"`
	FinalStatus          int        `json:"finalStatus"`
	TerminationSide      string     `json:"terminationSide"`
	FailureReason        string     `json:"failureReason"`
	Direction            string     `json:"direction"`
	OriginalDestination  string     `json:"originalDestination"`
	RewrittenDestination string     `json:"rewrittenDestination"`
	Route                string     `json:"route"`
	Trunk                string     `json:"trunk"`
	RTPPackets           *int64     `json:"rtpPackets"`
	RTPLost              *int64     `json:"rtpLost"`
	RTPJitterMs          *float64   `json:"rtpJitterMs"`
	// VoiceAgent is the name of the agent the call was routed to (spec
	// voice-agents S-23), empty for every other call.
	VoiceAgentName string `json:"voiceAgent"`
}

const cdrCols = `id, correlation_id, sip_call_id, source, destination, start_time, ring_time, answer_time,
	end_time, duration_ms, billable_ms, sip_node, media_mode, final_status, termination_side, failure_reason,
	direction, original_destination, rewritten_destination, route_name, trunk_name,
	rtp_packets, rtp_lost, rtp_jitter_ms, voice_agent_name`

func scanCDR(r interface{ Scan(...any) error }, extra ...any) (CDR, error) {
	var c CDR
	var ring, answer sql.NullTime
	dest := append([]any{&c.ID, &c.CorrelationID, &c.SIPCallID, &c.Source, &c.Destination, &c.StartTime, &ring, &answer,
		&c.EndTime, &c.DurationMs, &c.BillableMs, &c.SIPNode, &c.MediaMode, &c.FinalStatus, &c.TerminationSide, &c.FailureReason,
		&c.Direction, &c.OriginalDestination, &c.RewrittenDestination, &c.Route, &c.Trunk, &c.RTPPackets, &c.RTPLost, &c.RTPJitterMs,
		&c.VoiceAgentName}, extra...)
	if err := r.Scan(dest...); err != nil {
		return c, err
	}
	c.RingTime, c.AnswerTime = nullTime(ring), nullTime(answer)
	return c, nil
}

// CDRFilter narrows a CDR listing; the zero value matches every call.
type CDRFilter struct {
	// Direction keeps one direction ("internal", "inbound", "outbound");
	// empty keeps all.
	Direction string
	// Failed keeps only calls whose final status is outside 2xx, the same
	// rule the CDR detail's explanation uses.
	Failed bool
	// VoiceAgent keeps only calls routed to that voice agent (spec
	// voice-agents S-23, the listCDRs filter); empty keeps all.
	VoiceAgent string
}

// ListCDRs returns up to limit CDRs matching f with id below before (0
// means from the newest), newest first, and the cursor for the next page
// ("" at the end).
func (s *Store) ListCDRs(ctx context.Context, f CDRFilter, before int64, limit int) ([]CDR, string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cdrCols+`
		FROM cdrs WHERE ($1 = 0 OR id < $1) AND ($3::text = '' OR direction = $3::text)
			AND (NOT $4::boolean OR final_status NOT BETWEEN 200 AND 299)
			AND ($5::text = '' OR voice_agent_name = $5::text)
		ORDER BY id DESC LIMIT $2`, before, limit+1, f.Direction, f.Failed, f.VoiceAgent)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()
	out := []CDR{}
	for rows.Next() {
		c, err := scanCDR(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = strconv.FormatInt(out[limit-1].ID, 10)
	}
	return out, next, nil
}

// CDRCounts is how many call records exist, and how many of them failed.
type CDRCounts struct {
	All    int64 `json:"all"`
	Failed int64 `json:"failed"`
}

// CountCDRs counts every CDR and the failed ones (final status outside 2xx).
func (s *Store) CountCDRs(ctx context.Context) (CDRCounts, error) {
	var c CDRCounts
	err := s.db.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE final_status NOT BETWEEN 200 AND 299)
		FROM cdrs`).Scan(&c.All, &c.Failed)
	return c, err
}

// ConcurrencyPoint is how many recorded calls were in progress at one
// instant, by direction.
type ConcurrencyPoint struct {
	At       time.Time `json:"at"`
	Inbound  int64     `json:"inbound"`
	Outbound int64     `json:"outbound"`
	Internal int64     `json:"internal"`
}

// CDRConcurrency samples, every step from from to to (inclusive), how many
// CDRs were in progress (start <= t < end), by direction. Calls still in
// progress have no CDR yet and are not counted.
func (s *Store) CDRConcurrency(ctx context.Context, from, to time.Time, step time.Duration) ([]ConcurrencyPoint, error) {
	rows, err := s.db.QueryContext(ctx, `WITH c AS (
			SELECT start_time, end_time, direction FROM cdrs WHERE end_time > $1 AND start_time <= $2
		)
		SELECT g.t,
			count(c.direction) FILTER (WHERE c.direction = 'inbound'),
			count(c.direction) FILTER (WHERE c.direction = 'outbound'),
			count(c.direction) FILTER (WHERE c.direction = 'internal')
		FROM generate_series($1::timestamptz, $2::timestamptz, $3::bigint * interval '1 millisecond') AS g(t)
		LEFT JOIN c ON c.start_time <= g.t AND c.end_time > g.t
		GROUP BY g.t ORDER BY g.t`, from, to, step.Milliseconds())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []ConcurrencyPoint{}
	for rows.Next() {
		var p ConcurrencyPoint
		if err := rows.Scan(&p.At, &p.Inbound, &p.Outbound, &p.Internal); err != nil {
			return nil, err
		}
		p.At = p.At.UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetCDR returns one CDR and its routing trace.
func (s *Store) GetCDR(ctx context.Context, id int64) (CDR, routing.Trace, error) {
	var raw []byte
	c, err := scanCDR(s.db.QueryRowContext(ctx, `SELECT `+cdrCols+`, trace FROM cdrs WHERE id = $1`, id), &raw)
	if err != nil {
		return c, nil, mapErr(err)
	}
	trace := routing.Trace{}
	if err := json.Unmarshal(raw, &trace); err != nil {
		return c, nil, fmt.Errorf("store: decode trace of cdr %d: %w", id, err)
	}
	return c, trace, nil
}
