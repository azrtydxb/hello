// momo:fromdb sha256=e44f0a96486dccea4b2b32e8f2302ddfe768d5128acf2949110a88241471467d
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// PhoneGroupBook models the table "phone_group_books".
type PhoneGroupBook struct {
	momo.Model
	GroupID  int64 `momo:"pk"`
	BookID   int64 `momo:"pk"`
	Position int32

	Book  *Phonebook  `momo:"belongs_to,fk:BookID,on_delete:cascade"`
	Group *PhoneGroup `momo:"belongs_to,fk:GroupID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *PhoneGroupBook) MomoConfig(c *momo.Config) {
	c.Unique("phone_group_books_group_id_position_key", &m.GroupID, &m.Position)
	c.Check("phone_group_books_position_check", "(\"position\" >= 0)")
}
