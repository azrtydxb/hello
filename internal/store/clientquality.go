package store

// What a web client reports about a call at its end (migration 00120,
// clients-webrtc-mobile-video S-20): the client's own receive-side view (codec,
// loss, jitter, round trip, the candidate types of the selected pair, the
// video it got). It is numbers and short words, never media. A report names
// the client's own dialog, which is one leg of a B2BUA call and can arrive
// before the call's CDR is written, so, like the phones' RTCP-XR reports, it
// is matched to the CDR when read: by the caller-leg Call-ID (sip_call_id) or
// the callee-leg Call-ID (peer_call_id).

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"time"
)

// ClientQualityRetention is how long a client's report is kept.
const ClientQualityRetention = 90 * 24 * time.Hour

// ClientVideo is the video a client received.
type ClientVideo struct {
	Width  int    `json:"width"`
	Height int    `json:"height"`
	FPS    int    `json:"fps"`
	Codec  string `json:"codec"`
}

// ClientQualityInput is one report as the client sent it.
type ClientQualityInput struct {
	Codec           string
	LossPct         float64
	JitterMs        float64
	RTTMs           float64
	CandidateLocal  string
	CandidateRemote string
	Video           *ClientVideo
	DurationMs      int64
}

// ClientQuality is a stored report, with the device that sent it.
type ClientQuality struct {
	Device          string       `json:"device"`
	Extension       string       `json:"extension"`
	Codec           string       `json:"codec"`
	LossPct         float64      `json:"lossPct"`
	JitterMs        float64      `json:"jitterMs"`
	RTTMs           float64      `json:"rttMs"`
	CandidateLocal  string       `json:"candidateLocal"`
	CandidateRemote string       `json:"candidateRemote"`
	Video           *ClientVideo `json:"video,omitempty"`
	DurationMs      int64        `json:"durationMs"`
	ReportedAt      time.Time    `json:"reportedAt"`
}

var clientQualityWrites atomic.Int64

// RecordClientQuality stores a report from one of the user's own client
// devices (anything else is ErrNotFound). A later report of the same dialog
// from the same device replaces the earlier one. Every 1000th write also
// removes reports older than ClientQualityRetention.
func (s *Store) RecordClientQuality(ctx context.Context, userID, deviceID int64, callID string, in ClientQualityInput) error {
	var w, h, fps *int
	var vc *string
	if in.Video != nil {
		w, h, fps, vc = &in.Video.Width, &in.Video.Height, &in.Video.FPS, &in.Video.Codec
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO cdr_client_quality
		(call_id, device_id, user_id, codec, loss_pct, jitter_ms, rtt_ms, candidate_local, candidate_remote,
		 video_width, video_height, video_fps, video_codec, duration_ms, reported_at)
		SELECT $1, d.id, d.user_id, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, now()
		FROM devices d WHERE d.id = $2 AND d.user_id = $3 AND d.kind <> 'desk'
		ON CONFLICT (call_id, device_id) DO UPDATE SET
			codec = EXCLUDED.codec, loss_pct = EXCLUDED.loss_pct, jitter_ms = EXCLUDED.jitter_ms, rtt_ms = EXCLUDED.rtt_ms,
			candidate_local = EXCLUDED.candidate_local, candidate_remote = EXCLUDED.candidate_remote,
			video_width = EXCLUDED.video_width, video_height = EXCLUDED.video_height, video_fps = EXCLUDED.video_fps,
			video_codec = EXCLUDED.video_codec, duration_ms = EXCLUDED.duration_ms, reported_at = now()`,
		callID, deviceID, userID, in.Codec, in.LossPct, in.JitterMs, in.RTTMs, in.CandidateLocal, in.CandidateRemote,
		w, h, fps, vc, in.DurationMs)
	if err != nil {
		return fmt.Errorf("store: record client quality: %w", err)
	}
	if err := requireRow(res); err != nil {
		return err
	}
	if clientQualityWrites.Add(1)%1000 == 0 {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM cdr_client_quality WHERE reported_at < $1`, time.Now().Add(-ClientQualityRetention))
	}
	return nil
}

// clientQualityOf returns the reports that name the CDR's caller leg or
// callee leg, oldest first.
func (s *Store) clientQualityOf(ctx context.Context, cdrID int64) ([]ClientQuality, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.sip_username, coalesce(e.number, ''), x.codec, x.loss_pct, x.jitter_ms, x.rtt_ms,
			x.candidate_local, x.candidate_remote, x.video_width, x.video_height, x.video_fps, x.video_codec, x.duration_ms, x.reported_at
		FROM cdrs c
		JOIN cdr_client_quality x ON x.call_id = c.sip_call_id OR (c.peer_call_id <> '' AND x.call_id = c.peer_call_id)
		JOIN devices d ON d.id = x.device_id
		LEFT JOIN extensions e ON e.id = d.extension_id
		WHERE c.id = $1 ORDER BY x.reported_at, d.sip_username`, cdrID)
	if err != nil {
		return nil, fmt.Errorf("store: client quality of cdr %d: %w", cdrID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []ClientQuality
	for rows.Next() {
		var q ClientQuality
		var w, h, fps sql.NullInt64
		var vc sql.NullString
		if err := rows.Scan(&q.Device, &q.Extension, &q.Codec, &q.LossPct, &q.JitterMs, &q.RTTMs, &q.CandidateLocal, &q.CandidateRemote,
			&w, &h, &fps, &vc, &q.DurationMs, &q.ReportedAt); err != nil {
			return nil, err
		}
		if w.Valid && h.Valid {
			q.Video = &ClientVideo{Width: int(w.Int64), Height: int(h.Int64), FPS: int(fps.Int64), Codec: vc.String}
		}
		out = append(out, q)
	}
	return out, rows.Err()
}
