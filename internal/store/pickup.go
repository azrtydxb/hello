package store

// Call pickup groups (phone-feature-parity spec S-50). A group is a name and
// member extensions: a member may answer a call ringing any other member
// (group pickup *40, directed pickup *41<ext>). Every change runs through
// configChange, so it is audited, bumps the configuration revision and
// notifies hello-sip.

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Pickup limits.
const (
	MaxPickupGroups  = 100
	MaxPickupMembers = 200
)

// PickupGroup is a pickup group with its members in number order.
type PickupGroup struct {
	ID        int64             `json:"id"`
	Name      string            `json:"name"`
	Members   []PagingExtension `json:"members"`
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

// PickupGroupInput is the writable shape of a group; an update replaces the
// whole group, member list included.
type PickupGroupInput struct {
	Name    string
	Members []int64 // extension ids
}

func listPickupGroups(ctx context.Context, q querier, where string, args ...any) ([]PickupGroup, error) {
	rows, err := q.QueryContext(ctx, `SELECT g.id, g.name, g.created_at, g.updated_at FROM pickup_groups g `+where+` ORDER BY g.name, g.id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []PickupGroup{}
	var ids []int64
	for rows.Next() {
		var g PickupGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.CreatedAt, &g.UpdatedAt); err != nil {
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
	byID := map[int64]*PickupGroup{}
	for i := range out {
		byID[out[i].ID] = &out[i]
	}
	mrows, err := q.QueryContext(ctx, `SELECT m.group_id, m.extension_id, e.number, e.name FROM pickup_group_members m
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

// ListPickupGroups returns every pickup group.
func (s *Store) ListPickupGroups(ctx context.Context) ([]PickupGroup, error) {
	return listPickupGroups(ctx, s.db, "")
}

// GetPickupGroup returns one pickup group.
func (s *Store) GetPickupGroup(ctx context.Context, id int64) (PickupGroup, error) {
	gs, err := listPickupGroups(ctx, s.db, `WHERE g.id = $1`, id)
	if err != nil {
		return PickupGroup{}, err
	}
	if len(gs) == 0 {
		return PickupGroup{}, ErrNotFound
	}
	return gs[0], nil
}

func writePickupMembers(ctx context.Context, tx *sql.Tx, id int64, members []int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM pickup_group_members WHERE group_id = $1`, id); err != nil {
		return err
	}
	for _, ext := range members {
		if _, err := tx.ExecContext(ctx, `INSERT INTO pickup_group_members (group_id, extension_id) VALUES ($1, $2)`, id, ext); err != nil {
			return pagingFK(err, "members")
		}
	}
	return nil
}

// CreatePickupGroup inserts a pickup group with its members.
func (s *Store) CreatePickupGroup(ctx context.Context, actor string, in PickupGroupInput, check Check) (PickupGroup, error) {
	var g PickupGroup
	err := s.configChange(ctx, actor, "create", "pickup_group", check, func(tx *sql.Tx) (int64, error) {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pickup_groups`).Scan(&n); err != nil {
			return 0, err
		}
		if n >= MaxPickupGroups {
			return 0, &InUseError{Msg: fmt.Sprintf("at most %d pickup groups", MaxPickupGroups)}
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO pickup_groups (name) VALUES ($1) RETURNING id`, in.Name).Scan(&id); err != nil {
			return 0, err
		}
		if err := writePickupMembers(ctx, tx, id, in.Members); err != nil {
			return id, err
		}
		gs, err := listPickupGroups(ctx, tx, `WHERE g.id = $1`, id)
		if err == nil && len(gs) == 1 {
			g = gs[0]
		}
		return id, err
	})
	return g, err
}

// UpdatePickupGroup replaces a pickup group.
func (s *Store) UpdatePickupGroup(ctx context.Context, actor string, id int64, in PickupGroupInput, check Check) (PickupGroup, error) {
	var g PickupGroup
	err := s.configChange(ctx, actor, "update", "pickup_group", check, func(tx *sql.Tx) (int64, error) {
		res, err := tx.ExecContext(ctx, `UPDATE pickup_groups SET name = $2, updated_at = now() WHERE id = $1`, id, in.Name)
		if err != nil {
			return id, err
		}
		if err := requireRow(res); err != nil {
			return id, err
		}
		if err := writePickupMembers(ctx, tx, id, in.Members); err != nil {
			return id, err
		}
		gs, err := listPickupGroups(ctx, tx, `WHERE g.id = $1`, id)
		if err == nil && len(gs) == 1 {
			g = gs[0]
		}
		return id, err
	})
	return g, err
}

// DeletePickupGroup removes a pickup group and, by cascade, its members.
func (s *Store) DeletePickupGroup(ctx context.Context, actor string, id int64, check Check) error {
	return s.configChange(ctx, actor, "delete", "pickup_group", check, func(tx *sql.Tx) (int64, error) {
		res, err := tx.ExecContext(ctx, `DELETE FROM pickup_groups WHERE id = $1`, id)
		if err != nil {
			return id, err
		}
		return id, requireRow(res)
	})
}
