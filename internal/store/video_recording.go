package store

// Video recording rows (clients-webrtc-mobile-video S-42, migration 00121).
// hello-sip writes a stream's row while it records and keeps it current after
// every stored fragment; hello-control finalizes, sweeps and purges them. The
// objects (init segment, fragments, the finalized file) live in the recordings
// bucket; the row says which and how big, so a stream whose node died can be
// finished from it.

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// VideoSegment is one stored fragment of a stream.
type VideoSegment struct {
	Seq     int    `json:"seq"`
	Object  string `json:"object"`
	Bytes   int64  `json:"bytes"`
	Ticks   uint64 `json:"ticks"`
	Base    uint64 `json:"base"`
	StartMs int64  `json:"startMs"`
	Key     bool   `json:"key"`
}

// VideoStream is one recorded video stream of a call.
type VideoStream struct {
	ID          int64          `json:"id"`
	Correlation string         `json:"-"`
	Part        int            `json:"part"`
	Leg         string         `json:"leg"`
	Direction   string         `json:"direction"`
	Codec       string         `json:"codec"`
	Container   string         `json:"container"`
	Prefix      string         `json:"-"`
	Segments    int            `json:"segments"`
	Bytes       int64          `json:"bytes"`
	Width       int            `json:"width"`
	Height      int            `json:"height"`
	OffsetMs    int64          `json:"offsetMs"`
	DurationMs  int64          `json:"durationMs"`
	SegmentList []VideoSegment `json:"-"`
	Final       string         `json:"-"`
	StartedAt   time.Time      `json:"startedAt"`
	EndedAt     *time.Time     `json:"endedAt,omitempty"`
	UpdatedAt   time.Time      `json:"-"`
	State       string         `json:"state"`
	Failure     string         `json:"failure,omitempty"`
	Node        string         `json:"-"`
}

// FinalObject is the object name the finalizer makes of the stream: the
// fragments live under <name>.parts, the joined file is <name>.<container>.
func (v VideoStream) FinalObject() string {
	return strings.TrimSuffix(v.Prefix, ".parts") + "." + v.Container
}

// InitObject is the object holding the stream's init segment.
func (v VideoStream) InitObject() string { return v.Prefix + "/init" }

// VideoStreamWrite is what hello-sip writes about a stream.
type VideoStreamWrite struct {
	Part      int
	Leg       string
	Codec     string
	Container string
	Prefix    string
	Width     int
	Height    int
	OffsetMs  int64
	Segments  []VideoSegment
	Bytes     int64
	Started   time.Time
	Ended     bool
	Failed    string
}

const videoStreamCols = `id, call_correlation, part, leg, direction, codec, container, object_prefix, segments, bytes, width, height,
	offset_ms, duration_ms, segment_list, COALESCE(final_object, ''), started_at, ended_at, updated_at, state, failure, node`

func scanVideoStream(r interface{ Scan(...any) error }) (VideoStream, error) {
	var v VideoStream
	var list []byte
	err := r.Scan(&v.ID, &v.Correlation, &v.Part, &v.Leg, &v.Direction, &v.Codec, &v.Container, &v.Prefix, &v.Segments, &v.Bytes,
		&v.Width, &v.Height, &v.OffsetMs, &v.DurationMs, &list, &v.Final, &v.StartedAt, &v.EndedAt, &v.UpdatedAt, &v.State, &v.Failure, &v.Node)
	if err == nil && len(list) > 0 {
		_ = json.Unmarshal(list, &v.SegmentList) // a malformed list is an empty one; the finalizer then has nothing to join
	}
	return v, err
}

func streamState(w VideoStreamWrite) string {
	switch {
	case w.Failed != "":
		return "failed"
	case w.Ended:
		return "ended"
	}
	return "recording"
}

func durationMs(segs []VideoSegment) int64 {
	var ticks uint64
	for _, s := range segs {
		ticks += s.Ticks
	}
	return int64(ticks / 90) //nolint:gosec // a call is not 3e16 ticks long
}

