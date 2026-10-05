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
	// CDRID, Source and Destination come from the call's CDR (one per
	// correlation id); a recording whose CDR is not written yet has no CDRID
	// and empty parties.
	CDRID       *int64 `json:"cdrId,omitempty"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	// Object is the MinIO key; it stays internal (clients play audio through
	// the audio route).
	Object string `json:"-"`
}

// recordingFrom selects recordingCols: the recording joined to its call's
// CDR, so a list shows who called whom.
const (
	recordingCols = `r.id, r.correlation_id, r.initiated_by, r.duration_ms, r.created_at, r.minio_object,
		c.id, COALESCE(c.source, ''), COALESCE(c.destination, '')`
	recordingFrom = ` FROM recordings r LEFT JOIN cdrs c ON c.correlation_id = r.correlation_id`
)

func scanRecording(r interface{ Scan(...any) error }) (Recording, error) {
	var rec Recording
	err := r.Scan(&rec.ID, &rec.CorrelationID, &rec.InitiatedBy, &rec.DurationMs, &rec.CreatedAt, &rec.Object,
		&rec.CDRID, &rec.Source, &rec.Destination)
	return rec, err
}

// ListRecordings returns up to limit recordings with id below before (0 means
// from the newest), newest first, and the cursor for the next page ("" at the
// end), like ListCDRs. A non-empty extension keeps only the recordings whose
// correlation id belongs to a call the extension was on (the CDR join).
func (s *Store) ListRecordings(ctx context.Context, extension string, before int64, limit int) ([]Recording, string, error) {
	// One static query (as in ListCDRs): an empty extension keeps everything
	// and a zero before starts at the newest.
	rows, err := s.db.QueryContext(ctx, `SELECT `+recordingCols+recordingFrom+`
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

// InsertRecording stores one recording row after the SIP node put the audio
// in the object store. It is call-plane (no audit, no revision), runs off
// the SIP transaction path, and returns the row's id. A correlation id that
// already has a recording fails the unique constraint: one recording per
// call (spec S-4).
func (s *Store) InsertRecording(ctx context.Context, correlationID, object, initiatedBy string, durationMs int64) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO recordings (correlation_id, minio_object, initiated_by, duration_ms)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		correlationID, object, initiatedBy, durationMs).Scan(&id)
	return id, mapErr(err)
}

// GetRecording returns one recording.
func (s *Store) GetRecording(ctx context.Context, id int64) (Recording, error) {
	rec, err := scanRecording(s.db.QueryRowContext(ctx,
		`SELECT `+recordingCols+recordingFrom+` WHERE r.id = $1`, id))
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

// ReplaceAnnouncement records that an announcement's audio was replaced:
// the object at the same key was overwritten by the caller, so only
// updated_at moves. It runs through configChange like every announcement
// change (audit, revision bump, NOTIFY), so hello-sip reloads the audio.
func (s *Store) ReplaceAnnouncement(ctx context.Context, actor string, id int64) (Announcement, error) {
	var a Announcement
	err := s.configChange(ctx, actor, "update", "announcement", nil, func(tx *sql.Tx) (int64, error) {
		var err error
		a, err = scanAnnouncement(tx.QueryRowContext(ctx, `
			UPDATE announcements SET updated_at = now() WHERE id = $1 RETURNING `+announcementCols, id))
		return id, err
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
