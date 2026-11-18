// momo:fromdb sha256=d1d4c7c34a92f1c2996eec3bceb919a9814315220782592f39a02b75379cdeb5
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// ParkLot models the table "park_lots".
type ParkLot struct {
	momo.Model
	ID            int64 `momo:"pk,serial"`
	Name          string
	SlotStart     int32
	SlotEnd       int32
	ReturnSecs    *int32    `momo:"default:120,dbdefault"`
	VoicemailSecs *int32    `momo:"default:60,dbdefault"`
	Enabled       *bool     `momo:"default:true,dbdefault"`
	CreatedAt     time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt     time.Time `momo:"default:now(),dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *ParkLot) MomoConfig(c *momo.Config) {
	c.Unique("park_lots_name_key", &m.Name)
	c.Check("park_lots_check", "((slot_end >= slot_start) AND ((slot_end - slot_start) < 100))")
	c.Check("park_lots_name_check", "(name ~ '^[A-Za-z0-9._-]{1,64}$'::text)")
	c.Check("park_lots_return_secs_check", "((return_secs >= 10) AND (return_secs <= 3600))")
	c.Check("park_lots_slot_end_check", "((slot_end >= 1) AND (slot_end <= 99999))")
	c.Check("park_lots_slot_start_check", "((slot_start >= 1) AND (slot_start <= 99999))")
	c.Check("park_lots_voicemail_secs_check", "((voicemail_secs >= 10) AND (voicemail_secs <= 3600))")
}
