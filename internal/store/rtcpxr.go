package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/azrtydxb/hello/internal/rtcpxr"
)

// RTCPXRRetention is how long a phone's quality report is kept.
const RTCPXRRetention = 90 * 24 * time.Hour

// RTCPXRQuality is what a phone reported about one call (migration 00051):
// the phone's own receive-side view, shown beside the relay's figures. A nil
// value was not in the report.
type RTCPXRQuality struct {
	// Device is the reporting device's user name and Extension its extension.
	Device    string `json:"device"`
	Extension string `json:"extension"`
	// Final is true for the call-end report.
	Final       bool      `json:"final"`
	MOSLQ       *float64  `json:"mosLq"`
	MOSCQ       *float64  `json:"mosCq"`
	JitterMs    *float64  `json:"jitterMs"`
	LossPct     *float64  `json:"lossPct"`
	DiscardPct  *float64  `json:"discardPct"`
	RoundTripMs *float64  `json:"roundTripMs"`
	ReportedAt  time.Time `json:"reportedAt"`
}

var rtcpxrWrites atomic.Int64

// RecordRTCPXR stores a phone's report. A later report of the same dialog
// from the same device replaces the earlier one, except that an interval
// report never replaces the call-end report. Every 1000th write also removes
// reports older than RTCPXRRetention.
func (s *Store) RecordRTCPXR(ctx context.Context, device, extension string, r rtcpxr.Report) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO cdr_rtcpxr
		(call_id, device, extension, final, mos_lq, mos_cq, jitter_ms, loss_pct, discard_pct, rtt_ms, reported_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
		ON CONFLICT (call_id, device) DO UPDATE SET
			extension = EXCLUDED.extension, final = EXCLUDED.final, mos_lq = EXCLUDED.mos_lq,
			mos_cq = EXCLUDED.mos_cq, jitter_ms = EXCLUDED.jitter_ms, loss_pct = EXCLUDED.loss_pct,
			discard_pct = EXCLUDED.discard_pct, rtt_ms = EXCLUDED.rtt_ms, reported_at = now()
		WHERE EXCLUDED.final OR NOT cdr_rtcpxr.final`,
		r.CallID, device, extension, r.Final, r.MOSLQ, r.MOSCQ, r.JitterMs, r.LossPct, r.DiscardPct, r.RoundTripMs)
	if err != nil {
		return fmt.Errorf("store: record rtcp-xr report: %w", err)
	}
	if rtcpxrWrites.Add(1)%1000 == 0 {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM cdr_rtcpxr WHERE reported_at < $1`, time.Now().Add(-RTCPXRRetention))
	}
	return nil
}

// rtcpxrOf returns the reports that name the CDR's caller leg (sip_call_id)
// or callee leg (peer_call_id), oldest first.
func (s *Store) rtcpxrOf(ctx context.Context, cdrID int64) ([]RTCPXRQuality, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT x.device, x.extension, x.final, x.mos_lq, x.mos_cq,
			x.jitter_ms, x.loss_pct, x.discard_pct, x.rtt_ms, x.reported_at
		FROM cdrs c JOIN cdr_rtcpxr x ON x.call_id = c.sip_call_id OR (c.peer_call_id <> '' AND x.call_id = c.peer_call_id)
		WHERE c.id = $1 ORDER BY x.reported_at, x.device`, cdrID)
	if err != nil {
		return nil, fmt.Errorf("store: rtcp-xr reports of cdr %d: %w", cdrID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []RTCPXRQuality
	for rows.Next() {
		var q RTCPXRQuality
		var mlq, mcq, j, l, d, rtt sql.NullFloat64
		if err := rows.Scan(&q.Device, &q.Extension, &q.Final, &mlq, &mcq, &j, &l, &d, &rtt, &q.ReportedAt); err != nil {
			return nil, err
		}
		q.MOSLQ, q.MOSCQ, q.JitterMs, q.LossPct, q.DiscardPct, q.RoundTripMs =
			nf(mlq), nf(mcq), nf(j), nf(l), nf(d), nf(rtt)
		out = append(out, q)
	}
	return out, rows.Err()
}

func nf(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	return &v.Float64
}
