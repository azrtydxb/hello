package store

// The ring map (migration 00051, spec phone-feature-parity S-42 and S-44): a
// tone per ring tag and vendor, global with an optional override per phone
// group. It is read only by provisioning (RenderData.Ring), so a change does
// not bump the configuration revision: the automatic resync notices that the
// phone's render data changed and queues a check-sync for the phones it
// reaches.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/azrtydxb/hello/internal/prov"
	"github.com/azrtydxb/hello/internal/ringtag"
)

// RingMapEntry is one choice of the ring map: the tone a vendor plays for a tag.
type RingMapEntry struct {
	Tag    string `json:"tag"`
	Vendor string `json:"vendor"`
	Tone   string `json:"tone"`
}

// RingMapGroup is a phone group's overrides.
type RingMapGroup struct {
	GroupID int64          `json:"groupId"`
	Entries []RingMapEntry `json:"entries"`
}

// RingMap is every explicit choice: the global ones and each group's.
type RingMap struct {
	Global []RingMapEntry `json:"global"`
	Groups []RingMapGroup `json:"groups"`
}

// GetRingMap returns the explicit choices; tags and vendors without one use
// the vendor's default for the tag's Bellcore slot.
func (s *Store) GetRingMap(ctx context.Context) (RingMap, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT group_id, tag, vendor, tone FROM ring_map ORDER BY COALESCE(group_id, 0), tag, vendor`)
	if err != nil {
		return RingMap{}, err
	}
	defer func() { _ = rows.Close() }()
	out := RingMap{Global: []RingMapEntry{}, Groups: []RingMapGroup{}}
	for rows.Next() {
		var (
			g sql.NullInt64
			e RingMapEntry
		)
		if err := rows.Scan(&g, &e.Tag, &e.Vendor, &e.Tone); err != nil {
			return RingMap{}, err
		}
		if !g.Valid {
			out.Global = append(out.Global, e)
			continue
		}
		if n := len(out.Groups); n == 0 || out.Groups[n-1].GroupID != g.Int64 {
			out.Groups = append(out.Groups, RingMapGroup{GroupID: g.Int64, Entries: []RingMapEntry{}})
		}
		last := &out.Groups[len(out.Groups)-1]
		last.Entries = append(last.Entries, e)
	}
	return out, rows.Err()
}

// ReplaceRingMap replaces the explicit choices of one scope as a whole: the
// global map when groupID is nil, else that phone group's overrides (an empty
// list removes them). ErrNotFound for a group that does not exist; a field
// error for a tag, vendor or tone the ring map does not know.
func (s *Store) ReplaceRingMap(ctx context.Context, actor string, groupID *int64, in []RingMapEntry) (RingMap, error) {
	err := s.provChange(ctx, actor, "update", "ring_map", func(tx *sql.Tx) (string, bool, error) {
		rid := "global"
		var gid any
		if groupID != nil {
			rid = "group:" + strconv.FormatInt(*groupID, 10)
			gid = *groupID
			var one int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM phone_groups WHERE id = $1 FOR UPDATE`, *groupID).Scan(&one); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return rid, false, ErrNotFound
				}
				return rid, false, err
			}
		}
		known := map[string]bool{}
		rows, err := tx.QueryContext(ctx, `SELECT name FROM ring_tags`)
		if err != nil {
			return rid, false, err
		}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				_ = rows.Close()
				return rid, false, err
			}
			known[n] = true
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return rid, false, err
		}
		seen := map[[2]string]bool{}
		for i, e := range in {
			p := fmt.Sprintf("entries[%d]", i)
			switch {
			case !known[e.Tag]:
				return rid, false, fieldError(p+".tag", "no such ring tag")
			case !slices.Contains(prov.RingVendors, prov.Vendor(e.Vendor)):
				return rid, false, fieldError(p+".vendor", "must be yealink, poly, grandstream, snom or fanvil")
			case !prov.ValidRingTone(prov.Vendor(e.Vendor), e.Tone):
				return rid, false, fieldError(p+".tone", "not a ringer of "+e.Vendor)
			case seen[[2]string{e.Tag, e.Vendor}]:
				return rid, false, fieldError(p, "tag and vendor appear twice")
			}
			seen[[2]string{e.Tag, e.Vendor}] = true
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM ring_map WHERE COALESCE(group_id, 0) = COALESCE($1::bigint, 0)`, gid); err != nil {
			return rid, false, err
		}
		for _, e := range in {
			if _, err := tx.ExecContext(ctx, `INSERT INTO ring_map (group_id, tag, vendor, tone) VALUES ($1, $2, $3, $4)`,
				gid, e.Tag, e.Vendor, e.Tone); err != nil {
				return rid, false, err
			}
		}
		return rid, false, nil
	})
	if err != nil {
		return RingMap{}, err
	}
	return s.GetRingMap(ctx)
}

// ringInfo is RenderData.Ring for a phone: the tags in priority order
// (built-ins in their canonical order, then custom tags by name) with the
// tone each plays on this vendor, the phone's group choices over the global
// ones over the vendor's defaults.
func (s *Store) ringInfo(ctx context.Context, phoneID int64, v prov.Vendor, model string) (prov.RingInfo, error) {
	if prov.RingSlots(v, model) == 0 {
		return prov.RingInfo{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT name, builtin, bellcore_slot FROM ring_tags`)
	if err != nil {
		return prov.RingInfo{}, err
	}
	type tagRow struct {
		prov.RingTag
		builtin bool
	}
	var all []tagRow
	for rows.Next() {
		var t tagRow
		if err := rows.Scan(&t.Name, &t.builtin, &t.Slot); err != nil {
			_ = rows.Close()
			return prov.RingInfo{}, err
		}
		all = append(all, t)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return prov.RingInfo{}, err
	}
	order := map[string]int{}
	for i, t := range ringtag.Builtins() {
		order[t.Name] = i + 1
	}
	slices.SortFunc(all, func(a, b tagRow) int {
		switch {
		case a.builtin != b.builtin:
			if a.builtin {
				return -1
			}
			return 1
		case a.builtin && order[a.Name] != order[b.Name]:
			return order[a.Name] - order[b.Name]
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		}
		return 0
	})
	tags := make([]prov.RingTag, len(all))
	for i, t := range all {
		tags[i] = t.RingTag
	}

	// Global rows first, then the group's, so the group's overwrite.
	tones := map[string]string{}
	mrows, err := s.db.QueryContext(ctx, `
		SELECT tag, tone FROM ring_map
		WHERE vendor = $2 AND (group_id IS NULL OR group_id = (SELECT group_id FROM phones WHERE id = $1))
		ORDER BY group_id IS NOT NULL`, phoneID, string(v))
	if err != nil {
		return prov.RingInfo{}, err
	}
	defer func() { _ = mrows.Close() }()
	for mrows.Next() {
		var tag, tone string
		if err := mrows.Scan(&tag, &tone); err != nil {
			return prov.RingInfo{}, err
		}
		tones[tag] = tone
	}
	if err := mrows.Err(); err != nil {
		return prov.RingInfo{}, err
	}
	return prov.NewRingInfo(v, model, tags, tones), nil
}
