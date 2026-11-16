package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/azrtydxb/momo"
)

// Music on hold (phone-feature-parity S-33, migration 00029). A class is a
// named, ordered playlist of announcements played in a loop or shuffled; at
// most one class is the global default, and a class with no announcements
// plays the built-in tone.

// MOH playback modes.
const (
	MOHModeLoop    = "loop"
	MOHModeShuffle = "shuffle"
)

// MaxMOHTracks bounds a class's playlist.
const MaxMOHTracks = 50

// MOHTrack is one announcement in a class's playlist.
type MOHTrack struct {
	AnnouncementID int64  `json:"announcementId"`
	Name           string `json:"name"`
}

// MOHClass is one music on hold class with its playlist in order.
type MOHClass struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Mode      string     `json:"mode"`
	IsDefault bool       `json:"isDefault"`
	Tracks    []MOHTrack `json:"tracks"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

// MOHClassInput is the writable shape of a class: an update replaces the
// playlist. Making a class the default clears the flag on every other one.
type MOHClassInput struct {
	Name            string
	Mode            string
	IsDefault       bool
	AnnouncementIDs []int64
}

// Validate checks the shape of a class input.
func (in MOHClassInput) Validate() error {
	switch n := len(strings.TrimSpace(in.Name)); {
	case n == 0:
		return fieldError("name", "required")
	case len(in.Name) > 64:
		return fieldError("name", "at most 64 characters")
	}
	if in.Mode != MOHModeLoop && in.Mode != MOHModeShuffle {
		return fieldError("mode", "must be loop or shuffle")
	}
	if len(in.AnnouncementIDs) > MaxMOHTracks {
		return fieldError("announcementIds", fmt.Sprintf("at most %d announcements", MaxMOHTracks))
	}
	return nil
}

const mohCols = `id, name, mode, is_default, created_at, updated_at`

func scanMOH(r interface{ Scan(...any) error }) (MOHClass, error) {
	var c MOHClass
	err := r.Scan(&c.ID, &c.Name, &c.Mode, &c.IsDefault, &c.CreatedAt, &c.UpdatedAt)
	c.Tracks = []MOHTrack{}
	return c, err
}

// listMOH reads classes (optionally filtered) with their tracks.
func listMOH(ctx context.Context, q querier, where string, args ...any) ([]MOHClass, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+mohCols+` FROM moh_classes `+where+` ORDER BY name`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []MOHClass{}
	idx := map[int64]int{}
	var ids []int64
	for rows.Next() {
		c, err := scanMOH(rows)
		if err != nil {
			return nil, err
		}
		idx[c.ID] = len(out)
		out = append(out, c)
		ids = append(ids, c.ID)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return out, nil
	}
	trows, err := q.QueryContext(ctx, `
		SELECT t.class_id, t.announcement_id, a.name
		FROM moh_class_tracks t JOIN announcements a ON a.id = t.announcement_id
		WHERE t.class_id = ANY(@ids) ORDER BY t.class_id, t.position`, momo.Named("ids", ids))
	if err != nil {
		return nil, err
	}
	defer func() { _ = trows.Close() }()
	for trows.Next() {
		var cid int64
		var t MOHTrack
		if err := trows.Scan(&cid, &t.AnnouncementID, &t.Name); err != nil {
			return nil, err
		}
		i := idx[cid]
		out[i].Tracks = append(out[i].Tracks, t)
	}
	return out, trows.Err()
}

// ListMOHClasses returns every class ordered by name.
func (s *Store) ListMOHClasses(ctx context.Context) ([]MOHClass, error) {
	return listMOH(ctx, s.q(ctx), "")
}

// GetMOHClass returns one class.
func (s *Store) GetMOHClass(ctx context.Context, id int64) (MOHClass, error) {
	return getMOH(ctx, s.q(ctx), id)
}

func getMOH(ctx context.Context, q querier, id int64) (MOHClass, error) {
	cs, err := listMOH(ctx, q, `WHERE id = @id`, momo.Named("id", id))
	if err != nil {
		return MOHClass{}, err
	}
	if len(cs) == 0 {
		return MOHClass{}, ErrNotFound
	}
	return cs[0], nil
}

// checkMOHClass fails with a field error when id names no class. A nil id
// is fine (it means "use the default").
func checkMOHClass(ctx context.Context, q querier, path string, id *int64) error {
	if id == nil {
		return nil
	}
	var n int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM moh_classes WHERE id = @id`, momo.Named("id", *id)).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return fieldError(path, "no such music on hold class")
	}
	return nil
}

