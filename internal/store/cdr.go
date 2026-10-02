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
}

const cdrCols = `id, correlation_id, sip_call_id, source, destination, start_time, ring_time, answer_time,
	end_time, duration_ms, billable_ms, sip_node, media_mode, final_status, termination_side, failure_reason,
	direction, original_destination, rewritten_destination, route_name, trunk_name`

func scanCDR(r interface{ Scan(...any) error }, extra ...any) (CDR, error) {
	var c CDR
	var ring, answer sql.NullTime
	dest := append([]any{&c.ID, &c.CorrelationID, &c.SIPCallID, &c.Source, &c.Destination, &c.StartTime, &ring, &answer,
		&c.EndTime, &c.DurationMs, &c.BillableMs, &c.SIPNode, &c.MediaMode, &c.FinalStatus, &c.TerminationSide, &c.FailureReason,
		&c.Direction, &c.OriginalDestination, &c.RewrittenDestination, &c.Route, &c.Trunk}, extra...)
	if err := r.Scan(dest...); err != nil {
		return c, err
	}
	c.RingTime, c.AnswerTime = nullTime(ring), nullTime(answer)
	return c, nil
}

// ListCDRs returns up to limit CDRs with id below before (0 means from the
// newest), newest first, and the cursor for the next page ("" at the end).
func (s *Store) ListCDRs(ctx context.Context, before int64, limit int) ([]CDR, string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cdrCols+`
		FROM cdrs WHERE $1 = 0 OR id < $1 ORDER BY id DESC LIMIT $2`, before, limit+1)
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
