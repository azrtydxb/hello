// momo:fromdb sha256=f572ab1dd1540a6932ff0c740a70b55f3d3c020911dab8b97c64886563c2db02
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// PttChannel models the table "ptt_channels".
type PttChannel struct {
	momo.Model
	GroupID   int64 `momo:"pk"`
	Channel   int32 `momo:"pk"`
	Label     string
	Transmit  *bool `momo:"default:true,dbdefault"`
	Subscribe *bool `momo:"default:true,dbdefault"`

	Group *PhoneGroup `momo:"belongs_to,fk:GroupID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *PttChannel) MomoConfig(c *momo.Config) {
	c.Check("ptt_channels_channel_check", "((channel >= 1) AND (channel <= 50))")
	c.Check("ptt_channels_label_check", "((char_length(label) >= 1) AND (char_length(label) <= 40))")
}
