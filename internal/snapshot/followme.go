package snapshot

import (
	"context"
	"fmt"
)

// Follow-me (migration 00075, pbx-gaps S-5 and S-6): the profile of each
// extension rides the snapshot, so ringing a follow-me never touches
// PostgreSQL.

// Follow-me strategies and target kinds.
const (
	FollowMeSimultaneous = "simultaneous"
	FollowMeSequential   = "sequential"

	FollowMeExternal  = "external"
	FollowMeExtension = "extension"
	FollowMeRingGroup = "ring_group"

	// FollowMeMaxTargets is the number of targets a profile holds.
	FollowMeMaxTargets = 5
)

// FollowMeTarget is one place a call is also offered to. Delay is how many
// seconds a sequential target rings before the next starts.
type FollowMeTarget struct {
	Kind   string
	Target string
	Delay  int
}

// FollowMe is an extension's follow-me profile.
type FollowMe struct {
	Enabled         bool
	Strategy        string
	RingOwnDevices  bool
	TimeoutSeconds  int
	ConfirmExternal bool
	DNDBlocks       bool
	Targets         []FollowMeTarget // in position order
}

// Active reports whether the profile rings anything beyond the extension.
func (f FollowMe) Active() bool { return f.Enabled && len(f.Targets) > 0 }

// WithFollowMe installs the follow-me profiles by extension number (tests
// and Load).
func (s *Snapshot) WithFollowMe(byExt map[string]FollowMe) *Snapshot {
	s.followMe = byExt
	return s
}

// FollowMe returns the extension's follow-me profile; ok is false when it has
// none stored.
func (s *Snapshot) FollowMe(number string) (FollowMe, bool) {
	f, ok := s.followMe[number]
	return f, ok
}

func loadFollowMe(ctx context.Context, db Querier) (map[string]FollowMe, error) {
	rows, err := db.Query(ctx, `
		SELECT e.number, f.enabled, f.strategy, f.ring_own_devices, f.timeout_seconds, f.confirm_external, f.dnd_blocks
		FROM follow_me f JOIN extensions e ON e.id = f.extension_id`)
	if err != nil {
		return nil, fmt.Errorf("snapshot: follow-me: %w", err)
	}
	out := map[string]FollowMe{}
	for rows.Next() {
		var (
			num string
			f   FollowMe
		)
		if err := rows.Scan(&num, &f.Enabled, &f.Strategy, &f.RingOwnDevices, &f.TimeoutSeconds, &f.ConfirmExternal, &f.DNDBlocks); err != nil {
			rows.Close()
			return nil, fmt.Errorf("snapshot: scan follow-me: %w", err)
		}
		out[num] = f
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("snapshot: follow-me: %w", err)
	}
	trows, err := db.Query(ctx, `
		SELECT e.number, t.kind, t.target, t.delay_seconds
		FROM follow_me_targets t JOIN extensions e ON e.id = t.extension_id
		ORDER BY e.number, t.position`)
	if err != nil {
		return nil, fmt.Errorf("snapshot: follow-me targets: %w", err)
	}
	for trows.Next() {
		var (
			num string
			t   FollowMeTarget
		)
		if err := trows.Scan(&num, &t.Kind, &t.Target, &t.Delay); err != nil {
			trows.Close()
			return nil, fmt.Errorf("snapshot: scan follow-me target: %w", err)
		}
		if f, ok := out[num]; ok {
			f.Targets = append(f.Targets, t)
			out[num] = f
		}
	}
	trows.Close()
	if err := trows.Err(); err != nil {
		return nil, fmt.Errorf("snapshot: follow-me targets: %w", err)
	}
	return out, nil
}
