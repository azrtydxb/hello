package snapshot

import (
	"context"
	"fmt"
)

// VoicemailGroupMember is one member box of a voicemail group: the extension
// number (for MWI) and the box a copy of a message goes to.
type VoicemailGroupMember struct {
	Extension string
	BoxID     int64
}

// VoicemailGroup is a voicemail group (migration 00045): a dial number and
// member boxes. MaxSeconds is the longest message, in seconds.
type VoicemailGroup struct {
	ID         int64
	Number     string
	Name       string
	Enabled    bool
	MaxSeconds int
	Members    []VoicemailGroupMember
}

// IsMember reports whether extension is a member of the group.
func (g VoicemailGroup) IsMember(extension string) bool {
	for _, m := range g.Members {
		if m.Extension == extension {
			return true
		}
	}
	return false
}

// WithVoicemailGroups installs the voicemail groups (tests and Load); it
// returns s.
func (s *Snapshot) WithVoicemailGroups(gs []VoicemailGroup) *Snapshot {
	if s.vmGroups == nil {
		s.vmGroups = map[string]VoicemailGroup{}
	}
	for _, g := range gs {
		s.vmGroups[g.Number] = g
	}
	return s
}

// VoicemailGroup returns the group dialled as number.
func (s *Snapshot) VoicemailGroup(number string) (VoicemailGroup, bool) {
	if number == "" {
		return VoicemailGroup{}, false
	}
	g, ok := s.vmGroups[number]
	return g, ok
}

// VoicemailGroups lists every voicemail group (in no particular order).
func (s *Snapshot) VoicemailGroups() []VoicemailGroup {
	out := make([]VoicemailGroup, 0, len(s.vmGroups))
	for _, g := range s.vmGroups {
		out = append(out, g)
	}
	return out
}

func loadVoicemailGroups(ctx context.Context, db Querier) ([]VoicemailGroup, error) {
	rows, err := db.Query(ctx, `SELECT id, number, name, enabled, max_seconds FROM voicemail_groups ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("snapshot: voicemail groups: %w", err)
	}
	var gs []VoicemailGroup
	for rows.Next() {
		var g VoicemailGroup
		if err := rows.Scan(&g.ID, &g.Number, &g.Name, &g.Enabled, &g.MaxSeconds); err != nil {
			rows.Close()
			return nil, fmt.Errorf("snapshot: scan voicemail group: %w", err)
		}
		gs = append(gs, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("snapshot: voicemail groups: %w", err)
	}
	if len(gs) == 0 {
		return nil, nil
	}
	byID := map[int64]int{}
	for i, g := range gs {
		byID[g.ID] = i
	}
	mrows, err := db.Query(ctx, `SELECT m.group_id, e.number, b.id
		FROM voicemail_group_members m
		JOIN extensions e ON e.id = m.extension_id
		JOIN voicemail_boxes b ON b.extension_id = e.id
		ORDER BY m.group_id, e.number`)
	if err != nil {
		return nil, fmt.Errorf("snapshot: voicemail group members: %w", err)
	}
	defer mrows.Close()
	for mrows.Next() {
		var (
			gid int64
			m   VoicemailGroupMember
		)
		if err := mrows.Scan(&gid, &m.Extension, &m.BoxID); err != nil {
			return nil, fmt.Errorf("snapshot: scan voicemail group member: %w", err)
		}
		if i, ok := byID[gid]; ok {
			gs[i].Members = append(gs[i].Members, m)
		}
	}
	return gs, mrows.Err()
}