// OpenVideoStream inserts a stream's row (or, when the call's node writes it
// again after a restart, takes the existing one over) and returns its id.
func (s *Store) OpenVideoStream(ctx context.Context, correlation, node string, w VideoStreamWrite) (int64, error) {
	list, _ := json.Marshal(nonNilSegments(w.Segments))
	var id int64
	err := s.q(ctx).QueryRowContext(ctx, `
		INSERT INTO recording_streams (call_correlation, part, leg, codec, container, object_prefix, segments, bytes, width, height,
			offset_ms, duration_ms, segment_list, started_at, state, failure, node, ended_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, CASE WHEN $15 = 'recording' THEN NULL ELSE now() END)
		ON CONFLICT (call_correlation, part, leg) DO UPDATE SET updated_at = now(), node = EXCLUDED.node
		RETURNING id`,
		correlation, w.Part, w.Leg, w.Codec, w.Container, w.Prefix, len(w.Segments), w.Bytes, w.Width, w.Height,
		w.OffsetMs, durationMs(w.Segments), list, w.Started, streamState(w), w.Failed, node).Scan(&id)
	return id, mapErr(err)
}

// UpdateVideoStream brings a stream's row up to date.
func (s *Store) UpdateVideoStream(ctx context.Context, id int64, w VideoStreamWrite) error {
	list, _ := json.Marshal(nonNilSegments(w.Segments))
	res, err := s.q(ctx).ExecContext(ctx, `
		UPDATE recording_streams SET segments = $2, bytes = $3, width = $4, height = $5, duration_ms = $6, segment_list = $7,
			state = CASE WHEN state IN ('finalized', 'purged') THEN state ELSE $8 END,
			failure = $9, updated_at = now(),
			ended_at = CASE WHEN $8 = 'recording' THEN ended_at ELSE COALESCE(ended_at, now()) END
		WHERE id = $1`, id, len(w.Segments), w.Bytes, w.Width, w.Height, durationMs(w.Segments), list, streamState(w), w.Failed)
	if err != nil {
		return mapErr(err)
	}
	return requireRow(res)
}

func nonNilSegments(in []VideoSegment) []VideoSegment {
	if in == nil {
		return []VideoSegment{}
	}
	return in
}

// VideoStreamsOf lists the streams of the call with this correlation id.
func (s *Store) VideoStreamsOf(ctx context.Context, correlation string) ([]VideoStream, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+videoStreamCols+` FROM recording_streams
		WHERE call_correlation = $1 AND state <> 'purged' ORDER BY part, leg`, correlation)
	if err != nil {
		return nil, err
	}
	return collectVideoStreams(rows)
}