// writeMOHTracks replaces a class's playlist; an unknown announcement is a
// field error naming its index.
func writeMOHTracks(ctx context.Context, tx *dbTx, classID int64, ids []int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM moh_class_tracks WHERE class_id = @class_id`, momo.Named("class_id", classID)); err != nil {
		return err
	}
	for i, aid := range ids {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM announcements WHERE id = @id`, momo.Named("id", aid)).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fieldError(fmt.Sprintf("announcementIds[%d]", i), "no such announcement")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO moh_class_tracks (class_id, position, announcement_id) VALUES (@class_id, @position, @announcement_id)`,
			momo.Named("class_id", classID), momo.Named("position", i), momo.Named("announcement_id", aid)); err != nil {
			return err
		}
	}
	return nil
}

// CreateMOHClass inserts a class with its playlist. A taken name is
// ErrConflict.
func (s *Store) CreateMOHClass(ctx context.Context, actor string, in MOHClassInput) (MOHClass, error) {
	if err := in.Validate(); err != nil {
		return MOHClass{}, err
	}
	var out MOHClass
	err := s.configChange(ctx, actor, "create", "moh_class", nil, func(tx *dbTx) (int64, error) {
		if in.IsDefault {
			if _, err := tx.ExecContext(ctx, `UPDATE moh_classes SET is_default = FALSE WHERE is_default`); err != nil {
				return 0, err
			}
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO moh_classes (name, mode, is_default) VALUES (@name, @mode, @is_default) RETURNING id`,
			momo.Named("name", in.Name), momo.Named("mode", in.Mode), momo.Named("is_default", in.IsDefault)).Scan(&id); err != nil {
			return 0, err
		}
		if err := writeMOHTracks(ctx, tx, id, in.AnnouncementIDs); err != nil {
			return id, err
		}
		var err error
		out, err = getMOH(ctx, tx, id)
		return id, err
	})
	return out, err
}

// UpdateMOHClass replaces a class's fields and playlist.
func (s *Store) UpdateMOHClass(ctx context.Context, actor string, id int64, in MOHClassInput) (MOHClass, error) {
	if err := in.Validate(); err != nil {
		return MOHClass{}, err
	}
	var out MOHClass
	err := s.configChange(ctx, actor, "update", "moh_class", nil, func(tx *dbTx) (int64, error) {
		if in.IsDefault {
			if _, err := tx.ExecContext(ctx, `UPDATE moh_classes SET is_default = FALSE WHERE is_default AND id <> @id`, momo.Named("id", id)); err != nil {
				return id, err
			}
		}
		res, err := tx.ExecContext(ctx, `UPDATE moh_classes SET name = @name, mode = @mode, is_default = @is_default, updated_at = now()
			WHERE id = @id`, momo.Named("id", id), momo.Named("name", in.Name), momo.Named("mode", in.Mode), momo.Named("is_default", in.IsDefault))
		if err != nil {
			return id, err
		}
		if err := requireRow(res); err != nil {
			return id, err
		}
		if err := writeMOHTracks(ctx, tx, id, in.AnnouncementIDs); err != nil {
			return id, err
		}
		out, err = getMOH(ctx, tx, id)
		return id, err
	})
	return out, err
}

// DeleteMOHClass removes a class. Extensions, groups and routes that named
// it fall back to the global default.
func (s *Store) DeleteMOHClass(ctx context.Context, actor string, id int64) error {
	return s.configChange(ctx, actor, "delete", "moh_class", nil, func(tx *dbTx) (int64, error) {
		res, err := tx.ExecContext(ctx, `DELETE FROM moh_classes WHERE id = @id`, momo.Named("id", id))
		if err != nil {
			return id, err
		}
		return id, requireRow(res)
	})
}
