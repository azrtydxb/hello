// momo:fromdb sha256=8cafccfcc99907312604f3427b73947021b6256936b9254525de2904984af1b8
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// QueueMember models the table "queue_members".
type QueueMember struct {
	momo.Model
	QueueID     int64 `momo:"pk"`
	ExtensionID int64 `momo:"pk"`
	Dynamic     bool  `momo:"default:false,dbdefault"`
	Penalty     int32 `momo:"default:0,dbdefault"`
	Position    int32

	Extension *Extension `momo:"belongs_to,fk:ExtensionID,on_delete:cascade"`
	Queue     *Queue     `momo:"belongs_to,fk:QueueID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *QueueMember) MomoConfig(c *momo.Config) {
	c.Unique("queue_members_queue_id_position_key", &m.QueueID, &m.Position)
	c.Check("queue_members_penalty_check", "((penalty >= 0) AND (penalty <= 99))")
	c.Check("queue_members_position_check", "(\"position\" >= 1)")
}
