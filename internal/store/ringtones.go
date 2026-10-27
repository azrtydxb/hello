package store

// The tone library (migration 00048, spec phone-feature-parity S-43): the
// uploaded WAVs a ring map choice can name. The audio is in MinIO; the row
// holds what the API lists and the provisioning handler needs to serve it.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/prov"
)

// RingtoneFile is a tone as the API lists it.
type RingtoneFile struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	SizeBytes  int64     `json:"sizeBytes"`
	SHA256     string    `json:"sha256"`
	SampleRate int       `json:"sampleRate"`
	DurationMS int       `json:"durationMs"`
	CreatedAt  time.Time `json:"createdAt"`
	// UsedBy is how many ring map choices name the tone; a tone in use
	// cannot be deleted.
	UsedBy int `json:"usedBy"`
}

const ringtoneCols = `t.id, t.name, t.size_bytes, t.sha256, t.sample_rate, t.duration_ms, t.created_at,
	(SELECT count(*) FROM ring_map m WHERE m.custom_tone_id = t.id)`

func scanRingtone(row interface{ Scan(...any) error }) (RingtoneFile, error) {
	var t RingtoneFile
	err := row.Scan(&t.ID, &t.Name, &t.SizeBytes, &t.SHA256, &t.SampleRate, &t.DurationMS, &t.CreatedAt, &t.UsedBy)
	return t, err
}

// ListRingtones returns the library, by name.
func (s *Store) ListRingtones(ctx context.Context) ([]RingtoneFile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+ringtoneCols+` FROM ringtones t ORDER BY t.name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []RingtoneFile{}
	for rows.Next() {
		t, err := scanRingtone(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RingtoneExists reports whether the library has a tone of that name.
func (s *Store) RingtoneExists(ctx context.Context, name string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM ringtones WHERE name = $1`, name).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// RingtoneObjectKey is the MinIO key of a tone: the hash is in it, so a name
// that is deleted and uploaded again with other audio never overwrites an
// object a phone may still be fetching.
func RingtoneObjectKey(name, sha256 string) string { return name + "/" + sha256 + ".wav" }

// CreateRingtone adds a tone; a name the library has is a conflict.
func (s *Store) CreateRingtone(ctx context.Context, actor, name string, info prov.ToneInfo, size int64, sha256 string) (RingtoneFile, error) {
	var out RingtoneFile
	err := s.provChange(ctx, actor, "create", "ringtones", func(tx *sql.Tx) (string, bool, error) {
		var id int64
		switch err := tx.QueryRowContext(ctx, `SELECT id FROM ringtones WHERE name = $1`, name).Scan(&id); {
		case err == nil:
			return "", false, conflict("the tone %s already exists; delete it first", name)
		case !errors.Is(err, sql.ErrNoRows):
			return "", false, err
		}
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO ringtones (name, object_key, size_bytes, sha256, sample_rate, duration_ms)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			name, RingtoneObjectKey(name, sha256), size, sha256, info.SampleRate, int(info.Duration/time.Millisecond)).Scan(&id); err != nil {
			return "", false, err
		}
		var err error
		out, err = scanRingtone(tx.QueryRowContext(ctx, `SELECT `+ringtoneCols+` FROM ringtones t WHERE t.id = $1`, id))
		return strconv.FormatInt(id, 10), false, err
	})
	return out, err
}

// DeleteRingtone removes a tone and returns its object key for the caller to
// remove from MinIO. A tone a ring map choice names is refused with the first
// of them named, so a phone is never pointed at a missing file. ErrNotFound
// for an unknown id.
func (s *Store) DeleteRingtone(ctx context.Context, actor string, id int64) (string, error) {
	var key string
	err := s.provChange(ctx, actor, "delete", "ringtones", func(tx *sql.Tx) (string, bool, error) {
		sid := strconv.FormatInt(id, 10)
		var name string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM ringtones WHERE id = $1 FOR UPDATE`, id).Scan(&name); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return sid, false, ErrNotFound
			}
			return sid, false, err
		}
		var tag, vendor string
		var group sql.NullInt64
		switch err := tx.QueryRowContext(ctx, `SELECT tag, vendor, group_id FROM ring_map WHERE custom_tone_id = $1 ORDER BY id LIMIT 1`, id).Scan(&tag, &vendor, &group); {
		case err == nil:
			where := "the ring map"
			if group.Valid {
				where = fmt.Sprintf("the ring map of phone group %d", group.Int64)
			}
			return sid, false, conflict("the tone %s is the %s tone for ring tag %s in %s; choose another tone first", name, vendor, tag, where)
		case !errors.Is(err, sql.ErrNoRows):
			return sid, false, err
		}
		return sid, false, tx.QueryRowContext(ctx, `DELETE FROM ringtones WHERE id = $1 RETURNING object_key`, id).Scan(&key)
	})
	return key, err
}

// RingtoneByName implements prov.Store.
func (s *Store) RingtoneByName(ctx context.Context, name string) (prov.Ringtone, error) {
	var t prov.Ringtone
	err := s.db.QueryRowContext(ctx, `SELECT id, name, object_key, size_bytes, sha256, sample_rate, duration_ms, created_at FROM ringtones WHERE name = $1`, name).
		Scan(&t.ID, &t.Name, &t.ObjectKey, &t.Size, &t.SHA256, &t.SampleRate, &t.DurationMS, &t.UploadedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = prov.ErrNotFound
	}
	return t, err
}
