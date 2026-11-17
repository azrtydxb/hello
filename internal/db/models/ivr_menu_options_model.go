// momo:fromdb sha256=73d03233cdd4adeced66815764c712a4f2c830671fb911e52795fd9705967920
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// IvrMenuOption models the table "ivr_menu_options".
type IvrMenuOption struct {
	momo.Model
	MenuID     int64  `momo:"pk"`
	Digit      string `momo:"pk"`
	DestKind   string
	DestTarget string `momo:"default:''::text,dbdefault"`

	Menu *IvrMenu `momo:"belongs_to,fk:MenuID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *IvrMenuOption) MomoConfig(c *momo.Config) {
	c.CheckIn(&m.DestKind,
		"announcement",
		"directory",
		"extension",
		"external",
		"fax",
		"hangup",
		"ivr",
		"paging",
		"queue",
		"ring_group",
		"time_condition",
		"voicemail",
		"voicemail_group",
	)
	c.Check("ivr_menu_options_digit_check", "(digit ~ '^[0-9*#]$'::text)")
}