func collectVideoStreams(rows *sql.Rows) ([]VideoStream, error) {
	defer func() { _ = rows.Close() }()
	out := []VideoStream{}
	for rows.Next() {
		v, err := scanVideoStream(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// RecordingsWithVideo says which of the recordings (by correlation id) have
// video streams.
func (s *Store) RecordingsWithVideo(ctx context.Context, correlations []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(correlations) == 0 {
		return out, nil
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT DISTINCT call_correlation FROM recording_streams
		WHERE call_correlation = ANY($1::text[]) AND state <> 'purged'`, pgTextArray(correlations))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out[c] = true
	}
	return out, rows.Err()
}

// ClaimVideoStreams takes up to n ended or failed streams that are not final
// yet, for ten minutes, so two replicas never finalize the same stream.
func (s *Store) ClaimVideoStreams(ctx context.Context, n int) ([]VideoStream, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `
		UPDATE recording_streams SET claim_until = now() + interval '10 minutes'
		WHERE id IN (SELECT id FROM recording_streams
			WHERE state IN ('ended', 'failed') AND final_object IS NULL AND segments > 0
			  AND (claim_until IS NULL OR claim_until < now())
			ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED)
		RETURNING `+videoStreamCols, n)
	if err != nil {
		return nil, err
	}
	return collectVideoStreams(rows)
}

// FinishVideoStream records the finalized file of a stream.
func (s *Store) FinishVideoStream(ctx context.Context, id int64, final string, bytes int64) error {
	res, err := s.q(ctx).ExecContext(ctx, `UPDATE recording_streams SET final_object = $2, bytes = $3, state = 'finalized',
		claim_until = NULL, updated_at = now() WHERE id = $1`, id, final, bytes)
	if err != nil {
		return mapErr(err)
	}
	return requireRow(res)
}

// ReleaseVideoStream gives up a claim (the finalizer failed; another try later).
func (s *Store) ReleaseVideoStream(ctx context.Context, id int64) error {
	_, err := s.q(ctx).ExecContext(ctx, `UPDATE recording_streams SET claim_until = now() + interval '1 minute' WHERE id = $1`, id)
	return err
}

// SweepVideoStreams ends the streams of a node that stopped writing them (a
// crash): a stream still recording that nobody touched for olderThan is ended
// with what its row lists. It returns how many.
func (s *Store) SweepVideoStreams(ctx context.Context, olderThan time.Duration) (int64, error) {
	res, err := s.q(ctx).ExecContext(ctx, `UPDATE recording_streams SET state = 'ended', ended_at = COALESCE(ended_at, updated_at),
		failure = CASE WHEN failure = '' THEN 'interrupted' ELSE failure END
		WHERE state = 'recording' AND updated_at < now() - make_interval(secs => $1)`, olderThan.Seconds())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ExpiredVideoStreams lists up to n streams that ended more than days ago.
func (s *Store) ExpiredVideoStreams(ctx context.Context, days, n int) ([]VideoStream, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+videoStreamCols+` FROM recording_streams
		WHERE state <> 'recording' AND COALESCE(ended_at, updated_at) < now() - make_interval(days => $1)
		ORDER BY id LIMIT $2`, days, n)
	if err != nil {
		return nil, err
	}
	return collectVideoStreams(rows)
}

// DeleteVideoStream removes a stream's row once its objects are gone.
func (s *Store) DeleteVideoStream(ctx context.Context, id int64) error {
	_, err := s.q(ctx).ExecContext(ctx, `DELETE FROM recording_streams WHERE id = $1`, id)
	return err
}

// DeleteVideoStreamsOf removes the rows of a call's streams and returns them
// first, so the caller can remove the objects.
func (s *Store) DeleteVideoStreamsOf(ctx context.Context, correlation string) ([]VideoStream, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `DELETE FROM recording_streams WHERE call_correlation = $1 RETURNING `+videoStreamCols, correlation)
	if err != nil {
		return nil, err
	}
	return collectVideoStreams(rows)
}

// VideoUsageBytes is what the video recordings hold in the object store.
func (s *Store) VideoUsageBytes(ctx context.Context) (int64, error) {
	var n int64
	err := s.q(ctx).QueryRowContext(ctx, `SELECT COALESCE(SUM(bytes), 0) FROM recording_streams WHERE state <> 'purged'`).Scan(&n)
	return n, err
}

// RecordingSettings are the settings of recordings.
type RecordingSettings struct {
	VideoRetentionDays int       `json:"videoRetentionDays"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

// GetRecordingSettings returns the settings row.
func (s *Store) GetRecordingSettings(ctx context.Context) (RecordingSettings, error) {
	var r RecordingSettings
	err := s.q(ctx).QueryRowContext(ctx, `SELECT video_retention_days, updated_at FROM recording_settings WHERE id = 1`).
		Scan(&r.VideoRetentionDays, &r.UpdatedAt)
	return r, mapErr(err)
}

// SetVideoRetention changes the video retention (1 to 3650 days).
func (s *Store) SetVideoRetention(ctx context.Context, actor string, days int) (RecordingSettings, error) {
	err := s.configChange(ctx, actor, "update", "recording_settings", nil, func(tx *sql.Tx) (int64, error) {
		_, err := tx.ExecContext(ctx, `UPDATE recording_settings SET video_retention_days = $1, updated_at = now() WHERE id = 1`, days)
		return 1, err
	})
	if err != nil {
		return RecordingSettings{}, err
	}
	return s.GetRecordingSettings(ctx)
}
