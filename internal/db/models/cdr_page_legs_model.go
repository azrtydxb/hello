// momo:fromdb sha256=4352d19ac4d6873c3ecb342d0926a2c5f96bbe7c9915ab26a723f6725df3e278
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// CdrPageLeg models the table "cdr_page_legs".
type CdrPageLeg struct {
	momo.Model
	CdrID     int64 `momo:"pk"`
	Position  int32 `momo:"pk"`
	Extension string
	Device    string `momo:"default:''::text,dbdefault"`
	Result    string

	Cdr *Cdr `momo:"belongs_to,fk:CdrID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *CdrPageLeg) MomoConfig(c *momo.Config) {
	c.CheckIn(&m.Result,
		"answered",
		"busy",
		"dnd",
		"no_answer",
		"no_registration",
	)
}
