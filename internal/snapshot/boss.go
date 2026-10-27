package snapshot

import (
	"context"
	"fmt"
)

// BossLink is one enabled boss and assistant link (migration 00046, spec
// pbx-gaps S-18, S-19). Boss and Assistant are extension numbers. DivertOn is
// the *64 state: it is in PostgreSQL and rides the snapshot, so every replica
// sees it.
type BossLink struct {
	Boss, Assistant string
	// RingMode is "both" or "assistant_first".
	RingMode       string
	Screening      bool
	DivertOn       bool
	OutboundAsBoss bool
}

// Ring modes of a boss link.
const (
	BossRingBoth           = "both"
	BossRingAssistantFirst = "assistant_first"
)

// WithBossLinks installs the enabled boss links (tests and Load).
func (s *Snapshot) WithBossLinks(links []BossLink) *Snapshot {
	s.bosses = map[string]BossLink{}
	for _, l := range links {
		s.bosses[l.Boss] = l
	}
	return s
}

// BossLinkOf returns the enabled link whose boss is the extension.
func (s *Snapshot) BossLinkOf(boss string) (BossLink, bool) {
	l, ok := s.bosses[boss]
	return l, ok
}

// OutboundBossOf returns the boss whose outbound caller-ID rule the
// assistant's outbound calls use. An assistant that serves several bosses
// with the flag on is ambiguous, so none applies.
func (s *Snapshot) OutboundBossOf(assistant string) (string, bool) {
	var bosses []string
	for _, l := range s.bosses {
		if l.Assistant == assistant && l.OutboundAsBoss {
			bosses = append(bosses, l.Boss)
		}
	}
	if len(bosses) != 1 {
		return "", false
	}
	return bosses[0], true
}

// loadBossLinks reads the enabled boss links, so a call to a boss never
// touches the database.
func loadBossLinks(ctx context.Context, db Querier) ([]BossLink, error) {
	rows, err := db.Query(ctx, `SELECT b.number, a.number, l.ring_mode, l.screening, l.divert_on, l.outbound_as_boss
FROM boss_links l JOIN extensions b ON b.id = l.boss_extension_id JOIN extensions a ON a.id = l.assistant_extension_id
WHERE l.enabled ORDER BY b.number`)
	if err != nil {
		return nil, fmt.Errorf("snapshot: boss links: %w", err)
	}
	defer rows.Close()
	var out []BossLink
	for rows.Next() {
		var l BossLink
		if err := rows.Scan(&l.Boss, &l.Assistant, &l.RingMode, &l.Screening, &l.DivertOn, &l.OutboundAsBoss); err != nil {
			return nil, fmt.Errorf("snapshot: scan boss link: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
