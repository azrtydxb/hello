// momo:fromdb sha256=277bf999b3d63065acbc20e8685e45d7581e428f5191f5566648e186a160708c
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// RingTag models the table "ring_tags".
type RingTag struct {
	momo.Model
	Name         string    `momo:"pk"`
	Builtin      bool      `momo:"default:false,dbdefault"`
	BellcoreSlot *int16    `momo:"default:1,dbdefault"`
	Description  string    `momo:"default:''::text,dbdefault"`
	CreatedAt    time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt    time.Time `momo:"default:now(),dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *RingTag) MomoConfig(c *momo.Config) {
	c.Check("ring_tags_bellcore_slot_check", "((bellcore_slot >= 1) AND (bellcore_slot <= 5))")
	c.Check("ring_tags_description_check", "(length(description) <= 200)")
	c.Check("ring_tags_name_check", "(name ~ '^[a-z0-9-]{1,32}$'::text)")
}
