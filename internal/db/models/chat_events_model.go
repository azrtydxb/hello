// momo:fromdb sha256=e1ab35a9b2afdac709bb67a500a3b10c6056e99260d4d57bf618b3415a30cd44
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// ChatEvent models the table "chat_events".
type ChatEvent struct {
	momo.Model
	ID        int64 `momo:"pk,serial"`
	VisitorID int64
	Type      string
	MessageID *int64
	At        time.Time `momo:"default:now(),dbdefault,index:chat_events_at"`

	Visitor *ChatVisitor `momo:"belongs_to,fk:VisitorID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *ChatEvent) MomoConfig(c *momo.Config) {
	c.Index("chat_events_visitor", &m.VisitorID, &m.ID)
}
