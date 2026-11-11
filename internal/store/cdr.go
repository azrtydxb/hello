package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	// CallerCodec and CalleeCodec are the audio codec each leg ended on, as
	// "name/clock rate" (migration 00055); null when none was negotiated.
	CallerCodec *string `json:"callerCodec"`
	CalleeCodec *string `json:"calleeCodec"`
	// Kind is call, page or intercom (migration 00013).
	Kind string `json:"kind"`
	// AnsweredBy is the assistant's extension when a boss's call was answered
	// by the assistant (migration 00065); empty otherwise.
	AnsweredBy string `json:"answeredBy"`
	// Origin is phone (dialled at a phone) or api (click-to-call), and
	// OriginClient the OAuth client or token id behind an api call (migration
	// 00057).
	Origin       string `json:"origin"`
	OriginClient string `json:"originClient"`
	// RingTag is the ring tag the call rang with and RingRule what chose it
	// (migration 00050); empty when no internal device rang.
	RingTag  string `json:"ringTag"`
	RingRule string `json:"ringRule"`
	// SRTP is which legs carried SRTP: none, caller, callee or both
	// (migration 00081).
	SRTP string `json:"srtp"`
	// RecordingID is the call's recording; set by GetCDR when it has one.
	RecordingID *int64 `json:"recordingId,omitempty"`
	// PageLegs is a page's outcome per member (migration 00014); set by
	// GetCDR for a page only.
	PageLegs []PageLeg `json:"pageLegs,omitempty"`
	// QueueLegs is what the call did in a call queue (migration 00040); set
	// by GetCDR.
	QueueLegs []QueueLeg `json:"queueLegs,omitempty"`
	// FollowMeLegs is what each follow-me target did (migration 00079); set
	// by GetCDR.
	FollowMeLegs []FollowMeLeg `json:"followMeLegs,omitempty"`
	// RTCPXR is what the phones on the call reported (migration 00066); set
	// by GetCDR, empty when no phone sent a report.
	RTCPXR []RTCPXRQuality `json:"rtcpxr,omitempty"`
}

// FollowMeLeg is one follow-me leg of a call: the target's kind (own,
// extension, ring_group or external), the extension it rang or the trunk it
// left on, and how it ended (answered, declined, busy, no_answer, failed,
// skipped or cancelled).
type FollowMeLeg struct {
	Kind      string `json:"kind"`
	Extension string `json:"extension"`
	Trunk     string `json:"trunk"`
	Result    string `json:"result"`
}

// QueueLeg is one event of a call in a queue: enter, agent, answered, wait,
// abandon or exit.
type QueueLeg struct {
	Event     string    `json:"event"`
	Queue     string    `json:"queue"`
	Extension string    `json:"extension"`
	WaitMs    int64     `json:"waitMs"`
	Result    string    `json:"result"`
	At        time.Time `json:"at"`
}

// PageLeg is one paged member's outcome: answered, busy, dnd, no_answer or
// no_registration. Device is empty for a member that was never rung.
type PageLeg struct {
	Extension string `json:"extension"`
	Device    string `json:"device"`
	Result    string `json:"result"`
}

const cdrCols = `id, correlation_id, sip_call_id, source, destination, start_time, ring_time, answer_time,
	end_time, duration_ms, billable_ms, sip_node, media_mode, final_status, termination_side, failure_reason,
	direction, original_destination, rewritten_destination, route_name, trunk_name,
	rtp_packets, rtp_lost, rtp_jitter_ms, kind, ring_tag, ring_rule, caller_codec, callee_codec, origin, origin_client, answered_by, srtp`

func scanCDR(r interface{ Scan(...any) error }, extra ...any) (CDR, error) {
	var c CDR
	var ring, answer sql.NullTime
	dest := append([]any{&c.ID, &c.CorrelationID, &c.SIPCallID, &c.Source, &c.Destination, &c.StartTime, &ring, &answer,
		&c.EndTime, &c.DurationMs, &c.BillableMs, &c.SIPNode, &c.MediaMode, &c.FinalStatus, &c.TerminationSide, &c.FailureReason,
		&c.Direction, &c.OriginalDestination, &c.RewrittenDestination, &c.Route, &c.Trunk, &c.RTPPackets, &c.RTPLost, &c.RTPJitterMs, &c.Kind, &c.RingTag, &c.RingRule, &c.CallerCodec, &c.CalleeCodec, &c.Origin, &c.OriginClient, &c.AnsweredBy, &c.SRTP,
	}, extra...)
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
	// Disposition keeps one disposition: "blocked" is the calls a trunk's
	// screening turned away (blocklist, anonymous reject, country
	// restriction; pbx-gaps S-7, S-8). Empty keeps all.
	Disposition string
}

// blockedReasonPrefix starts the failure reason of a screened call.
const blockedReasonPrefix = "blocked: "

// ListCDRs returns up to limit CDRs matching f with id below before (0
// means from the newest), newest first, and the cursor for the next page
// ("" at the end).
func (s *Store) ListCDRs(ctx context.Context, f CDRFilter, before int64, limit int) ([]CDR, string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cdrCols+`
		FROM cdrs WHERE ($1 = 0 OR id < $1) AND ($3::text = '' OR direction = $3::text)
			AND (NOT $4::boolean OR final_status NOT BETWEEN 200 AND 299)
			AND ($5::text = '' OR ($5::text = 'blocked' AND left(failure_reason, length($6::text)) = $6::text))
		ORDER BY id DESC LIMIT $2`, before, limit+1, f.Direction, f.Failed, f.Disposition, blockedReasonPrefix)
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
	var rec int64
	switch err := s.db.QueryRowContext(ctx, `SELECT id FROM recordings WHERE correlation_id = $1`, c.CorrelationID).Scan(&rec); {
	case err == nil:
		c.RecordingID = &rec
	case !errors.Is(err, sql.ErrNoRows):
		return c, trace, fmt.Errorf("store: recording of cdr %d: %w", id, err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT event, queue, extension, wait_ms, result, at
		FROM cdr_queue_legs WHERE cdr_id = $1 ORDER BY position`, id)
	if err != nil {
		return c, trace, fmt.Errorf("store: queue legs of cdr %d: %w", id, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var l QueueLeg
		if err := rows.Scan(&l.Event, &l.Queue, &l.Extension, &l.WaitMs, &l.Result, &l.At); err != nil {
			return c, trace, err
		}
		c.QueueLegs = append(c.QueueLegs, l)
	}
	if err := rows.Err(); err != nil {
		return c, trace, err
	}
	c.RTCPXR, err = s.rtcpxrOf(ctx, id)
	if err != nil {
		return c, trace, err
	}
	frows, err := s.db.QueryContext(ctx, `SELECT target_kind, extension, trunk, result
		FROM cdr_legs WHERE cdr_id = $1 AND role = 'follow_me' ORDER BY position`, id)
	if err != nil {
		return c, trace, fmt.Errorf("store: follow-me legs of cdr %d: %w", id, err)
	}
	defer func() { _ = frows.Close() }()
	for frows.Next() {
		var l FollowMeLeg
		if err := frows.Scan(&l.Kind, &l.Extension, &l.Trunk, &l.Result); err != nil {
			return c, trace, err
		}
		c.FollowMeLegs = append(c.FollowMeLegs, l)
	}
	return c, trace, frows.Err()
}
