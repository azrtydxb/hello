package snapshot

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/azrtydxb/hello/internal/secret"
)

// Door stations (migration 00053, spec phone-feature-parity S-45 and S-46).
// The station's targets ride the snapshot, so the check that keeps a door
// unit from dialling out never reads PostgreSQL on the SIP path.

// IsDoor reports whether number is a door station extension.
func (s *Snapshot) IsDoor(number string) bool {
	e, ok := s.exts[number]
	return ok && e.Door
}

// DoorGroups lists the names of the Door ring groups, sorted.
func (s *Snapshot) DoorGroups() []string {
	var out []string
	for name, g := range s.groups {
		if g.g.IsDoor {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
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

// doorOpen is a door station's door-open settings (migration 00054, spec
// S-47). The code is opened at load time; it is unexported and the type
// prints as redacted, so it cannot reach a log through a snapshot dump.
type doorOpen struct {
	code   string
	hangup bool
}

// String redacts the code.
func (doorOpen) String() string { return "doorOpen{redacted}" }

// GoString redacts the code.
func (doorOpen) GoString() string { return "doorOpen{redacted}" }

// DoorOpen returns the door-open code of the door station number and whether
// the call is to hang up after the code was sent. ok is false when number is
// no door station or has no usable code.
func (s *Snapshot) DoorOpen(number string) (code string, hangup, ok bool) {
	d, found := s.doorOpens[number]
	if !found || d.code == "" || !s.IsDoor(number) {
		return "", false, false
	}
	return d.code, d.hangup, true
}

// WithDoorOpen installs the door-open settings by station number.
func (s *Snapshot) WithDoorOpen(codes map[string]DoorOpenSetting) *Snapshot {
	s.doorOpens = make(map[string]doorOpen, len(codes))
	for n, c := range codes {
		s.doorOpens[n] = doorOpen{code: c.Code, hangup: c.Hangup}
	}
	return s
}

// DoorOpenSetting is the input of WithDoorOpen.
type DoorOpenSetting struct {
	Code   string
	Hangup bool
}

// loadDoorOpen reads and opens the door-open codes of the door stations. A
// code that does not open is skipped and logged without its value.
func loadDoorOpen(ctx context.Context, db Querier, box *secret.Box, log *slog.Logger) (map[string]DoorOpenSetting, error) {
	if log == nil {
		log = slog.Default()
	}
	rows, err := db.Query(ctx, `SELECT d.extension_id, e.number, d.code_enc, d.hangup_after_open
FROM door_open d JOIN extensions e ON e.id = d.extension_id WHERE e.kind = 'door'`)
	if err != nil {
		return nil, fmt.Errorf("snapshot: door-open codes: %w", err)
	}
	defer rows.Close()
	out := map[string]DoorOpenSetting{}
	for rows.Next() {
		var (
			id     int64
			number string
			enc    []byte
			hangup bool
		)
		if err := rows.Scan(&id, &number, &enc, &hangup); err != nil {
			return nil, fmt.Errorf("snapshot: scan door-open code: %w", err)
		}
		if len(enc) == 0 {
			continue
		}
		if box == nil {
			log.Error("door-open code cannot be opened: no secret key", "extension", number)
			continue
		}
		code, err := box.Open(enc, secret.DoorOpenAAD(id))
		if err != nil {
			log.Error("door-open code does not open", "extension", number)
			continue
		}
		out[number] = DoorOpenSetting{Code: code, Hangup: hangup}
	}
	return out, rows.Err()
}
