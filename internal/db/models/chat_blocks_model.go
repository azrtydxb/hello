// momo:fromdb sha256=ad254f51fe520416054a94e75e286fc2ca551b4a9182d0381f1d4f1aee235f0a
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// ChatBlock models the table "chat_blocks".
type ChatBlock struct {
	momo.Model
	ID        int64 `momo:"pk,serial"`
	SiteID    int64
	VisitorID *int64
	IPHash    []byte `momo:"nullable"`
	Until     time.Time
	ByUserID  *int64
	CreatedAt time.Time `momo:"default:now(),dbdefault"`

	ByUser  *User        `momo:"belongs_to,fk:ByUserID,on_delete:set_null"`
	Site    *ChatSite    `momo:"belongs_to,fk:SiteID,on_delete:cascade"`
	Visitor *ChatVisitor `momo:"belongs_to,fk:VisitorID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *ChatBlock) MomoConfig(c *momo.Config) {
	c.Check("chat_blocks_check", "((visitor_id IS NOT NULL) OR (ip_hash IS NOT NULL))")
	c.Index("chat_blocks_site", &m.SiteID, &m.Until)
	c.Index("chat_blocks_visitor", &m.VisitorID).Where("(visitor_id IS NOT NULL)")
}
