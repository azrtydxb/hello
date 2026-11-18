// momo:fromdb sha256=b61eb4f7ddc30fb675a3cd72e997d7298f0b1a9a91cb46fc8dfb0b2d68d796a6
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// RingMap models the table "ring_map".
type RingMap struct {
	momo.Model   `momo:"table:ring_map"`
	ID           int64 `momo:"pk,serial"`
	GroupID      *int64
	Tag          string `momo:"index:ring_map_tag"`
	Vendor       string
	Tone         string
	UpdatedAt    time.Time `momo:"default:now(),dbdefault"`
	CustomToneID *int64

	CustomTone *Ringtone   `momo:"belongs_to,fk:CustomToneID,on_delete:restrict"`
	Group      *PhoneGroup `momo:"belongs_to,fk:GroupID,on_delete:cascade"`
	RingTag    *RingTag    `momo:"belongs_to,fk:Tag,on_delete:restrict,on_update:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *RingMap) MomoConfig(c *momo.Config) {
	c.Check("ring_map_custom_tone", "((tone = 'custom'::text) = (custom_tone_id IS NOT NULL))")
	c.Check("ring_map_tone_check", "(tone ~ '^[A-Za-z0-9-]{1,24}$'::text)")
	c.CheckIn(&m.Vendor,
		"fanvil",
		"grandstream",
		"poly",
		"snom",
		"yealink",
	)
	c.Index("ring_map_custom_tone", &m.CustomToneID).Where("(custom_tone_id IS NOT NULL)")
	c.IndexExpr("ring_map_key", "coalesce(group_id, 0::bigint)", "tag", "vendor").Unique()
}
