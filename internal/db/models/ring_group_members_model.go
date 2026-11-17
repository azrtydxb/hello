// momo:fromdb sha256=ac183431a5d0002db9ecfa9609709064a66c2d525b167192f0f5530c0c4d00f7
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// RingGroupMember models the table "ring_group_members".
type RingGroupMember struct {
	momo.Model
	GroupID           int64 `momo:"pk"`
	ExtensionID       int64 `momo:"pk"`
	Position          int32
	Weight            *int32 `momo:"default:1,dbdefault"`
	Delay             int32  `momo:"default:0,dbdefault"`
	SourceConnectorID *int64

	Extension       *Extension          `momo:"belongs_to,fk:ExtensionID"`
	Group           *RingGroup          `momo:"belongs_to,fk:GroupID,on_delete:cascade"`
	SourceConnector *DirectoryConnector `momo:"belongs_to,fk:SourceConnectorID"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *RingGroupMember) MomoConfig(c *momo.Config) {
	c.Unique("ring_group_members_group_id_position_key", &m.GroupID, &m.Position)
	c.Check("ring_group_members_delay_check", "(delay >= 0)")
	c.Check("ring_group_members_position_check", "(\"position\" >= 1)")
	c.Check("ring_group_members_weight_check", "(weight > 0)")
}
