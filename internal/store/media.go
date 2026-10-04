package store

// Phase 5 media: call recordings (written by hello-sip's media stream) and
// announcements. Recordings are call-plane data: listing needs no audit and
// the SIP stream inserts its own rows, but a deletion is a user action, so it
// is audited and bumps the revision like every other mutation. Announcements
// are configuration: hello-sip's snapshot carries them, so every change runs
// through configChange (audit + revision + NOTIFY).

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

// Recording is one call recording. Object is its MinIO key and stays
// internal: clients play audio through the audio route.
type Recording struct {
	ID            int64     `json:"id"`
	CorrelationID string    `json:"correlationId"`
	InitiatedBy   string    `json:"initiatedBy"`
	DurationMs    int64     `json:"durationMs"`
	CreatedAt     time.Time `json:"createdAt"`
	// Object is the MinIO key; it stays internal (clients play audio through
	// the audio route).
	Object string `json:"-"`
}

const recordingCols = `id, correlation_id, initiated_by, duration_ms, created_at, minio_object`

func scanRecording(r interface{ Scan(...any) error }) (Recording, error) {
	var rec Recording
	err := r.Scan(&rec.ID, &rec.CorrelationID, &rec.InitiatedBy, &rec.DurationMs, &rec.CreatedAt, &rec.Object)
	return rec, err
}

// ListRecordings returns up to limit recordings with id below before (0 means
// from the newest), newest first, and the cursor for the next page ("" at the
// end), like ListCDRs. A non-empty extension keeps only the recordings whose
// correlation id belongs to a call the extension was on (the CDR join).
func (s *Store) ListRecordings(ctx context.Context, extension string, before int64, limit int) ([]Recording, string, error) {
	// One static query (as in ListCDRs): an empty extension keeps everything
	// and a zero before starts at the newest.
	rows, err := s.db.QueryContext(ctx, `SELECT `+recordingCols+` FROM recordings r
		WHERE ($1 = '' OR EXISTS (SELECT 1 FROM cdrs c
			WHERE c.correlation_id = r.correlation_id AND (c.source = $1 OR c.destination = $1)))
		AND ($2 = 0 OR r.id < $2)
		ORDER BY r.id DESC LIMIT $3`, extension, before, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()
	out := []Recording{}
	for rows.Next() {
		rec, err := scanRecording(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, rec)
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

// GetRecording returns one recording.
func (s *Store) GetRecording(ctx context.Context, id int64) (Recording, error) {
	rec, err := scanRecording(s.db.QueryRowContext(ctx,
		`SELECT `+recordingCols+` FROM recordings WHERE id = $1`, id))
	return rec, mapErr(err)
}

// DeleteRecording removes a recording row and returns its MinIO object key;
// the caller removes the object, so this package never depends on the object
// store.
func (s *Store) DeleteRecording(ctx context.Context, actor string, id int64) (string, error) {
	var object string
	err := s.configChange(ctx, actor, "delete", "recording", nil, func(tx *sql.Tx) (int64, error) {
		if err := tx.QueryRowContext(ctx,
			`SELECT minio_object FROM recordings WHERE id = $1`, id).Scan(&object); err != nil {
			return id, err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM recordings WHERE id = $1`, id)
		if err != nil {
			return id, err
		}
		return id, requireRow(res)
	})
	return object, err
}

// Announcement is one named, uploaded announcement audio. Object is its
// MinIO key and stays internal.
type Announcement struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	// Object is the MinIO key; it stays internal.
	Object string `json:"-"`
}

const announcementCols = `id, name, created_at, updated_at, minio_object`

func scanAnnouncement(r interface{ Scan(...any) error }) (Announcement, error) {
	var a Announcement
	err := r.Scan(&a.ID, &a.Name, &a.CreatedAt, &a.UpdatedAt, &a.Object)
	return a, err
}

// ListAnnouncements returns every announcement ordered by name.
func (s *Store) ListAnnouncements(ctx context.Context) ([]Announcement, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+announcementCols+` FROM announcements ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Announcement{}
	for rows.Next() {
		a, err := scanAnnouncement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAnnouncement returns one announcement.
func (s *Store) GetAnnouncement(ctx context.Context, id int64) (Announcement, error) {
	a, err := scanAnnouncement(s.db.QueryRowContext(ctx,
		`SELECT `+announcementCols+` FROM announcements WHERE id = $1`, id))
	return a, mapErr(err)
}

// AnnouncementByName returns the announcement with that name (ok=false when
// there is none).
func (s *Store) AnnouncementByName(ctx context.Context, name string) (Announcement, bool, error) {
	a, err := scanAnnouncement(s.db.QueryRowContext(ctx,
		`SELECT `+announcementCols+` FROM announcements WHERE name = $1`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Announcement{}, false, nil
	}
	if err != nil {
		return Announcement{}, false, mapErr(err)
	}
	return a, true, nil
}

// CreateAnnouncement inserts an announcement whose audio is already in the
// object store at object. A taken name is ErrConflict.
func (s *Store) CreateAnnouncement(ctx context.Context, actor, name, object string, check Check) (Announcement, error) {
	var a Announcement
	err := s.configChange(ctx, actor, "create", "announcement", check, func(tx *sql.Tx) (int64, error) {
		var err error
		a, err = scanAnnouncement(tx.QueryRowContext(ctx, `
			INSERT INTO announcements (name, minio_object) VALUES ($1, $2) RETURNING `+announcementCols,
			name, object))
		return a.ID, err
	})
	return a, err
}

// DeleteAnnouncement removes an announcement row and returns its MinIO
// object key; the caller removes the object.
func (s *Store) DeleteAnnouncement(ctx context.Context, actor string, id int64, check Check) (string, error) {
	var object string
	err := s.configChange(ctx, actor, "delete", "announcement", check, func(tx *sql.Tx) (int64, error) {
		if err := tx.QueryRowContext(ctx,
			`SELECT minio_object FROM announcements WHERE id = $1`, id).Scan(&object); err != nil {
			return id, err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM announcements WHERE id = $1`, id)
		if err != nil {
			return id, err
		}
		return id, requireRow(res)
	})
	return object, err
}
