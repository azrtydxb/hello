// momo:fromdb sha256=bd8887d19b751a6925a874d38eb5c165459c31a786f9fa7740ac02acd8e6d38f
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// PhoneGroupMulticast models the table "phone_group_multicast".
type PhoneGroupMulticast struct {
	momo.Model `momo:"table:phone_group_multicast"`
	GroupID    int64 `momo:"pk"`
	ChannelID  int64 `momo:"pk"`
	Position   int32
	Role       string

	Channel *MulticastChannel `momo:"belongs_to,fk:ChannelID,on_delete:cascade"`
	Group   *PhoneGroup       `momo:"belongs_to,fk:GroupID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *PhoneGroupMulticast) MomoConfig(c *momo.Config) {
	c.Unique("phone_group_multicast_group_id_position_key", &m.GroupID, &m.Position)
	c.Check("phone_group_multicast_position_check", "(\"position\" >= 0)")
	c.CheckIn(&m.Role,
		"both",
		"listen",
		"send",
	)
}
