// momo:fromdb sha256=f1cf315906e7efa13cde6ff7330db9ed65a9e379dd202e765c200638bc17c61d
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// MulticastChannel models the table "multicast_channels".
type MulticastChannel struct {
	momo.Model
	ID       int64 `momo:"pk,serial"`
	GroupID  *int64
	Position *int32
	Label    string
	Address  string
	Port     int32
	Codec    string
	Role     *string
	Priority *int32 `momo:"default:1,dbdefault"`
	Enabled  *bool  `momo:"default:true,dbdefault"`
	Kind     string `momo:"default:'paging'::text,dbdefault"`

	Group *PhoneGroup `momo:"belongs_to,fk:GroupID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *MulticastChannel) MomoConfig(c *momo.Config) {
	c.Unique("multicast_channels_group_id_position_key", &m.GroupID, &m.Position)
	c.CheckIn(&m.Codec,
		"G722",
		"PCMA",
		"PCMU",
	)
	c.CheckIn(&m.Kind,
		"paging",
		"ptt",
	)
	c.Check("multicast_channels_label_check", "((char_length(label) >= 1) AND (char_length(label) <= 40))")
	c.Check("multicast_channels_port_check", "((port >= 1024) AND (port <= 65535))")
	c.Check("multicast_channels_position_check", "(\"position\" >= 0)")
	c.Check("multicast_channels_priority_check", "((priority >= 1) AND (priority <= 25))")
	c.CheckIn(&m.Role,
		"both",
		"listen",
		"send",
	)
}
