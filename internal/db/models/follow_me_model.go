// momo:fromdb sha256=c9da780d7a6305b903416f9b16b57484b4f96e2e27ec03cf420998e37ab33a0f
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// FollowMe models the table "follow_me".
type FollowMe struct {
	momo.Model      `momo:"table:follow_me"`
	ExtensionID     int64     `momo:"pk"`
	Enabled         bool      `momo:"default:false,dbdefault"`
	Strategy        string    `momo:"default:'simultaneous'::text,dbdefault"`
	RingOwnDevices  *bool     `momo:"default:true,dbdefault"`
	TimeoutSeconds  *int32    `momo:"default:30,dbdefault"`
	ConfirmExternal *bool     `momo:"default:true,dbdefault"`
	DndBlocks       *bool     `momo:"default:true,dbdefault"`
	CreatedAt       time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt       time.Time `momo:"default:now(),dbdefault"`

	Extension *Extension `momo:"belongs_to,fk:ExtensionID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *FollowMe) MomoConfig(c *momo.Config) {
	c.CheckIn(&m.Strategy,
		"sequential",
		"simultaneous",
	)
	c.Check("follow_me_timeout_seconds_check", "((timeout_seconds >= 5) AND (timeout_seconds <= 120))")
}
