package snapshot

import (
	"context"
	"fmt"
	"slices"
)

// Door stations (migration 00047, spec phone-feature-parity S-45 and S-46).
// The station's targets ride the snapshot, so the check that keeps a door
// unit from dialling out never reads PostgreSQL on the SIP path.

// IsDoor reports whether number is a door station extension.
func (s *Snapshot) IsDoor(number string) bool {
	e, ok := s.exts[number]
	return ok && e.Door
}

// DoorMayCall reports whether the door station may call dialled: an
// extension or ring group on its own list, or any Door ring group. A dialled
// number that is another door station never qualifies.
func (s *Snapshot) DoorMayCall(station, dialled string) bool {
	e, ok := s.exts[station]
	if !ok || !e.Door {
		return false
	}
	if g, _, isGroup := s.RingGroup(dialled); isGroup {
		return g.IsDoor || slices.Contains(e.DoorGroups, dialled)
	}
	if t, isExt := s.exts[dialled]; isExt && t.Door {
		return false
	}
	return slices.Contains(e.DoorExtensions, dialled)
}

// loadDoorTargets resolves each door station's stored target ids to the
// extension numbers and ring group names the SIP path compares against; an
// id that no longer exists is dropped. It fills exts in place.
func loadDoorTargets(ctx context.Context, db Querier, exts []Extension) error {
	rows, err := db.Query(ctx, `SELECT e.number,
  coalesce((SELECT array_agg(t.number ORDER BY t.number) FROM extensions t
            WHERE t.kind = 'user' AND t.id IN (SELECT jsonb_array_elements_text(e.door_targets->'extensions')::bigint)), '{}'),
  coalesce((SELECT array_agg(g.name ORDER BY g.name) FROM ring_groups g
            WHERE g.id IN (SELECT jsonb_array_elements_text(e.door_targets->'ringGroups')::bigint)), '{}')
FROM extensions e WHERE e.kind = 'door'`)
	if err != nil {
		return fmt.Errorf("snapshot: door targets: %w", err)
	}
	defer rows.Close()
	byNumber := map[string]int{}
	for i, e := range exts {
		byNumber[e.Number] = i
	}
	for rows.Next() {
		var number string
		var targets, groups []string
		if err := rows.Scan(&number, &targets, &groups); err != nil {
			return fmt.Errorf("snapshot: scan door targets: %w", err)
		}
		if i, ok := byNumber[number]; ok {
			exts[i].DoorExtensions, exts[i].DoorGroups = targets, groups
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("snapshot: door targets: %w", err)
	}
	return nil
}
