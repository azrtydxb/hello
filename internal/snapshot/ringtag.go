package snapshot

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/azrtydxb/hello/internal/ringtag"
)

// Ring tags (migration 00044): the tags, their Bellcore slots and the ordered
// rules are compiled once per revision, so choosing the tag of a call never
// touches PostgreSQL.

// RingTagData is the ring tag configuration of a revision.
type RingTagData struct {
	Tags  []ringtag.Tag
	Rules []ringtag.Rule
}

// WithRingTags installs the ring tags and rules (tests and Load).
func (s *Snapshot) WithRingTags(d RingTagData) *Snapshot {
	s.ringTags = ringtag.NewTable(d.Tags, d.Rules)
	return s
}

// RingTags returns the compiled ring tag table; never nil.
func (s *Snapshot) RingTags() *ringtag.Table {
	if s.ringTags == nil {
		return ringtag.NewTable(nil, nil)
	}
	return s.ringTags
}

func loadRingTags(ctx context.Context, db Querier) (RingTagData, error) {
	var d RingTagData
	rows, err := db.Query(ctx, `SELECT name, builtin, bellcore_slot, description FROM ring_tags ORDER BY name`)
	if err != nil {
		return d, fmt.Errorf("snapshot: ring tags: %w", err)
	}
	for rows.Next() {
		var t ringtag.Tag
		var slot int16
		if err := rows.Scan(&t.Name, &t.Builtin, &slot, &t.Description); err != nil {
			rows.Close()
			return d, fmt.Errorf("snapshot: scan ring tag: %w", err)
		}
		t.BellcoreSlot = int(slot)
		d.Tags = append(d.Tags, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return d, fmt.Errorf("snapshot: ring tags: %w", err)
	}
	rrows, err := db.Query(ctx, `SELECT id, position, name, match, tag FROM ring_rules ORDER BY position, id`)
	if err != nil {
		return d, fmt.Errorf("snapshot: ring rules: %w", err)
	}
	for rrows.Next() {
		var r ringtag.Rule
		var raw []byte
		if err := rrows.Scan(&r.ID, &r.Position, &r.Name, &raw, &r.Tag); err != nil {
			rrows.Close()
			return d, fmt.Errorf("snapshot: scan ring rule: %w", err)
		}
		if err := json.Unmarshal(raw, &r.Match); err != nil {
			continue // a hand-edited match is skipped, never a failed load
		}
		d.Rules = append(d.Rules, r)
	}
	rrows.Close()
	if err := rrows.Err(); err != nil {
		return d, fmt.Errorf("snapshot: ring rules: %w", err)
	}
	return d, nil
}
