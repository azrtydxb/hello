// momo:fromdb sha256=541746af2963756714d671db0a90997814254241ba005a3a67535d72abc180cc
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// FollowMeTarget models the table "follow_me_targets".
type FollowMeTarget struct {
	momo.Model
	ExtensionID  int64 `momo:"pk"`
	Position     int16 `momo:"pk"`
	Kind         string
	Target       string
	DelaySeconds int32 `momo:"default:0,dbdefault"`

	Extension *FollowMe `momo:"belongs_to,fk:ExtensionID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *FollowMeTarget) MomoConfig(c *momo.Config) {
	c.Check("follow_me_targets_delay_seconds_check", "((delay_seconds >= 0) AND (delay_seconds <= 60))")
	c.CheckIn(&m.Kind,
		"extension",
		"external",
		"ring_group",
	)
	c.Check("follow_me_targets_position_check", "((\"position\" >= 1) AND (\"position\" <= 5))")
	c.Check("follow_me_targets_target_check", "((target <> ''::text) AND (length(target) <= 64))")
}
