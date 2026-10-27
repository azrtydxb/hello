package store

// Voicemail groups (spec pbx-gaps S-14). A group has a dial number and member
// extensions that each have a voicemail box. A message left for the group, or
// broadcast by a member with *63<number>, is delivered as one stored object
// and one voicemail_messages row per member box (copy on delivery, D12). Every
// change runs through configChange, so it is audited, bumps the configuration
// revision and notifies hello-sip.

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Voicemail group limits (spec D12).
const (
	MaxVoicemailGroups       = 100
	MaxVoicemailGroupMembers = 200
	// DefaultVoicemailGroupSeconds is the default message length; a group may
	// raise it to MaxVoicemailGroupSeconds.
	DefaultVoicemailGroupSeconds = 180
	MinVoicemailGroupSeconds     = 10
	MaxVoicemailGroupSeconds     = 600
)

// IVRVoicemailGroup is the destination kind that leaves a message in every
// member's box (migration 00046). The target is the group's number.
const IVRVoicemailGroup = "voicemail_group"

// VoicemailGroup is a group with its members in number order.
type VoicemailGroup struct {
	ID         int64             `json:"id"`
	Number     string            `json:"number"`
	Name       string            `json:"name"`
	Enabled    bool              `json:"enabled"`
	MaxSeconds int               `json:"maxSeconds"`
	Members    []PagingExtension `json:"members"`
	CreatedAt  time.Time         `json:"createdAt"`
	UpdatedAt  time.Time         `json:"updatedAt"`
}

// VoicemailGroupInput is the writable shape of a group; an update replaces
// the whole group, member list included.
type VoicemailGroupInput struct {
	Number     string
	Name       string
	Enabled    bool
	MaxSeconds int
	Members    []int64 // extension ids
}

