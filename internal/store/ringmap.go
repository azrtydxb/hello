package store

// The ring map (migration 00047, spec phone-feature-parity S-42 and S-44): a
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
	"strings"

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
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.group_id, m.tag, m.vendor, CASE WHEN m.custom_tone_id IS NULL THEN m.tone ELSE $1 || t.name END
		FROM ring_map m LEFT JOIN ringtones t ON t.id = m.custom_tone_id
		ORDER BY COALESCE(m.group_id, 0), m.tag, m.vendor`, prov.CustomPrefix)
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
		customs := map[string]int64{}
		crows, err := tx.QueryContext(ctx, `SELECT name, id FROM ringtones`)
		if err != nil {
			return rid, false, err
		}
		for crows.Next() {
			var (
				n  string
				id int64
			)
			if err := crows.Scan(&n, &id); err != nil {
				_ = crows.Close()
				return rid, false, err
			}
			customs[n] = id
		}
		_ = crows.Close()
		if err := crows.Err(); err != nil {
			return rid, false, err
		}
		toneIDs := make([]sql.NullInt64, len(in))
		seen := map[[2]string]bool{}
		for i, e := range in {
			p := fmt.Sprintf("entries[%d]", i)
			switch {
			case !known[e.Tag]:
				return rid, false, fieldError(p+".tag", "no such ring tag")
			case !slices.Contains(prov.RingVendors, prov.Vendor(e.Vendor)):
				return rid, false, fieldError(p+".vendor", "must be yealink, poly, grandstream, snom or fanvil")
			case seen[[2]string{e.Tag, e.Vendor}]:
				return rid, false, fieldError(p, "tag and vendor appear twice")
			case prov.ValidRingTone(prov.Vendor(e.Vendor), e.Tone):
			case strings.HasPrefix(e.Tone, prov.CustomPrefix):
				name, _ := prov.CustomToneName(e.Tone)
				switch id, ok := customs[name]; {
				case !prov.CustomRingSupported(prov.Vendor(e.Vendor)):
					return rid, false, fieldError(p+".tone", "custom tones are not provisioned to "+e.Vendor+" phones")
				case !ok:
					return rid, false, fieldError(p+".tone", "no such tone in the library")
				default:
					toneIDs[i] = sql.NullInt64{Int64: id, Valid: true}
				}
			default:
				return rid, false, fieldError(p+".tone", "not a ringer of "+e.Vendor)
			}
			seen[[2]string{e.Tag, e.Vendor}] = true
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM ring_map WHERE COALESCE(group_id, 0) = COALESCE($1::bigint, 0)`, gid); err != nil {
			return rid, false, err
		}
		for i, e := range in {
			tone := e.Tone
			if toneIDs[i].Valid {
				tone = "custom"
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO ring_map (group_id, tag, vendor, tone, custom_tone_id) VALUES ($1, $2, $3, $4, $5)`,
				gid, e.Tag, e.Vendor, tone, toneIDs[i]); err != nil {
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
		SELECT m.tag, CASE WHEN m.custom_tone_id IS NULL THEN m.tone ELSE $3 || t.name END
		FROM ring_map m LEFT JOIN ringtones t ON t.id = m.custom_tone_id
		WHERE m.vendor = $2 AND (m.group_id IS NULL OR m.group_id = (SELECT group_id FROM phones WHERE id = $1))
		ORDER BY m.group_id IS NOT NULL`, phoneID, string(v), prov.CustomPrefix)
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
	if err := mrows.Close(); err != nil {
		return prov.RingInfo{}, err
	}
	customs := map[string]bool{}
	for _, id := range tones {
		if name, ok := prov.CustomToneName(id); ok {
			customs[name] = true // a choice reaches the library only through its join, so it exists
		}
	}
	return prov.NewRingInfoCustom(v, model, tags, tones, customs), nil
}
