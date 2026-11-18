// momo:fromdb sha256=43ec8312dabf31349fdc7f8ac4bf68d9a35b1cb56e722f246a8d29655a00641e
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// MsgFaq models the table "msg_faq".
type MsgFaq struct {
	momo.Model `momo:"table:msg_faq"`
	ID         int64 `momo:"pk,serial"`
	AddressID  int64
	Title      string
	Body       string
	Enabled    *bool     `momo:"default:true,dbdefault"`
	CreatedAt  time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt  time.Time `momo:"default:now(),dbdefault"`

	Address *MsgAddress `momo:"belongs_to,fk:AddressID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *MsgFaq) MomoConfig(c *momo.Config) {
	c.Check("msg_faq_body_check", "((length(body) >= 1) AND (length(body) <= 4096))")
	c.Check("msg_faq_title_check", "((length(title) >= 1) AND (length(title) <= 200))")
	c.Index("msg_faq_address", &m.AddressID, &m.ID)
}