func listVoicemailGroups(ctx context.Context, q querier, where string, args ...any) ([]VoicemailGroup, error) {
	rows, err := q.QueryContext(ctx, `SELECT g.id, g.number, g.name, g.enabled, g.max_seconds, g.created_at, g.updated_at
		FROM voicemail_groups g `+where+` ORDER BY g.number, g.id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []VoicemailGroup{}
	var ids []int64
	for rows.Next() {
		var g VoicemailGroup
		if err := rows.Scan(&g.ID, &g.Number, &g.Name, &g.Enabled, &g.MaxSeconds, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		g.Members = []PagingExtension{}
		out = append(out, g)
		ids = append(ids, g.ID)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return out, nil
	}
	byID := map[int64]*VoicemailGroup{}
	for i := range out {
		byID[out[i].ID] = &out[i]
	}
	mrows, err := q.QueryContext(ctx, `SELECT m.group_id, m.extension_id, e.number, e.name FROM voicemail_group_members m
		JOIN extensions e ON e.id = m.extension_id WHERE m.group_id = ANY($1) ORDER BY m.group_id, e.number`, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = mrows.Close() }()
	for mrows.Next() {
		var gid int64
		var pe PagingExtension
		if err := mrows.Scan(&gid, &pe.ExtensionID, &pe.Number, &pe.Name); err != nil {
			return nil, err
		}
		byID[gid].Members = append(byID[gid].Members, pe)
	}
	return out, mrows.Err()
}

// ListVoicemailGroups returns every voicemail group.
func (s *Store) ListVoicemailGroups(ctx context.Context) ([]VoicemailGroup, error) {
	return listVoicemailGroups(ctx, s.db, "")
}

// GetVoicemailGroup returns one voicemail group.
func (s *Store) GetVoicemailGroup(ctx context.Context, id int64) (VoicemailGroup, error) {
	gs, err := listVoicemailGroups(ctx, s.db, `WHERE g.id = $1`, id)
	if err != nil {
		return VoicemailGroup{}, err
	}
	if len(gs) == 0 {
		return VoicemailGroup{}, ErrNotFound
	}
	return gs[0], nil
}

// writeVoicemailGroupMembers replaces the member list. Every member must have
// a voicemail box: a member without one could not receive a copy.
func writeVoicemailGroupMembers(ctx context.Context, tx *sql.Tx, id int64, members []int64) error {
	if len(members) > MaxVoicemailGroupMembers {
		return fieldError("members", fmt.Sprintf("at most %d members", MaxVoicemailGroupMembers))
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM voicemail_group_members WHERE group_id = $1`, id); err != nil {
		return err
	}
	for _, ext := range members {
		var boxes int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM voicemail_boxes WHERE extension_id = $1`, ext).Scan(&boxes); err != nil {
			return err
		}
		if boxes == 0 {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM extensions WHERE id = $1`, ext).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return fieldError("members", "names an extension that does not exist")
			}
			return fieldError("members", fmt.Sprintf("extension %d has no voicemail box", ext))
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO voicemail_group_members (group_id, extension_id) VALUES ($1, $2)`, id, ext); err != nil {
			return pagingFK(err, "members")
		}
	}
	return nil
}

// CreateVoicemailGroup inserts a group with its members.
func (s *Store) CreateVoicemailGroup(ctx context.Context, actor string, in VoicemailGroupInput, check Check) (VoicemailGroup, error) {
	var g VoicemailGroup
	err := s.configChange(ctx, actor, "create", "voicemail_group", check, func(tx *sql.Tx) (int64, error) {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM voicemail_groups`).Scan(&n); err != nil {
			return 0, err
		}
		if n >= MaxVoicemailGroups {
			return 0, &InUseError{Msg: fmt.Sprintf("at most %d voicemail groups", MaxVoicemailGroups)}
		}
		if err := checkDialNumber(ctx, tx, "number", in.Number, numVoicemailGroup); err != nil {
			return 0, err
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO voicemail_groups (number, name, enabled, max_seconds)
			VALUES ($1, $2, $3, $4) RETURNING id`, in.Number, in.Name, in.Enabled, in.MaxSeconds).Scan(&id); err != nil {
			return 0, err
		}
		if err := writeVoicemailGroupMembers(ctx, tx, id, in.Members); err != nil {
			return id, err
		}
		gs, err := listVoicemailGroups(ctx, tx, `WHERE g.id = $1`, id)
		if err == nil && len(gs) == 1 {
			g = gs[0]
		}
		return id, err
	})
	return g, err
}

// UpdateVoicemailGroup replaces a group. Renumbering a group something points
// at is refused: the references name its number.
func (s *Store) UpdateVoicemailGroup(ctx context.Context, actor string, id int64, in VoicemailGroupInput, check Check) (VoicemailGroup, error) {
	var g VoicemailGroup
	err := s.configChange(ctx, actor, "update", "voicemail_group", check, func(tx *sql.Tx) (int64, error) {
		var cur string
		if err := tx.QueryRowContext(ctx, `SELECT number FROM voicemail_groups WHERE id = $1`, id).Scan(&cur); err != nil {
			return id, mapErr(err)
		}
		if in.Number != cur {
			if err := refuseIfVoicemailGroupUsed(ctx, tx, "voicemail group "+cur+", which cannot be renumbered while it is named by", cur); err != nil {
				return id, err
			}
			if err := checkDialNumber(ctx, tx, "number", in.Number, numVoicemailGroup); err != nil {
				return id, err
			}
		}
		res, err := tx.ExecContext(ctx, `UPDATE voicemail_groups SET number = $2, name = $3, enabled = $4,
			max_seconds = $5, updated_at = now() WHERE id = $1`, id, in.Number, in.Name, in.Enabled, in.MaxSeconds)
		if err != nil {
			return id, err
		}
		if err := requireRow(res); err != nil {
			return id, err
		}
		if err := writeVoicemailGroupMembers(ctx, tx, id, in.Members); err != nil {
			return id, err
		}
		gs, err := listVoicemailGroups(ctx, tx, `WHERE g.id = $1`, id)
		if err == nil && len(gs) == 1 {
			g = gs[0]
		}
		return id, err
	})
	return g, err
}

// DeleteVoicemailGroup removes a group and, by cascade, its members. Messages
// already delivered stay in the member boxes. A group an IVR menu, time
// condition, queue or inbound route sends calls to is refused.
func (s *Store) DeleteVoicemailGroup(ctx context.Context, actor string, id int64, check Check) error {
	return s.configChange(ctx, actor, "delete", "voicemail_group", check, func(tx *sql.Tx) (int64, error) {
		var number string
		if err := tx.QueryRowContext(ctx, `SELECT number FROM voicemail_groups WHERE id = $1`, id).Scan(&number); err != nil {
			return id, mapErr(err)
		}
		if err := refuseIfVoicemailGroupUsed(ctx, tx, "voicemail group "+number, number); err != nil {
			return id, err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM voicemail_groups WHERE id = $1`, id)
		if err != nil {
			return id, err
		}
		return id, requireRow(res)
	})
}

// refuseIfVoicemailGroupUsed returns an *InUseError naming what sends calls
// to the group with this number.
func refuseIfVoicemailGroupUsed(ctx context.Context, tx *sql.Tx, what, number string) error {
	var refs []string
	for _, q := range []struct{ query, label string }{
		{`SELECT name FROM inbound_routes WHERE destination_kind = 'voicemail_group' AND destination = $1 ORDER BY name`, "inbound route"},
		{`SELECT m.name FROM ivr_menus m WHERE
			(m.timeout_kind = 'voicemail_group' AND m.timeout_target = $1)
			OR (m.invalid_kind = 'voicemail_group' AND m.invalid_target = $1)
			OR EXISTS (SELECT 1 FROM ivr_menu_options o WHERE o.menu_id = m.id AND o.dest_kind = 'voicemail_group' AND o.dest_target = $1)
			ORDER BY m.name`, "IVR menu"},
		{`SELECT name FROM time_conditions WHERE
			(open_kind = 'voicemail_group' AND open_target = $1) OR (closed_kind = 'voicemail_group' AND closed_target = $1)
			OR (holiday_kind = 'voicemail_group' AND holiday_target = $1) ORDER BY name`, "time condition"},
		{`SELECT name FROM queues WHERE
			(timeout_kind = 'voicemail_group' AND timeout_target = $1) OR (full_kind = 'voicemail_group' AND full_target = $1)
			OR (no_agents_kind = 'voicemail_group' AND no_agents_target = $1) ORDER BY name`, "queue"},
	} {
		rows, err := tx.QueryContext(ctx, q.query, number)
		if err != nil {
			return err
		}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				_ = rows.Close()
				return err
			}
			refs = append(refs, q.label+" "+n)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	if len(refs) > 0 {
		return inUseBy(what, refs)
	}
	return nil
}

// InsertVoicemailGroupMessage stores one recorded message in every box of
// boxIDs and returns the boxes that received a copy. A box that no longer
// exists is skipped, so the others still get theirs. The copies
// share one object (copy on delivery, D12), and all rows are written in one
// statement, so a message reaches every member or none. Like the other
// call-plane paths it is not audited.
func (s *Store) InsertVoicemailGroupMessage(ctx context.Context, boxIDs []int64, object, caller, callerName string, durationMs int64) ([]int64, error) {
	if len(boxIDs) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		INSERT INTO voicemail_messages (box_id, minio_object, caller, caller_name, duration_ms)
		SELECT u.b, $2::text, $3::text, $4::text, $5::bigint FROM unnest($1::bigint[]) AS u(b)
		JOIN voicemail_boxes vb ON vb.id = u.b
		RETURNING box_id`, boxIDs, object, caller, callerName, durationMs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var b int64
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// releaseVoicemailObject returns object when no message row references it any
// more, and "" while another row (a copy delivered to another member) still
// does, so the caller removes the stored audio only with its last reference.
// It runs after the row was deleted.
func releaseVoicemailObject(ctx context.Context, q querier, object string) (string, error) {
	if object == "" {
		return "", nil
	}
	var n int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM voicemail_messages WHERE minio_object = $1`, object).Scan(&n); err != nil {
		return "", err
	}
	if n > 0 {
		return "", nil
	}
	return object, nil
}
